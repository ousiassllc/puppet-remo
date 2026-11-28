package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは、ホストの人が実際のマウス・キーボードを使っている間、リモートの入力を一時停止する仕組み（#270。FR-M30）。
//
// 検知は platform.HostInputWatcher（自分の注入は検知しない）。実入力があると停止し、最後の実入力から idle の間、実入力がなければ自動で再開する
// （実入力が続けば再開は延びる）。停止中は Control.Do が、実際のカーソル・フォーカス・キー入力を動かす操作（domain.OpPausedByHostInput）を
// ErrInputPaused で拒否する（MCP・Web ビューア・run_task のどの経路も Control.Do を通る）。読み取りと、カーソル・フォーカスを動かさない操作は止めない。
// リース・承認・キルスイッチ・入力スコープ・InputGuard は変えない（キルスイッチのホットキーの押下も実入力として数えるが、キルスイッチは停止と独立に効く）。

// 設定（host-only。MCP からは変えられない。HostSettings が GUI から読み書きする）。
const (
	settingHostInputPauseEnabled = "hostinput.pause_enabled"
	settingHostInputPauseIdleSec = "hostinput.pause_idle_sec"

	// 既定は有効・5 秒。有効を既定にするのは、ホストの人の作業を邪魔しない側に倒すため（検知は injected フラグの有無だけを見て、
	// キーの内容は読まない）。5 秒は、手を止めて考える程度の間でリモートが割り込まない長さ。
	defaultHostInputPauseEnabled = true
	defaultHostInputPauseIdleSec = 5
	minHostInputPauseIdleSec     = 1
	maxHostInputPauseIdleSec     = 600
)

// 監査のイベント名（kind = input。ホストの実入力による停止・再開）。
const (
	hostInputPausedAuditEvent  = "input.host_paused"
	hostInputResumedAuditEvent = "input.host_resumed"
)

// 再開の理由（input.host_resumed の detail.reason）。
const (
	hostInputResumeIdle     = "idle"     // 待ち時間のあいだ実入力がなかった
	hostInputResumeDisabled = "disabled" // ホストが設定で無効にした
)

// HostInputReasonHostInput は HostInputStatus を見せるときの停止の理由（今は 1 つ）。
const HostInputReasonHostInput = "host_input"

// hostInputRunInterval は Run が停止の終了（再開）を確かめる間隔。再開の判定そのものは時刻で決まる（Check・Status も確かめる）ので、
// この間隔は監査の再開の行を書く遅れの上限になる。
const hostInputRunInterval = time.Second

// HostInputPolicy は一時停止の設定。
type HostInputPolicy struct {
	Enabled bool
	Idle    time.Duration // 最後の実入力からこの間、実入力がなければ再開する
}

// LoadHostInputPolicy は設定 hostinput.pause_enabled（既定 true）・hostinput.pause_idle_sec（既定 5。1〜600 秒）を読む。
// 値が不正なら domain.ErrInvalidArgument。settings が nil なら既定値。
func LoadHostInputPolicy(ctx context.Context, settings repository.Settings) (HostInputPolicy, error) {
	enabled, err := loadBool(ctx, settings, settingHostInputPauseEnabled, defaultHostInputPauseEnabled)
	if err != nil {
		return HostInputPolicy{}, err
	}
	sec, err := loadPositiveInt(ctx, settings, settingHostInputPauseIdleSec, defaultHostInputPauseIdleSec)
	if err != nil {
		return HostInputPolicy{}, err
	}
	if sec < minHostInputPauseIdleSec || sec > maxHostInputPauseIdleSec {
		return HostInputPolicy{}, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("setting %s must be between %d and %d", settingHostInputPauseIdleSec, minHostInputPauseIdleSec, maxHostInputPauseIdleSec))
	}
	return HostInputPolicy{Enabled: enabled, Idle: time.Duration(sec) * time.Second}, nil
}

// HostInputStatus は一時停止の状態（get_control_status が見せる）。
type HostInputStatus struct {
	Enabled bool // 設定が有効か（無効なら Paused は常に false）
	Paused  bool
	// 以下は Paused のときだけ。
	Since     time.Time     // 停止した時刻（最初の実入力）
	ResumesAt time.Time     // 実入力がなければ再開する時刻（最後の実入力 + 待ち時間）
	ResumesIn time.Duration // ResumesAt までの残り（0 以上）
}

// HostInputPause は Control と mcp が使う、一時停止の窓口。
type HostInputPause interface {
	// Check は停止中なら ErrInputPaused（detail: resumes_at・retry_after_ms）、そうでなければ nil。
	Check() error
	Status() HostInputStatus
}

// HostInputPauserConfig は NewHostInputPauser の設定。
type HostInputPauserConfig struct {
	Clock  domain.Clock
	Audit  AuditRecorder // nil なら記録しない
	Logger *slog.Logger  // nil なら slog.Default()
	Policy HostInputPolicy
}

// HostInputPauser は HostInputPause の実装。NoteHostInput は platform.HostInputWatcher のコールバック（メッセージループのスレッド）から
// 頻繁に呼ばれるので、短い臨界区間と非ブロッキングの監査（AuditRecorder.Record）だけで済ませる。
type HostInputPauser struct {
	clock domain.Clock
	audit AuditRecorder
	log   *slog.Logger

	mu        sync.Mutex
	policy    HostInputPolicy
	paused    bool
	since     time.Time
	lastInput time.Time
	onPause   func()
}

var _ HostInputPause = (*HostInputPauser)(nil)

// NewHostInputPauser は一時停止の状態機械を返す。時刻は Clock（テストは進められる Clock を渡す）。
func NewHostInputPauser(cfg HostInputPauserConfig) (*HostInputPauser, error) {
	if cfg.Clock == nil {
		return nil, domain.ErrInvalidArgument.WithMsg("host input pause requires clock")
	}
	if err := validateHostInputPolicy(cfg.Policy); err != nil {
		return nil, err
	}
	p := &HostInputPauser{clock: cfg.Clock, audit: cfg.Audit, log: cfg.Logger, policy: cfg.Policy}
	if p.log == nil {
		p.log = slog.Default()
	}
	return p, nil
}

func validateHostInputPolicy(p HostInputPolicy) error {
	if p.Idle < minHostInputPauseIdleSec*time.Second || p.Idle > maxHostInputPauseIdleSec*time.Second {
		return domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("host input idle time must be between %d and %d seconds", minHostInputPauseIdleSec, maxHostInputPauseIdleSec))
	}
	return nil
}

// SetOnPause は停止に入るたびに呼ぶ関数を設定する（Control.ReleaseInput のように、押下中のキー・ボタンを離すため）。
// 呼び出し元（NoteHostInput）はメッセージループのスレッドなので、重い処理は fn の中で別の goroutine へ渡す。
func (p *HostInputPauser) SetOnPause(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onPause = fn
}

// Policy は今の設定を返す。
func (p *HostInputPauser) Policy() HostInputPolicy {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.policy
}

// SetPolicy は設定を反映する。無効にしたときは、停止中ならすぐ再開する（理由 disabled）。待ち時間の変更は、停止中なら再開の時刻に反映される。
func (p *HostInputPauser) SetPolicy(policy HostInputPolicy) error {
	if err := validateHostInputPolicy(policy); err != nil {
		return err
	}
	now := p.clock.Now()
	p.mu.Lock()
	p.policy = policy
	var end *pauseEnd
	if !policy.Enabled && p.paused {
		end = p.endLocked(now, hostInputResumeDisabled)
	}
	p.mu.Unlock()
	p.recordResumed(end)
	return nil
}

// NoteHostInput はホストの実入力 1 件を受ける（platform.HostInputWatcher のコールバック）。無効の間は何もしない。
// 停止していなければ停止に入り（監査 input.host_paused・OnPause）、停止中なら再開を最後の実入力から数え直す。
func (p *HostInputPauser) NoteHostInput(kind platform.HostInputKind) {
	now := p.clock.Now()
	p.mu.Lock()
	if !p.policy.Enabled {
		p.mu.Unlock()
		return
	}
	// 待ち時間がすでに過ぎていた停止は、新しい停止の前に終わらせる（再開の行を、新しい停止の行より先に残す）。
	end := p.resumeIfIdleLocked(now)
	started := !p.paused
	if started {
		p.paused, p.since = true, now
	}
	p.lastInput = now
	idle, onPause := p.policy.Idle, p.onPause
	p.mu.Unlock()

	p.recordResumed(end)
	if !started {
		return
	}
	p.log.Info("remote input paused: the host is using the mouse or keyboard", "source", string(kind), "idle_sec", int(idle/time.Second))
	p.record(hostInputPausedAuditEvent, now, map[string]any{"source": string(kind), "idle_sec": int(idle / time.Second)})
	if onPause != nil {
		onPause()
	}
}

// Poll は待ち時間が過ぎた停止を終わらせる（監査 input.host_resumed）。Check・Status・NoteHostInput も同じ確認をするので、
// Poll は、誰も呼ばなくても再開の行が遅れて残らないようにするためのもの（Run が定期的に呼ぶ）。
func (p *HostInputPauser) Poll() {
	now := p.clock.Now()
	p.mu.Lock()
	end := p.resumeIfIdleLocked(now)
	p.mu.Unlock()
	p.recordResumed(end)
}

// Run は ctx が終わるまで、定期的に Poll を呼ぶ。
func (p *HostInputPauser) Run(ctx context.Context) {
	t := time.NewTicker(hostInputRunInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Poll()
		}
	}
}

func (p *HostInputPauser) Check() error {
	st := p.Status()
	if !st.Paused {
		return nil
	}
	return pausedError(st)
}

func (p *HostInputPauser) Status() HostInputStatus {
	now := p.clock.Now()
	p.mu.Lock()
	end := p.resumeIfIdleLocked(now)
	st := HostInputStatus{Enabled: p.policy.Enabled, Paused: p.paused}
	if p.paused {
		st.Since = p.since
		st.ResumesAt = p.lastInput.Add(p.policy.Idle)
		st.ResumesIn = max(0, st.ResumesAt.Sub(now))
	}
	p.mu.Unlock()
	p.recordResumed(end)
	return st
}

// pausedError は st（Paused）の ErrInputPaused。resumes_at は RFC 3339（UTC）、retry_after_ms は再開までのミリ秒（切り上げ）。
func pausedError(st HostInputStatus) error {
	sec := int(math.Ceil(st.ResumesIn.Seconds()))
	return domain.ErrInputPaused.
		WithMsg(fmt.Sprintf("input is paused because the host is using the mouse or keyboard; it resumes in about %d s if the host stops", sec)).
		WithDetail("resumes_at", st.ResumesAt.UTC().Format(time.RFC3339Nano)).
		WithDetail("retry_after_ms", int64(math.Ceil(float64(st.ResumesIn)/float64(time.Millisecond))))
}

// pauseEnd は終わった停止（監査の再開の行の材料）。
type pauseEnd struct {
	at     time.Time
	since  time.Time
	reason string
}

// resumeIfIdleLocked は、停止中で、最後の実入力から待ち時間が過ぎていれば停止を終える。mu を持って呼ぶ。
func (p *HostInputPauser) resumeIfIdleLocked(now time.Time) *pauseEnd {
	if !p.paused || now.Before(p.lastInput.Add(p.policy.Idle)) {
		return nil
	}
	// 再開の時刻は、確かめた時刻ではなく、待ち時間が過ぎた時刻（確かめるのが遅れても監査の時刻は正しい）。
	return p.endLocked(p.lastInput.Add(p.policy.Idle), hostInputResumeIdle)
}

func (p *HostInputPauser) endLocked(at time.Time, reason string) *pauseEnd {
	end := &pauseEnd{at: at, since: p.since, reason: reason}
	p.paused = false
	return end
}

func (p *HostInputPauser) recordResumed(end *pauseEnd) {
	if end == nil {
		return
	}
	p.log.Info("remote input resumed", "reason", end.reason)
	p.record(hostInputResumedAuditEvent, end.at, map[string]any{"paused_ms": end.at.Sub(end.since).Milliseconds(), "reason": end.reason})
}

func (p *HostInputPauser) record(event string, at time.Time, detail map[string]any) {
	if p.audit == nil {
		return
	}
	p.audit.Record(context.Background(), repository.AuditEntry{
		TS: at, Kind: "input", Event: event, ActorType: domain.ActorHost, ClientLabel: hostLabel,
		Result: auditOK, DetailJSON: marshalDetail(detail),
	})
}
