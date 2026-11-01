# M2 リモート Web UI のスクリーンショット

`task remote:demo`（`internal/app/internal/remotedemo`）が、本物の service・ルーター・WebSocket（`app.New`）を、偽の platform
（画面・入力・クリップボードなど）と偽の tailnet（状態・WhoIs）で、loopback に起動する。headless Chrome（1280 px 幅）で撮影した。
実際のトークン・ホスト名・個人情報は含まない（ホスト名・ユーザー・IP はすべて架空）。

- リモートルーター（`/viewer`・`/ws/screen`・`/static/*`）: `127.0.0.1:8765`。Host ヘッダーの検査があるため、ブラウザは偽の tailnet IP
  `100.64.0.1` で接続する（`google-chrome --host-resolver-rules="MAP 100.64.0.1 127.0.0.1" http://100.64.0.1:8765/viewer`）。
- ホスト GUI（ダッシュボード）: `127.0.0.1:8899`。接続元の端末の切り替え（登録済み / 未登録）は
  `http://127.0.0.1:8899/__demo/identity?as=registered|unregistered`。
- 画面は時間で変わる偽のデスクトップ（時計・進捗バー・グラフ。変化したタイルだけが差分で流れる）。入力は偽の注入器が受け、
  画面の「Injected input」欄・メモ帳・カーソルとログに反映される。

| ファイル | 内容 |
|---------|------|
| `viewer-live.png` | ライブ画面の配信（閲覧のみ。操作権は空きで、入力は注入器まで届かない） |
| `viewer-control.png` | 操作権を取得した後（リースパネルに保持者 `demo-laptop`・種別 `web`・「あなた」・解放ボタン、クリップボードとショートカットのパネル）。ブラウザからの入力が偽の注入器に届き、画面に反映されている |
| `viewer-shortcuts.png` | ショートカット送信メニュー（Alt+Tab を送信した直後。ホストの注入器が `alt+tab` を受けた） |
| `viewer-clipboard.png` | クリップボードの送信と取得（送ったテキストを、ホストのクリップボードから取得して表示） |
| `viewer-waiting.png` | 未登録デバイスの待機ページ（接続要求の送信後。許可されるまで待つ） |
| `dashboard-force-revoke.png` | ホストのダッシュボード（Web 保持者 `demo-laptop` と強制剥奪ボタン） |

## 撮影時の注意

- `viewer-control.png` の入力（マウス・キー）は、`<remo-screen-viewer>` の `readonly` 属性が「操作権を取得」の後も付いたままだったため、
  撮影用の使い捨てスクリプトで属性を外してから送った（`readonly` は描画時にだけ決まり、リースパネルの差し替えでは更新されない）。
  この挙動は製品コードの確認事項として別に報告している（このデモ・スクリーンショットでは製品コードを変えていない）。
