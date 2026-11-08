# #86 操作中オーバーレイ（枠と札だけ）の見た目

`overlay-frame-preview.png` は、Windows 実機の撮影ではない。`internal/app/overlay_layout.go` の `overlayBars`・`overlayLabelRect`
（`wails_overlay_windows.go` が実際にウィンドウを置くときに使う計算）が返した矩形を、2 枚のモニター
（1920x1080 の 100% と、2560x1440 の 150%。横に並べて半分に縮小）の架空のデスクトップ（壁紙とウィンドウ 2 枚）の上に塗った図（ImageMagick で描画。札の文字は Noto Sans CJK で、実機の Segoe UI とは字形・幅が違う）。

見えるはずのこと（実機の確認は `docs/operations/smoke-windows.md` §4.12 の O1）:

- 画面の中身（青いタイトルバーのウィンドウ）は枠・札に隠されない。枠は各モニターの縁に沿う細い線（100% で 4 px、150% で 6 px）、札は上端中央だけ。
- v0.1.1 では、全面の WebView2 ウィンドウが透明にならず全モニターが白一色になった。今回はモニターの真ん中にウィンドウを作らない。

「真ん中が覆われない・枠が重ならない・太さが DPI で変わる」は `internal/app/overlay_layout_test.go` が確かめる。
