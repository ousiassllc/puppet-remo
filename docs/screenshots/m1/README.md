# M1 ホスト GUI のスクリーンショット

`task gui:demo`（`internal/router/internal/guidemo`）が GUI ルーターを loopback（`127.0.0.1:8899`）に、偽の service と
架空のデータ（実在しないホスト名・ユーザー・IP）で起動し、headless Chrome で撮影した（1280×760）。実際のトークン・個人情報は含まない。
`client-token-issued.png` の平文は、発行時に乱数で作られた値を撮影前に明らかなダミー（`prm_DEMO_TOKEN_…`）へ差し替えている。

| ファイル | 内容 |
|---------|------|
| `dashboard.png` | ダッシュボード（サーバー状態・リース保持者・接続中セッション・キルスイッチ。強制剥奪・実行中タスクは出さない） |
| `dashboard-killswitch-active.png` | キルスイッチ発動中（解除のボタンだけを描画） |
| `killswitch-release-confirm.png` | 解除の確認モーダル |
| `connection-requests.png` | 接続要求（許可 / 拒否。承認モードは「常時承認」の既定選択） |
| `clients.png` | クライアント管理（有効化の切り替え・削除） |
| `client-token-issued.png` | トークン発行直後（平文は一度だけ。読み取り専用の入力欄とコピー） |
| `settings.png` | 設定（M1 の設定） |
