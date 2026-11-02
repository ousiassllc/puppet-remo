# M4 ホスト GUI（タスク・承認・オーバーレイ）のスクリーンショット

`internal/router/internal/guidemo`（偽の service `guitest` に架空のデータを入れて GUI ルーターを loopback に配信するデモ）の画面を、headless Chrome（`google-chrome --headless=new`、
`--ozone-platform=headless --use-angle=swiftshader-webgl`）の CDP（`Page.captureScreenshot`）で撮った。幅は 1280 px（一覧・詳細・ダッシュボード・オーバーレイ）、
承認ダイアログは実際のウィンドウに合わせた 460 px。指示・クライアント名・ホスト名・承認の文面はすべて架空で、実際の API キー・トークン・個人情報・実在のパスは含まない
（サムネイルは架空の画面（グラデーションと矩形）を JPEG にしたもの）。

```sh
task frontend:build
go run ./internal/router/internal/guidemo -addr 127.0.0.1:8951 -lease held_agent -approval safety_check
go run ./internal/router/internal/guidemo -addr 127.0.0.1:8952 -lease held_agent -approval judge_action
# 画面ごとに GET した HTML の /static/css/app.css を dist の絶対パスに差し替え、<script> を外した静的な HTML にして、CDP で file:// を開いて撮影した（下の「撮れていない状態」を参照）
```

| ファイル | 内容 |
|---------|------|
| `tasks-list.png` | タスク一覧（`/tasks`。FR-H10）。新しい順。実行中・完了・失敗（`judge_denied`）・上限到達・中断・失敗（`safety_check_denied`）・承認が拒否された、の各状態のバッジ、ステップ数 / 上限、依頼元、指示の先頭 |
| `tasks-detail.png` | タスク詳細（`/tasks/{id}`）。完了のタスク: ステップ数と内訳（高速パス 2・OpenAI 2）、引き継ぎ理由（`goal_maybe_done`）、トークン、最終回答と「完了はモデルの申告」の注意（FR-G13）、ステップの表（経路・アクション・結果・監査ログへのリンク・サムネイル。FR-G23） |
| `tasks-detail-running.png` | 実行中のタスクの詳細。状態の行に「中断」ボタンが出る（終了済みのタスクには出ない） |
| `tasks-detail-failed.png` | 失敗のタスクの詳細。`error_code`（`judge_denied`）と説明。中断ボタンなし |
| `dashboard-running-task.png` | ダッシュボード（`/`。FR-H02）。実行中のエージェントタスクのパネル（ID・依頼元・指示の先頭・状態・ステップ数・経過・「中断」）と、操作権のカードが「エージェント」保持中で「強制剥奪」（剥奪するとタスクが中断される旨つき） |
| `approval-safety-check.png` | 承認ダイアログ `safety_check`（FR-G08。460 px 幅）。OpenAI の安全チェックの文面（`Detail`）、「自動許可でも省略されない」「拒否・60 秒の無応答でタスクが失敗（`safety_check_denied`）」、残り時間、許可 / 拒否。`-approval safety_check` |
| `approval-judge-action.png` | 承認ダイアログ `judge_action`（FR-G12）。対象アクションの要約（`Detail`）、拒否・無応答ならアクションは実行されずタスクが失敗する旨。`-approval judge_action` |
| `overlay-agent.png` | エージェント実行中の操作中オーバーレイ（`/overlay?label=…`。FR-H07）。1 行目「操作中: demo-desktop（エージェント）」、2 行目「エージェント実行中: <指示の要約>」。背後に架空のデスクトップ（メモ帳）を敷いてある（実機のオーバーレイは透過ウィンドウで、下のアプリがそのまま見え・操作できる） |

## 撮れていない状態・限界

- **この環境では headless Chrome が `http://127.0.0.1:<port>` を開けなかった**（`--screenshot` も CDP の `Page.navigate` も応答せず、デモサーバーに接続が届かない。`data:` と `file://` は開ける）。
  そのため、デモサーバーへ `curl` で取った HTML（CSS の参照だけ差し替え、`<script>` なし）を `file://` で開いて撮っている。見た目（Tailwind の CSS）は実際の画面と同じだが、
  **htmx・`app.js` は動いていない**: ポーリング（`GET /poll`）・「中断」ボタンのクリック・承認ダイアログの 1 秒ごとの残り時間の更新は、この画像では撮れていない
  （残り時間は固定の偽の時計で 60 秒のまま。クリックの動作はハンドラのテスト `internal/web/handler/gui/tasks_test.go` で確認している）。
- **実際の Wails ウィンドウ**（承認ダイアログの 440 px 幅・`safety_check` / `judge_action` の高さ 560 px が内容に足りること、キャプチャ除外、最前面、クリック透過・全モニターのオーバーレイ）と **Windows 実機**は、
  Linux では確かめられていない（`docs/operations/smoke-windows.md`）。ダイアログの高さは、Detail が最大 500 文字のとき（約 6 行）を想定した値で、実機での確認が要る。
- 中断後の画面（`cancelled` のバッジ・トースト）、404 のタスク、承認ダイアログの 410 は撮っていない（ハンドラのテストで確認している）。
- 実際の OpenAI の安全チェックの文面（`pending_safety_checks` の `message`）・Judge の `Detail` は、サービスが作る値（別のスライス）で、画像のものは架空の文。
