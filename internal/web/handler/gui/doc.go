// Package gui はホスト GUI（Wails WebView）のハンドラ（docs/architecture/frontend.md §3・§5、API 仕様 §7）。
//
// GUI ルーター専用で、リモートルーターのハンドラとは互いに import しない。依存は service の型だけで、
// 必要な操作は本パッケージが利用者側のインターフェースとして定める（repository・tailnet には触れない）。
// 状態の分岐・権限の判定はここで行い、view に結果（Can*）として渡す。Templ は描画の有無を決めるだけ。
//
// 応答は常に HTML（JSON に切り替えない）。ページルートは HX-Request で分岐し（フルページ / フラグメント）、
// アクションルートは常にフラグメントを返す。失敗は data-error-code 付きのフラグメントを同じステータスで返し、
// HX-Retarget で #toast-root（トースト）へ差し込む。トークン平文を返す応答は Cache-Control: no-store。
package gui
