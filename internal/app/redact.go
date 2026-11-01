package app

import (
	"log/slog"
	"regexp"
	"strings"
)

// redactedValue は秘密を隠した跡に入れる値。
const redactedValue = "[REDACTED]"

// アプリ運用ログの秘密の安全網（NFR-S13）。呼び出し側が秘密を渡さないことが原則で、これは渡してしまったときのための二重の備え。
// ログへの書き込み（NewLogger の ReplaceAttr）と、ログ画面の読み出し（LogReader）の両方で使う。
var (
	// Bearer トークンの平文（接頭辞 prm_。NFR-S04）。
	tokenPattern = regexp.MustCompile(`prm_[A-Za-z0-9_-]{8,}`)
	// トークンのハッシュ（SHA-256 の 16 進。64 文字）。
	hexHashPattern = regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)
	// Authorization ヘッダーの値（"Bearer xxxx"。値は 20 文字以上の英数字と記号。"bearer authentication" のような普通の文は隠さない）。
	bearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{20,}`)
)

// sensitiveKeyParts は、キー名にこれを含む属性の値を丸ごと隠す（小文字で比較）。token_id のような ID は含まない。
var sensitiveKeyParts = []string{"secret", "password", "passwd", "apikey", "api_key", "api-key", "authorization", "plaintext", "hash"}

// sensitiveKey は属性のキー名が秘密を指すか。
func sensitiveKey(key string) bool {
	k := strings.ToLower(key)
	if k == "token" || strings.HasSuffix(k, "_token") || strings.HasSuffix(k, "-token") {
		return true
	}
	for _, p := range sensitiveKeyParts {
		if strings.Contains(k, p) {
			return true
		}
	}
	return false
}

// redactString は文字列に混ざったトークン・ハッシュ・Authorization の値を隠す。
func redactString(s string) string {
	if s == "" {
		return s
	}
	s = bearerPattern.ReplaceAllString(s, "Bearer "+redactedValue)
	s = tokenPattern.ReplaceAllString(s, redactedValue)
	return hexHashPattern.ReplaceAllString(s, redactedValue)
}

// redactAttr は slog.HandlerOptions.ReplaceAttr。秘密のキーの値を隠し、文字列（エラーを含む）の中のトークン・ハッシュを隠す。
func redactAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		return a
	}
	if sensitiveKey(a.Key) {
		return slog.String(a.Key, redactedValue)
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, redactString(v.String()))
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, redactString(err.Error()))
		}
	}
	return a
}
