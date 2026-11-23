# Issue #157 — ビューアのウィンドウ選択・グループ表示・手動操作

**これは偽 platform のスクリーンショットで、Windows 実機の成立性ゲート（S1〜S3）の代わりにならない。**
実機未確認（成立性ゲート待ち。[docs/operations/smoke-windows.md](../../operations/smoke-windows.md) §3.10・§3.10.3）。ここにある成功経路（メニューバーのメニュー・右クリックメニュー・保存ダイアログが
T の外へ出る）は、偽のメモ帳（`platformtest.FakeApp`。単色のウィンドウ）と偽のキャプチャ・偽の注入器で動かしたもので、実 Windows は使っていない。
ウィンドウのタイトル・プロセス名・ファイル名はすべて架空。単色の面は偽ウィンドウの描画で、アプリの実際の見た目ではない。
合成画像の黒い部分は、グループに含まれない領域（透明なキャンバス。背景は黒）。デスクトップや他のウィンドウの画素は含まない。

## 撮り方

```sh
# 偽のデスクトップ（偽のメモ帳 window_id 1001 と、周りの偽のウィンドウ）。task frontend:build のあとで起動する
go run ./internal/app/internal/remotedemo -viewer-scope
# 偽の tailnet IP で開く（Host ヘッダーの検査があるため）。headless Chrome の幅は 1280 px
google-chrome --host-resolver-rules="MAP 100.64.0.1 127.0.0.1" http://100.64.0.1:8765/viewer
# 安全停止の場面は、ホスト GUI のポートの制御エンドポイントで作る（loopback のみ）
curl 'http://127.0.0.1:8899/__demo/viewer-scope?do=foreign|foreign-off|cross-process|unprintable|reset'
```

## 一覧

| ファイル | 何を撮ったか |
|----------|--------------|
| `01-window-picker.png` | 操作権が空きのときの「操作するウィンドウ」の一覧。管理者権限のウィンドウは灰色で、理由つき・「選ぶ」ボタンなし。ダイアログ・メニューは一覧に出ない。下はモニター全体の表示（ウィンドウを選ぶ前） |
| `02-selected.png` | 「選ぶ」の直後。種別 `web` のリースが対象（`fakenotepad.exe`）つきで保持され、表示は T だけの合成画像（等倍）に替わる。クリップボードは書き込みだけ（読み取りの UI なし）、ショートカットの Alt+Tab・Win+D は無効。表示の右上に「モニター表示」（画面全体へ切り替える。スコープ付きのリースは保たれる）が出る |
| `03-file-menu.png` | メニューバーの「ファイル」をクリックして、メニュー（T のメンバー）が画像に写っている |
| `04-save-dialog-outside-target.png` | メニューの項目で保存ダイアログが開き、**T の矩形の外へはみ出す**ので合成画像が広がっている（原点も動く。入力の座標は合成画像内の座標で、サーバーが原点を足して物理座標にする） |
| `05-context-menu-outside-target.png` | T の右下の端で右クリックして、コンテキストメニューが T の外へ出ている |
| `06-halted-foreign-window.png` | 別プロセスのウィンドウが前面に出たあと、キー入力が注入されずに停止（`target_not_foreground`）。「再開」は自動では起きず、ホスト側で原因を取り除いてから押す |
| `07-halted-cannot-display.png` | メニューを撮れない（`capture_member_unavailable`）。「対象を表示できません。画面全体の表示には切り替えません。」と停止のバナー。モニターの画面には自動では切り替わらない（右上の「モニター表示」は利用者が選ぶ切り替えで、画面全体を見る権限として残る。D1） |
| `08-halted-cross-process-dialog.png` | 別プロセスのダイアログ（共通ダイアログのように T に所有される）が前面に出て停止（`related_cross_process`） |
