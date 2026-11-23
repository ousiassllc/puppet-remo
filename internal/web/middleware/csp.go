package middleware

import (
	"net/http"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/web/layout"
)

// CSPValue は全 HTML 応答に付ける Content-Security-Policy（NFR-S19、docs/architecture/frontend.md §7.1）。
// インラインのスクリプト・スタイル・イベント属性は許さず、同一オリジンの外部ファイルだけを読む。
const CSPValue = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; " +
	"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// NoticeCSPValue は待機ページ（layout.RemoteNotice。Pending・Denied・Expired・RemoteError）の応答に付ける CSP。
// CSPValue との違いは style-src だけで、'self' の代わりに、インライン <style>（layout.NoticeStyle）の SHA-256 ハッシュを置く。
// 'unsafe-inline' は許さず、他のページの CSP は変えない。待機ページは /static を読まないので、style-src の 'self' も要らない。
var NoticeCSPValue = strings.Replace(CSPValue, "style-src 'self'", "style-src "+layout.NoticeStyleHash(), 1)

// SetNoticeCSP は、待機ページを書く応答のヘッダーを NoticeCSPValue にする（CSP ミドルウェアが付けた CSPValue を置き換える）。
func SetNoticeCSP(h http.Header) {
	h.Set("Content-Security-Policy", NoticeCSPValue)
}

// CSP は応答に Content-Security-Policy ヘッダーを付ける。GUI ルーター・リモートルーターの両方で、
// ミドルウェア連鎖の最外（hostcheck などが返すエラーページ・キルスイッチの 503 にも付く位置）に置く。
// HTML かどうかは判定しない（JSON・静的ファイルに付いても害はない）。<meta> タグでは frame-ancestors が効かないためヘッダーで付ける。
func CSP() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", CSPValue)
			next.ServeHTTP(w, r)
		})
	}
}
