# Issue #54: ログ画面からログフォルダーを開く

`task gui:demo`（`internal/router/internal/guidemo`。偽の service・架空のデータ）を headless Chrome で撮影した。
デモは偽の「フォルダーを開く」を使うので、実際のファイルマネージャーは起動しない（記録するだけ）。Windows 実機の確認は `docs/operations/smoke-windows.md` の §4.14（未確認）。

| ファイル | 幅 | 内容 |
|---------|----|------|
| `logs-button.png` | 1100 | ログファイルのパスの右に「ログフォルダーを開く」ボタン |
| `logs-toast.png` | 1100 | ボタンを押した直後。「ログフォルダーを開きました。」のトースト |
| `logs-360.png` | 360 | 狭い幅（#51 のレイアウト）。ページは横に広がらない（`scrollWidth` 360） |
