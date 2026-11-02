# M4 ホスト内蔵エージェントのスクリーンショット

M4（Issue #19）の画面を 3 組に分けて載せている。実際のトークン・API キー・ホスト名・個人情報は含まない（クライアント名・ホスト名・指示・承認の文面はすべて架空。
サムネイルは架空の画面（グラデーションと矩形）を JPEG にしたもの。デモの API キーは「登録されている」ことを示すだけのダミーで、入力も表示もしていない）。

| 組 | ファイル | 何を撮ったか | 撮り方 |
|----|---------|-------------|--------|
| A. 実アプリ結線 | `run-*.png` | `app.New` が組み立てた本物の service・ルーター・オーバーレイ制御・承認キューに、`run_task` を MCP で流してタスクを動かしている最中・完了後・中断後の画面 | `remotedemo -agent-demo -agent-safety-check` + Firefox（WebDriver）。htmx のポーリング・実際のボタン操作あり |
| B. GUI の見た目（偽の service） | `tasks-*.png`・`dashboard-running-task.png`・`approval-*.png`（`approval-safety-check.png`・`approval-judge-action.png`）・`overlay-agent.png` | 偽の service（`guitest`）に架空のデータを入れて各状態を並べた GUI。実アプリ結線では出しにくい状態（失敗・上限到達・`judge_action` など）を含む | `guidemo` + 静的 HTML（`file://`）。htmx は動いていない |
| C. エージェント設定 | `agent-*.png` | `/agent`（設定・キーの登録・同意・高速パス） | `guidemo` + Chrome（CDP）。画面操作あり |

## A. 実アプリ結線のスクリーンショット（`run-*.png`）

`internal/app/internal/remotedemo`（`-agent-demo -agent-safety-check`）は、`app.New` で本物の service・ルーター・WebSocket・MCP サーバー・オーバーレイ制御（`overlayController`）・
承認キューを組み立て、OS 依存部（画面・入力・SecretStore・Wails のウィンドウ）と tailnet だけを偽物にする。OpenAI の代わりに、台本つきの偽の `openai.Model`
（クリック → 文字入力（ここに OpenAI の安全チェックが付く）→ 最終回答。1 ステップ約 1.5 秒）を `app.Config.OpenAIModel` に渡す。OpenAI も TypeSafe も呼ばない
（ネットワークへは何も出ない）。`run_task` は、デモのクライアントの Bearer トークン（明らかなダミー `prm_DEMO-dummy-token-…`。remotedemo のログに出るもの）で、MCP の Streamable HTTP を `curl` で叩いて呼ぶ。
GUI の画面は、タスクを動かしながら Firefox で開いて撮った（htmx・`app.js` が動いている。`/approvals/{id}` の許可と、中断は、ボタンを実際にクリックした）。

### 使ったコマンド

```sh
export PATH=$PATH:~/.bun/bin:~/go/bin
task frontend:build
# (1) デモのバイナリ。下の「オーバーレイのラベルの取り出し」の 5 行（コミットしていない一時的なパッチ）を main.go に足してビルドし、足した行はビルド後に戻した
go build -o /tmp/remotedemo-evidence2 ./internal/app/internal/remotedemo
# (2) デモを起動（-addr のポートは 8765 固定。MCP の Host ヘッダーの検査が設定のポートと比べるため）
PUPPET_REMO_STATIC_DIR=$PWD/static/dist /tmp/remotedemo-evidence2 -addr 127.0.0.1:8765 -gui-addr 127.0.0.1:8897 -agent-demo -agent-safety-check
# (3) タスクを開始する（remotedemo の doc comment の curl。$1 が指示）
T=prm_DEMO-dummy-token-not-a-secret-0123456789xxA
H=(-H "Host: 100.64.0.1:8765" -H "Authorization: Bearer $T" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream")
SID=$(curl -si "${H[@]}" -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}' http://127.0.0.1:8765/mcp | tr -d '\r' | sed -n 's/^[Mm]cp-[Ss]ession-[Ii]d: //p')
curl -s "${H[@]}" -H "Mcp-Session-Id: $SID" -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' http://127.0.0.1:8765/mcp
curl -s "${H[@]}" -H "Mcp-Session-Id: $SID" -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_task","arguments":{"prompt":"メモ帳に挨拶を入力してください"}}}' http://127.0.0.1:8765/mcp
# (4) 撮影: geckodriver（WebDriver）で headless Firefox を操作する。ウィンドウは 1280×900（承認ダイアログだけ幅を最小の 500 にした）
geckodriver --port 4551 --binary /snap/firefox/current/usr/lib/firefox/firefox
#   POST /session（-headless）→ POST /url → GET /screenshot（ページ全体は geckodriver 独自の GET /moz/screenshot/full）
#   承認の「許可」とタスクの「中断」は POST /element/{id}/click（実際のクリック。htmx が POST を送り、応答の差し替えとトーストが出る）
```

撮影の順（1 つのスクリプトで上から実行した）: タスク A を開始 → remotedemo のログに承認要求（`kind=safety_check`）の id が出るまで待つ → ダッシュボード・`/tasks`・タスク詳細を撮る
→ `/approvals/<id>` を開いて撮り、「許可」をクリック → タスクが完了するまで `/tasks/<id>` を再読み込み → オーバーレイ → タスク B を開始（同じく承認待ちで止まる）→ 詳細の「中断」をクリック。
**タスク A・B とも、2 ステップ目の安全チェックの承認待ち（最大 60 秒）で止まっている間に「実行中」の画面を撮っている**（台本の 1 ステップ目はクリックを実行済みで、ステップ数は 2 / 50 と出る）。

#### オーバーレイのラベルの取り出し

remotedemo は、オーバーレイ制御が `FakeWindows.ShowOverlay` に渡したラベルをログに出さない。そこで、ビルドの間だけ `internal/app/internal/remotedemo/main.go` の
`windows := app.NewFakeWindows()` の直後に次の 5 行を足して、制御が実際に渡したラベルをログに出した（撮影後に取り除き、コミットには含めていない）:

```go
go func() {
	for c := range windows.OverlayEvents() {
		log.Printf("EVIDENCE overlay hide=%v label=%q", c.Hide, c.Label)
	}
}()
```

ログに出た値（`overlayController.sync` → `label` → `overlayAgentLabel` が組み立てたものそのまま。派生した値ではない）:

```
EVIDENCE overlay hide=false label="操作中: demo-laptop（エージェント）\nエージェント実行中: メモ帳に挨拶を入力してください"   ← タスク A のリース取得時
EVIDENCE overlay hide=true  label=""                                                                               ← タスク A の終了時（オーバーレイを隠した）
EVIDENCE overlay hide=false label="操作中: demo-laptop（エージェント）\nエージェント実行中: ブラウザで天気を調べてください"   ← タスク B
EVIDENCE overlay hide=true  label=""                                                                               ← タスク B の中断時
```

### 画像

| ファイル | 内容 |
|---------|------|
| `run-dashboard-running.png` | ダッシュボード（`/`）。実行中のエージェントタスクのパネル（ID 末尾・依頼元・指示・「実行中」・ステップ 2 / 50・経過・赤い「中断」ボタン）、操作権（リース）のカードが「保持中・demo-laptop・エージェント」で「強制剥奪」（剥奪するとタスクが中断される旨つき）。ページは htmx で動いている（経過の秒数などは `/poll` で更新される） |
| `run-tasks-list-running.png` | `/tasks`。実行中のタスク 1 件（ステップ 2 / 50） |
| `run-task-detail-running.png` | `/tasks/{id}`。実行中のタスクの詳細（状態の行に「中断」ボタン、ステップ数 2（OpenAI 2）、ステップ 1 の `left_click`）。最終回答は「ありません」 |
| `run-approval-safety-check.png` | 承認ダイアログ `safety_check`（実際のフロー: remotedemo のログの id → `/approvals/{id}`）。OpenAI の安全チェックの文面（`malicious_instructions`）、「自動許可でも省略されない」「拒否・60 秒の無応答でタスクが失敗（`safety_check_denied`）」、残り時間（「残り N 秒で拒否されます」。撮影時は 50 秒台）、許可 / 拒否。**幅は 500 px**（Firefox の headless のウィンドウの最小幅。実際の承認ダイアログは 460 px で、ここでは折り返しが少し違う） |
| `run-approval-after-allow.png` | 「許可」をクリックした直後（`POST /approvals/{id}/allow` の応答「許可しました。」）。このあとタスクが再開して最後まで進む |
| `run-task-detail-completed.png` | 完了したタスクの詳細。「完了」・ステップ 3 / 50（高速パス 0・OpenAI 3）・最終回答と「完了はモデルの申告」の注意（FR-G13）、ステップの表（クリック・安全チェックの許可（`agent.safety_check`・`result: allowed`）・文字入力）。ページ全体 |
| `run-overlay-agent.png` | 実際のオーバーレイ制御が出したラベル（上のログのタスク A の値）を `GET /overlay?label=…` に渡したページ。1 行目「操作中: demo-laptop（エージェント）」、2 行目「エージェント実行中: メモ帳に挨拶を入力してください」。**背後の架空のデスクトップ（メモ帳。入力した文字は台本の `Hello from the demo agent`）は、撮影のために `/overlay` のページへ JavaScript で足した飾り**（実機のオーバーレイは透過ウィンドウで、下のアプリがそのまま見え・操作できる） |
| `run-cancel-before.png` | タスク B の詳細（実行中・「中断」ボタンあり） |
| `run-cancel-after-click.png` | 「中断」を**実際にクリック**した直後。状態が「中断」に変わり、「中断」ボタンが消え、右下にトースト「タスクを中断しました。」。（承認待ちだった安全チェックも、同じ中断で取り消される） |
| `run-task-detail-cancelled.png` | 中断後のタスク B の詳細を読み込み直したもの。「中断」・終了時刻つき・ステップ 2 に `safety check: canceled`（`result: canceled`・結果 `error`）。ページ全体 |
| `run-tasks-list-final.png` | `/tasks`。中断（2 / 50）と完了（3 / 50）が並ぶ |

実画面を撮ったことで気づいた点（画像を見ると分かる。範囲外なので直していない）: 完了・中断のタスクの詳細のステップの表で、**安全チェックの行と、その直後の文字入力の行がどちらも「#2」**になっている
（`run-task-detail-completed.png`）。ステップ数（3 / 50）は文字入力まで 3 と数えているので、表の番号と数え方が食い違って見える。

## B. 偽の service での GUI（`tasks-*.png`・`dashboard-running-task.png`・`approval-*.png`・`overlay-agent.png`）

`internal/router/internal/guidemo`（偽の service `guitest` に架空のデータを入れて GUI ルーターを loopback に配信するデモ）の画面を、headless Chrome（`google-chrome --headless=new`、
`--ozone-platform=headless --use-angle=swiftshader-webgl`）の CDP（`Page.captureScreenshot`）で撮った。幅は 1280 px（一覧・詳細・ダッシュボード・オーバーレイ）、
承認ダイアログは実際のウィンドウに合わせた 460 px。

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
| `dashboard-running-task.png` | ダッシュボード（`/`。FR-H02）。実行中のエージェントタスクのパネルと、操作権のカードが「エージェント」保持中で「強制剥奪」 |
| `approval-safety-check.png` | 承認ダイアログ `safety_check`（FR-G08。460 px 幅）。`-approval safety_check` |
| `approval-judge-action.png` | 承認ダイアログ `judge_action`（FR-G12）。対象アクションの要約（`Detail`）、拒否・無応答ならアクションは実行されずタスクが失敗する旨。`-approval judge_action`。**実アプリ結線では出していない**（台本に Judge を入れていないため） |
| `overlay-agent.png` | エージェント実行中のオーバーレイ（`/overlay?label=…`。FR-H07）。ラベルは `guidemo` が作った値（実アプリ結線の値は `run-overlay-agent.png`）。背後に架空のデスクトップ（メモ帳）を敷いてある |

## C. エージェント設定画面（`agent-*.png`）

`guidemo` が、偽の service（`guitest.AgentSettings`）に架空の状態を入れてホスト GUI を loopback に配信する。
`-agent empty`（既定）は エージェント無効・API キー未登録、`-agent registered` は OpenAI キー・TypeSafe キーが登録済みで、エージェントが同意つきで有効（高速パスは無効）。
偽の service は API キーを**登録の有無（真偽値）としてしか持たない**（キー欄に入力した文字列は `demo-not-a-real-key-…` の架空の文字列）。

```sh
task frontend:build
go build -o /tmp/guidemo ./internal/router/internal/guidemo
/tmp/guidemo -addr 127.0.0.1:8931 -agent empty        # 無効・キー未登録
/tmp/guidemo -addr 127.0.0.1:8932 -agent registered   # キー登録済み・エージェント有効
# headless Chrome を CDP で操作して撮影（幅 1280 px）
google-chrome --headless=new --no-sandbox --no-first-run --disable-gpu --remote-debugging-port=9341 \
  --proxy-server="direct://" --proxy-bypass-list="*" about:blank
```

画面の操作（入力・クリック・再読み込み）は CDP の `Runtime.evaluate` で行い、撮影は `Page.captureScreenshot`（ページ全体は `captureBeyondViewport`）。

| ファイル | 内容 |
|---------|------|
| `agent-settings-empty.png` | 初期状態（`-agent empty`）。ページ全体: 「既定では無効」の説明、OpenAI API キー（「未登録」・空の伏せ字の入力欄・保存先の説明）、有効化トグルと OpenAI へのスクリーンショット送信への同意の文言、モデル・上限・保持枚数・プロセスリスト、続けて「高速パス（TypeSafe）」（別のキー・別のトグル・TypeSafe への送信内容と送らないものを明記した別の同意・確信度・上限・除外ラベル） |
| `agent-key-masked-input.png` | API キー欄（NFR-S14）に架空の文字列を入力した状態。入力は黒丸（`type=password`）で伏せられ、「資格情報マネージャーにだけ保存し、登録後は表示しない」と明記 |
| `agent-key-empty-error.png` | キーを空のまま「登録」したときの 422。項目のエラー「API キーを入力してください。」だけを出し、入力欄は空のまま |
| `agent-enable-without-key-409.png` | キー未登録のまま、同意をつけて「有効にする」を保存したときの 409 `agent_disabled`（トースト。API 仕様 §7.3 のルートの記述どおり）。設定は変わらない |
| `agent-settings-enabled.png` | `-agent registered`。両方のキーが「登録済み」（入力欄は空・ボタンは「置き換える」と「削除」）、エージェントは有効・同意済み（日時つき）、毎回承認するプロセスに `explorer.exe`。高速パスは無効 |
| `agent-fastpath-card.png` | 高速パスのカード（`-agent registered`）。TypeSafe キーは「登録済み」、有効化トグルと別の同意（未チェック）、モデル `jev-1.13`・確信度 0.7・上限 15・除外ラベル |
| `agent-key-delete-confirm.png` | OpenAI API キーの「削除」を押したときの確認モーダル（サーバーが描画）。「エージェントは無効になり、実行中のタスクは中断されます」。確認だけで、削除はしていない |
| `agent-validation-error.png` | 範囲外のステップ数（999）・実行時間（10）、数値でない保持枚数、同意なしの有効化の 422。項目ごとのメッセージが入力値のまま再描画される（保存はされない）。数値欄に非数値を入れるとブラウザが空にするため、保持枚数の欄は空で表示される |

## 撮影環境の注記

- **Chrome がこの環境で loopback のページを開けない**（Chrome 148 の `google-chrome` も Playwright の Chromium 156・chrome-headless-shell も同じ）: `Page.navigate` は、リクエストの送信（`Network.requestWillBeSent`）まで進んで応答が来ず、
  `--dump-dom` も 40 秒で何も返さない。net-log では、`URL_REQUEST_START_JOB` と `COMPUTED_PRIVACY_MODE` のあと HTTP トランザクションが始まらず、サーバー側にはリクエストが届かない
  （`curl` は同じサーバーへ届く）。`--proxy-server=direct://`・`--incognito`・`--user-data-dir` の場所・`NetworkServiceInProcess`・Local Network Access / First-Party Sets 系の機能の無効化では直らず、原因は未特定。
  組 C の撮影時（`--proxy-server="direct://" --proxy-bypass-list="*"` で読み込めた）とは環境の状態が違う。そのため組 A は Firefox（snap の `firefox` と `geckodriver`）の WebDriver で撮った。
  組 B（静的 HTML を `file://` で開く）と組 C は、それぞれ上に書いた方法のまま。
- Firefox の headless はウィンドウ幅が 500 px 未満にならず、承認ダイアログ（組 A）は 460 px でなく 500 px 幅になった。
- `task frontend:build`（bun install と Tailwind のビルド）は、この環境で成功した。

## 撮れていない状態・限界（正直な一覧）

- **実際の Wails ウィンドウ**: 承認ダイアログの 440 px 幅・高さ 560 px が `safety_check` / `judge_action` の内容に足りること、ネイティブのタイトルバー・枠、キャプチャ除外、最前面。画像はすべてブラウザのページ。
  ダイアログの高さは Detail が最大 500 文字（約 6 行）のときを想定した値で、実機での確認が要る
- **実機のクリック透過・全モニターのオーバーレイ**（`WS_EX_TRANSPARENT`・マルチモニター・DPI 混在）: 画像は、`/overlay` のページの背後へ架空のデスクトップを JavaScript で足したもの。透過ウィンドウとして実デスクトップの上に出た状態は撮れていない
- **Windows の資格情報マネージャー**への実際の保存・削除（デモの SecretStore は「登録されている」ことを示すだけの偽物）と、キー無効（401）の通知
- **実際の OpenAI・TypeSafe（Jev）**: 呼んでいない。台本つきの偽モデルが返した操作（クリック・文字入力）と安全チェックで動かしただけで、実際の応答の文面・遅延・失敗は確かめていない。
  安全チェックの文面（`pending_safety_checks` の `message`）・Judge の `Detail` も架空の文
- **Windows 実機**（`docs/operations/smoke-windows.md`）。入力の注入・画面キャプチャ・ホットキー（キルスイッチ）は偽物で動かしている
- **組 A でも撮っていない状態**: 失敗（`judge_denied`・`safety_check_denied` など）・上限到達の実フロー（組 B の偽データのみ）、`judge_action` の承認ダイアログ、承認の 60 秒タイムアウト、
  キルスイッチ・「強制剥奪」による中断、404 のタスク、承認ダイアログの 410（ハンドラのテストで確認している）、高速パス（TypeSafe）を通ったステップ（デモの台本は OpenAI の経路だけ）
- **組 B**: 静的 HTML なので htmx・`app.js` が動いていない（ポーリング・「中断」のクリック・承認ダイアログの残り時間の更新は撮れていない。組 A で実フローを撮った）
- **組 C**: キー削除の確定（モーダルの「削除する」）を押した後の画面（確認までを撮影し、確定の挙動はハンドラのテストで確認した）、高速パスを有効にした状態
