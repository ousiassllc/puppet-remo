# M4 エージェント設定画面（`/agent`）のスクリーンショット

`internal/router/internal/guidemo` が、偽の service（`guitest.AgentSettings`）に架空の状態を入れてホスト GUI を loopback に配信する。
`-agent empty`（既定）は エージェント無効・API キー未登録、`-agent registered` は OpenAI キー・TypeSafe キーが登録済みで、エージェントが同意つきで有効（高速パスは無効）。
偽の service は API キーを**登録の有無（真偽値）としてしか持たない**。実在のキー・トークン・個人情報は含まない（キー欄に入力した文字列は `demo-not-a-real-key-…` の架空の文字列）。

```sh
task frontend:build
go build -o /tmp/guidemo ./internal/router/internal/guidemo
/tmp/guidemo -addr 127.0.0.1:8931 -agent empty        # 無効・キー未登録
/tmp/guidemo -addr 127.0.0.1:8932 -agent registered   # キー登録済み・エージェント有効
# headless Chrome を CDP で操作して撮影（幅 1280 px）
google-chrome --headless=new --no-sandbox --no-first-run --disable-gpu --remote-debugging-port=9341 \
  --proxy-server="direct://" --proxy-bypass-list="*" about:blank
```

撮影環境の注記: この環境では、既定の設定の headless Chrome が loopback のページを読み込めず（原因は未特定）、`--proxy-server="direct://"` と `--proxy-bypass-list="*"` を付けると読み込めた
（アプリの動作とは無関係）。画面の操作（入力・クリック・再読み込み）は CDP の `Runtime.evaluate` で行い、撮影は `Page.captureScreenshot`（ページ全体は `captureBeyondViewport`）。
`task frontend:build`（bun install と Tailwind のビルド）は、この環境で成功した。

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

## 撮影できなかったもの（正直な一覧）

- 実際の Wails ウィンドウ（ネイティブのタイトルバー・ウィンドウの枠・Windows の WebView2 の描画）。ここの画像は headless Chrome のページ
- Windows の資格情報マネージャーへの実際の保存と削除（偽の service は登録の有無の真偽値だけを持つ。本物の `SecretStore` は別のスライス）
- 実際の OpenAI・TypeSafe の呼び出し、キー無効（401）の通知、有効化・同意・削除の監査ログへの記録（service の実装と、それを GUI に結ぶ後のスライスの範囲）
- キー削除の確定（モーダルの「削除する」）を押した後の画面。確認までを撮影し、確定の挙動（`#agent-sections` の差し替え・登録状態の更新・モーダルを閉じる・トースト）はハンドラのテストで確認した
- 高速パスを有効にした状態（`-agent registered` は高速パスを無効のまま起動する。デモの画面から有効にはできるが、撮影していない）
- 実際の Windows（実機）での表示
