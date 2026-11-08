package view

// Toast は完了の短い通知 1 件（保存しました、など）。エラーは通知にせず、インラインアラート（atoms.Alert）で出す。
type Toast struct {
	Message string
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
	NavApps      Nav = "apps"
	NavSettings  Nav = "settings"
	NavLogs      Nav = "logs"
	NavAudit     Nav = "audit"
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

// Lease はリース保持者の表示。CanRevoke のときだけ強制剥奪のボタンを出す（状態駆動）。
// Pending（承認待ち。pending_approval）のときは HolderLabel・KindLabel が要求者で、Held は false・CanRevoke も false
// （強制剥奪は held のときだけ。docs に承認待ちの剥奪の記述が無いため現行の動きを変えない）。
type Lease struct {
	Held          bool
	Pending       bool   // 承認待ち（FR-L07）。「承認待ち: <名前>（種別）」を出す
	CanRevoke     bool   // 剥奪できる保持者（mcp・web・agent。FR-L06・L08）がいる
	RevokeHint    string // 強制剥奪のボタンに添える説明（agent は「タスクが中断される」）。空なら出さない
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
	Tasks      RunningTasks // 実行中のタスク（M4。FR-H02）。空ならパネルは中身を出さない
}

// ApprovalModeOption は承認モードの選択肢（常時承認・初回のみ承認・自動許可。FR-H03）。
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
	// Modes は承認モードの選択肢（現在の値が Selected）。変更は UpdateURL へ PATCH する（approved_at が NULL に戻る。FR-H04）。
	Modes     []ApprovalModeOption
	UpdateURL string
	// FirstTimeStatus は first_time のときだけ、承認済み（日時）・未承認の表示。それ以外は空。
	FirstTimeStatus string
	// CanResetApproval は first_time で承認済みのときだけ true（承認の取り消しのボタン。確認は要らない）。
	CanResetApproval bool
	ResetApprovalURL string
	Flags            []ClientFlag
	DetailURL        string
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

// ConfirmModal は確認モーダル（サーバーが描画し、確定のボタンだけが実際の操作を送る）。
type ConfirmModal struct {
	Title      string
	Message    string
	ConfirmURL string
	Method     string // delete | put | post
	Vals       string // put のとき hx-vals に渡す JSON（空なら付けない）
	Target     string // 結果を差し込む先の CSS セレクタ
	Swap       string
	Label      string // 確定ボタンの文言
	// Destructive は取り返しのつかない操作（削除・失効・剥奪・中断）か。true なら確定ボタンは危険（--danger）、false なら主（O7）。
	// 既定は主（false）。破壊的な操作だけ true にする。
	Destructive bool
}

// SettingsValues は設定フォームの入力値（検証エラー時は入力されたままの文字列）。
type SettingsValues struct {
	Port             string
	Hotkey           string
	ScreenshotMaxDim string
	LeaseIdleSec     string
	ViewerQuality    string
	ViewerFPS        string
	// 監査ログ（FR-S07）。保持期間は日、容量の上限はバイト（単位は未決のため、保存値のまま表示する）。
	AuditThumbnails     bool
	AuditRecordText     bool
	AuditRetentionDays  string
	AuditThumbsMaxBytes string
	UpdateApplyMode     string // confirm | auto_idle（FR-S09）
	UpdateChannel       string // stable | nightly（FR-S09）
	Autostart           bool
}

// SettingsField は設定の項目別のエラー。
type SettingsErrors struct {
	Port             string
	Hotkey           string
	ScreenshotMaxDim string
	LeaseIdleSec     string
	ViewerQuality    string
	ViewerFPS        string
	// 監査ログ・更新の項目別のエラー（チェックボックスは検証エラーにならない）。
	AuditRetentionDays  string
	AuditThumbsMaxBytes string
	UpdateApplyMode     string
	UpdateChannel       string
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

// AppRow は起動許可アプリ 1 件の行。実行ファイルのパス・引数・作業ディレクトリは、ホスト所有者だけが見る GUI にだけ出す。
// Form が nil でなければ、行の代わりに編集フォームを出す（GET /apps/{id}/edit を開いたページ）。
type AppRow struct {
	ID         string
	Label      string
	ExePath    string
	Args       []string
	WorkingDir string // 空なら「実行ファイルのあるディレクトリ」と表示する
	// Corrupt は保存済みの引数が読めない行（domain.LaunchableApp.Corrupt）。引数の代わりに警告を出す。起動はされない。
	Corrupt bool
	// Unlaunchable は起動できない行（Corrupt、または domain.CheckLaunchTarget が拒否する手書きの値）。警告を出す。
	Unlaunchable bool
	EditURL      string
	// DeleteConfirm は削除の確認モーダルの URL。
	DeleteConfirm string
	Form          *AppForm
}

// AppList は起動許可アプリの一覧（登録順ではなく表示名の昇順）。
type AppList struct {
	Rows []AppRow
	// Count / Max は登録数と上限（NFR-C06）。
	Count, Max int
	// ListURL は一覧を取り直す URL（編集のキャンセルが使う）。
	ListURL string
}

// AppFormValues は追加・編集フォームの入力値（検証エラー時は入力されたままの文字列）。Args は 1 行 1 引数の文字列。
type AppFormValues struct {
	Label      string
	ExePath    string
	Args       string
	WorkingDir string
}

// AppFormErrors は追加・編集フォームの項目別のエラー。
type AppFormErrors struct {
	Label      string
	ExePath    string
	Args       string
	WorkingDir string
}

// AppForm は起動許可アプリの追加・編集フォーム。ID が空なら追加、そうでなければその行の編集。
// DOMID はフォーム要素の id で、追加と編集（行ごと）が同じページに並んでも重ならない値にする。
type AppForm struct {
	ID        string
	DOMID     string
	ActionURL string // 追加は POST /apps、編集は PUT /apps/{id}
	Values    AppFormValues
	Errors    AppFormErrors
	// PickURL はファイル選択の POST 先。CancelURL は編集のキャンセル（一覧の取り直し）。
	PickURL   string
	CancelURL string
	// Corrupt は編集する行の保存済みの引数が読めなかったこと（引数欄は空）。引数を入れ直すよう案内を出す。
	Corrupt bool
}

// Apps は起動許可アプリのページ（起動の方式・一覧・追加フォーム・拒否リスト）。
type Apps struct {
	Chrome  Chrome
	Mode    LaunchModePanel
	List    AppList
	NewForm AppForm
	Denied  DeniedSection
}

// LaunchModePanel は起動の方式（launch.mode）の表示と切り替え。方式の切り替えはホスト GUI だけにある（NFR-S20）。
// Denylist が false なら ホワイトリスト方式（既定）。切り替えの操作は、ブラックリストへは確認モーダル、ホワイトリストへは直接。
type LaunchModePanel struct {
	Denylist bool
	// ConfirmURL はブラックリスト方式へ切り替える確認モーダルの URL。ActionURL は切り替えの PUT 先（ホワイトリストへ戻すときに使う）。
	ConfirmURL string
	ActionURL  string
}

// DeniedRow は拒否リストの 1 件。Editing なら行の代わりに編集フォームを出す（EditValue・EditError はその入力）。
type DeniedRow struct {
	ID            string
	IsPath        bool   // パス（実体のパスで照合）か、実行ファイル名か
	Value         string // ホスト所有者だけが見る GUI にだけ出す
	EditURL       string
	DeleteConfirm string
	Editing       bool
	EditValue     string
	EditError     string
	ActionURL     string // 編集の PUT 先
}

// DeniedSection は拒否リストの一覧と追加フォーム（#denied-section。操作の応答はこの全体を差し替える）。
type DeniedSection struct {
	Rows []DeniedRow
	// Count / Max は登録数と上限。Active は拒否リストが効いている（ブラックリスト方式）こと。
	Count, Max int
	Active     bool
	AddValue   string // 追加フォームの入力値（検証エラー時は入力されたまま）
	AddError   string
	AddURL     string
	ListURL    string // 編集のキャンセルが取り直す URL
	// OOB は応答に添えて差し替える（方式の切り替えの応答）とき true。
	OOB bool
}

// AppExePathField は実行ファイルの入力欄。ファイル選択（POST /apps/pick-file）の応答が、この欄だけを差し替える。
// FormID は入力欄の id を作る元（フォームの識別子）で、追加と編集のフォームが並んでも id が重ならないようにする。
type AppExePathField struct {
	FormID  string
	Value   string
	Error   string
	PickURL string
}

// RequestsTab は接続要求画面のタブ（未処理の一覧と、履歴）。
type RequestsTab string

const (
	RequestsTabPending RequestsTab = "pending"
	RequestsTabHistory RequestsTab = "history"
)

// HistoryRow は接続要求の履歴 1 件。
type HistoryRow struct {
	ID          string
	DisplayName string
	LoginName   string
	RemoteIP    string
	ReceivedAt  string // 届いた日時（first_seen_at）
	Result      string // 結果の表示名（許可・拒否・未処理・失効）
	ResultTone  BadgeTone
	ResultCode  string // approved / denied / pending / expired
	DecidedAt   string // 処理した日時。許可・拒否だけ入る（それ以外は "-"）
}

// RequestHistory は接続要求の履歴の 1 ページ。PrevURL・NextURL が空なら、その向きのページは無い。
type RequestHistory struct {
	Rows    []HistoryRow
	Page    int
	PrevURL string
	NextURL string
}

// RequestsHistory は接続要求の履歴のページ。
type RequestsHistory struct {
	Chrome  Chrome
	History RequestHistory
}

// LogRow はアプリのログ（FR-H13）の 1 件。
type LogRow struct {
	Time      string
	Level     string // ファイルの表記（DEBUG・INFO・WARN・ERROR など）
	LevelTone BadgeTone
	Message   string
	Attrs     []LogAttr
}

// LogAttr はログに付いた属性（キー = 値）。
type LogAttr struct{ Key, Value string }

// LogFilterLink はレベルの絞り込みのリンク 1 つ。
type LogFilterLink struct {
	Code   string // all / warn / error
	Label  string
	URL    string
	Active bool
}

// AppLog はアプリのログの 1 ページ（絞り込みのリンク・表・ページ送り）。PrevURL・NextURL が空なら、その向きのページは無い。
type AppLog struct {
	Rows    []LogRow
	Page    int
	Filters []LogFilterLink
	Filter  string // 今の絞り込み（all / warn / error）
	PrevURL string
	NextURL string
}

// AppLogPage はログ画面のページ。Path は今のログファイルの場所。OpenFolderURL は「ログフォルダーを開く」ボタンの POST 先
// （固定の URL。パスは送らない。#54）。
type AppLogPage struct {
	Chrome        Chrome
	Path          string
	OpenFolderURL string
	Log           AppLog
}

// AuditFilterOption は監査ログの絞り込みの選択肢 1 つ。
type AuditFilterOption struct {
	Value    string
	Label    string
	Selected bool
}

// AuditFilterErrors は絞り込みの項目別のエラーメッセージ（空なら正常）。Paging は limit・cursor の不備（入力欄が無いので、まとめて出す）。
type AuditFilterErrors struct {
	From, To, Kind, Result, Paging string
}

// Any は、どれかの項目にエラーがあるか。
func (e AuditFilterErrors) Any() bool { return e != (AuditFilterErrors{}) }

// AuditFilters は絞り込みフォームの入力値（URL のクエリそのまま）と選択肢。Limit は既定以外を指定したときだけ入る。
type AuditFilters struct {
	From, To, Q, Limit string
	Clients            []AuditFilterOption // 先頭は「すべて」
	Kinds              []AuditFilterOption // 先頭は「すべて」
	Results            []AuditFilterOption // 先頭は「すべて」
	Errors             AuditFilterErrors
}

// AuditRow は監査ログの一覧の 1 行。Client はクライアント名のスナップショット（削除済みでも残る）。
type AuditRow struct {
	ID         int64
	DetailURL  string
	Time       string
	Client     string
	Kind       string
	Event      string
	Result     string
	ResultTone BadgeTone
	Duration   string // 無いときは "-"
	HasThumb   bool
}

// AuditList は監査ログの一覧の 1 ページ（絞り込み・表・「次へ」）。NextURL が空なら次のページは無い。
type AuditList struct {
	Filters AuditFilters
	Rows    []AuditRow
	NextURL string
}

// AuditListPage は監査ログ画面のページ。
type AuditListPage struct {
	Chrome Chrome
	List   AuditList
}

// AuditArg は詳細の引数 1 つ（キーと、秘密を伏せ終えた文字列の値）。
type AuditArg struct{ Key, Value string }

// AuditDetail は監査ログ 1 件の詳細ページ。ThumbURL が空ならサムネイルは無い（ThumbNote が理由）。
type AuditDetail struct {
	Chrome     Chrome
	ID         int64
	Time       string
	Kind       string
	Event      string
	Actor      string
	Client     string
	SessionID  string
	LeaseID    string
	Tool       string
	Result     string
	ResultTone BadgeTone
	ErrorCode  string
	Duration   string
	Args       []AuditArg
	ThumbURL   string
	ThumbW     string
	ThumbH     string
	ThumbNote  string
	BackURL    string
}

// 以下はリモートの Web UI（ビューア。docs/architecture/frontend.md §3・§4.3）の表示モデル。

// Option は選択欄の選択肢 1 つ。
type Option struct {
	Value    string
	Label    string
	Selected bool
}

// ViewerToolbarErrors は設定フォームの項目ごとのエラー（空なら無い）。
type ViewerToolbarErrors struct {
	Monitor string
	Quality string
	FPS     string
}

// ViewerToolbar はビューアのツールバー（モニター・画質・FPS。FR-W04・W05）。値はフォームの文字列で、
// 検証エラーのときは利用者の入力値をそのまま出す。
type ViewerToolbar struct {
	Monitors []Option
	Quality  string
	FPS      string
	Errors   ViewerToolbarErrors
}

// ViewerLease はビューアのリースパネル（FR-W08）。保持者の種別・名前・状態は常に出し、操作は Can* が true のものだけを描画する（状態駆動）。
// CanAcquire は空き、CanRelease は自分が保持、CanRevoke は他者（mcp・web・agent）が保持しているとき。
// Pending は承認待ち（pending_approval。FR-L07）で、HolderLabel・KindLabel が要求者、PendingMine が要求者本人。承認待ちの間は
// Held・Mine・Can* がすべて false（操作権を持たず、取得も解放も強制剥奪もできない）。
// Task は保持者が agent のとき、実行中のタスクの要約（FR-W08。読めたときだけ）。
type ViewerLease struct {
	Held        bool
	Mine        bool
	Pending     bool
	PendingMine bool
	HolderLabel string
	KindLabel   string // 保持者（承認待ちなら要求者）の種別（mcp / web / agent）
	CanAcquire  bool
	CanRelease  bool
	CanRevoke   bool
	Task        *ViewerTask
}

// ViewerTask はビューアのリースパネルに出す、実行中のエージェントのタスクの要約（FR-W08）。ビューアはタスクの依頼元とは別の
// クライアントなので、指示（prompt）と最終回答は持たない（状態・ステップ数・上限・開始時刻・依頼元の表示名だけ）。
type ViewerTask struct {
	Status    string // 状態の表示（承認待ち / 実行中）
	Steps     string // 「3 / 50」（実行したステップ数 / 上限）
	Elapsed   string // 経過時間（開始から。承認待ちは「-」）
	MaxTime   string // 実行時間の上限の表示
	StartedAt string // 開始時刻（HH:MM:SS。承認待ちは空）
	Requester string // 依頼元のクライアントの表示名
}

// KeyMenuItem はショートカット送信メニューの 1 項目。Keys は API 仕様 §3.3 の `keys` の書式。
type KeyMenuItem struct {
	Label string
	Keys  string
}

// ViewerOps はクリップボードとショートカット送信（操作権が要る操作。FR-W06・W07）。Enabled のときだけ描画する。
type ViewerOps struct {
	Enabled bool
	Keys    []KeyMenuItem
}

// Viewer はビューアのページ。ViewerID は X-Viewer-Id と ws-url に埋め込む。
type Viewer struct {
	ViewerID string
	WSURL    string
	Monitor  int
	Quality  int
	FPS      int
	Toolbar  ViewerToolbar
	Lease    ViewerLease
	Ops      ViewerOps
}

// RemoteError はリモートの Web UI のエラーページ。
type RemoteError struct {
	Code    string
	Title   string
	Message string
}

// ApprovalDialog は承認ダイアログ（/approvals/{id}。専用の Wails ウィンドウの中身。FR-H05）。表示するのはクライアント名・種別・
// 要求の内容・残り時間だけで、秘密・パス・引数は持たない。M4 の safety_check・judge_action は、サービスが秘密・入力した文字列の本文・
// パスを除いて渡す Detail（短い説明）を出す。
type ApprovalDialog struct {
	ID          string
	Kind        string // lease | safety_check | judge_action（data-approval-type）
	Heading     string // ダイアログの見出し（種類ごと）
	ClientLabel string
	KindLabel   string // 要求元の種別（mcp / web / エージェント）
	Summary     string // 要求の内容（種類ごとの 1 文）
	Detail      string // safety_check の安全チェックの文面・judge_action の対象アクションの要約。lease は空
	Note        string // 結果の説明（safety_check は「自動許可でも省略できない」と、拒否・時間切れでタスクが失敗すること）
	Remaining   ApprovalRemaining
	AllowURL    string
	DenyURL     string
}

// ApprovalRemaining は承認ダイアログの残り時間（HTMX が 1 秒ごとに再取得して差し替える）。
type ApprovalRemaining struct {
	ID         string
	Seconds    int    // 残り秒数（切り上げ。0 未満は 0）
	RefreshURL string // GET /approvals/{id}
}

// ApprovalDone は許可・拒否を受け付けたあとのダイアログの表示（ウィンドウは closeWindow で閉じる）。
type ApprovalDone struct {
	Allowed bool
}

// ApprovalGone は承認要求が終わっている（失効・解決済み・存在しない）ときのダイアログの表示。
type ApprovalGone struct {
	Code    string
	Message string
}
