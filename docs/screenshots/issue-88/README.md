# #88 起動の方式（ホワイトリスト / ブラックリスト）と拒否リスト

ホスト GUI の起動許可アプリ画面（`/apps`）。Windows 実機の撮影ではなく、`internal/app/internal/remotedemo`（偽の platform。実行ファイルは `FakeAppLauncher` に置いた架空のもの）を
ローカルで起動し、ヘッドレスの Chromium で操作して撮った（ホスト所有者の GUI の見た目の確認用。起動の実機確認は `docs/operations/smoke-windows.md` §4.16）。

| ファイル | 内容 |
| --- | --- |
| `apps-allowlist-mode.png` | 既定のホワイトリスト方式。拒否リストは「使われていない」旨が出る |
| `apps-switch-confirm.png` | 「ブラックリスト方式に切り替える…」の確認モーダル |
| `apps-denylist-mode.png` | ブラックリスト方式。拒否リストに実行ファイル名 2 件とパス 1 件。追加フォームは不正な入力（`..\bad`）の 422 の表示 |
