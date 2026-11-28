# #261・#262・#263 の before / after（Light / Dark × 1280 / 390）

`before-*` は develop `0242bfa`、`after-*` はこのブランチ。`task gui:demo` 相当（`internal/router/internal/guidemo`。偽の service）を Chromium（Playwright・`prefers-color-scheme`）で撮った全画面。

- dashboard: カードの題 16/600、キルスイッチの説明 14/400、ライトのカードに外枠・題下の線なし、ダークは `--line-soft` 1px、390 幅のヘッダー（ロゴ・「ホスト」・稼働中が折れない）
- apps: 起動許可アプリの警告（左帯なし・アイコン＋`--surface-sel` 地）
- audit / tasks: 表の見出し 13/400 `--mute`・本文 14/400・行の区切り `--line-soft`
- `after-header-killswitch-light-390.png`: 最も長いバッジ「キルスイッチ発動中」のときの 390 幅ヘッダー
