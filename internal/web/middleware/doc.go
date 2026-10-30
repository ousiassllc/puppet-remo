// Package middleware はリモートルーターのミドルウェア連鎖（docs/api/endpoints.md §1）を提供する。
//
// 連鎖の順序（いずれかで拒否したら以降は実行しない）:
//
//	hostcheck → csrfheader → killswitch → whois → allowlist → bearer（/mcp のみ）
//
// キルスイッチ（3）は WhoIs・許可リスト照合・接続要求の作成より前に置く（FR-A06、FR-H06）。
// CSP ヘッダーを付ける csp は別（両ルーターの最外）で、Remote の外側に置く。
//
// エラー応答の形式は経路で決まる: /api/v1/*・/mcp・/ws/* は problem+json、
// それ以外（Web UI・静的ファイル）は HTML（ページはエラーページ、状態変更はフラグメント）。
package middleware
