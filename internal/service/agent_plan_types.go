package service

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは ChatGPT のプランを使う 2 つの接続方式（公式の Sign in with ChatGPT = chatgpt、Codex の app-server = chatgpt-codex。
// FR-G25〜G30・#247）の service 層の契約: 状態・インターフェース・設定のキーだけを置く。
// 実装は ChatGPTAuthService（chatgpt_auth*.go）・CodexAuthService（codex_auth*.go）・AgentSettingsService（接続方式の切り替え）。
// トークン・認可コード・ID トークンは、ここのどの型にも持たせない（NFR-S13・S14）。

// 設定のキー（docs/architecture/er.md「settings のキー」）。
const (
	SettingAgentConnection     = "agent.connection"
	SettingChatGPTModel        = "agent.chatgpt.model"
	SettingChatGPTHostID       = "agent.chatgpt.host_id"
	SettingChatGPTClientID     = "agent.chatgpt.client_id"
	SettingChatGPTAccountSub   = "agent.chatgpt.account_sub"
	SettingChatGPTAccountEmail = "agent.chatgpt.account_email"
	SettingChatGPTWelcomeShown = "agent.chatgpt.welcome_shown"
	SettingCodexModel          = "agent.codex.model"
)

// 既定のモデル（FR-S21。Codex は #247）。
const (
	DefaultChatGPTModel = "gpt-6.1-terra"
	DefaultCodexModel   = "gpt-6.1-terra"
)

// PlanState はプランの接続の状態（FR-G28〜G30 の状態図。Codex も同じ語彙）。
type PlanState string

const (
	PlanSignedOut      PlanState = "signed_out"
	PlanSigningIn      PlanState = "signing_in"
	PlanConnected      PlanState = "connected"
	PlanReauthRequired PlanState = "reauth_required"
	PlanUsageLimited   PlanState = "usage_limited"
)

// PlanProblem は直近のサインイン・呼び出しの失敗の分類（GUI のカードの説明。秘密・本文を含まない）。空は「なし」。
type PlanProblem string

const (
	PlanProblemNone            PlanProblem = ""
	PlanProblemAccessDenied    PlanProblem = "access_denied"    // 利用者が同意しなかった
	PlanProblemScopeMissing    PlanProblem = "scope_missing"    // chatgpt.tokens.use.direct が付与されなかった（FR-G28 (6)）
	PlanProblemPolicyDenied    PlanProblem = "policy_denied"    // ワークスペースのポリシーで拒否（Codex の経路へ案内する。#247）
	PlanProblemNotEligible     PlanProblem = "not_eligible"     // subscription_sharing_user_not_eligible など（FR-G29 (4)・G30 (5)）
	PlanProblemAccountMismatch PlanProblem = "account_mismatch" // 再サインインで sub が保存済みと違う
	PlanProblemTimeout         PlanProblem = "timeout"          // サインインが 5 分で終わらなかった
	PlanProblemRevokeFailed    PlanProblem = "revoke_failed"    // 解除で ChatGPT 側の失効を確認できなかった（FR-G29 (6) ③）
	PlanProblemFailed          PlanProblem = "failed"           // その他の失敗（ネットワーク・検証など）
)

// PlanModel はアカウントごとのモデルの一覧の 1 件（FR-G26。GET /v1/models の slug・display_name / Codex の model/list）。
type PlanModel struct {
	Slug        string
	DisplayName string
}

// PlanGate は Agent・AgentSettings が見る、プランの接続方式の窓口（ChatGPTAuth・CodexAuth が満たす）。
type PlanGate interface {
	// Ready は run_task を受け付けられるか（FR-G01）。受け付けられなければ agent_disabled の detail.reason
	// （domain.AgentDisabledChatGPT* / AgentDisabledCodex*）を返す。ネットワークを使わない。
	Ready() (reason string, ok bool)
	// MarkUsageLimited は使用量の上限（FR-G30）。接続を usage_limited にする（メモリだけ。再起動で connected）。
	MarkUsageLimited()
	// ReportAuthFailure はモデルの呼び出しの認可エラー（FR-G29 (4)）。更新を 1 回だけ試し、使えなければ reauth_required にする。
	ReportAuthFailure(ctx context.Context)
	// MarkPolicyDenied はワークスペースのポリシーによる拒否（ErrPolicyDenied）。状態は変えず、カードに Codex への案内を出す。
	MarkPolicyDenied()
}

// ChatGPTStatus は公式の経路のカードの表示（FR-H11）。メール以外の秘密・識別子を持たない。
type ChatGPTStatus struct {
	State PlanState
	// AccountEmail はサインインしたアカウントのメール（この画面の本人確認の表示だけ。ログ・監査に出さない）。
	AccountEmail string
	// WelcomePending は初回のサインイン直後の確認（FR-G28 (7)）をまだ出していない。
	WelcomePending bool
	Problem        PlanProblem
}

// ChatGPTSignOutResult は解除の結果（FR-G29 (6)）。
type ChatGPTSignOutResult struct {
	Revoked       bool   // ChatGPT 側の失効を確認できた
	CancelledTask string // 中断した実行中のタスクの ID（無ければ空）
}

// ChatGPTAuth は公式の Sign in with ChatGPT の認可と接続の状態の唯一の持ち主（docs/components/overview.md §4.3.1）。
// GUI のハンドラは利用者側のインターフェースとして受け取る。
type ChatGPTAuth interface {
	PlanGate
	Status() ChatGPTStatus
	// StartSignIn は認可の要求をブラウザで開き、コールバックの受信口を開く（FR-G28）。状態が signed_out・reauth_required 以外は
	// domain.CodeConflict のエラー。ブラウザを開けなければ signed_out のまま失敗。サインインの完了は非同期（Status で読む）。
	StartSignIn(ctx context.Context) error
	// CancelSignIn は進行中のサインインを止めて受信口を閉じる（冪等）。
	CancelSignIn()
	// SignOut は解除（FR-G29 (6)）。状態が signed_out なら CodeConflict。失効の失敗でもローカルは消す。
	SignOut(ctx context.Context, forgetAccount bool) (ChatGPTSignOutResult, error)
	// Resume は usage_limited を connected に戻す（FR-G30 (3)）。usage_limited でなければ CodeConflict。
	Resume(ctx context.Context) error
	// DismissWelcome は初回の確認を閉じたことを保存する（FR-G28 (7)）。
	DismissWelcome(ctx context.Context) error
	// OpenUsage は使用量の設定（固定の https://chatgpt.com/settings/usage）を既定のブラウザで開く（FR-G30 (3)・NFR-S21）。
	OpenUsage(ctx context.Context) error
	// Models はアカウントのモデルの一覧（FR-G26。connected でなければ CodeConflict）。必要なら更新してから呼ぶ。
	Models(ctx context.Context) ([]PlanModel, error)
	// AccessToken は Responses API の Bearer（残り 5 分以下なら更新してから返す。排他。FR-G29 (2)）。openai の ChatGPT 用 Model が
	// 呼び出しのたびに読む（保持しない）。使えるトークンが無ければ openai.ErrPlanUnauthorized を包んだエラー。
	AccessToken(ctx context.Context) (string, error)
}

// CodexEngineState は Codex のエンジン（app-server の実行ファイル）の状態（#247）。
type CodexEngineState string

const (
	CodexEngineUnsupported CodexEngineState = "unsupported" // この OS・CPU には公式のエンジンが無い（Windows amd64 / arm64 だけ）
	CodexEngineMissing     CodexEngineState = "missing"     // まだ取得していない
	CodexEngineInstalling  CodexEngineState = "installing"  // 取得・検証の途中
	CodexEngineInstalled   CodexEngineState = "installed"   // 検証済みで使える
	CodexEngineOutdated    CodexEngineState = "outdated"    // 取得済みだが固定した版と違う（取り直しが要る）
	CodexEngineFailed      CodexEngineState = "failed"      // 直近の取得・検証・起動に失敗した（Error に理由）
)

// CodexEngineStatus はエンジンの表示。
type CodexEngineStatus struct {
	State    CodexEngineState
	Version  string // 固定した版（例: rust-v0.164.0）
	Progress int    // 取得の進み（0〜100。installing のときだけ）
	Error    string // failed のときの理由（利用者向けの日本語。パス・URL の秘密を含まない）
}

// CodexStatus は Codex のカードの表示（#247）。Codex のトークンはアプリが読まず、ここにも無い。
type CodexStatus struct {
	Engine       CodexEngineStatus
	State        PlanState
	AccountEmail string // app-server の account/read が返したメール（表示だけ）
	PlanType     string // 同じく planType（plus / pro / business など。表示だけ）
	Problem      PlanProblem
}

// CodexAuth は Codex の app-server の経路のエンジン・ログイン・接続の状態の唯一の持ち主（#247）。
type CodexAuth interface {
	PlanGate
	Status() CodexStatus
	// Install は固定した版のエンジンの取得を始める（非同期。検証は SHA-256・サイズ）。unsupported・installing なら CodeConflict。
	Install(ctx context.Context) error
	// CancelInstall は取得を止める（冪等）。
	CancelInstall()
	// StartSignIn は app-server の ChatGPT のログイン（ブラウザ）を始める。エンジンが無ければ CodeConflict。
	StartSignIn(ctx context.Context) error
	// CancelSignIn は進行中のログインを止める（冪等）。
	CancelSignIn(ctx context.Context)
	// SignOut は app-server にログアウトを頼み（実行中の Codex 経路のタスクは中断）、removeEngine なら取得したエンジンと
	// 専用の CODEX_HOME も消す。signed_out なら CodeConflict（removeEngine だけのときは除く）。
	SignOut(ctx context.Context, removeEngine bool) error
	// Resume は usage_limited を connected に戻す。usage_limited でなければ CodeConflict。
	Resume(ctx context.Context) error
	// Models は app-server の model/list（connected でなければ CodeConflict）。
	Models(ctx context.Context) ([]PlanModel, error)
}

// AgentConnectionSettings はホスト GUI の接続方式の切り替えとプランのモデルの保存（FR-H11・S20・S21。AgentSettingsService が満たす）。
type AgentConnectionSettings interface {
	// SetConnection は agent.connection を保存し、監査 agent.connection（from・to・reason）を残す。openai_api_key にするときは
	// confirmed が真（確認モーダルの確定）でなければ domain.ErrInvalidArgument、OpenAI キー未登録なら CodeConflict（FR-G30 (4)・NFR-S22）。
	// reason は usage_limit / manual（空は manual）。エージェントの有効・同意・トークンは変えない。
	SetConnection(ctx context.Context, conn domain.AgentConnection, confirmed bool, reason string) error
	// SavePlanModel は agent.chatgpt.model（chatgpt）か agent.codex.model（chatgpt-codex）を保存する。一覧との照合は呼び出し側
	// （GUI が Models の結果で 422）。名前の形の不備は *ValidationError。
	SavePlanModel(ctx context.Context, conn domain.AgentConnection, model string) error
}
