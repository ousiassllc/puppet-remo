package appserver

import (
	"fmt"
	"regexp"
	"strings"
)

// tokenLike は資格情報らしいもの（JWT・OAuth のクライアント／リフレッシュトークン・API キー・Bearer）に当たる。
var tokenLike = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}(\.[A-Za-z0-9_-]+){0,2}|\b(?:sk|oaiapp|rt|at)[-_][A-Za-z0-9_-]{16,}|\b[Bb]earer\s+\S+`)

// Redact は app-server の文のうち、利用者に見せるものから資格情報らしいものを取り除く。
func Redact(s string) string { return tokenLike.ReplaceAllString(s, "[redacted]") }

// exitText は終了したプロセスの説明: 終了コードと標準エラーの末尾。app-server はトークンを出さないが、利用者が
// RUST_LOG を設定すると多く出しうるので、末尾も他の文と同じく Redact を通す。
func exitText(code int, tail string) string {
	msg := fmt.Sprintf("Codex app-server が終了しました（終了コード %d）", code)
	if t := strings.TrimSpace(tail); t != "" {
		msg += "\n--- 標準エラーの末尾 ---\n" + Redact(t)
	}
	return msg
}
