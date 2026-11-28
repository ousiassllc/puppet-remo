# #261・#262・#263 の before / after（Light / Dark × 1280 / 390）

`before-*` は develop `0242bfa`、`after-*` はこのブランチ。`task gui:demo` 相当（`internal/router/internal/guidemo`。偽の service）を Chromium（Playwright・`prefers-color-scheme`）で撮った全画面。

- dashboard: カードの題 16/600、キルスイッチの説明 14/400、ライトもダークもカードに外枠・題下の線なし（面の色 `#141414` / `#050505` で分ける。`after-dashboard-dark-1280.png` は枠を 0 にして撮り直した）、390 幅のヘッダー
- apps: 起動許可アプリの警告（左帯なし・アイコン＋`--surface-sel` 地）
- audit / tasks: 表の見出し 13/400 `--mute`・本文 14/400・行の区切り `--line-soft`
- `after-header-killswitch-{light,dark}-{390,360,320}.png`: キルスイッチ発動中（最も長いバッジ「キルスイッチ発動中」）のヘッダー。「ホスト」は sm 未満で sr-only、ロゴは 96px より縮めず、360px 未満（320）は文字ロゴの代わりにシンボル 24px（aria-label「Nefos Remote」）。1 段のまま（ヘッダー高 56px・横スクロールなし）
