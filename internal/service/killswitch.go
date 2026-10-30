package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// KillSwitch は緊急停止（FR-H06、NFR-S07、NFR-P05。docs/components/overview.md §5.8）。
// 発動すると、全リースを剥奪し、登録されたクローザーで全セッションを閉じ、以後の入力・新しい要求を断る。
// 状態は settings に永続化され、再起動しても維持される。解除はホスト所有者が GUI で明示的に行う。
type KillSwitch interface {
	Active() bool
	// Trigger は同期で次の順に実行する: 発動状態の永続化 → LeaseManager.ReleaseAll → クローザー（全セッション・押下中の入力の解放）
	// → 監査。各段の失敗は後続を止めず、まとめて返す（発動状態は変わらない）。1 秒以内（NFR-P05）。
	// M4 の Agent.CancelAll と Bus への通知は、それぞれクローザーと購読で足す。
	Trigger(source string) error
	// Release はホスト GUI からのみ呼ぶ。永続化に失敗したら発動状態のまま。
	Release() error
}

// KillSwitchSnapshot は発動状態（settings の killswitch.state）。
type KillSwitchSnapshot struct {
	Active      bool
	TriggeredAt time.Time
	Source      string
}

// LeaseReleaser は KillSwitch が全リースを剥奪する窓口（LeaseManager の一部）。
type LeaseReleaser interface{ ReleaseAll(reason string) }

// CloserFunc は発動時に呼ばれる後始末（MCP・WebSocket セッションを閉じる、押下中のキー・ボタンを解放するなど）。
// ctx は発動の期限（既定 1 秒）で取り消される。
type CloserFunc func(ctx context.Context) error

// KillSwitchConfig は NewKillSwitch の設定。
type KillSwitchConfig struct {
	Settings repository.Settings
	Clock    domain.Clock
	Audit    AuditRecorder // nil なら記録しない
	Logger   *slog.Logger  // nil なら slog.Default()
	// Timeout は永続化とクローザーの期限。0 なら 1 秒（NFR-P05）。
	Timeout time.Duration
}

const defaultKillSwitchTimeout = time.Second

// killSwitchRecord は settings.killswitch.state の JSON（docs/architecture/er.md）。
type killSwitchRecord struct {
	Active      bool       `json:"active"`
	TriggeredAt *time.Time `json:"triggered_at,omitempty"`
	Source      string     `json:"source,omitempty"`
}

// KillSwitchManager は KillSwitch の実装。
type KillSwitchManager struct {
	settings repository.Settings
	clock    domain.Clock
	audit    AuditRecorder
	log      *slog.Logger
	timeout  time.Duration

	// opMu は Trigger と Release を直列化する。mu は状態とクローザー一覧だけを守る（Active は mu だけを取る）。
	opMu    sync.Mutex
	mu      sync.Mutex
	state   KillSwitchSnapshot
	leases  LeaseReleaser
	closers []namedCloser
}

type namedCloser struct {
	name string
	fn   CloserFunc
}

var _ KillSwitch = (*KillSwitchManager)(nil)

// NewKillSwitch は settings から永続化済みの状態を読んで KillSwitchManager を返す。
// 読めない・壊れている場合はエラー（停止状態を失わないため、黙って解除扱いにしない）。
func NewKillSwitch(ctx context.Context, cfg KillSwitchConfig) (*KillSwitchManager, error) {
	if cfg.Settings == nil || cfg.Clock == nil {
		return nil, domain.ErrInvalidArgument.WithMsg("kill switch settings and clock are required")
	}
	k := &KillSwitchManager{
		settings: cfg.Settings,
		clock:    cfg.Clock,
		audit:    cfg.Audit,
		log:      cfg.Logger,
		timeout:  cfg.Timeout,
	}
	if k.log == nil {
		k.log = slog.Default()
	}
	if k.timeout <= 0 {
		k.timeout = defaultKillSwitchTimeout
	}
	raw, ok, err := cfg.Settings.Get(ctx, settingKillSwitchState)
	if err != nil {
		return nil, fmt.Errorf("read setting %s: %w", settingKillSwitchState, err)
	}
	if ok {
		var rec killSwitchRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("parse setting %s: %w", settingKillSwitchState, err)
		}
		k.state = KillSwitchSnapshot{Active: rec.Active, Source: rec.Source}
		if rec.TriggeredAt != nil {
			k.state.TriggeredAt = *rec.TriggeredAt
		}
	}
	return k, nil
}

// BindLeases は発動時に全リースを剥奪する LeaseManager を結ぶ。
// LeaseManager は取得時にキルスイッチの状態（KillSwitchState）を読むため、組み立ての順は
// NewKillSwitch → NewLeaseManager → BindLeases になる。結ぶ前の Trigger はエラーを返す（発動自体は行う）。
func (k *KillSwitchManager) BindLeases(l LeaseReleaser) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.leases = l
}

// RegisterCloser は発動時に呼ぶ後始末を登録する。全クローザーは並行に呼ばれる。
func (k *KillSwitchManager) RegisterCloser(name string, fn CloserFunc) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.closers = append(k.closers, namedCloser{name: name, fn: fn})
}

// State は発動状態を返す。
func (k *KillSwitchManager) State() KillSwitchSnapshot {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.state
}

func (k *KillSwitchManager) Active() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.state.Active
}

func (k *KillSwitchManager) Trigger(source string) error {
	k.opMu.Lock()
	defer k.opMu.Unlock()
	start := k.clock.Now()

	k.mu.Lock()
	if !k.state.Active {
		k.state = KillSwitchSnapshot{Active: true, TriggeredAt: start, Source: source}
	}
	st, leases := k.state, k.leases
	closers := slices.Clone(k.closers)
	k.mu.Unlock()

	// 永続化とクローザーはそれぞれ自分の期限（k.timeout）を持つ。永続化（遅いディスクなど）が期限を使い切っても、
	// クローザー（セッション切断・入力の解放）は期限切れの ctx で呼ばれない。
	var errs []error
	persistCtx, cancelPersist := context.WithTimeout(context.Background(), k.timeout)
	if err := k.persist(persistCtx, st); err != nil {
		errs = append(errs, err)
	}
	cancelPersist()
	if leases == nil {
		errs = append(errs, errors.New("kill switch: no lease manager is bound"))
	} else {
		leases.ReleaseAll(string(domain.ReleaseKillSwitch))
	}
	closeCtx, cancelClose := context.WithTimeout(context.Background(), k.timeout)
	defer cancelClose()
	errs = append(errs, runClosers(closeCtx, closers)...)

	err := errors.Join(errs...)
	if err != nil {
		k.log.Error("kill switch trigger finished with errors", "source", source, "err", err)
	}
	k.recordAudit("killswitch.trigger", source, start, err)
	return err
}

func (k *KillSwitchManager) Release() error {
	k.opMu.Lock()
	defer k.opMu.Unlock()
	start := k.clock.Now()
	if !k.Active() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), k.timeout)
	defer cancel()
	if err := k.persist(ctx, KillSwitchSnapshot{}); err != nil {
		k.recordAudit("killswitch.release", "", start, err)
		return err
	}
	k.mu.Lock()
	k.state = KillSwitchSnapshot{}
	k.mu.Unlock()
	k.recordAudit("killswitch.release", "", start, nil)
	return nil
}

func (k *KillSwitchManager) persist(ctx context.Context, st KillSwitchSnapshot) error {
	rec := killSwitchRecord{Active: st.Active, Source: st.Source}
	if st.Active {
		t := st.TriggeredAt.UTC()
		rec.TriggeredAt = &t
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := k.settings.Set(ctx, settingKillSwitchState, raw); err != nil {
		return fmt.Errorf("persist kill switch state: %w", err)
	}
	return nil
}

// runClosers は全クローザーを並行に呼び、ctx の期限までに終わらなかったものはエラーとして返す。
func runClosers(ctx context.Context, closers []namedCloser) []error {
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(closers))
	for _, c := range closers {
		go func() { results <- result{c.name, c.fn(ctx)} }()
	}
	var errs []error
	for range closers {
		select {
		case r := <-results:
			if r.err != nil {
				errs = append(errs, fmt.Errorf("closer %s: %w", r.name, r.err))
			}
		case <-ctx.Done():
			return append(errs, fmt.Errorf("kill switch closers did not finish: %w", ctx.Err()))
		}
	}
	return errs
}

func (k *KillSwitchManager) recordAudit(event, source string, at time.Time, err error) {
	if k.audit == nil {
		return
	}
	detail := map[string]any{}
	if source != "" {
		detail["source"] = source
	}
	took := k.clock.Now().Sub(at).Milliseconds()
	e := repository.AuditEntry{
		TS:          at,
		Kind:        "killswitch",
		Event:       event,
		ActorType:   domain.ActorHost,
		ClientLabel: hostLabel,
		Result:      auditOK,
		DurationMS:  &took,
		DetailJSON:  marshalDetail(detail),
	}
	if err != nil {
		e.Result = auditError
		e.ErrorCode = string(domain.CodeInternal)
	}
	k.audit.Record(context.Background(), e)
}

// hostLabel はホスト操作の client_label（docs/architecture/er.md）。
const hostLabel = "(host)"
