package view

// NavAgent はエージェント設定の画面のサイドバー項目。
const NavAgent Nav = "agent"

// AgentValues は OpenAI エージェント設定フォームの入力値（検証エラー時は入力されたままの文字列）。
type AgentValues struct {
	Enabled bool
	// Consent は OpenAI へのスクリーンショット送信への同意（FR-G14）。同意済み（consent_at がある）なら描画時にチェック済みにする。
	Consent          bool
	Model            string
	MaxSteps         string
	MaxDurationSec   string
	KeepScreenshots  string
	DenyProcesses    string // 改行区切り
	ApproveProcesses string // 改行区切り
}

// AgentErrors は OpenAI エージェント設定の項目別のエラー。General は項目に結び付かない不備のメッセージ。
type AgentErrors struct {
	Consent          string
	Model            string
	MaxSteps         string
	MaxDurationSec   string
	KeepScreenshots  string
	DenyProcesses    string
	ApproveProcesses string
	General          string
}

// AgentForm は OpenAI エージェント設定のフォーム。
type AgentForm struct {
	Values AgentValues
	Errors AgentErrors
	// ConsentAt は同意の日時（表示用。未同意なら空）。
	ConsentAt string
}

// FastpathValues は高速パス設定フォームの入力値。
type FastpathValues struct {
	Enabled bool
	// Consent は TypeSafe へのデータ送信への同意（FR-G22。OpenAI の同意とは別）。
	Consent        bool
	Model          string
	MinConfidence  string
	MaxSteps       string
	ExcludedLabels string // 改行区切り
}

// FastpathErrors は高速パス設定の項目別のエラー。
type FastpathErrors struct {
	Consent        string
	Model          string
	MinConfidence  string
	MaxSteps       string
	ExcludedLabels string
	General        string
}

// FastpathForm は高速パス設定のフォーム。
type FastpathForm struct {
	Values    FastpathValues
	Errors    FastpathErrors
	ConsentAt string
}

// APIKeyPanel は API キー（OpenAI・TypeSafe）の登録パネル。キーの値は決して持たない（登録の有無と、登録の失敗の理由だけ。NFR-S14）。
type APIKeyPanel struct {
	// ID はパネルの DOM の id（OOB・差し替えの対象）。
	ID string
	// Provider は表示名（OpenAI・TypeSafe）。
	Provider string
	// Target は資格情報マネージャーのターゲット名（表示用）。
	Target string
	// PutURL は登録（PUT）、ConfirmURL は削除の確認モーダル（GET）。
	PutURL     string
	ConfirmURL string
	Registered bool
	// Error は登録の失敗の理由（入力欄は常に空で描画する）。
	Error string
}

// AgentSettings はエージェント設定のページ。
type AgentSettings struct {
	Chrome      Chrome
	Agent       AgentForm
	AgentKey    APIKeyPanel
	Fastpath    FastpathForm
	FastpathKey APIKeyPanel
}
