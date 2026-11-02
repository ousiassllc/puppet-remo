package view

// NavUpdate は更新の画面のサイドバー項目。
const NavUpdate Nav = "update"

// UpdateLatest は見つかった新版の表示。外部（GitHub の Release）由来の文字列（Notes）を含む。描画側（templ）が HTML エスケープする。
type UpdateLatest struct {
	Version     string
	Notes       string // リリースノートの要約（外部由来）
	PublishedAt string
	// Verified は署名の検証に成功したか。false のときは Reject が利用者向けの理由。
	Verified bool
	Reject   string
}

// UpdatePanel は更新のパネル（現在の版・チャネル・最新の確認結果・操作）。
type UpdatePanel struct {
	CurrentVersion string
	ChannelLabel   string
	ApplyModeLabel string
	// Updatable は更新の対象のビルドか（dev は false で、確認・適用のボタンを出さない）。
	Updatable bool
	// LastChecked は最後に確認した時刻（未確認なら空）。
	LastChecked string
	// Error は最後の確認の失敗の、利用者向けの短い文（成功なら空）。
	Error string
	// KeyNotConfigured は署名鍵が未設定（確認はするが適用できない）。
	KeyNotConfigured bool
	Latest           *UpdateLatest
	// CanApply は「更新する」を出せる（署名検証済みの新版がある）。
	CanApply bool
	// Applying は適用中（インストーラを起動して終了を待っている）。
	Applying bool
	// ConfirmRoute は適用の確認モーダルの URL。
	ConfirmRoute string
}

// Update は更新のページ。
type Update struct {
	Chrome Chrome
	Panel  UpdatePanel
}
