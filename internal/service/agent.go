package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/openai"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルはホスト内蔵エージェント（Agent。docs/components/overview.md §5.6、FR-G01〜G15・G24、FR-L08）の窓口と
// タスクの寿命（Start・Get・Cancel・CancelAll・Close）。ループは agent_run.go、高速パスは fastpath.go。
//
// 実行状態（ループ・履歴）はメモリだけに持つ（NFR-A08）。タスクの記録（agent_tasks）はステップごとに更新し、終了状態は
// 1 回だけ書く。再起動で残った行の回収は Maintenance.AbortActiveTasks が行う。

// 上限（FR-G02・G13）。
const (
	maxAgentPromptRunes = 10_000
	maxAgentResultRunes = 10_000
)

// DefaultAgentModelTimeout は 1 回のモデル呼び出し（Responses API 1 回）の上限（FR-G04、NFR-S16。再試行しない）。
const DefaultAgentModelTimeout = 60 * time.Second

// agentCancelSettleWait は Cancel が、ループの終了を待つ最大の時間。モデルの HTTP は ctx で直ちに打ち切られるので、通常はすぐ終わる。
const agentCancelSettleWait = 2 * time.Second

// agentPersistTimeout はタスクの行・監査を書くときの上限。ループの ctx（キャンセル済みでもありうる）とは独立にする。
const agentPersistTimeout = 5 * time.Second

// 監査のイベント名（kind = agent。docs/architecture/er.md「イベントのカタログ」）。
const (
	agentTaskStartAuditEvent   = "agent.task_start"   // タスクの受付（Start）。detail は指示の文字数と適用した上限だけ
	agentTaskEndAuditEvent     = "agent.task_end"     // タスクの終了（1 回だけ）。最終回答の本文は入れない
	agentSafetyCheckAuditEvent = "agent.safety_check" // OpenAI の pending_safety_checks の承認の結果
	agentTaskCancelAuditEvent  = "agent.task_cancel"  // ホスト GUI の中断ボタン（TaskAdmin。actor_type = host）
)

// AgentClients は Agent が依頼元クライアントの agent_enabled を読む窓口（repository.Clients が満たす）。
type AgentClients interface {
	Get(ctx context.Context, id string) (domain.Client, error)
}

// AgentServiceConfig は NewAgent の設定。Audit・Bus・Fast・Logger 以外は必須。
type AgentServiceConfig struct {
	Tasks    repository.Tasks
	Clients  AgentClients
	Settings AgentConfigReader
	Control  Control
	Leases   LeaseManager
	Kill     KillSwitchState
	// Approval は OpenAI の安全チェック（FR-G08）の承認。Control の Judge の承認と同じものを渡す。nil なら安全チェックは常に拒否（fail-closed）。
	Approval Approval
	Model    openai.Model
	// Fast は高速パス（FR-G16）。nil なら常に OpenAI のみで実行する。
	Fast  *FastPath
	Audit AuditRecorder // nil なら記録しない
	// Bus は OpenAI の HTTP 401（キー無効）を GUI のトーストで知らせる先。nil なら通知しない。
	Bus    *Bus
	Clock  domain.Clock
	IDs    domain.IDGen
	Logger *slog.Logger // nil なら slog.Default()
	// ModelTimeout は 1 回のモデル呼び出しの上限。0 なら DefaultAgentModelTimeout。
	ModelTimeout time.Duration
}

// AgentService は Agent の実装。同時に動くタスクは 1 つだけ（リースが保証し、プロセス内でも 1 つに絞る）。
type AgentService struct {
	cfg AgentServiceConfig
	log *slog.Logger

	mu     sync.Mutex
	run    *run // 実行中（受付済みを含む）のタスク。無ければ nil
	closed bool
}

var _ Agent = (*AgentService)(nil)

// NewAgent は AgentService を作る。goroutine は起動しない（リースの剥奪の監視は Watch、タスクの goroutine は Start が起動する）。
func NewAgent(cfg AgentServiceConfig) (*AgentService, error) {
	switch {
	case cfg.Tasks == nil, cfg.Clients == nil, cfg.Settings == nil, cfg.Control == nil, cfg.Leases == nil, cfg.Kill == nil,
		cfg.Model == nil, cfg.Clock == nil, cfg.IDs == nil:
		return nil, errors.New("service: agent requires Tasks, Clients, Settings, Control, Leases, Kill, Model, Clock and IDs")
	}
	if cfg.ModelTimeout <= 0 {
		cfg.ModelTimeout = DefaultAgentModelTimeout
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &AgentService{cfg: cfg, log: log}, nil
}

// Start はタスクを受け付けて、リースの取得とループを goroutine で始める。返る時点のタスクは pending_approval
// （リースの取得とその承認待ち。取得できたら running になる）で、結果は Get で読む（FR-G02。NFR-P08: 1 秒以内に返る）。
//
// 検査の順序: 引数 → FR-G01 の条件（ErrAgentDisabled）→ キルスイッチ → 上限 → 他者のリース保持（ErrLeaseHeldByOther）・
// 実行中のタスク。いずれもタスクの行を作らない。条件の検査と行の作成の間に他者が取得した場合は、行を作ったあとの取得の失敗として
// タスクを failed（lease_held_by_other）にする。
func (a *AgentService) Start(ctx context.Context, by domain.Actor, in StartInput) (domain.Task, error) {
	if err := validateStartInput(in); err != nil {
		return domain.Task{}, err
	}
	cfg, err := a.checkConditions(ctx, by)
	if err != nil {
		return domain.Task{}, err
	}
	maxSteps, maxDur, err := appliedLimits(cfg, in)
	if err != nil {
		return domain.Task{}, err
	}

	id := a.cfg.IDs.NewID()
	runCtx, cancel := context.WithCancelCause(context.Background())
	r := &run{
		a: a, id: id, cfg: cfg, ctx: runCtx, cancel: cancel, done: make(chan struct{}),
		actor: domain.Actor{Kind: domain.ActorAgent, ClientID: by.ClientID, Label: by.Label, SessionID: id},
		task: domain.Task{
			ID: id, ClientID: by.ClientID, ClientLabel: by.Label, Prompt: in.Prompt, Status: domain.TaskPendingApproval,
			Model: cfg.Model, MaxSteps: maxSteps, MaxDurationSec: maxDur, CreatedAt: a.cfg.Clock.Now(),
		},
	}

	if err := a.reserve(r); err != nil {
		cancel(nil)
		return domain.Task{}, err
	}
	if err := a.cfg.Tasks.Create(ctx, r.task); err != nil {
		a.clear(r)
		cancel(nil)
		return domain.Task{}, fmt.Errorf("create the task: %w", err)
	}
	a.record(ctx, a.taskStartEntry(r))
	a.log.Info("agent task accepted", "task_id", id, "client_id", by.ClientID, "model", cfg.Model,
		"max_steps", maxSteps, "max_duration_sec", maxDur, "prompt_len", utf8.RuneCountInString(in.Prompt))
	accepted := r.task // goroutine が r.task を更新する前の写しを返す
	go r.execute()
	return accepted, nil
}

// validateStartInput は引数を検査する（FR-G02）。設定値との比較（下げる方向のみ）は appliedLimits。
func validateStartInput(in StartInput) error {
	n := utf8.RuneCountInString(in.Prompt)
	switch {
	case n < 1:
		return domain.ErrInvalidArgument.WithMsg("prompt must not be empty")
	case n > maxAgentPromptRunes:
		return domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("prompt must be at most %d characters", maxAgentPromptRunes))
	case in.MaxSteps < 0:
		return domain.ErrInvalidArgument.WithMsg("max_steps must not be negative")
	case in.MaxDurationSec < 0:
		return domain.ErrInvalidArgument.WithMsg("max_duration_sec must not be negative")
	}
	return nil
}

// checkConditions は FR-G01 の利用条件（設定で有効・同意済み・OpenAI キー登録済み・依頼元の agent_enabled）とキルスイッチを検査し、
// タスクの開始時の設定を返す。条件を満たさなければ ErrAgentDisabled、キルスイッチ中は ErrKillSwitchActive。
func (a *AgentService) checkConditions(ctx context.Context, by domain.Actor) (AgentConfig, error) {
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return AgentConfig{}, domain.ErrAgentDisabled.WithMsg("the app is shutting down")
	}
	if by.ClientID == "" {
		return AgentConfig{}, domain.ErrAgentDisabled
	}
	cfg, err := a.cfg.Settings.Config(ctx)
	if err != nil {
		return AgentConfig{}, fmt.Errorf("read the agent settings: %w", err)
	}
	if !cfg.Enabled || cfg.ConsentAt == nil {
		return AgentConfig{}, domain.ErrAgentDisabled
	}
	registered, err := a.cfg.Settings.OpenAIKeyRegistered(ctx)
	if err != nil {
		return AgentConfig{}, fmt.Errorf("check the OpenAI API key: %w", err)
	}
	if !registered {
		return AgentConfig{}, domain.ErrAgentDisabled
	}
	client, err := a.cfg.Clients.Get(ctx, by.ClientID)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return AgentConfig{}, domain.ErrAgentDisabled
	case err != nil:
		return AgentConfig{}, fmt.Errorf("read the client: %w", err)
	case !client.AgentEnabled:
		return AgentConfig{}, domain.ErrAgentDisabled
	}
	if a.cfg.Kill.Active() {
		return AgentConfig{}, domain.ErrKillSwitchActive
	}
	return cfg, nil
}

// appliedLimits はタスクに適用する上限を返す。指定は設定値を下げる方向にだけ使え、上回る指定は丸めずに ErrInvalidArgument（FR-G02）。
func appliedLimits(cfg AgentConfig, in StartInput) (maxSteps, maxDurationSec int, err error) {
	maxSteps, maxDurationSec = cfg.MaxSteps, cfg.MaxDurationSec
	if in.MaxSteps > 0 {
		if in.MaxSteps > cfg.MaxSteps {
			return 0, 0, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("max_steps must be at most %d (the host's limit)", cfg.MaxSteps))
		}
		maxSteps = in.MaxSteps
	}
	if in.MaxDurationSec > 0 {
		if in.MaxDurationSec > cfg.MaxDurationSec {
			return 0, 0, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("max_duration_sec must be at most %d (the host's limit)", cfg.MaxDurationSec))
		}
		maxDurationSec = in.MaxDurationSec
	}
	return maxSteps, maxDurationSec, nil
}

// reserve は r を実行中のタスクとして登録する。すでにタスクが動いている・他者がリースを保持中（承認待ちを含む）なら
// ErrLeaseHeldByOther を返し、何も登録しない。
func (a *AgentService) reserve(r *run) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return domain.ErrAgentDisabled.WithMsg("the app is shutting down")
	}
	if a.run != nil {
		return heldByOther(domain.ActorAgent, a.run.actor.Label)
	}
	if h, ok := a.cfg.Leases.Holder(); ok {
		return heldByOther(h.Actor.Kind, h.Actor.Label)
	}
	if p, ok := a.cfg.Leases.Pending(); ok {
		return heldByOther(p.Actor.Kind, p.Actor.Label)
	}
	a.run = r
	return nil
}

// heldByOther は LeaseMachine と同じ形（detail の holder_kind・holder_label）の ErrLeaseHeldByOther を作る。
func heldByOther(kind domain.ActorKind, label string) error {
	return domain.ErrLeaseHeldByOther.
		WithMsg(fmt.Sprintf("the lease is held by %q", kind)).
		WithDetail("holder_kind", string(kind)).
		WithDetail("holder_label", label)
}

// clear は r が実行中のタスクなら登録を外す。
func (a *AgentService) clear(r *run) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.run == r {
		a.run = nil
	}
}

func (a *AgentService) runByID(id string) *run {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.run != nil && a.run.id == id {
		return a.run
	}
	return nil
}

// RunningTasks は実行中（承認待ちを含む）のタスクの数（0 か 1）。更新の適用の可否（UpdateBusyTasks）が使う。
func (a *AgentService) RunningTasks() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.run != nil {
		return 1
	}
	return 0
}

// Get は by 自身が依頼したタスクを返す。他者の・無いタスクは domain.ErrTaskNotFound。
func (a *AgentService) Get(ctx context.Context, by domain.Actor, id string) (domain.Task, error) {
	return a.getTask(ctx, id, by.ClientID)
}

// getTask は id のタスクを返す。owner が空でなければ、依頼元が owner のタスクだけ（依頼元が削除済み・別のクライアントは ErrTaskNotFound）。
func (a *AgentService) getTask(ctx context.Context, id, owner string) (domain.Task, error) {
	t, err := a.cfg.Tasks.Get(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return domain.Task{}, taskNotFound(id)
	}
	if err != nil {
		return domain.Task{}, fmt.Errorf("read the task: %w", err)
	}
	if owner != "" && t.ClientID != owner {
		return domain.Task{}, taskNotFound(id)
	}
	return t, nil
}

func taskNotFound(id string) error { return fmt.Errorf("task %q: %w", id, domain.ErrTaskNotFound) }

// Cancel は by 自身のタスクを中断する（cancel_task。FR-G10）。終了済みなら現在の状態をそのまま返す。
func (a *AgentService) Cancel(ctx context.Context, by domain.Actor, id string) (domain.Task, error) {
	return a.cancelTask(ctx, id, by.ClientID, "cancel_task")
}

// CancelByHost はホスト GUI の中断ボタン（TaskAdmin が使う）。依頼元は問わない。
func (a *AgentService) CancelByHost(ctx context.Context, id string) (domain.Task, error) {
	return a.cancelTask(ctx, id, "", "host")
}

// cancelTask は id のタスクの ctx を取り消し、ループの終了（終了状態の保存）を短く待ってから現在の状態を返す。
func (a *AgentService) cancelTask(ctx context.Context, id, owner, reason string) (domain.Task, error) {
	t, err := a.getTask(ctx, id, owner)
	if err != nil {
		return domain.Task{}, err
	}
	if t.Status.Terminal() {
		return t, nil
	}
	r := a.runByID(id)
	if r == nil {
		return t, nil
	}
	r.cancel(cancelCause{reason: reason})
	timer := time.NewTimer(agentCancelSettleWait)
	defer timer.Stop()
	select {
	case <-r.done:
	case <-ctx.Done():
	case <-timer.C:
	}
	return a.getTask(ctx, id, owner)
}

// CancelAll は実行中のタスクの ctx を取り消す（キルスイッチ・エージェントの無効化・クライアントの削除と無効化・終了。FR-G10）。
// すぐ戻る（終了状態の保存はループの goroutine が行う）。reason は監査（agent.task_end の detail）に残る。
func (a *AgentService) CancelAll(reason string) {
	a.mu.Lock()
	r := a.run
	a.mu.Unlock()
	if r != nil {
		r.cancel(cancelCause{reason: reason})
	}
}

// CancelClient は clientID が依頼した実行中のタスクを中断する（クライアントの削除・無効化、agent_enabled の無効化。FR-G10）。
func (a *AgentService) CancelClient(clientID, reason string) {
	a.mu.Lock()
	r := a.run
	a.mu.Unlock()
	if r != nil && r.actor.ClientID == clientID {
		r.cancel(cancelCause{reason: reason})
	}
}

// Close は実行中のタスクを中断し、ループの終了（終了状態の保存・入力とリースの解放）を ctx の期限まで待つ。以後の Start は断る。
func (a *AgentService) Close(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	r := a.run
	a.mu.Unlock()
	if r == nil {
		return nil
	}
	r.cancel(cancelCause{reason: "shutdown"})
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for the agent task to finish: %w", ctx.Err())
	}
}

// Watch はリースの変化を購読し、実行中のタスクのリースが他者の操作（強制剥奪・アイドルタイムアウト・セッション切断・キルスイッチ）で
// 解放されたら、そのタスクを中断する（FR-L08。cancelled）。購読は Watch の中で済み、ctx の終了で戻る。
// Watch が動いていなくても、次のアクションの Control.Do が ErrLeaseRevoked / ErrLeaseRequired を返してタスクは終わる。
func (a *AgentService) Watch(ctx context.Context) <-chan struct{} {
	events := a.cfg.Leases.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case ev := <-events:
				a.onLeaseEvent(ev)
			case <-ctx.Done():
				return
			}
		}
	}()
	return done
}

func (a *AgentService) onLeaseEvent(ev domain.LeaseEvent) {
	// 承認待ちの終わり（From が pending_approval）は取得の失敗として Acquire が返すので、保持中のリースの解放だけを見る。
	// 保持者自身の解放（explicit）はタスクの終了処理。
	if ev.From != domain.LeaseHeld || ev.To != domain.LeaseIdle || ev.Reason == domain.ReleaseExplicit {
		return
	}
	a.mu.Lock()
	r := a.run
	a.mu.Unlock()
	if r != nil && ev.Lease.Holder.SameSession(r.actor) {
		reason := "lease_" + string(ev.Reason)
		if ev.Reason == domain.ReleaseKillSwitch {
			reason = "kill_switch" // キルスイッチの CancelAll（クローザー）と同じ理由にそろえる
		}
		r.cancel(cancelCause{reason: reason})
	}
}

// cancelCause はタスクの ctx を取り消した理由（context.Cause で読む）。
type cancelCause struct{ reason string }

func (c cancelCause) Error() string { return "agent task cancelled: " + c.reason }

// record は監査へ追記する（Audit が nil なら何もしない）。ctx のキャンセルは書き込みに影響しないが、念のため切り離す。
func (a *AgentService) record(ctx context.Context, e repository.AuditEntry) {
	if a.cfg.Audit != nil {
		a.cfg.Audit.Record(context.WithoutCancel(ctx), e)
	}
}
