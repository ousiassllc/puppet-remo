# #282 forced-colors の主ボタン・ナビの現在地・ロゴ / #283 行 hover 専用トークン（Light / Dark、`before-` と `after-`）

`task gui:demo` 相当（`internal/router/internal/guidemo`。偽の service・架空のデータ・秘密なし）を Linux の Chromium（Playwright。`colorScheme`・`forcedColors`）で撮影。`before-` は develop `ca9120d`、`after-` はこのブランチ（幅 1280 × 高さ 900。`*-390.png` は幅 390）。**Windows 実機のハイコントラストでは確かめていない**（手順は `docs/operations/smoke-windows.md` §4.21）。

## #283: ライトで hover した行の中立チップ

- `{before,after}-audit-row-hover-{light,dark}-1280.png`: 監査ログで、「あり」チップのある行（1 行目）にマウスを載せた状態（viewport のみ）
- computed style の実測: hover 行の背景 ライト `rgb(235,235,234)` → `rgb(245,245,244)`（`--row-hover` = `#f5f5f4`）、ダーク `rgb(30,30,33)`（変わらない）。チップの地 ライト `rgb(231,231,229)`・ダーク `rgb(42,42,42)`（チップは触っていない）。チップの地と行の背景の輝度比（地の見分けやすさ）は、ライトで 1.04 → 1.14 になり、`after-audit-row-hover-light-1280.png` ではチップの輪郭が見える（`before-` は地に溶ける）
- ダークの `before-` と `after-` は、行の背景が同じ（`rgb(30,30,33)`）。ピクセルの差はデモの時計（時刻の文字）だけ

### `--mute` と `#f5f5f4` のコントラスト比（実測。WCAG 2.x の相対輝度）

| 文字 | 地 | 比 | AA 4.5:1 |
|---|---|---|---|
| `--mute` `#6b6b6e`（ライト） | `--row-hover` `#f5f5f4` | **4.87** | 超える |
| `--mute` `#8b8b8e`（ダーク） | `--row-hover` `#1e1e21` | **4.89** | 超える |
| `--mute` `#6b6b6e`（ライト。参考） | `--surface-sel` `#ebebea`（以前の行 hover） | 4.45 | 未満 |
| `--mute` `#6b6b6e`（ライト。参考） | `--accent-soft` `#e7e7e5`（タスクのカードの hover） | 4.29 | 未満 |

超えたので、表の行の中の `group-hover:text-mute-on-soft`（と行の `group`）を外した。`--accent-soft` 地で hover するタスクのカード（sm 未満）は AA 未満のままなので切り替えを残している。値は `internal/web/hover_row_contrast_test.go`（`TestRowHoverToken`）が CSS から計算して検査する。

## #282: forced-colors（Chromium の `forcedColors: 'active'` のエミュレーション）

- `{before,after}-audit-forced-colors-{light,dark}-1280.png`: 監査ログの全体（viewport）。主ボタン「絞り込む」・ナビの現在地「監査ログ」・ヘッダーのロゴ
- `{before,after}-audit-forced-colors-{light,dark}-390.png`: 幅 390（ロックアップが出る幅。ナビはドロワーで閉じている）
- 主ボタン: before は塗りも輪郭も無いただの太字 → after は `ButtonText` の塗り・`ButtonFace` の字・1px の輪郭（ライトは黒地に白字、ダークは白地に黒字）。副ボタンは輪郭だけのまま（塗りで見分けられる）
- ナビの現在地: before は他の項目と同じ見た目 → after は `Highlight` の地・`HighlightText` の字（ライトは濃紺地に白字）
- ロゴ: before は薄灰（ダークは暗い灰）の四角い板がロゴの周りに残る → after は板が消え、リング・矢印・文字だけが `CanvasText` で出る
- 状態チップは変更していない（Issue の方針。アイコンで区別できる）
- 撮っていない（未確認）: シンボルだけのロゴ（360px 未満）、ナビの「接続要求」の件数バッジ（`bg-accent` の丸。forced-colors では塗りが消えて数字だけになる見込み）

### 実装の注意（エミュレーションで見つけた点）

`.btn-primary` と現在地のナビは `forced-color-adjust: none` が要る。`auto` のままだと、Chromium は文字の下に Canvas 色の板（バックプレート）を敷き、地を `ButtonText`・字を `ButtonFace` に指定しても字が板に埋もれて消えた（地と字の色が一致して見えた）。
