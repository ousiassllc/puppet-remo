# API 仕様

要件は [機能要件](../requirements/functional.md)（FR-M / FR-G / FR-W）と [非機能要件](../requirements/non-functional.md)、ルーター構成は [アーキテクチャ設計](../architecture/overview.md) を参照。

## 1. 概要

puppet-remo は 2 つのルーターで HTTP を提供する。

| ルーター | 到達性 | 提供するもの |
|---------|--------|-------------|
| **リモートルーター** | tailnet IP の TCP ポート（既定 8765。FR-S01）。HTTP のみ | MCP（`/mcp`）、Web UI（`/viewer`）、WebSocket（`/ws/screen`）、JSON API（`/api/v1`） |
| **GUI ルーター** | プロセス内のみ（Wails `AssetOptions.Handler`）。ソケットを開かない | ホスト GUI（ダッシュボード、接続要求、クライアント管理、起動許可アプリ、承認、タスク、エージェント設定、監査ログ、設定、更新）と SSE |

ホスト専用の操作（承認、トークン発行、API キー登録、キルスイッチ解除、**起動許可アプリの登録・変更・削除**など）は GUI ルーターにだけ存在し、リモートルーターには**同名の経路も作らない**（構造で隔離する）。M1 の実装が本書と異なる点（M1 で提供するルート、`/poll`、エラーの形、許可の挙動など）は [§7.5](#75-m1-実装での差分) にまとめる。

### 共通規約

| 項目 | 内容 |
|------|------|
| ベース URL | `http://<tailnet-ip または MagicDNS 名>:<port>`（リモート）。GUI ルーターはベース URL を持たない（Wails の内部オリジン） |
| 文字コード | UTF-8 |
| 時刻 | JSON は RFC 3339（UTC）。DB の Unix ミリ秒は変換して返す |
| ID | ULID（クライアント・トークン・接続要求・タスク）。監査ログは整数 |
| バージョン | JSON API は `/api/v1`。MCP のツール・WebSocket のプロトコルは本書の版で固定し、破壊的変更時に WebSocket のサブプロトコル名（`puppet-remo.v1`）を上げる |
| 状態を変更するリクエスト（リモート） | ヘッダー `X-Puppet-Remo: 1` 必須（NFR-S03）。Web UI のルートは HTMX のフォーム（`Content-Type: application/x-www-form-urlencoded`）のみ受け付ける。`/api/v1` は参照専用（GET のみ）で、状態を変更する JSON リクエストは存在しない。MCP は Bearer 必須のため対象外 |
| 応答の種類 | **ページルート・アクションルート（Web UI、GUI）は常に HTML**（`HX-Request` の有無で JSON に切り替えない。ページルートは `HX-Request` でフルページとフラグメントを出し分け、アクションルートは常にフラグメント）。JSON を返すのは `/api/v1/*` と MCP のみ。詳細は [フロントエンド設計](../architecture/frontend.md) |
| サイズ上限 | HTTP ボディ ≤ 1 MiB、WebSocket のクライアント→サーバーのメッセージ ≤ 64 KiB（NFR-S09） |
| HTML 応答のヘッダー | **両ルーター**のすべての HTML 応答（ページ・フラグメント・エラーページ）に、ミドルウェアが `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'` を付ける（`<meta>` では出さない。NFR-S19）。インラインのスクリプト・スタイル・イベントハンドラ属性は使わない。`connect-src 'self'` は同一オリジンの WebSocket も含む。詳細は [フロントエンド設計](../architecture/frontend.md) |
| 静的ファイルのキャッシュ | `/static/*`（および GUI の静的アセット）は `Cache-Control: no-cache` + `ETag`（ビルド ID）。`If-None-Match` が一致すれば 304 |

### リモートルーターのミドルウェア連鎖

すべてのリクエストは次の順に検査される。いずれかで拒否されたら以降は実行しない（[アーキテクチャ設計](../architecture/overview.md)）。**キルスイッチの判定（3）は、WhoIs・許可リスト照合・接続要求の作成（4・5）より前**に置く。キルスイッチ中は未登録デバイスのリクエストにも接続要求を作らず 503 を返す（FR-A06、FR-H06）。

| 順 | 検査 | 失敗時 |
|----|------|--------|
| 1 | `Host` ヘッダー（tailnet IP / MagicDNS 名 + ポート以外は拒否）と、`Origin` ヘッダー（同一オリジンのみ）。NFR-S02 | 400 `invalid_host` / 403 `invalid_origin` |
| 2 | 状態変更なら `X-Puppet-Remo: 1`（Web UI・JSON API）。NFR-S03 | 403 `missing_header` |
| 3 | キルスイッチ（FR-H06）。発動中は以降を実行しない（接続要求も作らない） | 503 `kill_switch_active` |
| 4 | WhoIs で接続元のユーザーと node ID を解決（10 秒キャッシュ）。NFR-S05 | 403 `whois_failed` |
| 5 | 許可リスト照合（node ID + ログイン名） | 403 `device_not_registered`（接続要求を作成・集約。FR-A06）。登録済みでも該当機能が無効なら 403 `client_disabled` |
| 6 | `/mcp` のみ: Bearer トークン（形式・照合は NFR-S04。node ID 束縛は FR-A05）。認証失敗は同一デバイスにつき 1 分 10 回まで（NFR-S04） | 401 `unauthorized`（`WWW-Authenticate: Bearer`）、超過は 429 `rate_limited` |

エラーの形式は経路で決まる。**`/api/v1/*` と `/ws/screen` の接続前（ハンドシェイク）のエラーは problem+json**（§2）、**MCP（`/mcp`）は HTTP ステータス（401 / 403 / 429 / 503 など）と、ツール実行後のエラーは `isError`**（§3.4）、**Web UI と GUI のルートは HTML**（ページルートはエラーページ、アクションルートはエラーのフラグメント。同じステータスコードを返し、フラグメントには `data-error-code="<code>"` を付ける）。403 `device_not_registered` をブラウザ（Web UI のルート）が受けたときは、「接続要求を送信しました」という待機ページ（HTML）を返す（FR-W02）。

## 2. エラー形式（RFC 9457 problem+json）

`/api/v1/*` と `/ws/screen` のハンドシェイク前のエラー、およびミドルウェア連鎖が `/api/v1` と `/mcp` へのリクエストを拒否するときの応答は `application/problem+json` で返す。

```json
{
  "type": "urn:puppet-remo:error:lease_held_by_other",
  "title": "Lease held by another session",
  "status": 409,
  "detail": "The lease is held by 'agent' (task 01JA...).",
  "code": "lease_held_by_other"
}
```

- `type` は `urn:puppet-remo:error:<code>`。`code` は拡張メンバーで、機械処理にはこれを使う
- Web UI・GUI のルート（HTML を返す経路）は problem+json を返さない。同じ `code` と HTTP ステータスで、HTML のエラーフラグメント（またはエラーページ）を返す。バリデーションエラー（422）はフォームを入力値とエラーメッセージ付きで再描画する。表示先は `response-targets` の `hx-target-422` / `hx-target-4*` / `hx-target-5*` が決める（[フロントエンド設計](../architecture/frontend.md)）
- MCP のツールエラーは HTTP ステータスではなく、ツール結果の `isError: true` と `structuredContent: {code, message, detail?}` で返す（§3.4）

### エラーコードと HTTP ステータス

FR-M17 のコードに、HTTP 層のコードを加えたもの。

| code | HTTP | 発生箇所 | 説明 |
|------|------|---------|------|
| `invalid_host` | 400 | 共通 | `Host` ヘッダーが不正 |
| `invalid_origin` | 403 | 共通 | `Origin` が同一オリジンでない |
| `missing_header` | 403 | 共通 | `X-Puppet-Remo` が無い |
| `whois_failed` | 403 | 共通 | WhoIs に失敗（fail closed） |
| `device_not_registered` | 403 | 共通 | 許可リストに無い（または登録時と異なるユーザー）。接続要求を作成する |
| `client_disabled` | 403 | 共通 | クライアントの Web / MCP が無効 |
| `unauthorized` | 401 | `/mcp` | Bearer が無い・無効・失効・期限切れ・他デバイスのトークン |
| `rate_limited` | 429 | 共通 | 認証失敗の頻発。`Retry-After` を付ける |
| `kill_switch_active` | 503 | 共通 | キルスイッチで受付停止中 |
| `lease_required` | 409 | 操作系 | リース無しで、暗黙取得もできない |
| `lease_held_by_other` | 409 | 操作系 | 他のセッションがリースを保持 |
| `lease_revoked` | 409 | 操作系 | 自身のリースが強制剥奪された（FR-L06）。剥奪された保持者の操作は、**次の `acquire`（取得の成功）まで**返し続ける（暗黙の取得もしない。別の保持者が現れた間は `lease_held_by_other`） |
| `approval_denied` | 403 | リース取得 | ホストが拒否した |
| `approval_timeout` | 408 | リース取得 | 60 秒で応答が無かった |
| `invalid_coordinates` | 400 | 操作系 | 座標がモニターの範囲外 |
| `invalid_argument` | 400 | 共通 | 引数が不正（`viewer_id` が別のクライアントに束縛されている場合を含む） |
| `conflict` | 409 | GUI | 現在の状態では実行できない（要求が `pending` でない、更新を適用できない状態など） |
| `window_not_found` | 404 | ウィンドウ系 | 対象が存在しない |
| `app_not_found` | 404 | `launch_app` | `app_id` が起動許可リストに無い（FR-M19。M1.5） |
| `input_blocked` | 409 | 操作系 | 自ウィンドウ・管理者権限ウィンドウなどで入力不可（NFR-S06 / UIPI）。`detail.reason` は `own_window` / `elevated_window` / `secure_attention_sequence`。`own_window` の `detail.own_windows` に puppet-remo のウィンドウの範囲を付ける（§3.4） |
| `audit_unavailable` | 503 | `launch_app` | 起動の「試行」を監査ログに書けなかったため、起動しなかった（fail-closed。NFR-S20。#37）。MCP は同じ `code` のツールエラー。DB のエラーの中身は載せない。再試行できる（書けるようになれば起動できる） |
| `launch_failed` | 409 | `launch_app` | 許可リストのアプリだが OS がプロセスを作成できなかった（FR-M19。M1.5）。`structuredContent` に `reason`（`file_missing` / `access_denied` / `elevation_required` / `other`）を付ける。**パス・引数は含めない** |
| `desktop_unavailable` | 503 | 取得・操作 | ロック画面・UAC 等でキャプチャ・入力できない |
| `agent_disabled` | 409 | `run_task` / `PUT /agent`（GUI） | エージェント無効、API キー未登録、またはクライアントに利用許可が無い |
| `task_not_found` | 404 | `get_task` / `cancel_task` | 対象が無い、または他クライアントのタスク |
| `internal` | 500 | 共通 | 想定外のエラー。詳細はアプリのログにだけ残し、`detail` には出さない |

エージェントの失敗理由（`judge_denied`、`safety_check_denied`、`unsupported_action`、`openai_error`）はタスクの `error_code` として `get_task` に載るもので、HTTP のエラーにはならない。高速パス（FR-G16〜G23）の TypeSafe 呼び出しの失敗・拒否・タイムアウトはタスクを失敗させず、OpenAI への引き継ぎとして `handoff_reason`（§3.3 `get_task`）に記録される。高速パスは内部機構であり、エラーコード・API・MCP ツールを増やさない。

## 3. MCP（`/mcp`）

### 3.1 トランスポートとセッション

| 項目 | 内容 |
|------|------|
| プロトコル | MCP の Streamable HTTP。実装は公式 Go SDK（`NewStreamableHTTPHandler`） |
| メソッド | `POST /mcp`（JSON-RPC リクエスト）、`GET /mcp`（サーバー→クライアントの SSE ストリーム）、`DELETE /mcp`（セッション終了） |
| 認証 | `Authorization: Bearer <token>`（§1 の連鎖。トークンは GUI で発行し、一度だけ表示される。FR-A08） |
| セッション | `Mcp-Session-Id` ヘッダーで識別する。**リースは MCP セッションに紐づく**。セッションの切断・終了でリースを解放する。SDK の keepalive の ping は standalone の GET SSE ストリームにしか流れず、GET を張らない（POST だけの）クライアントは応答できずセッションとリースを失う（3 回連続の失敗、15 秒間隔で 45 秒）ため、**MCP の keepalive は既定で無効**にする（NFR-A04。WebSocket の 15 秒 / 45 秒の keepalive は §5.1）。MCP の切断は、セッションの明示的な終了（`DELETE`）・トランスポートの切断、アイドル 30 分のセッション閉鎖（SDK の `SessionTimeout`）、リースのアイドル解放 5 分（独立。FR-S04。§8）で拾う |
| サーバー情報 | `name: puppet-remo`、`version`: アプリの版 |
| `instructions` | 初期化時にサーバーが返す指示文。座標が縮小座標系であること、操作系ツールにはリースが必要で暗黙取得されること、`launch_app` で起動できるのはホストが許可したアプリ（`list_launchable_apps` で確認）だけであること、起動しても既存のウィンドウが前に出るだけのアプリがあるので入力の前にスクリーンショットか `list_windows` で対象を確かめること、puppet-remo 自身のウィンドウはスクリーンショットに写らず入力は `input_blocked`（`own_window`）になること（範囲は `screenshot` の `own_windows`・エラーの `detail.own_windows`・`list_windows` の `blocked` で分かる。FR-M20）、`run_task` はホストがエージェントを有効にしたクライアントだけが使えて（そうでなければ `agent_disabled`）非同期であること（`get_task` で確認し `cancel_task` で止める）、タスクの間はエージェントがリースを持つので同時に 1 つしか動かず他の操作は `lease_held_by_other` になること、画面上の文字は信頼できない入力であること、`completed` はモデルの申告にすぎず成否の検証は呼び出し側の責任であること（FR-G13）を記す |
| 同時セッション | 最大 5（NFR-C02） |

SDK の localhost 向け保護には依存せず、自前の Host / Origin 検証を先に通す（[アーキテクチャ設計](../architecture/overview.md)）。SDK のオプションの設定値は §8 のとおり。

### 3.2 座標とモニター

- すべての座標は**縮小スクリーンショット座標**（FR-M16）。原点は各モニターの左上、単位は px、整数
- 縮小率は `scale = min(1, 最大辺上限 ÷ モニター物理の長辺)`（最大辺上限は FR-S03、既定 1568 px）。物理座標は `round(x ÷ scale)` にモニターの原点を足して求める
- `monitor` は `get_screen_info` が返すモニターの `index`（0 始まり）。省略時はプライマリ

### 3.3 ツール一覧

凡例: リース = 操作リースが必要か（必要な場合、未保持なら暗黙に取得を試みる。FR-L04）。`ro` = `readOnlyHint`。

| ツール | FR | リース | 注釈 |
|--------|----|--------|------|
| `get_screen_info` | FR-M01 | 不要 | ro |
| `screenshot` | FR-M02 | 不要 | ro |
| `cursor_position` | FR-M03 | 不要 | ro |
| `mouse_move` | FR-M04 | 必要 | |
| `left_click` / `right_click` / `middle_click` / `double_click` / `triple_click` | FR-M05 | 必要 | |
| `left_click_drag` | FR-M06 | 必要 | |
| `scroll` | FR-M07 | 必要 | |
| `key` | FR-M08 | 必要 | |
| `type` | FR-M09 | 必要 | |
| `wait` | FR-M10 | 不要 | ro |
| `list_windows` | FR-M11 | 不要 | ro |
| `focus_window` | FR-M12 | 必要 | |
| `clipboard_read` | FR-M13 | 必要 | ro |
| `clipboard_write` | FR-M13 | 必要 | |
| `get_ui_tree` | FR-M14 | 必要 | ro |
| `acquire_control` / `release_control` / `get_control_status` | FR-M15 | 不要 | `get_control_status` は ro |
| `list_launchable_apps` | FR-M18 | 不要 | ro |
| `launch_app` | FR-M19 | 必要 | |
| `run_task` / `get_task` / `cancel_task` | FR-G02 | 不要（`run_task` が内部でリースを取る） | `get_task` は ro |

#### 画面情報

**`get_screen_info`**

入力: なし。出力:

```json
{
  "monitors": [
    { "index": 0, "name": "\\\\.\\DISPLAY1", "primary": true,
      "origin": {"x": 0, "y": 0}, "physical": {"width": 2560, "height": 1440},
      "dpi_scale": 1.25, "scale": 0.6125, "scaled": {"width": 1568, "height": 882} }
  ]
}
```

**`screenshot`**

| 引数 | 型 | 必須 | 説明 |
|------|----|------|------|
| `monitor` | integer | いいえ | モニター index（既定: プライマリ） |
| `format` | `"png"` \| `"jpeg"` | いいえ | 既定 `png` |

出力: MCP の画像コンテンツ（`image/png` または `image/jpeg`、base64。縮小済み）と、テキストコンテンツの JSON `{ "monitor": 0, "physical": {…}, "scaled": {…}, "scale": 0.6125, "own_windows": [ {"monitor": 0, "rect": {"x": 784, "y": 0, "width": 393, "height": 442}} ] }`。

`own_windows` は、撮影した時点でそのモニター上にある puppet-remo 自身のウィンドウの範囲（無ければ空配列）。これらのウィンドウは画像に**写らず**（キャプチャ除外。NFR-S06）、入力は `input_blocked`（`own_window`）になるため、`rect`（この画像の座標。縮小座標）の範囲を避ける。画像には印を描かない（FR-M20）。範囲を取れなかったときも画像は返し、`own_windows` は空になる。

**`cursor_position`**

入力: なし。出力: `{ "x": 640, "y": 360, "monitor": 0 }`（縮小座標系）。

#### 入力操作

共通: リースが必要。マウス操作の `x` / `y` は縮小座標、`modifiers` は `ctrl` / `shift` / `alt` / `win` の配列（クリックの間だけ押下する）。成功時の出力は `{ "ok": true }`。

| ツール | 引数 |
|--------|------|
| `mouse_move` | `x`\*, `y`\*, `monitor` |
| `left_click` / `right_click` / `middle_click` / `double_click` / `triple_click` | `x`, `y`（省略時は現在のカーソル位置）, `modifiers`, `monitor` |
| `left_click_drag` | `from`\* `{x,y}`, `to`\* `{x,y}`, `modifiers`, `monitor`（`from` で左ボタンを押し、`to` まで移動して離す） |
| `scroll` | `x`\*, `y`\*, `direction`\*（`up` / `down` / `left` / `right`）, `amount`\*（ホイールの**ノッチ数**、1〜50。WebSocket の `input.mouse` の `wheel` と同じ単位。§5.3）, `monitor` |
| `key` | `keys`\*（下記） |
| `type` | `text`\*（Unicode 全般、10,000 文字まで。NFR-S09） |
| `wait` | `ms`\*（0〜10,000。リース不要） |

（\* は必須）

**`keys` の書式**: `+` で連結した小文字のトークン（大文字小文字は区別しない）。修飾キーは `ctrl` / `shift` / `alt` / `win`。キー名は `enter` / `escape` / `tab` / `backspace` / `delete` / `insert` / `home` / `end` / `pageup` / `pagedown` / `up` / `down` / `left` / `right` / `space` / `capslock` / `f1`〜`f24`、1 文字の英数字・記号（`a`、`1`、`;`）。例: `ctrl+shift+t`、`enter`。未知のトークンは `invalid_argument`。`ctrl+alt+delete`（SAS）は送れない（`input_blocked`）。

#### ウィンドウ・クリップボード・UI ツリー

**`list_windows`**: 入力なし。出力:

```json
{
  "windows": [
    { "id": "132456", "title": "メモ帳", "process_name": "notepad.exe", "pid": 4120,
      "rect": {"x": 40, "y": 30, "width": 800, "height": 600}, "monitor": 0,
      "foreground": true, "minimized": false, "elevated": false,
      "blocked": false }
  ]
}
```

`id` は HWND の 10 進文字列。`rect` は、そのウィンドウが最も重なるモニターの縮小座標で、モニターの範囲に切り詰める。`elevated` が true のウィンドウには入力できない（`input_blocked`）。

`blocked` は、そのウィンドウへの入力を puppet-remo が拒否するか。true のとき `blocked_reason`（`own_window` / `elevated_window`。false のときは省略）を付ける。puppet-remo 自身のウィンドウ（`own_window`）も一覧に出し、`blocked: true` の印を付ける。これらはスクリーンショットに写らない（NFR-S06）ので、`rect` の範囲への入力を避ける（FR-M20）。自ウィンドウと昇格の両方に当たるときは `own_window`。

**`focus_window`**: `id`\*。存在しなければ `window_not_found`。

**`clipboard_read`**: 入力なし。出力 `{ "text": "…" }`（1 MiB まで。テキスト以外は空文字）。**`clipboard_write`**: `text`\*（1 MiB まで）。

**`get_ui_tree`**

| 引数 | 型 | 説明 |
|------|----|------|
| `window_id` | string | 対象ウィンドウ（省略時は前面のウィンドウ） |
| `max_depth` | integer | 既定 5、最大 20（超えると `invalid_argument`） |
| `max_nodes` | integer | 既定 500、最大 5000（超えると `invalid_argument`） |

出力: `{ "window_id": "…", "truncated": false, "root": { "id": "…", "role": "Window", "name": "…", "value": "…", "rect": {…}, "enabled": true, "children": [ … ] } }`。`rect` は縮小座標。パスワード欄は `value` を含めず `"value_redacted": true` にする。`id` は UI Automation の RuntimeId から作る参照用の文字列で、他のツールの引数には使わない。

#### リース

| ツール | 出力 |
|--------|------|
| `acquire_control` | `get_control_status` と同じ（成功すると `held`）。承認が必要でなければ `idle → held`。承認が必要な場合（承認モード。M3 から）は `idle → pending_approval` にして、ホストの結果を待つ（最大 60 秒）。拒否は 403 `approval_denied`、60 秒の無応答は 408 `approval_timeout`（§2。ツールエラー。どちらも `idle` に戻る）。拒否・タイムアウトの直後 30 秒は、同じクライアントの取得を承認なしで `approval_denied` にする（クールダウン）。他者が保持中、または他者が承認待ちなら `lease_held_by_other`。暗黙の取得（入力系のツール・`launch_app`）も同じに承認を待ち、同じコードで失敗する（承認を待てるよう、リースを取得しうるツールのタイムアウトは 90 秒で、それ以外の 30 秒より長い）。`launch_app` に呼び出しごとの承認は無く、承認はリース取得時の 1 回だけ |
| `release_control` | `{ "ok": true }`。保持者でなければ何もしない |
| `get_control_status` | `{ "state": "idle" \| "pending_approval" \| "held", "holder": { "kind": "mcp" \| "web" \| "agent", "label": "…", "task_id": "…" }, "mine": true, "idle_remaining_sec": 240 }`（`holder` と `idle_remaining_sec` は `held` のときだけ）。`pending_approval`（M3）は承認待ちで、`holder` は出さず、`mine` は承認待ちの要求者が自分のセッションかどうか。`state` が `held` のとき `mine` は保持者が自分かどうか。`holder.kind` は `mcp` / `web`（M2 から）/ `agent`（M4 から。`run_task` のタスクの実行中。依頼した本人にも保持者は `agent` なので `mine` は `false`）。`task_id` は `agent` のときだけ出る（`lease_held_by_other` の `holder` には出さない）。強制剥奪された直後は `idle` |

#### アプリ起動（M1.5）

起動できるのは、ホスト所有者がホスト GUI で登録した許可リストのアプリだけ（FR-H12）。クライアントが渡せるのは `app_id` だけで、実行ファイルのパス・引数・作業ディレクトリは**入力にも出力にもエラーにも現れない**。

**`list_launchable_apps`**（FR-M18）: 入力なし。出力:

```json
{
  "apps": [
    { "id": "01JA…", "label": "メモ帳" },
    { "id": "01JB…", "label": "電卓" }
  ]
}
```

`id` は ULID（登録時に採番。編集しても変わらず、削除すると無効になり再利用されない）。`apps` は `label` の昇順（大文字小文字を区別しない）で、許可リストが空なら空配列。起動できない行（読めない行を含め、起動のたびの再検査を満たさない行）は返さない。リース不要。キルスイッチ中はミドルウェア連鎖（§1）が 503 で拒否する。

**`launch_app`**（FR-M19）

| 引数 | 型 | 必須 | 説明 |
|------|----|------|------|
| `app_id` | string | はい | `list_launchable_apps` の `id` |

追加の引数（`args`・`cwd`・`path` など）は受け付けない（スキーマに無い引数・`app_id` の欠落・型違いは、MCP SDK の引数検査が拒否し、起動しない。§3.4）。出力:

```json
{
  "ok": true,
  "app_id": "01JA…",
  "pid": 4120,
  "window": { "id": "6a2f…", "title": "無題 - メモ帳", "pid": 8812, "process_name": "notepad.exe" },
  "reused_existing_window": true
}
```

- `pid` は `CreateProcess` が返したプロセスの ID。
- `window` は起動後に前面にあったウィンドウ（`list_windows` の `id`・`title`・`pid`・`process_name` と同じ値）。前面のウィンドウを確かめられなかったとき（前面が無い・デスクトップが使えない）は**付かない**。`window.pid` は `pid` と違うことがある。
- `reused_existing_window` は常に付く。前面のウィンドウを確かめられ、起動した `pid` のトップレベルウィンドウが 1 つも見つからず（最小化・背面のものを含む）、かつ起動の前に無かった新しいウィンドウも開いていないとき `true`（Windows 11 のメモ帳などのタブ式の単一ウィンドウのアプリで、新しいウィンドウを開かず既存のウィンドウが前に出ただけの可能性が高い。そのウィンドウにはユーザーの未保存の作業があるかもしれない）。それ以外は `false`（`window` が無いときも `false`）。起動したプロセスがすぐ終了して別の `pid` の新しいウィンドウを開くアプリは、新しいウィンドウがあるので `false`（#55）。`true` は確定ではない（起動の前のウィンドウ一覧を読めなかったときは前面のウィンドウの `pid` だけで判断する）。
- プロセスの作成に成功した時点で起動は成功。そのあと、起動した `pid` のウィンドウが前面に出るのを、200 ミリ秒間隔で最大 16 回（およそ 3 秒。前面の `pid` が一致したらすぐ）観測して返す（起動の前後のウィンドウ一覧の差も見る）。ウィンドウが出なくても失敗にはしない。`window` を見たうえで、**入力の前に必ず `screenshot` か `list_windows` で対象を確かめる**（`instructions` とツールの説明にも同じ注意を書く）。

- 検査の順序は、キルスイッチ（ミドルウェア連鎖の 3）→ 操作リース → 許可リスト。リース未保持なら暗黙に取得する（FR-L04）。リースの承認は M1.5 には無い（M3 から）
- エラー: `lease_held_by_other`（他者がリースを保持）、`app_not_found`、`launch_failed`、`kill_switch_active`（HTTP 503）、`audit_unavailable`（HTTP 503。起動の「試行」の行を監査ログに書けなかったとき。何も起動していない。下の監査の項）。リースの暗黙取得に失敗したときは `lease_held_by_other` を返し、起動しない
- 同じアプリが起動中でも、呼ぶたびに新しいプロセスを起動する（冪等ではない。自分からフォーカスを切り替えることはしない。アプリ自身が既存のウィンドウを前に出すことはあり、結果の `reused_existing_window` で分かる）。起動済みのプロセスは、リースの解放・セッションの切断・キルスイッチ・許可リストからの削除では終了しない
- 監査は fail-closed の 2 行（#37。NFR-S20）: プロセスを作る**前**に「試行」の行（`tool.launch_app.attempt`。agent は `agent.launch_attempt`）を同期で書き、書けなければ起動せず `audit_unavailable` を返す。起動のあとに「結果」の行（下の `tool.launch_app` / `agent.step`）を書く。結果の行が書けなくても起動は取り消さず、運用ログに Warn を出す。試行の行はキルスイッチ・リース・許可リストの検査を通って起動に進む呼び出しにだけ書く。同期書き込みの遅延は許容し、新しい待ちの上限は足さない（既存の `ToolTimeout` のまま）。
- すべての結果（成功・拒否・失敗）を監査に追記する（FR-AU06）。入力スキーマに合わない呼び出し（`app_id` の欠落・型違い・追加のプロパティ）は SDK が実行前に拒否するため `Control.Launch` に届かない。この場合は MCP の受信ミドルウェアが `tool.launch_app`（`denied`・`error_code = invalid_argument`、`detail` なし。拒否された値は記録しない）を 1 件追記する。`list_launchable_apps` も `tool.list_launchable_apps` として追記する

#### 内蔵エージェント

**`run_task`**（FR-G02）

| 引数 | 型 | 必須 | 説明 |
|------|----|------|------|
| `prompt` | string | はい | 自然言語の指示（10,000 文字まで） |
| `max_steps` | integer | いいえ | ステップ上限。設定値（FR-S12）以下のみ指定できる |
| `max_duration_sec` | integer | いいえ | 実行時間の上限（秒）。設定値以下のみ |

出力（即時）: `{ "task_id": "01J…", "status": "pending_approval" | "running" }`。長時間実行になるため、完了は `get_task` で確認する（1 回の MCP 呼び出しは 1 秒以内に返る。NFR-P08）。エージェント無効なら `agent_disabled`、他者がリースを保持していれば `lease_held_by_other`、キルスイッチ中は `kill_switch_active`。`prompt` が空・10,000 文字（rune）超、`max_steps` / `max_duration_sec` が 1 未満・設定値超は `invalid_argument`（`prompt` の欠落・型違いは SDK が入力スキーマで拒否する）。

**高速パス（FR-G16）は `run_task` から透過**: 引数も出力も増えず、呼び出し側は使う・使わないを指定できない。使うかどうかはホストの設定 `agent.fastpath.enabled`（既定 off。GUI のみで変更。FR-S14）だけで決まり、キー未登録・同意なし・無効のときは黙ってスキップして OpenAI のみで実行する。タスクの完了は常に OpenAI が確認用スクリーンショットを見て宣言する（FR-G20）。どちらの経路で実行したかは `get_task` の `fast_steps` / `openai_steps` / `handoff_reason` で分かる。

**`get_task`**: `task_id`\*。出力:

```json
{
  "task_id": "01J…", "status": "running",
  "step_count": 7, "fast_steps": 4, "openai_steps": 3, "handoff_reason": "no_progress",
  "max_steps": 50,
  "input_tokens": 41230, "output_tokens": 912,
  "created_at": "2026-10-29T03:12:00Z", "started_at": "2026-10-29T03:12:02Z", "finished_at": null,
  "result_text": null, "error_code": null, "note": null
}
```

- `status` は FR-G03 の状態。終了状態では `finished_at` が入る
- `result_text` は最終回答（`completed` のみ。10,000 文字で切り詰め）。`note` には `completed` がモデルの申告であり成否の検証は呼び出し側の責任であることを記す（FR-G13）
- `error_code`: `failed` のときの理由（`input_blocked` / `judge_denied` / `approval_denied` / `safety_check_denied` / `unsupported_action` / `openai_error` / `invalid_coordinates`（モデルが縮小画像の外の座標を返した）など）。リースの喪失（強制剥奪・アイドルタイムアウト）・リース承認待ち中のキルスイッチはエラーではなく `status = cancelled`（`error_code` は `null`）
- `fast_steps` / `openai_steps`: 高速パス（Jev が候補表から選んだ手）と OpenAI のコンピューター操作ループで実行したステップ数（FR-G23）。`step_count = fast_steps + openai_steps`（高速ステップも `max_steps` に数える）。高速パスを使わなかったタスクは `fast_steps = 0`
- `handoff_reason`: 高速パスから OpenAI へ引き継いだ理由。`abstain` / `low_confidence` / `no_candidates` / `invalid_choice` / `no_progress` / `goal_maybe_done` / `fast_step_limit` / `chooser_unavailable` のいずれか。引き継ぎが無かった（高速パス無効、または高速パスの間にキャンセル・失敗で終了した）場合は `null`。引き継ぎ後はタスクが終了するまで OpenAI で続行する（v1 では高速パスに戻らない）。失敗理由ではなく、`error_code` とは別に扱う

**`cancel_task`**: `task_id`\*。出力: `{ "ok": true, "status": "cancelled" }`。すでに終了していれば現在の状態をそのまま返す。実行中の OpenAI 呼び出しを取り消し、リースを解放する（FR-G10）。ループの終了は最大 0.5 秒だけ待ち、それまでに終わらなければその時点の状態（`running`）を返す（1 秒以内に返る。NFR-P08）。依頼元のクライアント自身のタスクだけ（ほかは `task_not_found`）。

### 3.4 ツールエラー

ツールの失敗は JSON-RPC エラーではなく、ツール結果として返す。

```json
{
  "isError": true,
  "content": [{ "type": "text", "text": "lease_held_by_other: The lease is held by 'web' (viewer 01J…)." }],
  "structuredContent": { "code": "lease_held_by_other", "message": "…", "holder": {"kind": "web", "label": "my-laptop"} }
}
```

`detail` は、コードによって付く追加情報（無ければ省略）。`input_blocked` の `detail.reason` は `own_window` / `elevated_window`（`detail.window_id` も付く）/ `secure_attention_sequence`。

**`own_window` の `detail.own_windows`**（FR-M20）: AI が避けられるよう、puppet-remo 自身のウィンドウ（スクリーンショットに写らない）の範囲を付ける。`get_ui_tree`・`focus_window` の `own_window` にも付く。

```json
{
  "isError": true,
  "content": [{ "type": "text", "text": "input_blocked: the target is a puppet-remo window" }],
  "structuredContent": {
    "code": "input_blocked", "message": "the target is a puppet-remo window",
    "detail": {
      "reason": "own_window",
      "own_windows": [ { "monitor": 0, "rect": {"x": 784, "y": 0, "width": 393, "height": 442} } ]
    }
  }
}
```

- `monitor` はモニター index、`rect` は**そのモニターの縮小スクリーンショットの座標**（左上が原点。物理座標ではない）。`list_windows` の `rect` と同じ丸め（右下は最後のピクセルを縮小して 1 を足す）で、モニターの範囲に切り詰める
- 複数のモニターにまたがるウィンドウは、モニターごとに 1 件へ切り詰める。最小化されたウィンドウ、どのモニターとも重ならないウィンドウは含めない。どれも無い、またはウィンドウの一覧・モニター・設定を読めないときは、空配列（読めないときは `own_windows` ごと無い）で、拒否そのものは変えない
- ウィンドウのタイトル・プロセス名・PID・ID・内容は載せない（範囲だけ）。`elevated_window` には付けない

コードは §2 の表のとおり。引数のスキーマ違反（スキーマに無い引数・必須の欠落・型違い）は、ハンドラの手前で MCP SDK が拒否する。**このうち監査に残すのは `launch_app` だけ**（§3.3。FR-AU06）で、他のツールのスキーマ違反は FR-AU01 の範囲外（決定事項。#38。何も実行せず状態を変えないため。監査には残らない）。SDK（go-sdk）はこれを JSON-RPC エラーではなく、`isError` のツール結果（本文は `validating "arguments": …`。`structuredContent` は無く、`code` は付かない）で返す。

## 4. Web UI（リモートルーター）

状態駆動レンダリングで、リース状態に応じた操作だけを HTML に出す。ページルート（`GET /viewer` など）は `HX-Request` の有無でフルページとフラグメントを出し分け、アクションルート（`POST /viewer/...`）は常にフラグメントを返す。JSON は返さない。

### 4.1 ビューアのセッション（`viewer_id`）

Cookie を使わないため、ブラウザ上のビューアは `viewer_id`（ULID）で識別する。

- `GET /viewer` のレンダリング時に発行する。値は、接続元クライアント（node ID）と発行時刻に束縛される
- `<remo-screen-viewer>` の `ws-url` に埋め込み、HTMX のリクエストには `hx-headers` で `X-Viewer-Id` として付ける
- 有効期間: WebSocket の接続中、および発行から 5 分（接続前）。**切断後も 5 分間は有効**で、同じ `viewer_id` で再接続できる（[フロントエンド設計 §6.4](../architecture/frontend.md) の再接続）。束縛と異なるクライアントからの利用、未知・期限切れの `viewer_id` は 400 `invalid_argument`（WebSocket は閉じコード 4401）
- 同時接続は 1 つの `viewer_id` につき 1 本。同じ `viewer_id` の新しい接続は、古い接続を閉じて置き換える（古い接続のリースは解放される。古い接続は閉じコード 4401 で閉じる。§5.2）
- リース保持者（種別 `web`）は `viewer_id` で識別する。WebSocket の切断でリースを解放する（置き換えられた古い接続・クライアントの削除・無効化・キルスイッチによる切断も同じ）

### 4.2 ルート

認可は §1 の連鎖（登録済みで `web_enabled` なクライアント）。

| メソッド | パス | 概要 | 応答 |
|---------|------|------|------|
| GET | `/` | `/viewer` へリダイレクト | 302 |
| GET | `/viewer` | ビューアのページ（ツールバー、リースパネル、`<remo-screen-viewer>`） | 200 HTML（`viewer_id` を埋め込む） |
| GET | `/viewer/lease` | リースパネルのフラグメント | 200 HTML。保持者種別・名前・状態と、状態に応じたボタンのみ。保持者（承認待ちなら要求者）が `agent` のときは、実行中のタスクの要約（`[data-lease-task]`: 「AI エージェントのタスク実行中」・状態・ステップ数 / 上限・経過時間と上限・開始時刻・依頼元のクライアント名）を添える。**指示（prompt）と最終回答は出さない**（ビューアは依頼元とは別のクライアント。FR-W08）。タスクを読めないときは要約なしで描画する |
| POST | `/viewer/lease/acquire` | 操作権を取得 | 承認が要らなければ 200 リースパネル。**承認が要るクライアントは、ホストの結果を待たず 202 で承認待ちのパネル（`pending_approval`・本人）を即座に返す**。結果は WebSocket で届く（許可は `lease` の `held`、拒否・タイムアウトは `error` の `approval_denied` / `approval_timeout`。§5.3）。承認待ちのあいだの再試行は新しい承認を作らず同じ 202。他者が保持中・承認待ちなら 409 `lease_held_by_other`。拒否・タイムアウトの直後 30 秒は、承認を求めず 403 `approval_denied`（クールダウン。トースト。パネルは空きのまま） |
| POST | `/viewer/lease/release` | 操作権を解放 | 200 リースパネル |
| POST | `/viewer/lease/revoke` | 他者のリースを強制剥奪（保持者が `agent` ならそのタスクを中断。FR-L06） | 200 リースパネル（保持者は `idle` になる）。自身が取得するには改めて `acquire` する |
| POST | `/viewer/settings` | このビューアのモニター・品質・FPS | フォーム `monitor`, `quality`（1〜100）, `fps`（1〜30）。200 ツールバーのフラグメント。WebSocket の `config` に反映される |
| POST | `/viewer/clipboard/send` | テキストをホストのクリップボードに設定（FR-W07） | フォーム `text`（1 MiB まで）。要操作権。200 フラグメント |
| POST | `/viewer/clipboard/fetch` | ホストのクリップボードを取得して表示 | 要操作権。200 フラグメント（`<textarea readonly>`。利用者が手動でコピーする） |
| POST | `/viewer/keys/send` | ブラウザが先取りするショートカットの送信メニュー（FR-W06） | フォーム `keys`（§3.3 の書式）。要操作権。`ctrl+alt+delete` は 409 `input_blocked`。200 フラグメント |
| GET | `/ws/screen` | WebSocket（§5） | 101 |
| GET | `/static/*` | 静的アセット（`go:embed` した `dist/`） | 200（`Cache-Control: no-cache` + `ETag`。`If-None-Match` 一致で 304） |

ブラウザが未登録デバイスのとき、すべてのルートは 403 `device_not_registered` となり、`Accept: text/html` には待機ページ（「接続要求を送信しました。ホストの許可を待っています」）を返す。許可後に再読み込みするとビューアが開く。拒否・失効（24 時間）のときはその旨のページ。

**待機ページの出し分け**（FR-W02）: ステータスとエラーコード（403 `device_not_registered`）、接続要求の作成・集約・拒否後の抑止（FR-A06）は §1 の連鎖のとおりで変わらない。ページの内容だけを、許可リストの照合（と接続要求の作成・集約）のあとに、デバイスの node ID で接続要求の最新の 1 件を**読み取り専用**で引いて選ぶ: `pending`（期限内）または状態が無い・読めない → 待機、`denied`（拒否から 24 時間以内）→ 拒否、`expired`（期限切れ。`pending` が期限を過ぎたもの、拒否の抑止期間（24 時間）が過ぎたものを含む）→ 失効。ページは `data-error-code="device_not_registered"` と `data-connection-state="pending|denied|expired"` を持ち、スタイル・スクリプトは読み込まない（未登録のデバイスは `/static/*` も 403 のため）。接続要求の作成は照合の中で先に行われるため、通常は新しい要求が作られて待機になり、拒否の抑止中は拒否、新しい要求を作れなかったとき（作成の頻度制限など）は失効が出る。

**Web UI の入力とエラー**（M2。#62）:

- フォームは `application/x-www-form-urlencoded`、本文は 1 MiB まで（超過・読めないときは 400 `invalid_argument`。クリップボードの `text` は本文の上限の内側でも 1 MiB まで）。ビューアのセッションは `X-Viewer-Id`（§4.1）で伝え、無い・未知・期限切れ・別のクライアントに束縛されたものは、アクションルートも `GET /viewer/lease` も 400 `invalid_argument`
- アクションの失敗は、同じステータス・`data-error-code` 付きのトースト（`HX-Retarget: #toast-root`）。`POST /viewer/settings` の検証エラー（`monitor` が存在しない・`quality`・`fps` が範囲外・整数でない）は 422 `invalid_argument` で、入力値とメッセージ付きのツールバー（`#viewer-toolbar`）を再描画する（`hx-target-422`。ホスト GUI の検証エラーと同じ）。接続していない `viewer_id` の設定変更は 400 `invalid_argument`（`ViewerSessions.Configure`）
- `POST /viewer/lease/revoke` は、自分が保持しているときは 409 `conflict`（解放を使う）。保持者がいなければ何もせず現在のパネルを返す。リースの 3 つのルートと `GET /viewer/lease` の応答は、リースパネル（`#lease-panel`）に、操作権の有無で変わるクリップボード・ショートカットの領域（`#viewer-ops`）を OOB で添える（持っていないときは空）
- `POST /viewer/clipboard/send` の 200 は送った文字数だけを返し、本文は返さない。動的な応答はすべて `Cache-Control: no-store`

**ショートカット送信メニュー**（FR-W06。`keys` の値）: `alt+tab`（Alt+Tab）・`alt+f4`（Alt+F4）・`win+d`（Win+D。`keys` の書式では `win` は修飾キーのため Win キー単独は送れず、デスクトップの表示で代える）・`ctrl+w`（Ctrl+W）・`escape`（Esc）。`ctrl+alt+delete` は含めない（UI から送れない旨を表示する。FR-W08）。ルートは §3.3 の書式であれば任意の `keys` を受けるが、メニューが出すのは上の 5 つだけ。

## 5. WebSocket（`/ws/screen`）

### 5.1 接続

- URL: `ws://<host>:<port>/ws/screen?viewer_id=<ULID>`。サブプロトコル `puppet-remo.v1` を指定する
- 検査: §1 の連鎖（`Origin` は同一オリジンのみ）、`viewer_id` の束縛、`web_enabled`
- 同時閲覧者は最大 5（NFR-C03）。超過は閉じコード 4429
- keepalive: サーバーが 15 秒ごとに ping、45 秒応答が無ければ切断（NFR-A04）。切断すると、閲覧者を `Streamer` から外し、`web` のリースを解放する
- 閲覧者がゼロになるとキャプチャを停止する（FR-W09）
- ハンドシェイクの前のエラー（連鎖の拒否、サブプロトコルの不一致）は problem+json（§2。サブプロトコルの不一致は 400 `invalid_argument`）。ブラウザはハンドシェイクの失敗の理由を読めないため、`viewer_id` の不正（未知・期限切れ・別のクライアントに束縛）・同時閲覧者の上限・キルスイッチの発動中は、ハンドシェイクを受け付けてから閉じコード（§5.2）で閉じる
- 接続すると、プライマリモニター・既定の品質と FPS（設定 `viewer.default_quality` / `viewer.default_fps`）で配信を始める。同じ `viewer_id` の新しい接続は古い接続を閉じて置き換える（§4.1）
- アプリの終了時は、接続中の WebSocket を 1001 で閉じる

### 5.2 閉じコード

| コード | 意味 |
|--------|------|
| 4400 | 不正なメッセージ（形式・サイズ超過・範囲外の `config`・`wheel` の絶対値 > 50・バイナリフレーム） |
| 4401 | 認可失敗（未登録・無効・`viewer_id` 不正）。同じ `viewer_id` の新しい接続に置き換えられた古い接続も 4401 で閉じる（再接続しないコードなので、置き換え合わない） |
| 4429 | 同時閲覧者の上限 |
| 4503 | キルスイッチで受付停止 |
| 4510 | クライアントが削除・無効化された |

### 5.3 メッセージ（テキストフレーム、JSON）

すべて `{ "type": "<名前>", ... }` の形。

**サーバー → クライアント**

| type | 内容 |
|------|------|
| `ready` | 接続直後に 1 回（画面フレームより前）。`{ "monitors": [...], "monitor": 0, "width": 2560, "height": 1440, "quality": 70, "fps": 10, "lease": {…} }`。`width` / `height` はストリームの座標空間（選択モニターの物理ピクセル）。`monitors[]` の各要素は `get_screen_info`（§3.3）から縮小に関する項目（`scale`・`scaled`）を除いた `{ "index", "name", "primary", "origin": { "x", "y" }, "physical": { "width", "height" }, "dpi_scale" }`。`lease` は下の `lease` メッセージと同じ形 |
| `config` | 設定の反映結果。`{ "monitor", "width", "height", "quality", "fps" }`。モニター変更時はキーフレームが続く。WebSocket の `config` だけでなく、`POST /viewer/settings` で変えたときにも届く |
| `lease` | リースの変化。`{ "state": "idle" \| "pending_approval" \| "held", "holder": { "kind", "label" }, "mine": true }`。`holder` は `idle` のときだけ省く（M3: `pending_approval` では承認待ちの要求者の `holder` を付け、`mine` は要求者が自分のビューアかどうか。承認待ちでは入力が許されない）。リースの変化は、承認待ちの開始（`pending_approval`）・許可（`held`）・拒否・タイムアウト・取り下げ（`idle`）のたびに全ビューアへ送る。クライアントは CustomEvent `remo:lease-changed` を発火し、HTMX がリースパネルを再取得する |
| `killswitch` | `{ "active": true }`。続けて 4503 で閉じる。以降の接続も 4503 で閉じられる |
| `error` | `{ "code", "message" }`。入力の拒否と、`POST /viewer/lease/acquire` の承認待ちの失敗。`code` は §2 のコードで、`lease_required` / `lease_held_by_other`（承認待ちの間の入力は、要求者本人でもこれ）/ `lease_revoked` / `input_blocked` / `invalid_coordinates` / `invalid_argument`（`input.text` の長さなど）/ `kill_switch_active` / `desktop_unavailable` / `internal`、承認待ちの失敗の `approval_denied`（ホストが拒否）/ `approval_timeout`（60 秒の無応答）。承認待ちの失敗の `error` は、要求したビューアの接続だけに送り（他のビューアには送らない）、同時に `lease`（`idle`）が全ビューアへ届く。`internal` の `message` は詳細を含まない固定文。切断はしない |

**クライアント → サーバー**

| type | 内容 |
|------|------|
| `config` | `{ "monitor": 0, "quality": 70, "fps": 10 }`（任意の項目のみ。省いた項目は今の値のまま。範囲外は 4400。`quality` は 1〜100、`fps` は 1〜30、`monitor` は `ready` の `monitors[]` の `index`） |
| `input.mouse` | `{ "action": "move" \| "down" \| "up" \| "wheel", "x": 100, "y": 200, "button": "left" \| "right" \| "middle", "dx": 0, "dy": 1 }`。`x` / `y` は**ストリームの座標**（モニターの物理ピクセル。MCP の縮小座標ではない）。`wheel` は `dx` / `dy` を使い、**単位はノッチ**（数値。小数可。MCP `scroll` の `amount` と同じ単位。下記） |
| `input.key` | `{ "action": "down" \| "up", "code": "KeyA", "key": "a", "modifiers": ["ctrl"] }`。`code` はブラウザの `KeyboardEvent.code` で、サーバーがスキャンコードに変換する |
| `input.text` | `{ "text": "こんにちは" }`。IME で確定した文字列を Unicode で送る |
| `ping` | `{ "type": "ping" }` → サーバーが `{ "type": "pong" }` を返す |

入力メッセージはリース（保持者が自身の `viewer_id`）が必要で、InputGuard を通る。拒否は `error` で返し、入力は破棄する。入力の座標・モニターは、その時点の選択モニターのもの。クライアント → サーバーのメッセージは 64 KiB まで（超過は 4400。NFR-S09）。バイナリフレーム・未知の `type`・必須の項目の欠け・未知の `action` / `button` / 修飾キーも 4400。

`input.mouse` の `down` / `up` はカーソルを `x` / `y` へ動かしてからボタンを押す・離す（`move` は動かすだけ。ブラウザ側で `Click` / `Drag` にまとめず、1 つずつ注入する）。押したままのボタン・キーを離すのは、`up` の入力（ブラウザ側が、フォーカスを失ったときなどに送る）、WebSocket の終わり（切断、同じ `viewer_id` の新しい接続による置き換え、クライアントの削除・無効化）、キルスイッチ（`ReleaseAll`）。WebSocket の終わりでは、サーバーがその接続で押したままのボタン・キーに `up` を、リースを解放する前に注入する（ボタンは最後のポインタの位置。ベストエフォートで、失敗はログに残すだけで、リースの解放は続ける）。`input.key` の `code` と `key` は、監査ログに記録しない（`type` の本文と同じ扱い）。`Ctrl+Alt+Delete`（`Delete` に `ctrl` と `alt`）は `input_blocked` の `error` で返す

**`wheel` の単位と変換**（FR-M07 / FR-W）:

- `dx` / `dy` はホイールの**ノッチ数**。符号は MCP `scroll` の方向に合わせ、`dy > 0` が下、`dx > 0` が右。1 メッセージの絶対値は 50 まで（超過は 4400）
- サーバーは `ノッチ × 120`（Win32 の `WHEEL_DELTA`）を丸めて入力に変換し、丸めの端数は次の `wheel` に繰り越す（小数のノッチを失わない）。Win32 の縦ホイールは正が上向きなので、`dy` の符号は反転して送る
- ブラウザ側（`<remo-screen-viewer>`）は `WheelEvent.deltaMode` で換算してからノッチを送る: `DOM_DELTA_PIXEL` は `delta / 100`、`DOM_DELTA_LINE` は `delta / 3`、`DOM_DELTA_PAGE` は `delta × 10`

### 5.4 画面フレーム（バイナリフレーム）

サーバー→クライアントのバイナリメッセージ 1 つが、1 回の更新を表す。リトルエンディアン。

| オフセット | 型 | 内容 |
|-----------|----|------|
| 0 | u8 | バージョン（1） |
| 1 | u8 | フラグ（bit0: キーフレーム = 全面） |
| 2 | u16 | モニター index |
| 4 | u32 | 連番（フレームごとに +1） |
| 8 | u16 | フレームの幅（px） |
| 10 | u16 | フレームの高さ（px） |
| 12 | u16 | タイル数 N |
| 14〜 | N 個 | `u16 x, u16 y, u16 w, u16 h, u32 jpeg_len, jpeg_len バイトの JPEG` |

- タイルは変化した 64×64 の格子を矩形にまとめたもの。クライアントは `createImageBitmap` で復号し、`canvas` の `(x, y)` に描画する
- キーフレームは接続直後、モニター変更時、一定間隔（既定 5 秒）に、フレーム全体を覆うタイル群として送る
- 送信キューが詰まったクライアントには古いフレームを捨て、次のキーフレームから復帰させる（遅延の累積を防ぐ）
- 解像度が変わった場合（`width` / `height` が前回と異なる）は、クライアントはキャンバスを作り直し、キーフレームを待つ

## 6. JSON API（`/api/v1`、Huma）

運用確認と将来の Lit アイランドのための小さな参照専用 API（初版のビューアはこれを呼ばない）。OpenAPI は `GET /api/v1/openapi.json`。認可は §1 の連鎖（登録済みのクライアント）。エラーは problem+json。

| メソッド | パス | 概要 | 応答 |
|---------|------|------|------|
| GET | `/api/v1/status` | サーバー状態 | `{ "version", "tailnet_ip", "port", "killswitch": {"active": false}, "agent_enabled": false }` |
| GET | `/api/v1/monitors` | モニター一覧 | MCP の `get_screen_info` の `monitors` と同じ |
| GET | `/api/v1/lease` | リース状態 | MCP の `get_control_status` と同じ形（`mine` は呼び出しのクライアントの `viewer_id` を `X-Viewer-Id` で渡したときだけ意味を持つ） |

操作系の JSON API は設けない（入力は MCP と WebSocket のみ）。

## 7. GUI ルーター（プロセス内）

ホスト GUI。Wails の WebView からのみ到達でき、TCP のソケットを開かない。Templ + HTMX で描画し、HTML フラグメントを返す。リモートの `X-Puppet-Remo`・Host / Origin の検査は付けない。ソケットを開かず、到達できるのは本アプリのプロセス内の WebView だけで（NFR-S06 により AI の入力も受けない）、それを前提とする設計であり、ネットワークから来る CSRF・DNS リバインディングは経路が無いため成立しない。プロセス内の別の呼び出し元が現れる（GUI ルーターをソケットで公開する）変更では、この前提を見直し、`RequireCSRFHeader` と Host / Origin の検査を入れること。状態変更（GET / HEAD / OPTIONS 以外）のリクエストの本文は 1 MiB まで（`router.GUIMaxBodyBytes`。NFR-S09 と同じ値）で、超えたら 413 を返してハンドラを呼ばない。WebView は外部 URL へ遷移させない。HTML 応答には §1 の CSP ヘッダーを付ける。高速パスの設定（§7.3）は**このルーターにだけ**あり、リモートルーターに同名の経路は作らない。

GUI は AI が操作できないよう、自ウィンドウをキャプチャから除外し、InputGuard が入力を拒否する（NFR-S06）。以下はすべて**ホスト所有者の操作**として監査ログに `actor_type = host` で記録する（閲覧のみの `GET` を除く）。

### 7.1 ページ（`GET`、200 HTML）

| パス | 画面 | 優先度 |
|------|------|--------|
| `/` | ダッシュボード。M1: サーバー状態、接続中セッション、リース保持者（種別付き。M2 から他者が保持中のときだけ強制剥奪ボタンを出す。FR-L06）、キルスイッチ、接続要求・クライアント管理への導線。M4 から、実行中（承認待ちを含む）のエージェントのタスクのパネル（ID の末尾 8 文字・依頼元・指示の先頭 80 文字・状態・ステップ数 / 上限・経過時間・中断ボタン。実行中のタスクが無いときは中身を出さず、`#running-tasks` と隠し入力 `rev_tasks` だけを描画する）を出し、強制剥奪ボタンは `mcp` / `web` / `agent` の保持者のとき出す（`agent` には、剥奪するとタスクが中断される旨を添える）。FR-H02・H10 | M1（強制剥奪ボタンは M2、実行中タスクと中断ボタンは M4） |
| `/requests` | 接続要求の一覧（許可 / 拒否）。「未処理」と「履歴」のタブを持つ。FR-H03 | M1 |
| `/requests/history` | 接続要求の履歴（許可・拒否・失効・未処理のすべて）。届いた日時の新しい順に 1 ページ 20 件の表（届いた日時・デバイス名・tailnet IP・ユーザー・結果・処理した日時）で、読み取り専用。クエリ `page`（1 始まり。無い・数値でない・1 未満は 1、10000 を超える値は 10000 とみなす）。前へ・次へのリンクは、その向きのページがあるときだけ出る。範囲外のページは空の表示（200）。`HX-Request` ならフラグメント（`#request-history`。ページ送りのリンクが `hx-get` で差し替えて URL を更新する）、`HX-Boosted` とそれ以外はフルページ（履歴の復元もフルページ）。ポーリング（`/poll`）の対象ではない。監査ログの閲覧（`/audit`）とは別。FR-H03 | M1 |
| `/logs` | アプリのログ（運用ログ。NFR-O01）。日時・レベル・内容（属性つき）の表で、新しい順に 1 ページ 50 件、読み取り専用。画面にログファイルのパスと、「ログフォルダーを開く」ボタン（`POST /logs/open-folder`。§7.3。#54）を出す（ボタンは `#app-log` の外にあり、絞り込み・ページ送りで差し替わらない）。クエリ `level`（`all`（既定）・`warn`（WARN 以上）・`error`（ERROR のみ）。大文字小文字を区別せず、無い・未知の値は `all`）、`page`（1 始まり。無い・数値でない・1 未満は 1、100 を超える値は 100 とみなす）。前へ・次へのリンクは、その向きのページがあるときだけ出て、`level` を保つ。範囲外のページは空の表示（200）。ログファイルが無ければ空の表示。`HX-Request` ならフラグメント（`#app-log`。絞り込みとページ送りのリンクが `hx-get` で差し替えて URL を更新する）、`HX-Boosted` とそれ以外はフルページ（履歴の復元もフルページ）。ログの読み出しに失敗したら 500（理由は画面に出さない）。監査ログの閲覧（FR-AU01）とは別。FR-H13 | M1 |
| `/clients` | クライアント一覧。`/clients/{id}`: 詳細（有効化・承認モード・トークン）。FR-H04 | M1 |
| `/apps` | 起動許可アプリの一覧・追加フォーム（`HX-Request` なら一覧のフラグメント）。`/apps/new`: 追加フォーム、`/apps/{id}/edit`: 編集フォーム（`HX-Request` ならフォームのフラグメント。フルページは一覧の該当行が編集フォーム。対象が無ければ 404）。FR-H12 | M1.5 |
| `/approvals/{id}` | 承認ダイアログ（専用の Wails ウィンドウの中身。サイドバー・ヘッダーの無い簡素なレイアウト）。クライアント名・種別・要求の内容（`lease`: 「操作権（リース）の取得」）・残り時間（60 秒のカウントダウン）・許可 / 拒否のボタンだけを出し、秘密・パス・引数は出さない。`kind` は `lease` / `safety_check`（FR-G08）/ `judge_action`（FR-G12。M4）の 3 つで、見出しと要求の内容が違い（`data-approval-type` に種類）、`safety_check`・`judge_action` は `ApprovalRequest.Detail`（サービスが秘密・入力した文字列の本文・パス・引数を除いて渡す短い文。500 文字で切り、HTML エスケープして表示）と結果の説明も出す: `safety_check` は「クライアントの承認モードが『自動許可』でも省略されない。拒否・60 秒の無応答はタスクの失敗（`safety_check_denied`）」、`judge_action` は「拒否・無応答ならそのアクションは実行されず、タスクが失敗する」。`lease` は M3 のまま（`Detail` を持たず、結果の説明も出さない）。残り時間は HTMX が 1 秒ごとに `GET /approvals/{id}`（`HX-Request`）で再取得して `#approval-remaining` だけを差し替える（ボタンは差し替えない）。`id` が失効・解決済み・存在しない・`kind` が未知なら 410 `approval_timeout`（フルページは終了した旨の表示、HTMX の応答は `data-error-code` 付きのトースト）。応答は `Cache-Control: no-store`。FR-H05 | M3 |
| `/overlay` | 操作中オーバーレイ（全モニターを覆うクリック透過の Wails ウィンドウの中身。#69。FR-H07）。クエリ `label`（「操作中: <クライアント名>（種別）」。`app` が URL に入れる。HTML エスケープして描画し、80 文字を超えたら末尾を `…` にして切り詰める。無ければ空。改行で 2 行まで: 1 行目が札の文言、2 行目は補足で、`agent` のリース保持者では「エージェント実行中: <指示の要約>」（FR-H07。M4）。各行をそれぞれ切り詰め、3 行目以降は捨てる）。背景は透明で、画面の縁の枠と上端中央の札だけを出す（入力を受けず、画面の内容を隠さない）。サイドバー・ヘッダー・ポーリング・スクリプトは無い。応答は `Cache-Control: no-store`。リース・クライアントの情報は読まない（文言は `app` が渡す）。ホスト GUI のウィンドウとしては開かれず、ブラウザで開いても枠が出るだけ | M3 |
| `/audit` | 監査ログ一覧（ホスト GUI だけ。リモートのルーターには無い）。日時（`from` / `to`。RFC 3339 のテキスト入力）・クライアント・種別・結果・イベント名で絞り込み、`ts` 降順の表（日時はホストのローカル時刻・クライアント名（スナップショット）・種別・イベント・結果バッジ（`ok` は ok、`denied` は warn、`error` は danger）・所要時間・サムネイルの有無）と「次へ」（`cursor`。前へは無い）。`HX-Request` なら `#audit-list` のフラグメント。`/audit/{id}`: 詳細（日時・種別・イベント・操作主体・クライアント名・セッション・リース・ツール・結果・エラーコード・所要時間・引数のキー・値の表・サムネイルの `<img src="/audit/{id}/thumbnail">`）。サイドバーの「監査ログ」。FR-AU05 | M3（#72） |
| `/settings` | 設定。FR-S | M1〜 |
| `/update` | 更新の確認・適用。FR-H09。現在の版・チャネル（正式版 / ナイトリー）・適用方法・最後の確認の時刻（未確認なら「未確認」）・新版（版・公開日時・リリースノートの要約・署名の検証結果）と、「今すぐ確認」（`POST /update/check`）・「更新する」（署名を検証できた新版があるときだけ。確認モーダル `GET /update/apply/confirm` を経て `POST /update/apply`）。サイドバーに「更新」。リリースノートは GitHub の Release 由来の外部文字列なので HTML エスケープして表示する（`<pre>`）。確認に失敗したとき（オフライン・非公開のリポジトリ・不正な応答）は、理由の短い文を出す（URL・応答の本文は出さない）。署名鍵が未設定のときは「署名鍵が未設定」と出し、「更新する」を出さない。`dev` ビルドは確認・適用のボタンを出さない | M3（#74） |
| `/tasks` | タスク一覧（新しい順に 50 件。クエリ `cursor`（不透明な文字列。不正は 422 `invalid_argument`）で次のページ。HTMX の部分更新は `#task-list`、`hx-boost` は本文、それ以外はフルページ）。依頼元・指示の先頭 80 文字・状態・ステップ数 / 上限・開始・所要時間の表と詳細へのリンク。`/tasks/{id}`: 詳細（指示、状態、モデル、上限、`step_count`（= `fast_steps` + `openai_steps`）と内訳、`handoff_reason`（8 種の説明付き。無ければ出さない）、使用トークン、最終回答（`completed` のときは「モデルの申告であり、成否の確認は呼び出し側の責任」の注意書きを添える。FR-G13）、`error_code`（既知は説明付き）、作成・開始・終了の時刻、ステップの表（`step_no` 昇順の経路 `fast` / `openai`・アクションの要約・結果・引数（監査が伏せたもの）・`/audit/{audit_id}` へのリンク・サムネイルがあれば `/audit/{audit_id}/thumbnail`）。実行中・承認待ちは状態の行に中断ボタン）。無い `id` は 404 `not_found`。指示・最終回答・ステップの文字列は HTML エスケープし、入力した文字列の本文・要素の値は出さない（FR-G23）。FR-H10 | M4 |
| `/agent` | エージェント設定（有効化と同意、OpenAI API キー、モデル、上限、`keep_screenshots`、Judge のリスト、**高速パス**: 有効化と同意、TypeSafe API キー、モデル、最小確信度、最大ステップ、除外ラベル）。FR-H11 | M4 |
| `/poll` | ホスト GUI の更新（OOB のフラグメント。§7.4） | M1 |
| `/events` | SSE（§7.4）。ホスト GUI のレイアウトは購読しない | M1 |

### 7.2 接続要求とクライアント

| メソッド | パス | リクエスト（フォーム） | 応答 |
|---------|------|-----------------------|------|
| POST | `/requests/{id}/approve` | `approval_mode`（`always` / `first_time` / `auto`、既定 `always`。空は `always`。定義外は 422 `invalid_argument`。**M3 から**選べる（M1・M2 のフォームは `always` だけ）。許可した時点で `clients.approval_mode` に保存する。FR-H03）、`web_enabled`、`mcp_enabled`、`agent_enabled`（既定 off。`run_task` の可否に効く（M4。M1〜M3 は保存だけで効果がない）。FR-A09）、`display_name` | 200 一覧フラグメント（クライアントを登録し、要求を `approved`）。要求が `pending` でなければ 409 `conflict` |
| POST | `/requests/{id}/deny` | なし | 200 一覧フラグメント（`denied`。24 時間は同デバイスの再要求を作らない） |
| PATCH | `/clients/{id}` | `web_enabled`、`mcp_enabled`、`agent_enabled`（`run_task` の可否に効く（M4。M1〜M3 は保存だけで効果がない）。FR-A09。`agent_enabled` を外すと、そのクライアントの実行中のタスクを中断する）、`approval_mode`（M3 から。`always` / `first_time` / `auto`、定義外は 422 `invalid_argument`）（いずれか。`approval_mode` を変えると `approved_at` を NULL に戻し、監査 `client.update` の `detail` に `approval_mode_before`・`approval_mode` を入れる。同じ値の保存では戻さない） | 200 クライアントのフラグメント。Web / MCP を無効にしたクライアントのセッションは即時に切断（そのクライアントの実行中のエージェントのタスクも中断する（FR-G10）。`agent_enabled` の変更ではセッションを切断しない） |
| POST | `/clients/{id}/approval/reset` | なし | 200 クライアントのフラグメント（`first_time` で承認済みのときだけ。`approved_at` を NULL に戻し、次のリース取得から再び承認が要る。破壊的ではないので確認モーダルは挟まない。詳細ページからは `?view=detail`）。`first_time` でない・承認済みでなければ 409 `conflict`、クライアントが無ければ 404。監査は `approval.reset`（`kind = lease`、`actor_type = host`。`detail` に `client_id`・取り消した `approved_at`）と運用ログ `client approval reset`。拒否・タイムアウト直後のクールダウン（30 秒）も解く |
| DELETE | `/clients/{id}` | なし | 200（許可リストから除外。トークンは `CASCADE` で削除、セッション・タスクは切断・中断） |
| POST | `/clients/{id}/tokens` | `label`\*、`expires_in_days`（既定 90、`never` で無期限） | 200 **トークン平文を一度だけ表示するフラグメント**。`Cache-Control: no-store`。ラベル重複は 409。以後は `token_prefix` のみ表示 |
| DELETE | `/clients/{id}/tokens/{token_id}` | なし | 200 トークン一覧のフラグメント（`revoked_at` を設定。当該トークンの MCP セッションは切断） |

#### 起動許可アプリ（M1.5。FR-H12）

登録・変更・削除は GUI ルーターだけ。リモートルーター・MCP には対応する経路を作らない。フォームの値は `application/x-www-form-urlencoded`。検証の規則は FR-H12（表示名 1〜64 文字・重複不可、実行ファイルは絶対パスのローカル `.exe` で存在すること、引数は 1 行 1 引数で 32 個・合計 8,192 文字まで、作業ディレクトリは絶対パスの既存ディレクトリ）。

| メソッド | パス | リクエスト（フォーム） | 応答 |
|---------|------|-----------------------|------|
| POST | `/apps` | `label`\*、`exe_path`\*、`args`（改行区切り。LF・CRLF どちらも可、空行は無視）、`working_dir` | 200 一覧のフラグメント（追加した行を含む。追加フォームを空に戻す OOB とトーストを添える）。検証エラーは 422（フォーム自身を入力値と項目別のメッセージで再描画）。登録が 50 件に達していれば 409 `conflict`（NFR-C06） |
| PUT | `/apps/{id}` | 同上（全項目を置き換える） | 200 行のフラグメント（トーストを添える）。422 は同上（編集フォームを再描画）。対象が無ければ 404（`data-error-code="not_found"`）。実行ファイルを変えても `id` は変わらない |
| GET | `/apps/{id}/delete/confirm` | なし | 200 確認モーダル（フラグメント）。対象が無ければ 404 |
| DELETE | `/apps/{id}` | なし | 200 一覧のフラグメント（モーダルを閉じる OOB とトーストを添える）。対象が無ければ 404。起動済みのプロセスには影響しない |
| POST | `/apps/pick-file` | `form_id`（入力欄の `id` の元。`app-form-` で始まる値だけ受け付け、それ以外は `app-form-new` にする。HTMX が送る） | ネイティブのファイル選択ダイアログ（`.exe` のフィルター。メインウィンドウに対してモーダル）を開き、**選ばれたパスを入れた `exe_path` の入力欄**のフラグメントを 200 で返す（利用者は手入力と同じように編集できる）。キャンセルは 204（差し替えない）。ダイアログが閉じるまで応答を返さない。選択だけでは何も登録せず、保存は `POST /apps` / `PUT /apps/{id}` |

- 一覧・編集の HTML は、ホスト所有者だけが見るため実行ファイルのパス・引数・作業ディレクトリを表示する（リモートには出さない）
- 追加・変更・削除はすべて `actor_type = host`、`kind = settings` で監査する（`settings.launchable_app.create` / `.update` / `.delete`。FR-AU06）。選択ダイアログの操作は監査しない（登録されたときに記録する）
- 許可リストはホスト所有者だけが変えるため、`GET /poll` の対象に追加しない（一覧の更新は操作の応答のフラグメントで行う）
- キルスイッチ中も登録・編集・削除はできる（止めるのはリモートからの起動。FR-H06）
- 注意書き: 追加・編集のフォームに、`cmd.exe`・`powershell.exe` などコマンドインタープリタやスクリプトホストを登録すると任意コードの実行を許すことになる旨の注意を常時表示する（FR-H12、NFR-S20）

### 7.3 リース・キルスイッチ・承認・タスク・設定・監査・更新

| メソッド | パス | リクエスト | 応答 |
|---------|------|-----------|------|
| POST | `/lease/revoke` | なし | 200 リースのフラグメント（`#lease` の中身。保持者を剥奪して空きにする。`agent` ならタスクを中断）。**M2 から**（FR-H02、FR-L06）。操作主体は `host` で、監査は `lease.revoke`（`actor_type = host`）。**保持者がいなければ何もせず 200**（冪等。トーストも出さない）。剥奪できるのは `mcp` / `web` / `agent` の保持者（保持者の種別から決める）で、`agent` の剥奪はそのタスクを `cancelled` にする（M4。FR-L08）。成功すると「操作権を剥奪しました。」のトーストを OOB で添える |
| POST | `/logs/open-folder` | なし（本文・クエリ・パスを読まない） | 200（OOB のトースト「ログフォルダーを開きました。」だけ。ボタンは `hx-swap="none"`）。**ログ画面（FR-H13）のボタンの操作で、開く場所は実装（`service.AppLog.OpenFolder` の実体 `app.LogReader`）が固定する**: ログファイルのあるフォルダー（`%LOCALAPPDATA%\puppet-remo\logs\`）ちょうど 1 つを OS のファイルマネージャー（Windows は `ShellExecute` の `open`。シェル・コマンドラインを経由しない）で開く。パス・ファイル名・フォルダーをリクエストで指定する手段は無く、ファイルは開かず、フォルダーは作らない。フォルダーが無ければ 404 `not_found`（「ログフォルダーが見つかりません。」）、それ以外の失敗は 500 `internal`（「ログフォルダーを開けませんでした。」）で、どちらもエラーのトースト（固定の文言。パス・OS のエラー文は出さない）。GUI ルーターだけにあり（リモートルーターに同名の経路は作らない）、POST だけ（GET は 404）。ログの内容は変えず、監査はしない（設定・状態を変えない）。FR-H13 |
| POST | `/killswitch/trigger` | なし | 200 ダッシュボードのフラグメント（FR-H06 の手順。1 秒以内） |
| POST | `/killswitch/release` | なし | 200（解除。受付を再開）。ダッシュボードのキルスイッチのパネルを返し、ヘッダーのバッジの更新・確認モーダルを閉じる・トーストを OOB で添える |
| GET | `/killswitch` | なし | 200 キルスイッチのパネルのフラグメント。発動中は解除のボタンだけ、そうでなければ発動のボタンだけを含む。画面の更新は `GET /poll`（§7.4）が OOB で差し替える |
| GET | `/killswitch/release/confirm` | なし | 200 解除の確認モーダル（フラグメント）。発動中でなければ 409 `conflict` |
| GET | `/clients/{id}/delete/confirm`、`/clients/{id}/tokens/{token_id}/revoke/confirm` | なし | 200 確認モーダル（破壊的な操作は、サーバーが描画したモーダルの確定ボタンだけが実際の `DELETE` を送る。frontend.md §5.4）。対象が無ければ 404 |
| POST | `/approvals/{id}/allow` | なし | 200 ダイアログを閉じる指示（`HX-Trigger: closeWindow`。本文は「許可しました。」）。`Approval.Resolve(id, true)` を呼び、リースの取得が `held` になる。`id` が失効（60 秒）・解決済み・存在しないなら 410 `approval_timeout`（`HX-Trigger` なし。トースト） |
| POST | `/approvals/{id}/deny` | なし | 同上（拒否。`Approval.Resolve(id, false)`。取得は `approval_denied`） |
| POST | `/tasks/{id}/cancel` | なし | 200 タスクの状態の 1 行（`[data-task-status]`。状態バッジ・ステップ数 / 上限・経過時間。中断できる状態なら中断ボタン）と、完了のトースト（OOB）。ダッシュボードの実行中タスクと詳細の先頭が同じ部品で、ボタンは `closest [data-task-status]` を `outerHTML` で差し替える。操作主体は `host` で、監査は `TaskAdmin.CancelByHost` が記録する。**終了済みなら現在の状態をそのまま返し**（冪等。トーストは「すでに終了しています」）、中断した（`cancelled`）ときは「タスクを中断しました。」、まだ終わっていないときは「タスクの中断を要求しました。」。無い `id` は 404 `not_found`（エラーのトースト）。M4 |
| PUT | `/settings` | `server.port`（1024〜65535）、`killswitch.hotkey`、`screenshot.max_dimension`（256〜4096）、`lease.idle_timeout_sec`（30〜3600）、`viewer.default_quality`、`viewer.default_fps`、`audit.thumbnails_enabled`、`audit.record_text`、`audit.retention_days`、`audit.thumbs_max_bytes`、`update.apply_mode`、`update.channel`（`stable` / `nightly`）、`log.level`、`autostart`（OS のスタートアップ登録） | 200 フォームのフラグメント。検証エラーは 422 のフラグメント（項目別のメッセージ）。`server.port` の変更は再 Listen する |
| PUT | `/agent` | `enabled`、`consent`（`enabled` を on にするときは必須のチェック）、`model`、`max_steps`（1〜500）、`max_duration_sec`（30〜7200）、`keep_screenshots`（1〜10。FR-S19）、`deny_processes`、`approve_processes`（改行区切り） | 200 フラグメント。API キー未登録で `enabled` にしようとすると 409 `agent_disabled`。同意なしは 422。有効化時は `agent.consent_at` と監査記録（FR-G14） |
| PUT | `/agent/api-key` | `api_key`（伏せ字の入力） | 200 「登録済み」のフラグメント。**値は応答・ログ・監査に出さない**。資格情報マネージャーに保存し、保存に失敗したら 500 `internal` |
| DELETE | `/agent/api-key` | なし | 200（資格情報を削除し、エージェントを無効にする。実行中のタスクは中断） |
| PUT | `/agent/fastpath` | `enabled`、`consent`（`enabled` を on にするときは必須のチェック。エージェントの `consent` とは別）、`model`（既定 `jev-1.13`。FR-S15）、`min_confidence`（0〜1、既定 0.7。FR-S16）、`max_steps`（1〜500、既定 15。FR-S17）、`excluded_labels`（改行区切り。FR-S18） | 200 フラグメント。TypeSafe API キー未登録で `enabled` にしようとすると 409 `conflict`、同意なしは 422（項目別メッセージ）。on にしたとき `agent.fastpath.consent_at`（UTC ミリ秒）を記録し、off にしたとき削除する。どちらも高速パス用の別の監査イベントを残す（FR-G22）。エージェント自体の有効・無効とは独立 |
| PUT | `/agent/fastpath/api-key` | `api_key`（伏せ字の入力） | 200 「登録済み」のフラグメント。**値は応答・ログ・監査に出さず、登録後は表示しない**。資格情報マネージャーのターゲット `puppet-remo/typesafe:puppet-remo` に保存し、保存に失敗したら 500 `internal`。登録だけでは高速パスは有効にならない（`enabled` と同意が別に要る） |
| DELETE | `/agent/fastpath/api-key` | なし | 200（資格情報を削除し、`agent.fastpath.enabled` を off、`consent_at` を削除する。実行中のタスクは失敗させず、以後の高速ステップを行わず OpenAI で続行する） |
| GET | `/audit` | クエリ: `from`, `to`（RFC 3339）、`client_id`, `kind`, `result`, `q`（イベント名の部分一致）、`cursor`, `limit`（既定 50、最大 200） | 200 一覧フラグメント（`ts` 降順。`cursor` は最後の `id`）。**M3（#72）の実装**: `HX-Request`（履歴の復元を除く）は `#audit-list` のフラグメント、`HX-Boosted` とそれ以外（履歴の復元を含む）はフルページ。範囲外・結果なしは空の表示（200）。不正な値（RFC 3339 でない `from` / `to`・未知の `kind`（`auth` / `client` / `token` / `lease` / `tool` / `input` / `agent` / `killswitch` / `settings` / `update`）・未知の `result`（`ok` / `denied` / `error`）・数値でない・負の `limit` / `cursor`）は 422（同じフラグメントを項目別のメッセージ（`data-field-error`）付きで描き直し、読み出しはしない）。`limit` は 200 を超えると 200。`from` / `to` は RFC 3339 のテキスト入力で、URL にもそのまま出る（ブラウザの日時入力欄への変換・ホストのタイムゾーンの補完はしない）。「次へ」の URL は今の絞り込みと次の `cursor` を持つ |
| GET | `/audit/{id}` | なし | 200 詳細のページ（`HX-Request` は `#main` の中身）。対象が無い・`id` が正の整数でないときは 404（`data-error-code="not_found"`）。引数は `AuditReader` が秘密を伏せた値だけを、キーの昇順で出す（HTML エスケープ。M3・#72） |
| GET | `/audit/{id}/thumbnail` | なし | 200 `image/jpeg`（`audit_thumbnails.rel_path` を `thumbs/` 配下に限定して配信。パスの走査を拒否）。無ければ 404。**M3（#72）の実装**: `Cache-Control: no-store`・`X-Content-Type-Options: nosniff`。パスはユーザー入力ではなく DB の `rel_path` で、`ThumbStore` が `thumbs/` の外を指すパスを拒否したとき（`ErrInvalidArgument`）も画像は返さず 404（運用ログに警告）。サムネイルの無い行・ファイルの無い行・`id` が正の整数でない場合も 404。読み出しの失敗は 500 |
| GET | `/update/apply/confirm` | なし | 200 確認モーダル（版を表示。確定のボタンが `POST /update/apply`）。署名を検証できた新版が無いときは 409（`data-error-code="conflict"`、モーダルなし） |
| POST | `/update/check` | なし | 200 更新のパネルのフラグメント（`#update-panel`。新版の有無・署名の検証結果・失敗の理由）とトースト（OOB）。GitHub への確認と、新版があれば署名の検証まで済ませる（`update.detected` / `update.verify_failed` / `update.downgrade_rejected` を `actor_type = host` で監査）。確認の失敗は 200 で、パネルに理由を出す |
| POST | `/update/apply` | なし | 200（署名検証に成功した更新を適用する。適用の直前に署名をもう一度検証し、NSIS のインストーラを `/S` で起動して、アプリを終了する。パネル・確認モーダルを閉じる・トーストを返す。`update.apply_started` を監査）。**409 `conflict`**（トーストに理由）: リース保持中・承認待ち・接続中（MCP のセッション）・タスク実行中、適用できる更新が無い、署名鍵が未設定、適用の直前の検証に失敗。500 `internal`（インストーラを起動できなかった。`update.apply_failed` を監査。パス・OS のメッセージは画面に出さない） |

### 7.4 GUI の更新の配信（`GET /poll`・`GET /events`）

**ホスト GUI のレイアウトは `GET /poll` を 3 秒ごとに呼んで更新する。SSE（`GET /events`）は購読しない。** Wails v3.0.0 の Windows の AssetServer は `AssetOptions.Handler` の応答をハンドラが返るまでバッファし、`http.Flusher` を持たないため、開いたままの SSE の応答が WebView に届かない（frontend.md §5.3）。`GET /events` は、ストリームを流せる経路で GUI を配信するとき・テストのために残す。

#### `GET /poll`

| 項目 | 内容 |
|------|------|
| リクエスト | クエリ `rev_killswitch`（ダッシュボードを表示中の画面が送る。キルスイッチのパネルが持つ版）、`rev_requests`（接続要求の一覧を表示中の画面が送る。一覧が持つ版）。どちらも省略できる。`HX-Request` は不要 |
| 応答 | 200 HTML。`hx-swap-oob` のフラグメントだけを並べた 1 つの応答（フルページではない）。`Cache-Control: no-store`。CSP ヘッダーは §1 のとおり |

| OOB の差し込み先 | 内容 | 返す条件 |
|-----------------|------|---------|
| `#header-killswitch`（innerHTML） | ヘッダーのキルスイッチ状態バッジ | 常に |
| `#nav-requests-badge`（innerHTML） | サイドバーの接続要求の件数バッジ | 常に |
| `#server-status`・`#sessions`・`#lease`（innerHTML） | サーバー状態・接続中セッション・リース保持者（M2 から強制剥奪ボタンを含む） | `rev_killswitch` があるとき（ダッシュボード） |
| `#killswitch-panel`（outerHTML） | キルスイッチのパネル（状態駆動: 発動中は解除だけ） | `rev_killswitch` があり、現在の版と違うとき |
| `#request-list`（outerHTML） | 接続要求の一覧 | `rev_requests` があり、現在の版と違うとき |
| `#toast-root`（beforeend） | 前回の呼び出し以降に溜まった `toast`（取り出した分は次の応答に出ない） | あるとき |

版（`rev_*`）は、描画した表示モデルのハッシュで、一覧・パネルの隠し入力（`<input type="hidden" name="rev_…" data-poll-rev>`）が持つ。レイアウトの `#live-poll` が `hx-include="[data-poll-rev]"` で送り返す。版が同じ領域は差し替えない（入力中の許可フォームを、ポーリングで消さないため）。応答に秘密（トークンなど）は含めない。`approval`（M3）は、この経路の対象外。`tasks`（M4）はダッシュボードの `#running-tasks`（`rev_tasks`。実行中・承認待ちのタスクの表示。経過時間を含むので、実行中は毎回差し替わる。無いときは中身が空の同じ要素）で、タスクの一覧・詳細は対象にしない。

#### `GET /events`（SSE）

接続は 1 本で、`data` は `GET /poll` と同じボディのコンポーネントで描画した HTML フラグメント。

| event | 内容（`data`） | 更新される画面 |
|-------|---------------|---------------|
| `server-status` | サーバー状態のフラグメント（tailnet IP・ポート・tailscaled 状態） | ダッシュボード |
| `sessions` | 接続中セッション一覧のフラグメント | ダッシュボード |
| `lease` | リース保持者のフラグメント（種別付き。M2 から強制剥奪ボタンを含む） | ダッシュボード |
| `connection-requests` | サイドバーの件数バッジのフラグメント | 接続要求、トレイ通知 |
| `approval` | `{ "id": "…", "kind": "lease" \| "safety_check" \| "judge_action" }`。受信したアプリが承認ダイアログのウィンドウを開く（M3 は `lease` だけ） | （ウィンドウ生成。DOM は更新しない） |
| `approval-closed` | `{ "id": "…" }`（M3・#68）。承認待ちが終わった（許可・拒否・60 秒の失効・取り下げ）。受信したアプリが開いているダイアログを閉じる | （ウィンドウを閉じる。DOM は更新しない） |
| `tasks` | 実行中タスクのフラグメント（ステップ数・状態・現在の経路 `fast` / `openai` と引き継ぎ理由。M4） | ダッシュボード、タスク |
| `killswitch` | ヘッダーのキルスイッチ状態バッジのフラグメント | 全画面のヘッダー |
| `toast` | 通知メッセージ（更新の通知、DB 破損の隔離、TypeSafe が 401 を返して高速パスを無効にした通知など） | 全画面 |

承認ダイアログに表示する内容は `kind` による。`lease`: クライアント名・種別・（`agent` なら指示の要約）。`safety_check`: OpenAI の `pending_safety_checks` の内容。`judge_action`: アクションの内容、対象ウィンドウ（タイトル・プロセス名）、判定の理由。いずれも 60 秒で失効し拒否扱い。

### 7.5 M1 実装での差分

M1 の実装が本書の他の節と異なる点（段階の前倒しではなく、実装の事実）。

| 項目 | 設計 | M1 の実装 |
|------|------|----------|
| リモートルーターの提供物（§1） | `/mcp`、`/viewer`、`/ws/screen`、`/api/v1` | **`/mcp`・`GET /ws/screen`（M2。#61）・Web UI（`GET /`・`/viewer*`。M2。#62）・`GET /static/*`（M2。#62）**。それ以外のパスは 404。`/api/v1`（Huma）は M2 以降の別の段階 |
| GUI ルーターのルート（§7.1〜§7.3） | 表のとおり | M1 にあるのは `GET /`・`/requests`・`/clients`・`/clients/{id}`・`/settings`・`/poll`・`/events`・`/killswitch`、確認モーダル（`/clients/{id}/delete/confirm`・`/clients/{id}/tokens/{token_id}/revoke/confirm`・`/killswitch/release/confirm`）、`POST /requests/{id}/approve`・`/requests/{id}/deny`・`/clients/{id}/tokens`・`/killswitch/trigger`・`/killswitch/release`・`/lease/revoke`（M2）、`PATCH` / `DELETE /clients/{id}`、`DELETE /clients/{id}/tokens/{token_id}`、`PUT /settings`、`/static/*`、M1.5 の起動許可アプリ（`GET /apps`・`/apps/new`・`/apps/{id}/edit`・`/apps/{id}/delete/confirm`、`POST /apps`・`/apps/pick-file`、`PUT` / `DELETE /apps/{id}`）。M3 の監査ログの閲覧（#72）: `GET /audit`・`/audit/{id}`・`/audit/{id}/thumbnail`（読み取りだけ。ホスト GUI のルーターだけ）。M3 の更新（#74）: `GET /update`・`/update/apply/confirm`、`POST /update/check`・`/update/apply`（ホスト GUI のルーターだけ）。**未実装**（それぞれ表の段階で足す）: `/approvals/*`・`/tasks*`・`/agent*`・`/lease/revoke`・`/clients/{id}/approval/reset` |
| `PUT /settings` の項目（§7.3） | 14 項目 | `server.port`（1024〜65535）・`killswitch.hotkey`・`screenshot.max_dimension`（256〜4096）・`lease.idle_timeout_sec`（30〜3600）・`autostart` の 5 項目（M1）に、M2 で `viewer.default_quality`（1〜100）・`viewer.default_fps`（1〜30）を足した 7 項目。M3 の #73 で `audit.thumbnails_enabled`・`audit.record_text`（チェックボックス。外れていると値が送られず、off として保存する）・`audit.retention_days`（1〜36500）・`audit.thumbs_max_bytes`（1〜2^53。単位はバイトのまま表示する）・`update.apply_mode`（`confirm` / `auto_idle`。#74 で `service.Updater` が読む）を足した 12 項目。#74 で `update.channel`（`stable` / `nightly`。既定 `stable`。未知の値は 422）を 1 項目足した（左の項目数も 1 つ増える）。`log.level` は未実装（未決）。検証エラーは 422 のフラグメントで項目別に出す（`audit.retention_days` と `audit.thumbs_max_bytes` の範囲は `Maintenance` の読み取りと同じ。範囲・単位は所有者の確認待ち）。保存済みの `audit.*` の数値が範囲外のときは、フォームは既定値を表示する（`Maintenance` が実際に使う値）。他は各段階で足す |
| ホスト GUI の更新（§7.4） | SSE | ホスト GUI は **`GET /poll`**（3 秒ごと。表示中の領域が持つ隠し入力 `rev_*` の版が変わったときだけ OOB で差し替える。`toast` は `Bus` の購読を `gui.Handler` が待ち行列にし、`/poll` が取り出す）で更新する。`GET /events`（SSE）は残しているが、GUI のレイアウトは購読しない。`htmx-ext-sse` は `dist/js` に同梱するが読み込まない（未使用） |
| GUI のエラー応答（§2） | `/api/v1`・MCP は problem+json / `isError`。HTML の経路は同じ `code` と HTTP ステータスのフラグメント | GUI の対象なし（クライアント・トークン・起動許可アプリ）は 404、`data-error-code="not_found"`（§2 のコード表に無い、GUI 内のコード）。`invalid_argument`（入力の不備）は 422 のフラグメント（§2 の表の 400 ではなく、フォーム再描画の 422）。想定外は 500 `internal`（詳細はログにだけ出す） |
| 接続要求の許可（§7.2） | `pending` なら登録して `approved` | 期限切れの `pending` は一覧に出ず、許可もできない（409 `conflict`）。同じ `stable_node_id` の登録済みクライアントがあるとき（ログイン名の変更後の再許可）は、古いクライアントを置き換える（トークンは CASCADE で削除、セッションは切断、リースは解放）。このとき監査に `client.replace` を記録する。古いクライアントの切断（ツール呼び出しの完了待ち）は、他の許可・GUI の操作を止めないよう、許可の直列化のロックの外で行う。同じデバイスの `pending` を 5 秒以内に更新済みなら、DB に書かない |
| Web 入力の監査（§5.3・M2） | Web UI 入力を記録（FR-AU01） | 入力の `kind` は `input`（`actor_type = web`、`session_id = viewer_id`）。`input.mouse` の `move` は連続するため記録せず、`down` / `up` / `wheel` と `input.key`（キーの名前は入れない）・`input.text`（既定は文字数のみ）・クリップボード（既定は文字数のみ）を記録する。設定 `audit.record_text`（FR-S07）が true のときは `input.text` とクリップボードの本文も `detail_json` の `text` に記録する（4,096 文字まで。ER「イベントのカタログ」。M3・#71）。**サムネイル（M3・#71。FR-AU02）**: 設定 `audit.thumbnails_enabled`（既定 true）が有効で、`result = ok` の `down` / `up` / `wheel` / `input.key` / `input.text` は、操作の直後に非同期で縮小スクリーンショットを撮り、同じ監査の行に付ける（`GET /audit/{id}/thumbnail`）。モニターは常にプライマリで、撮影は **Web 入力の全体で 1 秒に 1 枚まで**（ビューアごとではない。間引かれた入力の行はサムネイル無しで記録する）。`move`・クリップボード・拒否や失敗の入力は撮らない。撮影・保存の失敗は運用ログだけで、入力の結果を変えない。MCP の操作系ツールは、クリック・ドラッグ・`scroll`・`key`・`type`・`focus_window`・`launch_app` が成功したときに同様に撮る（間引きなし。モニターは明示した `monitor`、無ければ座標を含むモニター、座標も無ければプライマリ。`mouse_move` だけの移動は撮らない）。エントリは撮影のあとに記録するため、`Audit` の記録の順序は操作の完了順と前後することがある（`ts` は操作の時刻） |
| 監査（§7 の冒頭） | ホスト操作は `actor_type = host` | `client_id` は、クライアントの行がすでに無いとき（`client.delete` の直後など）NULL で記録する（`client_label` は残る） |
| MCP ツールの監査（§3.3） | — | `Control` を通らないツール（`get_screen_info`・`screenshot`・`cursor_position`・`list_windows`・`acquire_control`・`release_control`・`get_control_status`・`run_task`・`get_task`・`cancel_task`）の呼び出しは、`mcp` のツール登録で `kind = tool` として監査する。入力系などは `Control` が記録する。タスクのツールの `detail` は、`run_task` が `prompt_len`（文字数のみ。指示の本文は載せない。FR-AU03）・指定した `max_steps` / `max_duration_sec`・成功時の `task_id`、`get_task` / `cancel_task` が `task_id` で、`task_id` 列は付けない（タスクの詳細のステップには `Agent` が記録する `agent.*` の行だけが出る） |
| HTTP サーバー（§1） | — | tailnet 向けの `http.Server` は `ReadHeaderTimeout` 10 秒、`IdleTimeout` 120 秒、`MaxHeaderBytes` 64 KiB。`WriteTimeout` は付けない。WhoIs の LocalAPI 呼び出しは 5 秒で打ち切り、失敗は 403 `whois_failed`（fail closed） |
| MCP の keepalive（§3.1・NFR-A04） | — | 既定で ping しない（§3.1） |
| Web の承認待ち（§4.2・M3。#67） | `POST /viewer/lease/acquire` は承認待ちなら承認待ちのパネルを 202 で即座に返す | 承認が要るクライアントでは、`LeaseManager.RequestAcquire`（`Acquire` の待たない版）が承認待ちにして待ちを `LeaseManager` の goroutine（リクエストの ctx から独立。上限は承認の失効 + 30 秒。キルスイッチ・切断・シャットダウンで取り下げ）へ渡し、ハンドラは待たずに **202** の承認待ちのパネル（`pending_approval`・本人）を返す（オーナー決定）。結果は WebSocket で届く: 許可は `lease`（`held`）、拒否・タイムアウトは `Bus` の `lease-result`（`service.LeaseResult`）を ws が要求者のビューアだけの接続へ `error`（`approval_denied` / `approval_timeout`）で送る（`static/src/app/app.ts` が日本語のトーストにする）。承認待ちのあいだの再試行（408 に見えた再送など）は、新しい承認の要求も goroutine も作らず同じ 202 を返す（`Approval.Request` は 1 回）。拒否・タイムアウトの直後 30 秒は、同じクライアントの取得を承認なしで 403 `approval_denied`（ダイアログなし。監査は `lease.acquire` の `denied`・`detail.cooldown`）。承認が要らなければ従来どおり 200。承認待ちのあいだ、ほかのビューア（と、WebSocket の `lease` で更新された本人のパネル）は「承認待ち: <名前>（種別）」を見る。WebSocket の切断（セッションの終わり）で承認待ちは取り下げられる |
| 承認ダイアログ（§7.1〜§7.4・M3。#68） | `/approvals/{id}` は専用の Wails ウィンドウ。許可 / 拒否は `Approval.Resolve` | `gui.Deps.Approval`（`Resolve`・`Pending`。`service.Approval` が満たす）。ウィンドウは `app` の `approvalDialogs` が `Bus` の `approval` / `approval-closed` を購読して開閉する（`Approval.Pending` を真実の源にする。`Windows.OpenApproval(id)`）。ダイアログが同時に開くのは 1 つで、重なった要求は到着順（`Approval.Pending` の順）に待たせ、今の要求が終わったら（`approval-closed`）まだ承認待ちの次の要求のダイアログを開く（待たされた要求の 60 秒はその要求自身の到着時刻から数える。待っている間に失効した要求は開かない）。許可も拒否もせずウィンドウを閉じても承認待ちは続き、60 秒の失効で拒否になる（再度は開かない。docs に定めが無いため、承認が勝手に通らない保守的な動き）。その要求が終わるまでは待たされている次の要求のダイアログも開かない。ウィンドウを閉じる `HX-Trigger: closeWindow` は `static/src/app/app.ts` が受け、Wails のランタイム（`/wails/runtime.js`）の `Window.Close` を呼ぶ（`app` 側も承認の終了で閉じるので、これが失敗しても承認の結果に影響しない）。`kind` が `lease` 以外（M4）は開かず 410 |
| 操作中オーバーレイ（§7.1・M3。#69） | `/overlay` は全モニターを覆うクリック透過の Wails ウィンドウの中身 | `GET /overlay?label=…`（`handler/gui/overlay.go`・`pages.Overlay`・`layout.Overlay`・`organisms.Overlay`）。`label` は templ が HTML エスケープし、80 文字（ルーン）を超えたら切り詰める。htmx・`app.js`・ポーリングは読み込まず（入力を受けない。更新は `app` がウィンドウの URL を差し替える）、CSS（`/static/css/app.css`）だけを読む。ウィンドウは `app` の `overlayController` が `Bus` の `lease` を購読して `Windows.ShowOverlay(monitors, label)`・`HideOverlay()` で出し入れする（`held` → 全モニター、保持者の変更 → `label` の更新、解放・`pending_approval` → 消去。§7.4 の `lease`） |
| エージェントのタスク画面（§7.1・§7.3・§7.4・M4。FR-H10・H02。Issue #19） | `/tasks`・`/tasks/{id}`・`POST /tasks/{id}/cancel`、ダッシュボードの実行中タスク | `gui.Deps.Tasks`（利用者側のインターフェース `Tasks`。メソッド集合は `service.TaskAdmin` と同じ: `List`・`Detail`・`Running`・`CancelByHost`。`Deps` の必須の依存）。`handler/gui/tasks.go`・`pages.Tasks` / `TaskDetail`（と `*Boosted`）・`organisms.TaskList` / `TaskDetail` / `TaskStatus` / `RunningTasksPanel`・`view/tasks.go`。サイドバーの「タスク」（`view.NavTasks`）。中断のボタンは確認モーダルを挟まない（強制剥奪のボタンと同じ）。`domain.ErrTaskNotFound` は GUI の `not_found`（404）に写す（`errors.go`）。ダッシュボードの `#running-tasks` は `GET /poll` の `rev_tasks`（経過時間を含む版）で差し替える。`guitest.Tasks` は `TaskAdmin` の取り決め（新しい順・`cursor`・`Limit` 0 は 50・無い id は `task_not_found`・中断の冪等）を守る偽実装。デモ（`guidemo`）は `-lease held_agent` で agent のリースと実行中タスク、`-approval safety_check|judge_action` で承認ダイアログを出す。スクリーンショットは `docs/screenshots/m4/`（`tasks-*`・`approval-*`・`overlay-*`・`dashboard-*`） |
| 承認ダイアログの種類・エージェントのオーバーレイ（§7.1・M4。FR-H05・H07） | 承認の種類は `lease` / `safety_check` / `judge_action`。オーバーレイは `agent` のとき「エージェント実行中」と指示の要約を併記する | `handler/gui/approvals.go` の `approvalTexts`（種類ごとの見出し・要求の内容・結果の説明）と `Detail` の表示（`organisms.ApprovalDialog`）。`app` の `approvalDialogs` は 3 つの種類すべてでダイアログを開き、`Windows.OpenApproval(id, kind)` の `kind` で背の高さを選ぶ（`lease` 340、`safety_check`・`judge_action` 560）。オーバーレイは `app` の `overlayController` が `agent` の保持者のとき `taskReader.Detail(Actor.SessionID = task_id)` で指示を読み、2 行目に「エージェント実行中: <指示の要約（空白をたたみ 40 文字）>」を付けた `label` を `ShowOverlay` へ渡す（読めなければ「エージェント実行中」だけ）。TaskAdmin の結線は後続のスライスが行う（それまで `nil` で、要約なし） |
| 更新（§7.1・§7.3・M3。#74） | 「更新」の画面・確認・適用 | `GET /update`・`/update/apply/confirm`、`POST /update/check`・`/update/apply`（`handler/gui/update.go`・`pages.Update`・`organisms.UpdatePanel`）。確認・適用は `service.Updater` が行い、ハンドラは描画と受け付けだけ。取得元は `https://api.github.com/repos/ousiassllc/puppet-remo/releases`（`stable` は `/releases/latest`、`nightly` は直近 30 件）を**認証なし**で呼ぶ（リポジトリは公開が前提。非公開の間は 404 / 401 / 403 で確認が失敗し、運用ログに残るだけでアプリは止まらない）。ダウンロードの URL は応答の値を使わず、タグとファイル名から組み立てる。署名（`<インストーラ>.minisig`。minisign・Ed25519）を埋め込みの公開鍵で検証し、署名の trusted comment が `version=<版> file=<ファイル名>` に一致しなければ拒否する。検証に成功した版だけを画面・トースト（`Bus` の `toast`。同じ版は 1 度）で知らせる。適用できないとき（リース保持中・承認待ち・MCP セッションの接続中・タスク実行中）は 409 `conflict` で、理由を日本語でトーストに出す。`auto_idle` は、署名検証済みの新版があるとき 30 秒ごとに空き（上記の 4 条件が無い）を確認して自動で適用する（`actor_type = system`）。監査は `kind = update`（`update.detected` / `verify_failed` / `downgrade_rejected` / `apply_started` / `apply_failed`。同じ版・理由の繰り返しは記録しない。URL・署名・パスは記録しない） |
| エージェント設定（§7.1・§7.3・M4。#19） | `/agent` と `PUT /agent`・`/agent/api-key`・`/agent/fastpath`・`/agent/fastpath/api-key`、`DELETE` の 2 つ | `GET /agent`（`handler/gui/agent.go`・`pages.AgentSettings` / `AgentSettingsBoosted`）。画面は `#agent-sections`（OpenAI のカードと高速パスのカード）で、キーごとのパネル（`#agent-key`・`#fastpath-key`。登録の有無は `service.AgentSettingsView` の真偽値だけ）とフォーム（`#agent-form`・`#fastpath-form`）に分かれ、それぞれが自身を `outerHTML` で差し替える。`gui.Deps.AgentSettings`（`service.AgentSettings` と同じメソッド集合）が必須の依存。**`PUT /agent`・`PUT /agent/fastpath`**: 検証エラーは 422 のフォーム（入力値のまま・項目別のメッセージ。数値に読めない項目と範囲の不備を同時に示す）。成功は 200 のフォームとトースト（OOB）。**API キー未登録での有効化**は、`PUT /agent` が 409 `agent_disabled`（エラーコード表の `agent_disabled` も 409 で、同じ）、`PUT /agent/fastpath` が 409 `conflict`。トーストのフラグメントで、設定は変わらない。リスト（`deny_processes` など）は GUI が改行で分割し、各行の前後の空白を除いて空行を捨てて渡す。**`PUT /agent/api-key`・`PUT /agent/fastpath/api-key`**: キーは**本文の `api_key` だけ**で受ける（URL・クエリは読まない）。前後の空白・改行は除き、空・4,096 バイト超・空白や制御文字・非 ASCII を含むものは 422（項目のメッセージ。実体を呼ばない）。入力欄は `type="password"`・`autocomplete="off"` で、`value` を描画しない（失敗の再描画でも空）。応答・トースト・エラー・運用ログのどこにも値を出さない（保存の失敗は 500 `internal` の汎用トーストで、運用ログに残すエラー文からもキーを伏せる）。キーに関わる応答（登録・削除の確認・削除）は `Cache-Control: no-store`（GUI ルーターは動的な応答すべてに付ける）。成功は 200 の「登録済み」のパネルとトースト。画面に「資格情報マネージャーにだけ保存し、登録後は表示しない」と明記する。**`DELETE /agent/api-key`・`DELETE /agent/fastpath/api-key`**: 先に確認モーダル（`GET /agent/api-key/delete/confirm`・`GET /agent/fastpath/api-key/delete/confirm`。サーバーが描画。キー未登録なら 409 `conflict`）を経る。確定は 200 で、有効・同意の状態も変わるため `#agent-sections` 全体を返し、モーダルを閉じる・トーストを OOB で添える。 |
| WebSocket（§5・M2。#61） | 閉じコード・メッセージは §5 | ライブラリは `github.com/coder/websocket` v1.8.15。接続は圧縮しない（JPEG）。クライアントからのメッセージの上限（64 KiB）はライブラリの上限（1009 で閉じる）ではなく自前で検査し、4400 で閉じる。置き換えられた古い接続は 4401（§5.2）。`lease` の `holder` は `idle` のときだけ省く（M3: `pending_approval` は承認待ちの要求者の `holder` と、要求者本人かの `mine`）。`ready` の `monitors[]` の形は §5.3 |
| Web UI（§4・M2。#62） | §4.2 のとおり | ハンドラは `web/handler/remote`（Gin。`router.NewRemote` が `/`・`/viewer`・`/viewer/` のパターンでマウントする）。未登録デバイスのページの選択は `middleware.RemoteConfig.ConnStates`（`service.ConnRequestStates`。読み取り専用。nil なら常に待機）。ページは `pages.Pending` / `Denied` / `Expired`（アクションルートにも同じページを返す）。ミドルウェアの HTML の拒否（`invalid_host` など）は `pages.RemoteError`（ページ）と `atoms.Alert`（アクションルートのフラグメント）。存在しない `monitor` の検証エラーは、ホスト GUI の検証エラーと同じ 422 `invalid_argument` のフォーム再描画（§4.2）。`<remo-screen-viewer>` の `dist/js/screen-viewer.js` はレイアウトが読み込むが、成果物は別の子 Issue（#63）が出す（それまでは 404 で、ライブ画面は出ない） |

## 8. 決定事項と実装時の検証事項

### 8.1 決定事項

| 項目 | 決定 |
|------|------|
| MCP Go SDK（v1.8.0）の設定 | `DisableLocalhostProtection` は loopback で届くリクエストにしか効かず、tailnet IP への接続には無関係。保護は自前の Host / Origin ミドルウェア（NFR-S02）が担う。`CrossOriginProtection` は非推奨のため使わない。`SessionTimeout` は既定（0）だと閉じないため**アイドル 30 分**に明示する（リースのアイドル 5 分とは独立）。`KeepAlive` は**既定の 0（ping しない）**のまま（NFR-A04。理由は §3.1）。`EventStore` は v1 では使わず、ストリームの再開は無い（切断後はクライアントが再初期化する）。`MaxRequestBodyBytes` は **1 MiB** に明示する（NFR-S09）。`Stateless = false`（リース保持者はセッションに束縛）。応答は既定の `JSONResponse` |
| OpenAPI の範囲 | Huma が公開するのは §6 の JSON API（`/api/v1`）のみ。MCP のツール定義は MCP の `tools/list` が正とする。高速パスは内部機構で API ではなく、OpenAPI にも MCP のツールにも現れない |
| WebSocket の高解像度 | v1 では 4K 以上でも縮小配信せず、物理解像度のまま配信する。`ready` / `config` に後から `scale` を足せる形にしてあるが、v1 では送らない |
| ホイールの単位 | §5.3 のとおり、WebSocket の `wheel` の `dx` / `dy` と MCP `scroll` の `amount` はノッチ数。サーバーが `×120` に変換する |
| 静的ファイルのキャッシュ | `Cache-Control: no-cache` + `ETag`（ビルド ID）（§1） |

### 8.2 実機・実 API で検証する事項

設計は決定済みで、実装時に実機で確かめるもの。

| 項目 | 確認内容 |
|------|---------|
| GUI ルーターの CSP | Wails の WebView のオリジン（開発時は WebView の URL のポートが開発用ポートになる）で §1 の CSP のまま動作すること、Wails がインラインスクリプトを注入しないこと（runtime.js は同一オリジン） |
| 実機の `CreateProcess` による起動（M1.5） | 登録した `.exe`（引数・作業ディレクトリ付き）が、シェルを経由せず標準権限で起動し、`pid` が返ること。パスや引数に空白・日本語・引用符を含むときに、登録どおりに渡ること。`requireAdministrator` のアプリが `launch_failed`（`elevation_required`）になること |
| ファイル選択ダイアログ（M1.5） | `POST /apps/pick-file` が、ダイアログを開いている間（長時間を含む）WebView で応答待ちのままタイムアウトせず、選択後に入力欄へパスが入ること。キャンセルで 204 になること。ダイアログが MCP の入力（`key` / `type`）を受けないこと（NFR-S06） |

## 改訂履歴

| 版 | 日付 | 変更内容 | 変更理由 |
|---|------|---------|---------|
| 1.0 | 2026-10-29 | 初版作成 | — |
| 1.1 | 2026-10-29 | ページ・アクションルートは常に HTML、JSON は `/api/v1` と MCP のみとし、エラー形式を経路別に整理。`/api/v1` を参照専用に | HALT の規約（[フロントエンド設計](../architecture/frontend.md)）との整合 |
| 1.2 | 2026-10-29 | MCP `get_task` に `fast_steps` / `openai_steps` / `handoff_reason` を追加し、`run_task` で高速パスが透過であることを明記。WebSocket `wheel` と MCP `scroll` の単位をノッチに統一（変換規則を追記）。共通規約に CSP ヘッダーと静的ファイルのキャッシュ（`no-cache` + ETag）を追加。GUI ルーターに高速パスの設定ルート（`/agent/fastpath`、`/agent/fastpath/api-key`）と `keep_screenshots` を追加し、タスク詳細・SSE にステップの経路と引き継ぎ理由を追加。§8 を決定事項と実機検証事項に改め、MCP SDK の設定値・OpenAPI の範囲・高解像度の扱いを決定。Jev による外部 Judge の項は削除（高速パスは内部機構） | 未決事項の決定 |
| 1.3 | 2026-10-29 | PR #2 のレビュー対応: ミドルウェア連鎖でキルスイッチ（3）を WhoIs・許可リスト・接続要求の作成より前に移動（キルスイッチ中は接続要求を作らない）。Bearer の参照先を NFR-S04 に統一。M1 のリース（`state` は `idle` / `held`、`holder.kind` は `mcp` のみ、承認なし）と、ダッシュボードの段階（強制剥奪ボタンは M2、実行中タスクと中断ボタンは M4）を明記 | M1 の実装者が迷わない仕様にするためのレビュー指摘の解消 |
| 1.4 | 2026-10-30 | GUI の実装（#12）に合わせて §7.3 に `GET /killswitch` と確認モーダルのルートを追加し、§7.4 の `connection-requests` / `killswitch` の data を「バッジ・ヘッダーのフラグメント + 一覧・パネルは `sse:` トリガーで再取得」に具体化 | SSE の 1 イベントを複数の要素が受けると同じ HTML が差し込まれる htmx の仕様への対応 |
| 1.5 | 2026-10-30 | §7.1・§7.4 に `GET /poll`（OOB のフラグメントを返すホスト GUI のポーリング）を追加し、ホスト GUI のレイアウトは SSE を購読しないことを明記。`GET /events` は残し、`connection-requests` / `killswitch` の data を「バッジ・ヘッダーのフラグメント」に戻した（一覧・パネルの再取得は `/poll` が担う）。§7.3 の `GET /killswitch` から SSE の記述を削除 | Wails v3.0.0 の Windows の AssetServer が応答をバッファし、SSE が WebView に届かないため（#12） |
| 1.6 | 2026-10-30 | §3.1 のセッションの行を、MCP の keepalive ping を無効にする（POST だけのクライアントが ping に応えられずセッションを失うため）内容に改め、§3.3 `get_ui_tree` に `max_depth` ≤ 20・`max_nodes` ≤ 5000 の上限を追記した | SDK v1.8.0 は server→client の ping を standalone の GET ストリームにしか流さない |
| 1.7 | 2026-10-30 | §3.1 のセッションの行に NFR-A04 との対応（MCP の keepalive は既定で無効、WebSocket は 15 秒 / 45 秒）と、切断の検知手段（DELETE・トランスポートの切断・`SessionTimeout`・リースのアイドル解放）を明記し、§8.1 の SDK 設定に `KeepAlive` を既定の 0 とする行を追加した | MCP の keepalive を既定で無効にした実装との整合（オーナー決定） |
| 1.8 | 2026-10-30 | §7.2 の `agent_enabled` を、M1 でも許可フォームと `PATCH /clients/{id}` で保存・編集できるが効果は M4 から、と明記。PATCH の応答説明を Web / MCP の無効化でセッションを切断すると正した | M1 の実装と FR-A09 の食い違いの解消（オーナー決定） |
| 1.9 | 2026-10-30 | §7.5「M1 実装での差分」を新設（M1 のリモートルーターは `/mcp` だけ、M1 の GUI ルートと `PUT /settings` の 5 項目、`/poll`・SSE の扱い、GUI の `not_found` と 422、接続要求の許可（期限切れは許可不可・再許可での置き換えと `client.replace`・5 秒以内は DB に書かない）、`client_id` の NULL、MCP ツールの監査、`http.Server` の `IdleTimeout` 120 秒・`MaxHeaderBytes` 64 KiB・WhoIs の 5 秒）。§1 に参照を追加 | M1 の実装との差分の文書化（オーナー決定） |
| 1.10 | 2026-10-30 | 起動許可リストと MCP の `launch_app` / `list_launchable_apps` を追加（M1.5）: §1 の GUI ルーターの提供物と隔離の対象、§2 のエラー（`app_not_found` 404・`launch_failed` 409）、§3.1 の `instructions`、§3.3 のツール表と「アプリ起動」節、§7.1 の `/apps`、§7.2 の「起動許可アプリ」（`POST /apps`・`PUT` / `DELETE /apps/{id}`・削除の確認・`POST /apps/pick-file`）、§8.2 の実機検証事項 | M1.5 の仕様追記（#16。オーナー決定 8 件） |
| 1.11 | 2026-10-30 | §7.2 の起動許可アプリに注意書きの表示を追記 | #16 のレビュー指摘（FR-H12・NFR-S20 との整合） |
| 1.12 | 2026-10-30 | §3.4 の引数のスキーマ違反を、実装（go-sdk）の挙動に合わせて訂正（JSON-RPC の `invalid params` ではなく `isError` のツール結果）。§3.3 の `launch_app` の引数検査の記述も同様に揃えた | #22（`launch_app` の入力検査の実測との食い違い） |
| 1.13 | 2026-10-30 | §7.1・§7.2 の起動許可アプリを実装（#23）に合わせて具体化（各ルートのフラグメントと OOB、`pick-file` の `form_id`、`args` の改行）。§7.5 の GUI ルートに `/apps` 系を追加 | #23 の実装との整合 |
| 1.14 | 2026-10-30 | §3.3 の `list_launchable_apps` が起動できない行を返さないことを追記 | #16 の再レビュー指摘 |
| 1.15 | 2026-11-01 | §7.1 に `GET /requests/history`（接続要求の履歴。1 ページ 20 件・`page` クエリ・フラグメントの応答）を追加し、`/requests` のタブに触れた | #44 |
| 1.16 | 2026-11-01 | §7.1 に `GET /logs`（アプリのログ。`level`・`page` クエリ、1 ページ 50 件、フラグメントの応答）を追加 | #45 |
| 1.17 | 2026-11-01 | §3.3 の `launch_app` の出力に `window`・`reused_existing_window` を追加（起動後に前面にあったウィンドウの観測と、既存のウィンドウが前に出ただけのときの印）、§3.1 の `instructions` に入力の前に確かめる旨を追記 | #46 |
| 1.18 | 2026-11-01 | §3.4 に `own_window` の `detail.own_windows`（モニターと縮小座標の `rect`）を追加し、§3.3 の `screenshot` に `own_windows`、`list_windows` に `blocked`・`blocked_reason` を追加。§2 の `input_blocked` の行と §3.1 の `instructions` を更新 | #47（FR-M20） |
| 1.19 | 2026-11-01 | §2 の `lease_revoked` に「次の `acquire` まで返し続ける」、§3.3 の `get_control_status` の `holder.kind` に `web`（M2）、§7.1 のダッシュボードと §7.3 の `POST /lease/revoke`（冪等・`host` で監査・`agent` は M4）、§7.5 の GUI ルートに `/lease/revoke` を反映 | #58（M2 の強制剥奪の実装） |
| 1.20 | 2026-11-01 | §7.3 の `PUT /settings` に `viewer.default_quality`（1〜100）・`viewer.default_fps`（1〜30）を M2 で扱う旨を追記（§7.5） | #59 |
| 1.21 | 2026-11-01 | §4.1 に切断後 5 分の有効期間・同時接続 1 本（置き換え）・切断以外の終わり方でのリース解放、§5.3 に `input.mouse` の `down` / `up`（移動してから押す・離す）・`input.key` の監査・SAS の `input_blocked`、§7.5 に Web 入力の監査を追記 | #60 |
| 1.22 | 2026-11-01 | §5.1 にハンドシェイク前後の拒否の分け方・既定の配信・終了時の 1001、§5.2 の 4400 / 4401 の範囲、§5.3 の `ready` の `monitors[]`・`lease` の `holder`・`error` の `code` の一覧・メッセージの上限、§7.5 に WebSocket の差分とリモートルーターの提供物（`GET /ws/screen`）を追記 | #61 |
| 1.23 | 2026-11-01 | §5.3 の押下中のボタン・キーを、WebSocket の終わり（切断・置き換え・クライアントの削除・無効化）でも、リースの解放の前に `up` で離すと改めた（「切断では離さない」を撤回） | #61 |
| 1.24 | 2026-11-01 | §4.2 に待機ページの出し分け（接続要求の状態を読み取り専用で引く）・Web UI の入力とエラー（`X-Viewer-Id`・本文 1 MiB・検証エラーの 422・トースト）・ショートカット送信メニューの一覧を、§7.5 に Web UI の実装と `/`・`/viewer*`・`/static/*` を追記 | #62 |
| 1.25 | 2026-11-02 | §7.5 の「Web 入力の監査」に `audit.record_text` が true のときの本文の記録（`text`）を追記 | #71 |
| 1.26 | 2026-11-02 | §7.1 の `/audit`・§7.3 の `GET /audit`・`/audit/{id}`・`/audit/{id}/thumbnail` に M3 の実装（フラグメントとフルページの出し分け、不正な値の 422、日時の入力形式、サムネイルの 404）、§7.5 の「未実装」から `/audit*` を除いた | #72 |
| 1.27 | 2026-11-02 | §7.5 の `PUT /settings` の項目に M3 の 5 項目（`audit.thumbnails_enabled`・`audit.record_text`・`audit.retention_days`・`audit.thumbs_max_bytes`・`update.apply_mode`）と検証・保存の扱いを追記 | #73 |
| 1.28 | 2026-11-02 | §3.3 の `acquire_control`・`get_control_status` に承認待ち（`pending_approval`・`approval_denied` 403・`approval_timeout` 408・`holder` は `held` のときだけ・`mine`）、§4.2 の `POST /viewer/lease/acquire` の承認待ち・拒否・タイムアウト、§5.3 の `lease` の `pending_approval`（`holder` は `idle` のときだけ省く）と承認待ち中の入力の拒否、§7.5 に Web の承認待ちの実装を追記 | #67 |
| 1.29 | 2026-11-02 | §7.1 の `/approvals/{id}`（レイアウト・表示内容・残り時間の再取得・410）、§7.2 の許可フォームと `PATCH /clients/{id}` の承認モード（`approval_mode` の検証・`client.update` の `detail`）と `POST /clients/{id}/approval/reset`（409・監査は未決）、§7.3 の `allow` / `deny`、§7.4 の `approval-closed`、§7.5 に承認ダイアログの実装を追記 | #68 |
| 1.30 | 2026-11-02 | §7.5 の「Web 入力の監査」に、サムネイルの結線（撮る入力・プライマリ・全体で 1 秒 1 枚・MCP の操作の撮影）を追記し、「未実装」を除いた | #71 |
| 1.31 | 2026-11-02 | §7.1 に `/overlay`（操作中オーバーレイの中身。`label` の HTML エスケープと切り詰め）、§7.5 に操作中オーバーレイ（#69）を追加し、承認ダイアログの行の「複数の要求の扱いは未決」を到着順の待ち行列に更新 | #69・#68 |
| 1.32 | 2026-11-02 | §4.2 の `POST /viewer/lease/acquire` を、承認が要るとき 202 と承認待ちのパネルを即座に返し、結果（許可は `lease`、拒否・タイムアウトは `error` の `approval_denied` / `approval_timeout`）を WebSocket で届ける形に更新。§5.3 の `error` に `approval_denied` / `approval_timeout`、§3.3 の承認待ちのツールのタイムアウト（90 秒）と拒否直後の 30 秒のクールダウン、§7.3 の承認のリセットの監査（`approval.reset`）、§7.5 の該当行の「未決・未実装」を解消 | #67・#66・#68 |
| 1.33 | 2026-11-02 | §7.1 の `/update`、§7.3 の `GET /update/apply/confirm`・`POST /update/check`・`POST /update/apply`（200・409 の理由・500）、`PUT /settings` の `update.channel` を追記し、§7.5 に更新の実装（#74。認証なしの取得・署名の検証・版の束縛・適用の可否・監査）を追加。「未実装」から `/update*` を外した | #74 |
| 1.34 | 2026-11-02 | §3.3 の `get_control_status` の `holder.kind` に `agent`・`task_id`、`run_task` の `invalid_argument`、`instructions` のタスクの記述、MCP ツールの監査（`run_task` / `get_task` / `cancel_task`。`prompt` は文字数のみ）を実装に合わせて追記 | #19（mcp の `run_task` / `get_task` / `cancel_task`） |
| 1.35 | 2026-11-02 | §7.5 にエージェント設定の GUI（#19）: キーの受け付け方（本文のみ・形式の検査・`no-store`・値を出さない）、確認モーダルを経る `DELETE`、`PUT /agent` の 409 `agent_disabled` が §2 のエラーコード表（403）と食い違うことを記載 | #19 |
| 1.36 | 2026-11-02 | §7.1 の `/`（実行中タスクのパネル・強制剥奪は `mcp` / `web` / `agent`）・`/approvals/{id}`（`safety_check`・`judge_action` の表示）・`/overlay`（2 行目の補足）・`/tasks`（一覧・詳細）、§7.3 の `POST /lease/revoke`（`agent` の剥奪）・`POST /tasks/{id}/cancel`（応答・冪等・404）、§7.4 の `tasks`（`rev_tasks`）、§7.5 にタスク画面・承認の種類・エージェントのオーバーレイの実装を追記し、「`kind` が `lease` 以外は 410（M4）」を未知の `kind` に更新 | #19（M4 ホスト GUI・タスク） |
| 1.37 | 2026-11-02 | §3.3 の `get_task` の `error_code` に `invalid_coordinates`、リース喪失は `cancelled`、`cancel_task` の待ちは 0.5 秒、§4.2 の `GET /viewer/lease` に保持者 `agent` のときのタスクの要約（指示・最終回答は出さない）、§7.2 の `agent_enabled` の効果（M4）と中断 | #19 のレビュー指摘 |
| 1.38 | 2026-11-02 | 資格情報のターゲット名を実装の実名（OpenAI は `puppet-remo/openai:puppet-remo`、TypeSafe は `puppet-remo/typesafe:puppet-remo`。go-keyring の `service:user`）に統一した。要件・実装は変えない | #19 |
| 1.39 | 2026-11-02 | §2 のエラーコード表の `agent_disabled` を 403 から 409 に改め、§7.5 の「表と食い違う」の注記を除いた（`PUT /agent` の 409 と統一） | #19 |
| 1.40 | 2026-11-02 | §7.1 の `/logs` に「ログフォルダーを開く」ボタン、§7.3 に `POST /logs/open-folder`（開く場所を実装が固定。パスを受け取らない）を追加 | #54 |
| 1.41 | 2026-11-02 | §3.3 の `launch_app` の `reused_existing_window` を、起動前後のウィンドウ一覧の差で判定する（新しいウィンドウがあれば `false`）に改めた | #55 |
| 1.42 | 2026-11-02 | §2 に `audit_unavailable`（503。`launch_app` の起動の「試行」を監査に書けないと起動しない）、§3.3 の `launch_app` に fail-closed の 2 行の監査を追記。§3.4 に、監査に残すスキーマ違反は `launch_app` だけであることを明記 | #37・#38 |
