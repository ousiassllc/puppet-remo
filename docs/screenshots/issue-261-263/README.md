# #261・#262・#263 の before / after（Light / Dark × 1280 / 390）

`before-*` は develop `0242bfa`、`after-*` はこのブランチ。`task gui:demo` 相当（`internal/router/internal/guidemo`。偽の service）を Chromium（Playwright・`prefers-color-scheme`）で撮った全画面。

- dashboard: カードの題 16/600、キルスイッチの説明 14/400、ライトもダークもカードに外枠・題下の線なし（面の色 `#141414` / `#050505` で分ける）、390 幅のヘッダー
- apps: 起動許可アプリの警告（左帯なし・アイコン＋`--surface-sel` 地）
- audit / tasks: 表の見出し 13/400 `--mute`・本文 14/400・行の区切り `--line-soft`
- `after-header-killswitch-{light,dark}-{390,360,320}.png`: キルスイッチ発動中（最も長いバッジ「キルスイッチ発動中」）のヘッダー。「ホスト」は sm 未満で sr-only、ロゴは 96px より縮めず、360px 未満（320）は文字ロゴの代わりに単色 32px 版のシンボルを 24px で出す（aria-label「Nefos Remote」。Playwright の getBoundingClientRect の実測で、320・330・359 幅のライト・ダークとも img 24×24px。360 幅から文字ロゴ）。1 段のまま（ヘッダー高 56px・横スクロールなし）

- `after-viewer-dark-1280.png`: リモートのビューア（`/viewer`。`task remote:demo` 相当の偽の画面）。映像の面（`<remo-screen-viewer>` の `bg-surface`・canvas の地 `--surface` `#141414`）は枠なし。映像が来る間は左右・上の境目が背景 `#050505` に対してはっきり出る（コントラスト比 2.3〜3.0）。映像が来る前の面だけが背景と近い（`#070707` 対 `#050505`、1.012）ので、面の地を `--bone` から `--surface` に変えた

注: ダークの 1280（dashboard・apps・audit・tasks・viewer）は、カードの枠を 0 にした今の状態で撮り直した。ダークの 390 の 4 枚（`after-{dashboard,apps,audit,tasks}-dark-390.png`）は、枠を 0 にする前の撮影で、カードに `--line-soft` の 1px 枠が残っている（390 は撮り直していない）。
