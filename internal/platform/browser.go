package platform

import "context"

// BrowserOpener はシステムの既定のブラウザで URL を開く（ChatGPT のサインイン・使用量の管理・Codex のログイン。FR-G28・G30・#247）。
// 開いてよい URL は呼び出し側（service）が固定した https の URL だけ（NFR-S21）。実装は https 以外を拒み、URL をログ・エラーに出さない
// （id_token_hint などを含みうる。NFR-S13）。Windows は ShellExecute の open。
type BrowserOpener interface {
	OpenURL(ctx context.Context, url string) error
}
