# Issue #247 のスクリーンショット（エージェント設定の ChatGPT のプラン）

`/agent`（エージェント設定）の、接続方式の選択・ChatGPT のプランのカード（公式の Sign in with ChatGPT）・Codex 経由のカードの見た目。

**これは偽の service を使うデモ（`internal/router/internal/guidemo`）の画面で、Windows 実機の代わりではない。**
ChatGPT・Codex には接続せず、サインイン・ブラウザの戻り・資格情報マネージャー・Codex の app-server の実際の挙動は確かめていない。メールは架空（`demo-user@example.com`）。

| ファイル | 状態（`-agent`） | 内容 |
|---|---|---|
| `agent-chatgpt-signed-out.png` | `chatgpt_signed_out` | 未サインイン。「Continue with ChatGPT」 |
| `agent-chatgpt-connected.png` | `chatgpt_connected` | 接続済み。メール・「ChatGPT のプランを使用中」・使用量を管理・モデル・解除 |
| `agent-chatgpt-connected-dark.png` | `chatgpt_connected` | 同じ状態の dark |
| `agent-chatgpt-usage-limited.png` | `chatgpt_usage_limited` | 使用量の上限。使用量を管理・再開・API キーに切り替える…（OpenAI キー登録済みのときだけ） |
| `agent-chatgpt-policy-denied.png` | `chatgpt_policy_denied` | ワークスペースのポリシーで拒否。「Codex 経由で使う」 |
| `agent-codex-card.png` | `codex_connected` | Codex 経由。エンジン・アカウント（メール・プラン）・モデル・利用条件・ログアウト |
| `agent-codex-installing.png` | `codex_installing` | エンジンの取得中（進捗・中止） |
| `agent-chatgpt-signout-modal.png` | `chatgpt_connected` + 「解除」 | 解除の確認モーダル（「別のアカウントでサインインする」） |
| `agent-api-key-switch-modal.png` | `chatgpt_usage_limited` + 「API キーに切り替える…」 | API キーへの切り替えの確認モーダル |

## 撮り方

1280x800・light（dark は別名のファイル）。Playwright は `static/node_modules` のもの、ブラウザは Playwright が見つけた Chromium（`PLAYWRIGHT_CHROMIUM` で実行ファイルを指定することもできる）。

```sh
task frontend:install && task frontend:build      # static/dist を作る
go build -o /tmp/guidemo ./internal/router/internal/guidemo
node docs/screenshots/issue-247/capture.mjs /tmp/guidemo docs/screenshots/issue-247
```

`capture.mjs` は状態ごとに guidemo を loopback の空きポートで起動して撮り、止める。手で見るなら:

```sh
go run ./internal/router/internal/guidemo -addr 127.0.0.1:8899 -agent chatgpt_usage_limited   # → http://127.0.0.1:8899/agent
```

`-agent` の値: `empty` / `registered` / `chatgpt_signed_out` / `chatgpt_signing_in` / `chatgpt_connected` / `chatgpt_usage_limited` / `chatgpt_policy_denied` / `codex_missing` / `codex_installing` / `codex_signed_out` / `codex_connected` / `codex_usage_limited`。
