package view

// ToastLevel はトーストの種別。
type ToastLevel string

const (
	ToastInfo  ToastLevel = "info"
	ToastError ToastLevel = "error"
)

// Toast は通知 1 件。ErrorCode が空でなければ data-error-code に出す。
type Toast struct {
	Level     ToastLevel
	Message   string
	ErrorCode string
}

// Header はヘッダー・サイドバーの全画面共通の表示（キルスイッチの状態と、接続要求の件数）。
type Header struct {
	KillSwitchActive bool
	PendingRequests  int
}

// Nav はサイドバーの項目。
type Nav string

const (
	NavDashboard Nav = "dashboard"
	NavRequests  Nav = "requests"
	NavClients   Nav = "clients"
	NavSettings  Nav = "settings"
)

// Chrome は全画面共通の枠（ヘッダー・サイドバー）の表示。
type Chrome struct {
	Active Nav
	Header Header
}

// BadgeTone はバッジの色の種類。
type BadgeTone string

const (
	ToneNeutral BadgeTone = "neutral"
	ToneOK      BadgeTone = "ok"
	ToneWarn    BadgeTone = "warn"
	ToneDanger  BadgeTone = "danger"
)

// ServerStatus はダッシュボードのサーバー状態（FR-H02）。
type ServerStatus struct {
	TailscaledLabel string
	TailscaledTone  BadgeTone
	TailnetIPs      []string
	DNSName         string
	Port            int
	ListenLabel     string
	ListenTone      BadgeTone
	ListenAddrs     []string
	Reason          string
}

// Session は接続中のセッション 1 件。
type Session struct {
	ClientLabel string
	KindLabel   string
	Since       string
}

// Sessions は接続中セッション一覧。
type Sessions struct{ Items []Session }

// Lease はリース保持者の表示（M1 は表示のみ。強制剥奪の操作は持たない）。
type Lease struct {
	Held          bool
	HolderLabel   string
	KindLabel     string // 保持者の種別（mcp など）
	Since         string
	IdleRemaining string
}

// KillSwitch はキルスイッチの表示。Active のときは解除だけ、そうでなければ発動だけを出す（状態駆動）。
type KillSwitch struct {
	Active       bool
	Since        string
	Source       string
	CanTrigger   bool
	CanRelease   bool
	ConfirmRoute string // 解除の確認モーダルの URL
	Rev          string // 表示の版（ポーリングが送り返す。変わっていなければ差し替えない）
}

// Dashboard はダッシュボード。
type Dashboard struct {
	Chrome     Chrome
	Status     ServerStatus
	Sessions   Sessions
	Lease      Lease
	KillSwitch KillSwitch
}

// ApprovalModeOption は承認モードの選択肢。M1 は「常時承認」だけを出す（他は M3 で選択肢を足す）。
type ApprovalModeOption struct {
	Value    string
	Label    string
	Selected bool
}

// RequestRow は接続要求 1 件。
type RequestRow struct {
	ID          string
	DisplayName string
	LoginName   string
	RemoteIP    string
	Count       int
	FirstSeen   string
	LastSeen    string
	ExpiresAt   string
	ApproveURL  string
	DenyURL     string
	Modes       []ApprovalModeOption
}

// RequestList は接続要求の一覧。
type RequestList struct {
	Rows []RequestRow
	Rev  string // 一覧の版（ポーリングが送り返す。変わっていなければ差し替えない。入力中のフォームを消さないため）
}

// Requests は接続要求のページ。
type Requests struct {
	Chrome Chrome
	List   RequestList
}

// Client はクライアント 1 件のカード。
type Client struct {
	ID          string
	DisplayName string
	LoginName   string
	NodeID      string
	Approved    string // 承認モードの表示（「常時承認」など）
	Flags       []ClientFlag
	DetailURL   string
	// CanDelete / CanOpenDetail は、一覧のカードだけが true（詳細ページでは出さない）。
	CanDelete     bool
	CanOpenDetail bool
	DeleteConfirm string
}

// ClientFlag は機能の有効・無効の切り替え 1 つ。現在の状態の逆だけを操作として出す。
type ClientFlag struct {
	Field     string // web_enabled | mcp_enabled | agent_enabled
	Label     string
	Enabled   bool
	ToggleURL string // PATCH 先
	NextValue string // 押すと送る値（"true" / "false"）
}

// ClientList はクライアントの一覧。
type ClientList struct{ Items []Client }

// Clients はクライアント管理のページ。
type Clients struct {
	Chrome Chrome
	List   ClientList
}

// Token は発行済みトークン 1 件（接頭辞だけ。平文は持たない）。
type Token struct {
	ID         string
	Label      string
	Prefix     string
	Status     string // 有効 / 失効済み / 期限切れ
	StatusTone BadgeTone
	CreatedAt  string
	ExpiresAt  string // 「無期限」を含む
	LastUsedAt string
	// CanRevoke は有効なトークンだけ true。
	CanRevoke     bool
	RevokeConfirm string
}

// TokenList は 1 クライアントのトークン一覧。
type TokenList struct {
	ClientID string
	Items    []Token
}

// ExpiryOption はトークンの有効期限の選択肢。
type ExpiryOption struct {
	Value    string // 日数、または never
	Label    string
	Selected bool
}

// TokenForm はトークン発行フォーム。
type TokenForm struct {
	ActionURL string
	Label     string
	Expiry    []ExpiryOption
	Error     string // 項目別のエラー（422）。空なら描画しない
}

// ClientDetail はクライアント詳細のページ。
type ClientDetail struct {
	Chrome  Chrome
	Client  Client
	Tokens  TokenList
	Form    TokenForm
	BackURL string
}

// TokenReveal は発行直後に一度だけ出すトークン平文。
type TokenReveal struct {
	Label     string
	Plaintext string
	ExpiresAt string
}

// ConfirmModal は破壊的な操作の確認モーダル（サーバーが描画し、確定のボタンだけが実際の操作を送る）。
type ConfirmModal struct {
	Title      string
	Message    string
	ConfirmURL string
	Method     string // delete | post
	Target     string // 結果を差し込む先の CSS セレクタ
	Swap       string
	Label      string // 確定ボタンの文言
}

// SettingsValues は設定フォームの入力値（検証エラー時は入力されたままの文字列）。
type SettingsValues struct {
	Port             string
	Hotkey           string
	ScreenshotMaxDim string
	LeaseIdleSec     string
	Autostart        bool
}

// SettingsField は設定の項目別のエラー。
type SettingsErrors struct {
	Port             string
	Hotkey           string
	ScreenshotMaxDim string
	LeaseIdleSec     string
}

// SettingsForm は設定フォーム。
type SettingsForm struct {
	Values SettingsValues
	Errors SettingsErrors
	// CanSetAutostart は自動起動の切り替えが使えるとき（OS の登録手段があるとき）だけ true。
	CanSetAutostart bool
}

// Settings は設定のページ。
type Settings struct {
	Chrome Chrome
	Form   SettingsForm
}

// ErrorPage はエラーページ（ページルートの 404 など）。
type ErrorPage struct {
	Chrome  Chrome
	Code    string
	Title   string
	Message string
}
