// Package protocol は Codex の app-server の JSON-RPC のうち、アプリが使う部分: メソッド名と、送る・読むメッセージの項目。
// 手で書いた型で、固定した版（catalog）の app-server に合わせている。
//
// このパッケージのどの型にも、トークンの項目は無い: アプリは Codex のトークンを読まず、保存せず、返さない
// （account/read は更新を頼まず、アカウント・プラン・メールだけを答える。ログインの結果は URL）。
// これはテスト（TestTypesHaveNoTokenField）で強制する。
package protocol

import "encoding/json"

// アプリが送る要求。
const (
	MethodInitialize        = "initialize"
	MethodAccountRead       = "account/read"
	MethodLoginStart        = "account/login/start"
	MethodLoginCancel       = "account/login/cancel"
	MethodLogout            = "account/logout"
	MethodRateLimitsRead    = "account/rateLimits/read"
	MethodModelList         = "model/list"
	MethodThreadStart       = "thread/start"
	MethodThreadUnsubscribe = "thread/unsubscribe"
	MethodTurnStart         = "turn/start"
	MethodTurnInterrupt     = "turn/interrupt"
)

// NotifInitialized はハンドシェイクを終える通知（アプリが送る唯一の通知）。
const NotifInitialized = "initialized"

// アプリが読む通知。
const (
	NotifLoginCompleted = "account/login/completed"
	NotifAccountUpdated = "account/updated"
	NotifRateLimits     = "account/rateLimits/updated"
	NotifAgentDelta     = "item/agentMessage/delta"
	NotifItemStarted    = "item/started"
	NotifItemCompleted  = "item/completed"
	NotifTurnStarted    = "turn/started"
	NotifTurnCompleted  = "turn/completed"
	NotifError          = "error"
)

// サーバーからの要求のうち、アプリが扱うもの（item/tool/call だけ道具として受け、残りは拒否する）。
const (
	ReqCommandApproval     = "item/commandExecution/requestApproval"
	ReqFileChangeApproval  = "item/fileChange/requestApproval"
	ReqPermissionsApproval = "item/permissions/requestApproval"
	ReqToolUserInput       = "item/tool/requestUserInput"
	ReqMCPElicitation      = "mcpServer/elicitation/request"
	ReqDynamicToolCall     = "item/tool/call"
	ReqLegacyPatchApproval = "applyPatchApproval"
	ReqLegacyExecApproval  = "execCommandApproval"
	ReqAuthTokensRefresh   = "account/chatgptAuthTokens/refresh" //nolint:gosec // メソッド名で、資格情報ではない
	ReqAttestation         = "attestation/generate"
)

// LoginBrowser は account/login/start のブラウザのログイン。デバイスコード・API キー・トークンの受け渡しは使わない。
const LoginBrowser = "chatgpt"

// スレッド・ターンを、道具を使わない形に保つ値。
const (
	ApprovalNever   = "never"
	SandboxReadOnly = "read-only" // thread/start の SandboxMode
	PolicyReadOnly  = "readOnly"  // turn/start の SandboxPolicy の type
)

// ターンの状態（turn.status）。
const (
	TurnCompleted   = "completed"
	TurnFailed      = "failed"
	TurnInterrupted = "interrupted"
)

// 入力・項目の型。
const (
	ItemAgentMessage   = "agentMessage"
	ItemDynamicToolRun = "dynamicToolCall"
	PhaseFinalAnswer   = "final_answer"
	InputText          = "text"
	InputLocalImage    = "localImage"
	ContentInputText   = "inputText"
	ContentInputImage  = "inputImage"
	ToolTypeNamespace  = "namespace"
	ToolTypeFunction   = "function"
)

// BucketCodex は利用上限（limit id ごと）のうち Codex の枠のキー。
const BucketCodex = "codex"

// ClientInfoName・ClientInfoTitle はアプリを app-server へ名乗る名前（clientInfo）。
const (
	ClientInfoName  = "puppet-remo"
	ClientInfoTitle = "Puppet Remo"
)

// 起動引数 -c の設定のキーと値（TOML の値）。
const (
	ConfigCredentialsStore = "cli_auth_credentials_store" //nolint:gosec // 設定のキーで、資格情報ではない
	CredentialsKeyring     = "keyring"                    // OS の安全な保管場所。使えなければ失敗する（ファイルへは落とさない）
	ConfigWebSearch        = "web_search"
	WebSearchDisabled      = "disabled"
)

// ToolSettings は、画面操作の関数ツール以外のエージェントの道具（シェル・パッチ・画像の読み込み・Web 検索・アプリ・
// プラグイン・サブエージェント・メモリ・画像生成・ブラウザ・コンピューターの操作）を切る -c の設定。
// 第 1 層で、読み取り専用の sandbox・approvalPolicy never・サーバー要求の拒否・道具を使った項目でのターンの中断が残りの層
// （機能名は版で変わりうるので、未知の名前は app-server が無視する前提で何重にも掛ける）。
var ToolSettings = []string{
	ConfigWebSearch + `="` + WebSearchDisabled + `"`,
	"features.shell_tool=false",
	"features.unified_exec=false",
	"features.apply_patch_freeform=false",
	"features.view_image=false",
	"features.js_repl=false",
	"features.code_mode=false",
	"features.multi_agent=false",
	"features.collab=false",
	"features.apps=false",
	"features.plugins=false",
	"features.memories=false",
	"features.image_generation=false",
	"features.browser_use=false",
	"features.computer_use=false",
}

// ClientInfo は initialize の clientInfo。
type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// Capabilities は initialize の capabilities。ExperimentalAPI は dynamicTools（item/tool/call）を使うために真にする。
type Capabilities struct {
	ExperimentalAPI bool `json:"experimentalApi"`
}

// InitializeParams は initialize のパラメーター。
type InitializeParams struct {
	ClientInfo   ClientInfo    `json:"clientInfo"`
	Capabilities *Capabilities `json:"capabilities,omitempty"`
}

// InitializeResult は initialize の答え。CodexHome はサーバーが使う CODEX_HOME で、アプリ専用のものか確かめる。
type InitializeResult struct {
	CodexHome string `json:"codexHome"`
}

// GetAccountParams は account/read のパラメーター。RefreshToken は常に false（Codex に更新を頼まない）。
type GetAccountParams struct {
	RefreshToken bool `json:"refreshToken"`
}

// Account はログイン中のアカウント（表示用のメールとプランだけ）。
type Account struct {
	Type     string `json:"type"`
	Email    string `json:"email,omitempty"`
	PlanType string `json:"planType,omitempty"`
}

// GetAccountResult は account/read の答え。Account が nil は「ログインしていない」。
type GetAccountResult struct {
	Account            *Account `json:"account"`
	RequiresOpenAIAuth bool     `json:"requiresOpenaiAuth"`
}

// LoginParams は account/login/start のパラメーター。
type LoginParams struct {
	Type string `json:"type"`
}

// LoginResult は account/login/start の答え: ブラウザのログインの URL。
type LoginResult struct {
	Type    string `json:"type"`
	LoginID string `json:"loginId"`
	AuthURL string `json:"authUrl,omitempty"`
}

// CancelLoginParams は account/login/cancel のパラメーター。
type CancelLoginParams struct {
	LoginID string `json:"loginId"`
}

// LoginCompleted は通知 account/login/completed。
type LoginCompleted struct {
	Success bool    `json:"success"`
	LoginID *string `json:"loginId"`
	Error   *string `json:"error"`
}

// AccountUpdated は通知 account/updated。ログアウト後は AuthMode が nil。
type AccountUpdated struct {
	AuthMode *string `json:"authMode"`
	PlanType *string `json:"planType"`
}

// ModelListParams は model/list のパラメーター。
type ModelListParams struct {
	Cursor *string `json:"cursor,omitempty"`
}

// Model はアカウントが使えるモデル。Model が thread/start に渡す名前。
type Model struct {
	ID          string `json:"id"`
	Model       string `json:"model"`
	DisplayName string `json:"displayName"`
	Hidden      bool   `json:"hidden"`
	IsDefault   bool   `json:"isDefault"`
}

// ModelListResult は model/list の答え。
type ModelListResult struct {
	Data       []Model `json:"data"`
	NextCursor *string `json:"nextCursor"`
}

// DynamicToolFunction は namespace の中の関数 1 つ（dynamicTools）。
type DynamicToolFunction struct {
	Type        string         `json:"type"` // ToolTypeFunction
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// DynamicToolSpec は thread/start の dynamicTools の要素: 関数をまとめる namespace。
type DynamicToolSpec struct {
	Type        string                `json:"type"` // ToolTypeNamespace
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Tools       []DynamicToolFunction `json:"tools"`
}

// ThreadStartParams は thread/start のパラメーター: ディスクへ書かない（ephemeral）・読み取り専用・承認を求めない・
// 空の作業ディレクトリのスレッド。システムプロンプトは BaseInstructions（Codex 既定のコーディング用の指示を置き換える）。
type ThreadStartParams struct {
	Model            string            `json:"model,omitempty"`
	Ephemeral        bool              `json:"ephemeral"`
	BaseInstructions *string           `json:"baseInstructions,omitempty"`
	Cwd              string            `json:"cwd,omitempty"`
	Sandbox          string            `json:"sandbox,omitempty"`
	ApprovalPolicy   string            `json:"approvalPolicy,omitempty"`
	DynamicTools     []DynamicToolSpec `json:"dynamicTools,omitempty"`
}

// ThreadStartResult は thread/start の答え。
type ThreadStartResult struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

// ThreadRef は thread/unsubscribe のパラメーター。
type ThreadRef struct {
	ThreadID string `json:"threadId"`
}

// TextInput はターンの入力のテキスト。
type TextInput struct {
	Type string `json:"type"` // InputText
	Text string `json:"text"`
}

// LocalImageInput はターンの入力の画像: このマシンのファイル。
type LocalImageInput struct {
	Type string `json:"type"` // InputLocalImage
	Path string `json:"path"`
}

// SandboxPolicy は turn/start の sandboxPolicy。
type SandboxPolicy struct {
	Type string `json:"type"`
}

// TurnStartParams は turn/start のパラメーター。ポリシーはターンでも指定し、スレッドより緩くならないようにする。
type TurnStartParams struct {
	ThreadID       string         `json:"threadId"`
	Input          []any          `json:"input"` // TextInput・LocalImageInput
	ApprovalPolicy string         `json:"approvalPolicy,omitempty"`
	SandboxPolicy  *SandboxPolicy `json:"sandboxPolicy,omitempty"`
}

// TurnRef は答えに含まれるターン。
type TurnRef struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// TurnStartResult は turn/start の答え。
type TurnStartResult struct {
	Turn TurnRef `json:"turn"`
}

// TurnInterruptParams は turn/interrupt のパラメーター。
type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// AgentDelta は通知 item/agentMessage/delta。
type AgentDelta struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

// Item はスレッドの項目。アプリはエージェントのメッセージだけを読む（それ以外の型は道具の使用）。
type Item struct {
	Type  string  `json:"type"`
	ID    string  `json:"id"`
	Text  string  `json:"text"`
	Phase *string `json:"phase"`
}

// ItemNotification は通知 item/started・item/completed。
type ItemNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Item     Item   `json:"item"`
}

// TurnError は失敗したターンのエラー。
type TurnError struct {
	Message        string    `json:"message"`
	CodexErrorInfo ErrorInfo `json:"codexErrorInfo"`
}

// TurnResult は turn/completed のターン。
type TurnResult struct {
	ID     string     `json:"id"`
	Status string     `json:"status"`
	Error  *TurnError `json:"error"`
}

// TurnCompletedNotification は通知 turn/completed。
type TurnCompletedNotification struct {
	ThreadID string     `json:"threadId"`
	Turn     TurnResult `json:"turn"`
}

// ErrorNotification は通知 error。WillRetry が真の間はサーバーが自分で再試行している。
type ErrorNotification struct {
	ThreadID  string    `json:"threadId"`
	TurnID    string    `json:"turnId"`
	WillRetry bool      `json:"willRetry"`
	Error     TurnError `json:"error"`
}

// DynamicToolCallParams はサーバーの要求 item/tool/call のパラメーター。Arguments は関数の引数（JSON）。
type DynamicToolCallParams struct {
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId"`
	CallID    string          `json:"callId"`
	Namespace string          `json:"namespace"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

// ContentItem は item/tool/call の応答の内容 1 件: inputText（Text）か inputImage（ImageURL）。
type ContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"imageUrl,omitempty"`
}

// DynamicToolResponse は item/tool/call への応答。
type DynamicToolResponse struct {
	ContentItems []ContentItem `json:"contentItems"`
	Success      bool          `json:"success"`
}

// RateLimitWindow は利用上限の窓 1 つ。
type RateLimitWindow struct {
	UsedPercent int    `json:"usedPercent"`
	ResetsAt    *int64 `json:"resetsAt"` // Unix 秒
}

// RateLimitSnapshot は 1 つの上限の使用状況。
type RateLimitSnapshot struct {
	Primary   *RateLimitWindow `json:"primary"`
	Secondary *RateLimitWindow `json:"secondary"`
	PlanType  *string          `json:"planType"`
}

// RateLimitsResult は account/rateLimits/read の答え。
type RateLimitsResult struct {
	RateLimits          RateLimitSnapshot            `json:"rateLimits"`
	RateLimitsByLimitID map[string]RateLimitSnapshot `json:"rateLimitsByLimitId"`
}

// Codex は Codex の枠のスナップショット（サーバーが名前を付けていればその枠、無ければ主の枠）。
func (r RateLimitsResult) Codex() RateLimitSnapshot {
	if s, ok := r.RateLimitsByLimitID[BucketCodex]; ok {
		return s
	}
	return r.RateLimits
}

// Types は検査（トークンの項目が無いこと）の対象にする型の一覧。型を足したらここにも足す。
var Types = []any{
	ClientInfo{}, Capabilities{}, InitializeParams{}, InitializeResult{}, GetAccountParams{}, Account{}, GetAccountResult{},
	LoginParams{}, LoginResult{}, CancelLoginParams{}, LoginCompleted{}, AccountUpdated{}, ModelListParams{}, Model{},
	ModelListResult{}, DynamicToolFunction{}, DynamicToolSpec{}, ThreadStartParams{}, ThreadStartResult{}, ThreadRef{},
	TextInput{}, LocalImageInput{}, SandboxPolicy{}, TurnStartParams{}, TurnRef{}, TurnStartResult{}, TurnInterruptParams{},
	AgentDelta{}, Item{}, ItemNotification{}, TurnError{}, TurnResult{}, TurnCompletedNotification{}, ErrorNotification{},
	DynamicToolCallParams{}, ContentItem{}, DynamicToolResponse{}, RateLimitWindow{}, RateLimitSnapshot{}, RateLimitsResult{},
}
