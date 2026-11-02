// Package remote はリモートルーター（tailnet 経由のブラウザ）の Web UI のハンドラ（docs/api/endpoints.md §4）。
//
// ページルート（GET /viewer）は HX-Request でフルページとフラグメントを出し分け、アクションルート（リース・設定・クリップボード・
// ショートカット）は常にフラグメントを返す。応答は常に HTML で、JSON は返さない。エラーは同じ code とステータスの
// フラグメント（data-error-code 付きのトースト。検証エラー 422 は入力値つきのフォーム）で返す。
//
// handler/gui を import せず、service と domain だけを使う（repository・platform・tailnet は使わない。scripts/check-imports.sh）。
// Host / Origin・CSRF ヘッダー・キルスイッチ・WhoIs・許可リストの検査は、ミドルウェア連鎖（web/middleware）が済ませている。
// ここは連鎖が認可したクライアント（middleware.ClientFrom）と、X-Viewer-Id で識別するビューアのセッション（viewer_id）に対して動く。
// クリップボードの本文は、ログにも監査にも出さない（文字数だけ）。
package remote
