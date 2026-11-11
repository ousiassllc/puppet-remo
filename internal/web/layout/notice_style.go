package layout

import (
	"crypto/sha256"
	"encoding/base64"
)

// NoticeStyle は RemoteNotice の <style> の中身（固定の CSS）。
//
// 未登録デバイスには /static/* が 403 で、app.css も Web フォントも配信できない。そこで待機ページだけが、最小のスタイルをインラインで持つ
// （docs/architecture/frontend.md §8.4「承認済みの逸脱」・§7.1）。CSP は 'unsafe-inline' ではなく、この文字列の SHA-256 ハッシュ
// （NoticeStyleHash）を style-src に足して通す。1 バイトでも変えるとハッシュが変わるので、CSS はここ 1 か所にだけ書く。
//
// 値は app.css のトークンと同じ 16 進: --ink・--bone（ページの地。ライト #f5f5f4・ダーク #050505。--surface はカード用でここでは使わない）。余白は 4px 基準（--s-*）。
// フォントはシステムフォント（Web フォントは使わない）。見出しは xl 24px SemiBold、本文は base 16px Regular。
const NoticeStyle = `
:root { --ink: #050505; --page: #f5f5f4; }
@media (prefers-color-scheme: dark) { :root { --ink: #ffffff; --page: #050505; } }
body { margin: 0; padding: 16px; background: var(--page); color: var(--ink); font-family: system-ui, sans-serif; font-size: 16px; font-weight: 400; line-height: 1.8; }
main { max-width: 80rem; margin: 0 auto; }
h1 { margin: 0 0 8px; font-size: 24px; font-weight: 600; line-height: 1.5; }
p { margin: 0; }
p + p { margin-top: 16px; }
@media (min-width: 48rem) { body { padding: 24px; } }
`

// NoticeStyleHash は NoticeStyle の CSP のハッシュソース（'sha256-…'）。
func NoticeStyleHash() string {
	sum := sha256.Sum256([]byte(NoticeStyle))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}
