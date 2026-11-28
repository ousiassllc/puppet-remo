# #280 表の行 hover・clients 画面のデータ入りスクショ（Light / Dark × 1280、#272 / PR #274 の後追い）

`task gui:demo` 相当（`internal/router/internal/guidemo`。偽の service・架空のデータ・秘密なし）を Chromium（Playwright・`prefers-color-scheme`、幅 1280 × 高さ 900）で撮影。Windows 実機の forced-colors 確認は含まない。

- `hover-audit-{light,dark}-1280.png`: 監査ログの表（50 行）の 4 行目にマウスを載せた状態（viewport のみ）。hover 背景は `--surface-sel`（ライト `rgb(235,235,234)`・ダーク `rgb(30,30,33)`）で、行全体が塗られる
- `hover-client-tokens-{light,dark}-1280.png`: クライアント詳細（`/clients/client-001`）のトークン表（2 行）の 1 行目に hover。監査ログと同じ `--table-row-hover`
- `client-detail-tokens-{light,dark}-1280.png`: 同じ画面の hover なし（行の区切り・見出し・バッジ（有効 / 失効済み）・`code` 接頭辞・「未使用」の比較用）
- `clients-data-{light,dark}-1280.png`: クライアント一覧（`/clients`、3 件。常時承認・初回のみ承認（承認済み）・自動許可、MCP / エージェント無効の混在）の全画面
- 所見 1: `/clients` は表ではなくクライアントごとのカード（表は詳細のトークン一覧だけ）。#272 の README の「clients は空状態」は、これまで一覧にデータの入った状態を撮っていなかったため。カードは他画面のカードと同じ地・角丸・余白で、ライト・ダークとも崩れなし
- 所見 2: トークン表は意図どおり compact の余白（td 6px 8px。監査ログ等は 10px 12px。`tokens_css_test.go` が検査）。見出し 13px / `--mute`・行の区切り `divide-line-soft`・hover の `--table-row-hover`・バッジの書式は他の表と同じ
- 所見 3（軽微・未修正）: ライトで hover 行の中立チップ（監査ログの「あり」バッジ、トークン表の `code` 接頭辞）は地が `--accent-soft`（`rgb(231,231,229)`）で、hover 背景（`rgb(235,235,234)`）とほぼ同じになり、輪郭が溶ける（文字は読める。ダークは地 42 対 hover 30 で見分けられる）。トークンの設計判断になるので、このスクショでは直していない

## forced-colors（`docs/architecture/frontend.md` 変更履歴 1.59 の方法）

**Chromium の `forcedColors: 'active'` エミュレーション（Playwright）での結果。Windows 実機のハイコントラストテーマでは確かめていない。** 同じ guidemo・幅 1280・Light / Dark（`colorScheme`）。

- `forced-colors-cards-settings-{light,dark}-1280.png`（設定画面のカードと入力欄）: カードは `CanvasText` の 1px 輪郭、入力欄・選択欄も 1px の輪郭で、見出し・説明・文字はすべて読める。チェックボックス（OFF）も枠で見える
- `forced-colors-inputs-audit-filter-{light,dark}-1280.png`（監査ログの絞り込みの入力欄）: 入力欄・セレクトの輪郭は出る。日時欄のプレースホルダーは UA の色（ライト暗赤・ダーク緑）になるが読める
- `forced-colors-table-hover-audit-{light,dark}-1280.png`（監査ログの表の行 hover、4 行目）: 表全体は外枠と行の区切りが残り、hover 行は `Highlight` の 1px 輪郭（ライト濃紺・ダーク水色）で出る。文字・バッジ（輪郭のみ）・リンクは読める。ただしライトの濃紺は黒い区切り線との差が小さく、ダークの水色より目立たない
- `forced-colors-token-form-table-hover-{light,dark}-1280.png`（クライアント詳細のトークン発行欄と表の行 hover）: 同様に輪郭で出て、バッジ・`失効` ボタンの輪郭・入力欄も読める
- 所見（どれも読めなくなってはいないが、目印が消える所）: (1) 塗りの主ボタン（「絞り込む」「トークンを発行」）は塗りが消えて輪郭も無く、ただの太字に見える（副ボタン「無効にする」「失効」は輪郭が出る）。(2) 現在のページのナビ項目（選択中の背景）が消え、他の項目と見分けがつかない。(3) ヘッダーのロゴ画像の地（ライトは薄灰、ダークはやや明るい黒）が四角く残る。コードは変えていない
