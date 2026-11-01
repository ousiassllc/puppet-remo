# M3 ホスト GUI のスクリーンショット

`go run ./internal/router/internal/guidemo`（`task frontend:build` のあと。`-addr 127.0.0.1:8899`）が、偽の service（`guitest`）に架空のデータを入れて
ホスト GUI を loopback に配信する。headless Chrome（幅 1280 px。承認ダイアログだけは実際のウィンドウに合わせた 460 px）で撮影した。実際のトークン・ホスト名・個人情報は含まない
（クライアント名・ホスト名・ユーザーはすべて架空で、サムネイルは架空の画面（グラデーションと矩形）を JPEG（長辺 320 px・品質 60）にしたもの）。

| ファイル | 内容 |
|---------|------|
| `dashboard-pending-approval.png` | ダッシュボードの操作権カードが承認待ち（`pending_approval`。FR-L07）。要求者（`demo-desktop`・MCP）と「ホストの承認を待っています」。`-lease pending_approval` |
| `approval-dialog.png` | 承認ダイアログ（`/approvals/ap-demo-001`。FR-H05）。クライアント・種別・要求の内容・残り時間（固定の偽の時計で 60 秒のまま）・許可/拒否。専用ウィンドウの大きさ（460×380）。`-lease pending_approval` |
| `clients-approval-mode.png` | クライアント管理（`/clients`）の承認モード（常時承認・初回のみ承認・自動許可）の選択。初回のみ承認では承認済みの日時と「承認を取り消す」 |
| `requests-approval-mode.png` | 接続要求（`/requests`）の許可前フォームの承認モード選択（既定は常時承認） |
| `settings.png` | 設定（`/settings`。#73）の全体。「監査ログ」（サムネイルの保存・本文の記録・保持期間・容量の上限と、平文保存の警告）と「更新」（更新の適用方法。確認と適用は後の版で有効になる旨）の項目 |
| `audit-list.png` | 監査ログの一覧（`/audit`。#72）。絞り込みフォーム、`ts` 降順の表（結果バッジ・所要時間・サムネイルの有無）、「次へ」 |
| `audit-detail.png` | 監査ログの詳細（`/audit/60`。#72）。項目・引数のキー・値の表・サムネイルの `<img>` |
| `audit-detail-escaped.png` | 引数に HTML の断片（`<script>…`）があっても、エスケープされて文字としてそのまま表示される |
| `audit-invalid.png` | 不正な日時（`from=yesterday`）の絞り込み。項目の下にメッセージが出て、「該当なし」とは言わない（422） |

## リモート Web UI（`/viewer`）の承認待ち

`remotedemo`（`internal/app/internal/remotedemo`）を `-approval always` で起動して撮影した。既定の `-approval auto` は、これまでどおり承認なしでリースを取る
（`task remote:demo` の動作は変わらない）。`always` ではデモのクライアント（`demo-laptop`）が毎回承認を求め、承認ダイアログは Wails が無いので開かず、
承認要求の id だけをログに出す。許可・拒否はホスト GUI の `POST /approvals/{id}/allow|deny`（承認ダイアログの許可・拒否ボタンと同じ経路）で行った。

```sh
task frontend:build
go run ./internal/app/internal/remotedemo -addr 127.0.0.1:8765 -gui-addr 127.0.0.1:8897 -approval always
# ログの "approval requested id=…" の id を使う
google-chrome --headless=new --no-sandbox --no-first-run --ozone-platform=headless --use-angle=swiftshader-webgl \
  --remote-debugging-port=9333 --host-resolver-rules="MAP 100.64.0.1 127.0.0.1" about:blank   # 画面操作は CDP（ブラウザの取得ボタンのクリックと撮影）
```

ブラウザの取得リクエスト（`POST /viewer/lease/acquire`）は決まるまで応答しない（最大 60 秒）ので、「取得ボタンを押す → 承認待ちのパネルになるまで待つ → 撮影」
の順に撮った。パネルは WebSocket の `lease` イベントで取得リクエストの応答を待たずに更新される。

| ファイル | 内容 |
|---------|------|
| `viewer-lease-pending.png` | 取得ボタンを押したあとの承認待ちのパネル（`LeasePendingBadge`:「承認待ち」・`demo-laptop`・`web`・「あなた」・「ホストの承認を待っています。」）。クリップボードとショートカットのパネルは出ない |
| `viewer-lease-approved.png` | ホストが許可した後（保持中・「あなた」・解放ボタン。クリップボードとショートカットのパネルが出る） |

### 撮れていない状態

- **拒否後のトースト（403 `approval_denied`）**: 撮れなかった。サーバーは 403 のトースト断片を返す（ネットワークで確認）が、ブラウザには出ない。承認待ちの間に
  WebSocket の `lease` で `#lease-panel` が差し替わり、取得ボタン（リクエストを出した要素）が DOM から無くなる。応答が戻っても `#toast-root` に何も出なかった
  （要素が無くなったあとの応答を htmx が反映せず、`htmx:responseError` も document まで伝わらないためと見られる。コードでは確かめていない）。拒否のあとのパネルは「空き」に戻るだけ。HTML を加工して
  載せることはしていない。
- **タイムアウト後のトースト（408 `approval_timeout`）**: 撮れなかった。60 秒でサーバーは 408 を返すが、ブラウザのネットワーク記録に 408 の応答は現れず、
  **同じ `acquire` が再送されて新しい承認要求ができた**（ログで確認: `approval_timeout` の直後に別の `approval requested`。パネルは承認待ちのまま）。
  Chrome が 408 を自動で再送したためと見られる（推測）。どう直すか（ステータスの選択・取得の応答の返し方）は docs に決まりが無いので、ここでは決めていない。
- 承認待ちを即座に返す窓口（非同期の取得）は未実装（docs 未決）。
- 監査ログの Web（リモート）画面は M3 には無い（リモートルーターでは `/audit*` は 404）。オーバーレイも未実装なので撮っていない。
