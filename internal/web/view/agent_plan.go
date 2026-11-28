package view

// このファイルは ChatGPT のプランを使う接続方式（公式 = chatgpt、Codex 経由 = chatgpt-codex。#247・FR-G25〜G30・FR-H11）の
// 画面の表示モデル。トークン・認可コード・ID トークンは、どの型にも持たせない（NFR-S13）。メールは本人の確認用の表示だけ。
// Rev は表示モデルのハッシュ（GET /poll が版の比較に使う。メールを含む表示モデルから作るが、ハッシュは秘密を含まない）。

// ConnectionOption は接続方式の選択肢 1 つ（FR-H11）。
type ConnectionOption struct {
	// Value は agent.connection の値（chatgpt / chatgpt-codex / openai_api_key）。
	Value       string
	Label       string
	Description string
	Current     bool
	// ConfirmURL があるときは、選ぶと確認モーダル（GET）を開く（openai_api_key への変更。FR-G30 (4)）。なければ PUT /agent/connection を送る。
	ConfirmURL string
}

// ConnectionPanel は接続方式の選択。
type ConnectionPanel struct {
	Options []ConnectionOption
	// Current は現在の接続方式（agent.connection）。
	Current string
}

// PlanModelOption はモデルの選択肢 1 つ（アカウントごとの一覧の slug と表示名）。
type PlanModelOption struct {
	Slug     string
	Label    string
	Selected bool
}

// PlanModelSection はカードのモデルの選択（FR-G26）。Options が空なら現在のモデルと「モデルを変更」ボタン（一覧の取得は押したとき）、
// あれば選択欄と保存ボタン。
type PlanModelSection struct {
	// ID は DOM の id（chatgpt-model / codex-model）。
	ID      string
	Current string
	// ModelsURL は一覧の取得（GET）、PutURL は保存（PUT）、CardID は保存の応答（カード）の差し込み先。
	ModelsURL string
	PutURL    string
	CardID    string
	Options   []PlanModelOption
}

// ChatGPTCard は公式の経路のカード（#chatgpt-card。FR-H11）。
type ChatGPTCard struct {
	// Selected は接続方式がこの経路（chatgpt）か。
	Selected bool
	// State は signed_out / signing_in / connected / reauth_required / usage_limited。
	State string
	// Problem は直近の失敗の分類（空は無し）。ProblemMessage は画面の説明（固定文）。
	Problem        string
	ProblemMessage string
	// OfferCodex は policy_denied のとき「Codex 経由で使う」ボタンを出す（押すと接続方式を chatgpt-codex に変える。自動では変えない）。
	OfferCodex bool
	Email      string
	Model      PlanModelSection
	// OpenAIKeyRegistered は「API キーに切り替える…」を出す条件（登録済みのときだけ。FR-G30 (3)）。
	OpenAIKeyRegistered bool
	// WelcomePending は初回のサインイン直後の確認（FR-G28 (7)）を出す。
	WelcomePending bool
	Rev            string
}

// CodexCard は Codex 経由の経路のカード（#codex-card。#247）。
type CodexCard struct {
	Selected bool
	// Engine は unsupported / missing / installing / installed / outdated / failed。
	Engine        string
	EngineVersion string
	// Progress は取得の進み（0〜100。installing のときだけ）。EngineError は failed の理由（固定文）。
	Progress    int
	EngineError string
	// State は signed_out / signing_in / connected / reauth_required / usage_limited。
	State          string
	Problem        string
	ProblemMessage string
	Email          string
	PlanType       string
	Model          PlanModelSection
	// OpenAIKeyRegistered は使用量の上限のとき「API キーに切り替える…」を出す条件。
	OpenAIKeyRegistered bool
	Rev                 string
}
