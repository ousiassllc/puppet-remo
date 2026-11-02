package service

import (
	"context"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは M4（ホスト内蔵エージェント。Issue #19）の service 層の契約: スライス間で共有する型とインターフェースだけを置く。
// 実装は Agent・FastPath（agent*.go）、RuleJudge（judge.go）、AgentSettings（agent_settings.go）。値・手順は
// docs/components/overview.md §5.5〜§5.6.1、functional.md FR-G / FR-H10・H11 / FR-S10〜S19 のとおり。

// 承認要求の種類（ApprovalRequest.Kind）に足す M4 の 2 つ（FR-H05）。
const (
	ApprovalKindSafetyCheck = "safety_check" // OpenAI の pending_safety_checks（FR-G08）。「自動許可」でも省略できない
	ApprovalKindJudgeAction = "judge_action" // Judge が承認を求めた個別アクション（FR-G12）
)

// 設定のキー（docs/architecture/er.md「settings のキー」。値の型と既定値は同じ表）。
const (
	SettingAgentEnabled          = "agent.enabled"
	SettingAgentConsentAt        = "agent.consent_at"
	SettingAgentModel            = "agent.model"
	SettingAgentMaxSteps         = "agent.max_steps"
	SettingAgentMaxDurationSec   = "agent.max_duration_sec"
	SettingAgentDenyProcesses    = "agent.deny_processes"
	SettingAgentApproveProcesses = "agent.approve_processes"
	SettingAgentKeepScreenshots  = "agent.keep_screenshots"

	SettingFastpathEnabled        = "agent.fastpath.enabled"
	SettingFastpathConsentAt      = "agent.fastpath.consent_at"
	SettingFastpathModel          = "agent.fastpath.model"
	SettingFastpathMinConfidence  = "agent.fastpath.min_confidence"
	SettingFastpathMaxSteps       = "agent.fastpath.max_steps"
	SettingFastpathExcludedLabels = "agent.fastpath.excluded_labels"
)

// AgentConfig はエージェント設定の読み取り結果（既定値を適用済み）。タスクの開始時に 1 回読み、そのタスクでは固定する。
type AgentConfig struct {
	Enabled          bool
	ConsentAt        *time.Time // OpenAI へのスクリーンショット送信への同意の日時（FR-G14）
	Model            string     // 既定 gpt-6.1-sol。文字列として扱い、プロバイダ・プランの追加（#49）はここを広げる
	MaxSteps         int        // 既定 50
	MaxDurationSec   int        // 既定 600
	DenyProcesses    []string
	ApproveProcesses []string
	KeepScreenshots  int // 既定 3
	Fastpath         FastpathConfig
}

// FastpathConfig は高速パスの設定（FR-S14〜S18）。
type FastpathConfig struct {
	Enabled        bool
	ConsentAt      *time.Time // TypeSafe へのデータ送信への同意の日時（FR-G22）。OpenAI の同意とは独立
	Model          string     // 既定 jev-1.13
	MinConfidence  float64    // 既定 0.7
	MaxSteps       int        // 既定 15
	ExcludedLabels []string
}

// AgentSettingsView は /agent 画面用の読み取り結果。API キーの値は決して含めない（登録の有無だけ）。
type AgentSettingsView struct {
	AgentConfig
	OpenAIKeyRegistered   bool
	TypeSafeKeyRegistered bool
}

// AgentSettingsInput は PUT /agent のフォーム入力（FR-H11）。改行区切りのリストは呼び出し側（GUI）が分割して渡す。
type AgentSettingsInput struct {
	Enabled          bool
	Consent          bool // Enabled を on にするときは必須（FR-G14）
	Model            string
	MaxSteps         int
	MaxDurationSec   int
	KeepScreenshots  int
	DenyProcesses    []string
	ApproveProcesses []string
}

// FastpathSettingsInput は PUT /agent/fastpath のフォーム入力（FR-G22。エージェントの Consent とは別）。
type FastpathSettingsInput struct {
	Enabled        bool
	Consent        bool
	Model          string
	MinConfidence  float64
	MaxSteps       int
	ExcludedLabels []string
}

// AgentSettings はホスト GUI の「エージェント設定」の窓口（FR-H11・G14・G22）。すべて操作主体 host で監査する。
// 項目の不備は *ValidationError（項目名 → メッセージ）、条件を満たさない有効化（API キー未登録など）は
// domain.ErrAgentDisabled（code agent_disabled）。HTTP ステータスは 409（API 仕様 §2 のエラーコード表と §7 の PUT /agent で同じ）。
// API キーの値はログ・エラー・監査・戻り値のどこにも出さない（NFR-S14）。
// SaveAgent で enabled = false にする保存は、有効または同意済みの状態からなら、他の欄の不備に妨げられない（他の欄は保存しない）。
type AgentSettings interface {
	Load(ctx context.Context) (AgentSettingsView, error)
	SaveAgent(ctx context.Context, in AgentSettingsInput) error
	SetOpenAIKey(ctx context.Context, key string) error
	DeleteOpenAIKey(ctx context.Context) error // エージェントを無効にし、実行中のタスクを中断する
	SaveFastpath(ctx context.Context, in FastpathSettingsInput) error
	SetTypeSafeKey(ctx context.Context, key string) error
	DeleteTypeSafeKey(ctx context.Context) error // 高速パスを無効にし consent_at を消す。実行中のタスクは失敗させず OpenAI で続行する
}

// AgentConfigReader は Agent・FastPath・RuleJudge が設定を読む窓口（AgentSettings の実装が満たす）。API キーの値は返さず、
// 登録の有無だけを返す。Config は既定値を適用済みで、保存済みの値が不正なら既定値を使って運用ログに警告する（HostSettings.Load と同じ規則）。
type AgentConfigReader interface {
	Config(ctx context.Context) (AgentConfig, error)
	OpenAIKeyRegistered(ctx context.Context) (bool, error)
	TypeSafeKeyRegistered(ctx context.Context) (bool, error)
}

// StartInput は run_task の引数（FR-G02）。MaxSteps・MaxDurationSec の 0 は設定値。設定値を上回る指定は設定値に丸めるのではなく
// domain.ErrInvalidArgument で拒否する（「下げる方向にのみ指定できる」）。Prompt は 1〜10,000 文字。
type StartInput struct {
	Prompt         string
	MaxSteps       int
	MaxDurationSec int
}

// Agent は MCP の run_task / get_task / cancel_task の窓口（docs §5.6）。by は依頼したクライアント（mcp の Actor）。
// Start は 1 秒以内に返り、リース（種別 agent）の取得とループは goroutine で動く。
// 条件を満たさない（エージェント無効・API キー未登録・クライアントの agent_enabled が無効）は domain.ErrAgentDisabled、
// キルスイッチ中は ErrKillSwitchActive、他者がリース保持中は ErrLeaseHeldByOther（いずれもタスクの行を作らない）。
// Get / Cancel は依頼したクライアント自身のタスクだけ（他者の・無いタスクは domain.ErrTaskNotFound）。
// Cancel は終了済みなら現在の状態をそのまま返す。
type Agent interface {
	Start(ctx context.Context, by domain.Actor, in StartInput) (domain.Task, error)
	Get(ctx context.Context, by domain.Actor, id string) (domain.Task, error)
	Cancel(ctx context.Context, by domain.Actor, id string) (domain.Task, error)
	// CancelAll はキルスイッチ・エージェントの無効化・クライアントの削除・無効化で実行中のタスクを中断する（reason は監査用）。
	CancelAll(reason string)
}

// TaskAdmin はホスト GUI（/tasks・ダッシュボード。FR-H10）の窓口。操作主体は host（依頼元のクライアントで絞らない）。
type TaskAdmin interface {
	// List はタスクを新しい順に 1 ページ返す（ページ送りは Cursor。TaskListQuery.Limit 0 は 50）。
	List(ctx context.Context, q TaskListQuery) (TaskPage, error)
	// Detail はタスクと各ステップ（監査ログの task_id 紐づけの行）。無ければ domain.ErrTaskNotFound。
	Detail(ctx context.Context, id string) (TaskDetail, error)
	// Running は実行中・承認待ちのタスク（ダッシュボード用。高々 1 件）。
	Running(ctx context.Context) ([]domain.Task, error)
	// CancelByHost は中断ボタン。終了済みなら現在の状態を返す。監査は host。
	CancelByHost(ctx context.Context, id string) (domain.Task, error)
}

// TaskListQuery・TaskPage は一覧の絞り込みとページ（repository.TaskQuery と同じ形。Cursor は不透明な文字列）。
type TaskListQuery struct {
	Cursor string
	Limit  int
}

// TaskPage は一覧の 1 ページ。Next が空なら最後のページ。
type TaskPage struct {
	Tasks []domain.Task
	Next  string
}

// TaskDetail はタスクの詳細。Steps は step_no の昇順。
type TaskDetail struct {
	domain.Task
	Steps []TaskStep
}

// TaskStepKind は TaskStep の種類。ステップ番号（No）は「実行したステップ」の番号で、1 つのステップに複数の行が付きうる
// （OpenAI の 1 回の応答の複数のアクション、そのアクションの前の Judge の判定・安全チェック）。番号を持つ（ステップ数に数える）
// のは action だけで、それ以外は直前に実行したステップ（まだ無ければ 0）か、これから実行するステップに付く補助の行。
type TaskStepKind string

const (
	TaskStepAction      TaskStepKind = "action"       // アクションを 1 つ実行した行（agent.step で tool がある）。ステップ数に数える
	TaskStepPass        TaskStepKind = "pass"         // 行動しない高速パス（reobserve・引き継ぎ）。番号は直前に実行したステップ
	TaskStepJudge       TaskStepKind = "judge"        // Judge の判定（agent.judge）。番号はそのアクションのステップ
	TaskStepSafetyCheck TaskStepKind = "safety_check" // OpenAI の安全チェックの承認（agent.safety_check）。番号はそれが止めるステップ
)

// TaskStep は 1 行（監査ログの行）。入力した文字列の本文・要素の値は含まない（FR-G23）。
type TaskStep struct {
	No      int
	Kind    TaskStepKind
	Route   string // "fast" | "openai"
	Event   string // agent.step など
	Result  string // 監査の result（ok / error / denied）
	Summary string // アクションの短い説明（本文なし）
	Args    []AuditArg
	AuditID int64 // /audit/{id}（サムネイルは監査の画面と同じ経路）。0 は無し
	Thumb   bool
	At      time.Time
}
