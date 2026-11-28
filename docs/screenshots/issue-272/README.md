# #272 の before / after（Light / Dark × 1280）

`before-*` は develop `e5cbaf7`、`after-*` はこのブランチ。`task gui:demo` 相当（`internal/router/internal/guidemo`。偽の service）を Chromium（Playwright・`prefers-color-scheme`）で撮った全画面（幅 1280）。

- `apps`: 起動許可アプリの空の一覧と拒否リストの空の一覧が、破線の枠なし（アイコン + 1 行）になった
- `audit` / `tasks` / `requests`: 表の本文の行間 1.5（td 14px × 1.5 = 21px）、字間（見出し・項目 .01em / 本文・説明 .02em / ナビ・ボタン 0）、行の hover は `--surface-sel`
- `clients`: デモのトークンが空なので空状態のまま（行頭の名前 15 / 600 は `TestKeyValueAttributesUseMono`・`organisms_test.go` が検査）
- `after-{apps,audit}-forced-colors-1280.png`: `forced-colors: active`（Chromium の forcedColors エミュレーション）。表の区切り・見出し・空の状態は崩れず、横スクロールなし。行の hover（audit の 1 行目）は `Highlight` の 1px の輪郭で出る

実測（Chromium の computed style。ライト・ダークとも）: td 行間 21px（14px × 1.5）、body・td の字間 0.28px（.02em × 14px）、h1 の字間 0.22px（.01em × 22px）、ナビ・ボタン `normal`（0）、hover 背景 ライト `rgb(235,235,234)`・ダーク `rgb(30,30,33)`（`--surface-sel`）、forced-colors の hover は `solid 1px` の輪郭。
