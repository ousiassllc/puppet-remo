# アプリのアイコンの見本

`app-icon-sizes.png` は、アイコンの 16・32・256px を並べた見本（`task common:generate:icons` が作る。`build/icons/render.mjs`）。

- 上の 2 行: 実寸の 16・32・256px。明るい背景（#e8e8e8）と、タスクバー想定の暗い背景（#202020）。白地の角丸のおかげで暗い背景でも見える
- 下の行: 16px を 16 倍、32px を 8 倍にした拡大（最近傍補間）。糸 4 本と棒 2 本の交差が数えられる

元データは `build/appicon.svg`（65px 以上の大きいサイズ用。細い線。`appicon.png`・ICO の 128・256px）と `build/appicon-small.svg`（16〜64px 用に線を太くした版）。
小さい版は、大きい版の細い線が 64px 以下では灰色に薄れるため、ICO の 16・24・32・48・64px とトレイの `internal/app/tray.png`（64px）に使う。
