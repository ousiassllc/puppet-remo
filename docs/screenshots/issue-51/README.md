# Issue #51: 狭いウィンドウ幅でサイドバーの固定幅によりページが横に広がる

`task gui:demo`（`internal/router/internal/guidemo`。偽の service・架空のデータ）を headless Chrome で撮影した（devicePixelRatio 1.25 なので画像は表示幅の 1.25 倍）。

| ファイル | 幅 | 内容 |
|---------|----|------|
| `before-360.png` | 360 | 修正前。サイドバーが 224px の固定幅で、ページ全体が 415px に広がる（`scrollWidth` 415 > `clientWidth` 360）。右が切れる |
| `after-360.png` | 360 | 修正後。ナビが本文の上に折り返して並び、ページは横に広がらない（`scrollWidth` 360） |
| `after-1280.png` | 1280 | 修正後。`md`（768px）以上は従来の左の固定幅のサイドバー |

修正後は 360・420・640・767・768・1024・1280 幅のダッシュボードで `scrollWidth == clientWidth`、360 幅の全 10 画面（ダッシュボード・接続要求・クライアント・起動許可アプリ・タスク・監査ログ・エージェント・設定・更新・ログ）でも同じ。
