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
- MCP のツールエラーは HTTP ステータスではなく、ツール結果の `isError: true` と `structuredContent: {code, message}` で返す（§3.4）

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
| `lease_revoked` | 409 | 操作系 | 自身のリースが剥奪された |
| `approval_denied` | 403 | リース取得 | ホストが拒否した |
| `approval_timeout` | 408 | リース取得 | 60 秒で応答が無かった |
| `invalid_coordinates` | 400 | 操作系 | 座標がモニターの範囲外 |
| `invalid_argument` | 400 | 共通 | 引数が不正（`viewer_id` が別のクライアントに束縛されている場合を含む） |
| `conflict` | 409 | GUI | 現在の状態では実行できない（要求が `pending` でない、更新を適用できない状態など） |
| `window_not_found` | 404 | ウィンドウ系 | 対象が存在しない |
| `app_not_found` | 404 | `launch_app` | `app_id` が起動許可リストに無い（FR-M19。M1.5） |
| `input_blocked` | 409 | 操作系 | 自ウィンドウ・管理者権限ウィンドウなどで入力不可（NFR-S06 / UIPI） |
| `launch_failed` | 409 | `launch_app` | 許可リストのアプリだが OS がプロセスを作成できなかった（FR-M19。M1.5）。`structuredContent` に `reason`（`file_missing` / `access_denied` / `elevation_required` / `other`）を付ける。**パス・引数は含めない** |
| `desktop_unavailable` | 503 | 取得・操作 | ロック画面・UAC 等でキャプチャ・入力できない |
| `agent_disabled` | 403 | `run_task` | エージェント無効、API キー未登録、またはクライアントに利用許可が無い |
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
| `instructions` | 初期化時にサーバーが返す指示文。座標が縮小座標系であること、操作系ツールにはリースが必要で暗黙取得されること、`launch_app` で起動できるのはホストが許可したアプリ（`list_launchable_apps` で確認）だけであること、`run_task` が非同期であることを記す |
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

出力: MCP の画像コンテンツ（`image/png` または `image/jpeg`、base64。縮小済み）と、テキストコンテンツの JSON `{ "monitor": 0, "physical": {…}, "scaled": {…}, "scale": 0.6125 }`。

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
      "foreground": true, "minimized": false, "elevated": false }
  ]
}
```

`id` は HWND の 10 進文字列。`rect` は、そのウィンドウが最も重なるモニターの縮小座標で、モニターの範囲に切り詰める。`elevated` が true のウィンドウには入力できない（`input_blocked`）。

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
| `acquire_control` | `get_control_status` と同じ。M1 は承認なしで取得できる（`idle → held`）。承認が必要な場合（承認モード。M3 から）は承認を待つ（最大 60 秒） |
| `release_control` | `{ "ok": true }`。保持者でなければ何もしない |
| `get_control_status` | `{ "state": "idle" \| "pending_approval" \| "held", "holder": { "kind": "mcp" \| "web" \| "agent", "label": "…", "task_id": "…" }, "mine": true, "idle_remaining_sec": 240 }`（`holder` は `held` のとき）。M1 の `state` は `idle` / `held` のみ、`holder.kind` は `mcp` のみ。`pending_approval` は M3、`web` は M2、`agent`（と `task_id`）は M4 から |

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

`id` は ULID（登録時に採番。編集しても変わらず、削除すると無効になり再利用されない）。`apps` は `label` の昇順（大文字小文字を区別しない）で、許可リストが空なら空配列。リース不要。キルスイッチ中はミドルウェア連鎖（§1）が 503 で拒否する。

**`launch_app`**（FR-M19）

| 引数 | 型 | 必須 | 説明 |
|------|----|------|------|
| `app_id` | string | はい | `list_launchable_apps` の `id` |

追加の引数（`args`・`cwd`・`path` など）は受け付けない（スキーマに無い引数・`app_id` の欠落・型違いは、MCP SDK の引数検査が拒否し、起動しない。§3.4）。出力: `{ "ok": true, "app_id": "01JA…", "pid": 4120 }`。`pid` は `CreateProcess` が返したプロセスの ID。プロセスの作成に成功した時点で返し、ウィンドウの表示は待たない（確認は `list_windows`）。

- 検査の順序は、キルスイッチ（ミドルウェア連鎖の 3）→ 操作リース → 許可リスト。リース未保持なら暗黙に取得する（FR-L04）。リースの承認は M1.5 には無い（M3 から）
- エラー: `lease_held_by_other`（他者がリースを保持）、`app_not_found`、`launch_failed`、`kill_switch_active`（HTTP 503）。リースの暗黙取得に失敗したときは `lease_held_by_other` を返し、起動しない
- 同じアプリが起動中でも、呼ぶたびに新しいプロセスを起動する（冪等ではない。フォーカス切り替えはしない）。起動済みのプロセスは、リースの解放・セッションの切断・キルスイッチ・許可リストからの削除では終了しない
- すべての結果（成功・拒否・失敗）を監査に追記する（FR-AU06）。`list_launchable_apps` も `tool.list_launchable_apps` として追記する

#### 内蔵エージェント

**`run_task`**（FR-G02）

| 引数 | 型 | 必須 | 説明 |
|------|----|------|------|
| `prompt` | string | はい | 自然言語の指示（10,000 文字まで） |
| `max_steps` | integer | いいえ | ステップ上限。設定値（FR-S12）以下のみ指定できる |
| `max_duration_sec` | integer | いいえ | 実行時間の上限（秒）。設定値以下のみ |

出力（即時）: `{ "task_id": "01J…", "status": "pending_approval" | "running" }`。長時間実行になるため、完了は `get_task` で確認する（1 回の MCP 呼び出しは 1 秒以内に返る。NFR-P08）。エージェント無効なら `agent_disabled`、他者がリースを保持していれば `lease_held_by_other`、キルスイッチ中は `kill_switch_active`。

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
- `error_code`: `failed` のときの理由（`input_blocked` / `judge_denied` / `approval_denied` / `safety_check_denied` / `unsupported_action` / `openai_error` など）
- `fast_steps` / `openai_steps`: 高速パス（Jev が候補表から選んだ手）と OpenAI のコンピューター操作ループで実行したステップ数（FR-G23）。`step_count = fast_steps + openai_steps`（高速ステップも `max_steps` に数える）。高速パスを使わなかったタスクは `fast_steps = 0`
- `handoff_reason`: 高速パスから OpenAI へ引き継いだ理由。`abstain` / `low_confidence` / `no_candidates` / `invalid_choice` / `no_progress` / `goal_maybe_done` / `fast_step_limit` / `chooser_unavailable` のいずれか。引き継ぎが無かった（高速パス無効、または高速パスの間にキャンセル・失敗で終了した）場合は `null`。引き継ぎ後はタスクが終了するまで OpenAI で続行する（v1 では高速パスに戻らない）。失敗理由ではなく、`error_code` とは別に扱う

**`cancel_task`**: `task_id`\*。出力: `{ "ok": true, "status": "cancelled" }`。すでに終了していれば現在の状態をそのまま返す。1 秒以内に実行中の OpenAI 呼び出しを取り消し、リースを解放する（FR-G10）。

### 3.4 ツールエラー

ツールの失敗は JSON-RPC エラーではなく、ツール結果として返す。

```json
{
  "isError": true,
  "content": [{ "type": "text", "text": "lease_held_by_other: The lease is held by 'web' (viewer 01J…)." }],
  "structuredContent": { "code": "lease_held_by_other", "message": "…", "holder": {"kind": "web", "label": "my-laptop"} }
}
```

コードは §2 の表のとおり。引数のスキーマ違反（スキーマに無い引数・必須の欠落・型違い）は、ハンドラの手前で MCP SDK が拒否する。SDK（go-sdk）はこれを JSON-RPC エラーではなく、`isError` のツール結果（本文は `validating "arguments": …`。`structuredContent` は無く、`code` は付かない）で返す。

## 4. Web UI（リモートルーター）

状態駆動レンダリングで、リース状態に応じた操作だけを HTML に出す。ページルート（`GET /viewer` など）は `HX-Request` の有無でフルページとフラグメントを出し分け、アクションルート（`POST /viewer/...`）は常にフラグメントを返す。JSON は返さない。

### 4.1 ビューアのセッション（`viewer_id`）

Cookie を使わないため、ブラウザ上のビューアは `viewer_id`（ULID）で識別する。

- `GET /viewer` のレンダリング時に発行する。値は、接続元クライアント（node ID）と発行時刻に束縛される
- `<remo-screen-viewer>` の `ws-url` に埋め込み、HTMX のリクエストには `hx-headers` で `X-Viewer-Id` として付ける
- 有効期間: WebSocket の接続中、および発行から 5 分（接続前）。束縛と異なるクライアントからの利用は 400 `invalid_argument`
- リース保持者（種別 `web`）は `viewer_id` で識別する。WebSocket の切断でリースを解放する

### 4.2 ルート

認可は §1 の連鎖（登録済みで `web_enabled` なクライアント）。

| メソッド | パス | 概要 | 応答 |
|---------|------|------|------|
| GET | `/` | `/viewer` へリダイレクト | 302 |
| GET | `/viewer` | ビューアのページ（ツールバー、リースパネル、`<remo-screen-viewer>`） | 200 HTML（`viewer_id` を埋め込む） |
| GET | `/viewer/lease` | リースパネルのフラグメント | 200 HTML。保持者種別・名前・状態と、状態に応じたボタンのみ |
| POST | `/viewer/lease/acquire` | 操作権を取得 | 200 リースパネル。承認待ちなら承認待ちのパネル（ホストの承認後に WebSocket の `lease` で更新）。他者が保持なら 409 `lease_held_by_other` |
| POST | `/viewer/lease/release` | 操作権を解放 | 200 リースパネル |
| POST | `/viewer/lease/revoke` | 他者のリースを強制剥奪（保持者が `agent` ならそのタスクを中断。FR-L06） | 200 リースパネル（保持者は `idle` になる）。自身が取得するには改めて `acquire` する |
| POST | `/viewer/settings` | このビューアのモニター・品質・FPS | フォーム `monitor`, `quality`（1〜100）, `fps`（1〜30）。200 ツールバーのフラグメント。WebSocket の `config` に反映される |
| POST | `/viewer/clipboard/send` | テキストをホストのクリップボードに設定（FR-W07） | フォーム `text`（1 MiB まで）。要操作権。200 フラグメント |
| POST | `/viewer/clipboard/fetch` | ホストのクリップボードを取得して表示 | 要操作権。200 フラグメント（`<textarea readonly>`。利用者が手動でコピーする） |
| POST | `/viewer/keys/send` | ブラウザが先取りするショートカットの送信メニュー（FR-W06） | フォーム `keys`（§3.3 の書式）。要操作権。`ctrl+alt+delete` は 409 `input_blocked`。200 フラグメント |
| GET | `/ws/screen` | WebSocket（§5） | 101 |
| GET | `/static/*` | 静的アセット（`go:embed` した `dist/`） | 200（`Cache-Control: no-cache` + `ETag`。`If-None-Match` 一致で 304） |

ブラウザが未登録デバイスのとき、すべてのルートは 403 `device_not_registered` となり、`Accept: text/html` には待機ページ（「接続要求を送信しました。ホストの許可を待っています」）を返す。許可後に再読み込みするとビューアが開く。拒否・失効（24 時間）のときはその旨のページ。

## 5. WebSocket（`/ws/screen`）

### 5.1 接続

- URL: `ws://<host>:<port>/ws/screen?viewer_id=<ULID>`。サブプロトコル `puppet-remo.v1` を指定する
- 検査: §1 の連鎖（`Origin` は同一オリジンのみ）、`viewer_id` の束縛、`web_enabled`
- 同時閲覧者は最大 5（NFR-C03）。超過は閉じコード 4429
- keepalive: サーバーが 15 秒ごとに ping、45 秒応答が無ければ切断（NFR-A04）
- 閲覧者がゼロになるとキャプチャを停止する（FR-W09）

### 5.2 閉じコード

| コード | 意味 |
|--------|------|
| 4400 | 不正なメッセージ（形式・サイズ超過） |
| 4401 | 認可失敗（未登録・無効・`viewer_id` 不正） |
| 4429 | 同時閲覧者の上限 |
| 4503 | キルスイッチで受付停止 |
| 4510 | クライアントが削除・無効化された |

### 5.3 メッセージ（テキストフレーム、JSON）

すべて `{ "type": "<名前>", ... }` の形。

**サーバー → クライアント**

| type | 内容 |
|------|------|
| `ready` | 接続直後に 1 回。`{ "monitors": [...], "monitor": 0, "width": 2560, "height": 1440, "quality": 70, "fps": 10, "lease": {…} }`。`width` / `height` はストリームの座標空間（選択モニターの物理ピクセル） |
| `config` | 設定の反映結果。`{ "monitor", "width", "height", "quality", "fps" }`。モニター変更時はキーフレームが続く |
| `lease` | リースの変化。`{ "state": "idle" \| "pending_approval" \| "held", "holder": { "kind", "label" }, "mine": true }`。クライアントは CustomEvent `remo:lease-changed` を発火し、HTMX がリースパネルを再取得する |
| `killswitch` | `{ "active": true }`。以降の接続は 4503 で閉じられる |
| `error` | `{ "code", "message" }`。入力の拒否（`lease_required` / `input_blocked` など）。切断はしない |

**クライアント → サーバー**

| type | 内容 |
|------|------|
| `config` | `{ "monitor": 0, "quality": 70, "fps": 10 }`（任意の項目のみ。範囲外は 4400） |
| `input.mouse` | `{ "action": "move" \| "down" \| "up" \| "wheel", "x": 100, "y": 200, "button": "left" \| "right" \| "middle", "dx": 0, "dy": 1 }`。`x` / `y` は**ストリームの座標**（モニターの物理ピクセル。MCP の縮小座標ではない）。`wheel` は `dx` / `dy` を使い、**単位はノッチ**（数値。小数可。MCP `scroll` の `amount` と同じ単位。下記） |
| `input.key` | `{ "action": "down" \| "up", "code": "KeyA", "key": "a", "modifiers": ["ctrl"] }`。`code` はブラウザの `KeyboardEvent.code` で、サーバーがスキャンコードに変換する |
| `input.text` | `{ "text": "こんにちは" }`。IME で確定した文字列を Unicode で送る |
| `ping` | `{ "type": "ping" }` → サーバーが `pong` を返す |

入力メッセージはリース（保持者が自身の `viewer_id`）が必要で、InputGuard を通る。拒否は `error` で返し、入力は破棄する。

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

ホスト GUI。Wails の WebView からのみ到達でき、TCP のソケットを開かない。Templ + HTMX で描画し、HTML フラグメントを返す。リモートの `X-Puppet-Remo` は不要（ネットワークから到達できないため）。WebView は外部 URL へ遷移させない。HTML 応答には §1 の CSP ヘッダーを付ける。高速パスの設定（§7.3）は**このルーターにだけ**あり、リモートルーターに同名の経路は作らない。

GUI は AI が操作できないよう、自ウィンドウをキャプチャから除外し、InputGuard が入力を拒否する（NFR-S06）。以下はすべて**ホスト所有者の操作**として監査ログに `actor_type = host` で記録する（閲覧のみの `GET` を除く）。

### 7.1 ページ（`GET`、200 HTML）

| パス | 画面 | 優先度 |
|------|------|--------|
| `/` | ダッシュボード。M1: サーバー状態、接続中セッション、リース保持者（表示のみ）、キルスイッチ、接続要求・クライアント管理への導線。リース強制剥奪ボタンは M2（FR-L06）、実行中タスクと中断ボタンは M4（FR-H10）で追加する。FR-H02 | M1 |
| `/requests` | 接続要求の一覧（許可 / 拒否）。FR-H03 | M1 |
| `/clients` | クライアント一覧。`/clients/{id}`: 詳細（有効化・承認モード・トークン）。FR-H04 | M1 |
| `/apps` | 起動許可アプリの一覧・追加フォーム（`HX-Request` なら一覧のフラグメント）。`/apps/new`: 追加フォーム、`/apps/{id}/edit`: 編集フォーム（`HX-Request` ならフォームのフラグメント。フルページは一覧の該当行が編集フォーム。対象が無ければ 404）。FR-H12 | M1.5 |
| `/approvals/{id}` | 承認ダイアログ（専用の Wails ウィンドウ）。FR-H05 | M3 |
| `/audit` | 監査ログ一覧。`/audit/{id}`: 詳細（引数、サムネイル）。FR-AU05 | M3 |
| `/settings` | 設定。FR-S | M1〜 |
| `/update` | 更新の確認・適用。FR-H09 | M3 |
| `/tasks` | タスク一覧。`/tasks/{id}`: 詳細（ステップ、使用トークン、最終回答。ステップごとの経路 `route`（`fast` / `openai`）と、高速パスから OpenAI へ引き継いだ理由 `handoff_reason`、`fast_steps` / `openai_steps`）。FR-H10 | M4 |
| `/agent` | エージェント設定（有効化と同意、OpenAI API キー、モデル、上限、`keep_screenshots`、Judge のリスト、**高速パス**: 有効化と同意、TypeSafe API キー、モデル、最小確信度、最大ステップ、除外ラベル）。FR-H11 | M4 |
| `/poll` | ホスト GUI の更新（OOB のフラグメント。§7.4） | M1 |
| `/events` | SSE（§7.4）。ホスト GUI のレイアウトは購読しない | M1 |

### 7.2 接続要求とクライアント

| メソッド | パス | リクエスト（フォーム） | 応答 |
|---------|------|-----------------------|------|
| POST | `/requests/{id}/approve` | `approval_mode`（`always` / `first_time` / `auto`、既定 `always`。**M3 から**。M1 のフォームには出さず、`always` で保存して M3 まで参照しない。FR-H03）、`web_enabled`、`mcp_enabled`、`agent_enabled`（既定 off。M1 のフォームにも出して保存するが、**M1 では効果がなく**、`run_task` の可否に効くのは M4 から。FR-A09）、`display_name` | 200 一覧フラグメント（クライアントを登録し、要求を `approved`）。要求が `pending` でなければ 409 `conflict` |
| POST | `/requests/{id}/deny` | なし | 200 一覧フラグメント（`denied`。24 時間は同デバイスの再要求を作らない） |
| PATCH | `/clients/{id}` | `web_enabled`、`mcp_enabled`、`agent_enabled`（M1 でも受け付けて保存するが、**M1 では効果がない**。`run_task` の可否に効くのは M4 から。FR-A09）、`approval_mode`（M3 から）（いずれか。`approval_mode` を変えると `approved_at` を NULL に戻す） | 200 クライアントのフラグメント。Web / MCP を無効にしたクライアントのセッションは即時に切断（タスクの中断は M4 から。`agent_enabled` の変更ではセッションを切断しない） |
| POST | `/clients/{id}/approval/reset` | なし | 200（`first_time` の承認済みを取り消す） |
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
| POST | `/lease/revoke` | なし | 200 リースのフラグメント（保持者を剥奪。`agent` ならタスクを中断）。**M2 から**（M1 のダッシュボードに強制剥奪ボタンは無い。FR-H02）。`agent` の剥奪は M4 |
| POST | `/killswitch/trigger` | なし | 200 ダッシュボードのフラグメント（FR-H06 の手順。1 秒以内） |
| POST | `/killswitch/release` | なし | 200（解除。受付を再開）。ダッシュボードのキルスイッチのパネルを返し、ヘッダーのバッジの更新・確認モーダルを閉じる・トーストを OOB で添える |
| GET | `/killswitch` | なし | 200 キルスイッチのパネルのフラグメント。発動中は解除のボタンだけ、そうでなければ発動のボタンだけを含む。画面の更新は `GET /poll`（§7.4）が OOB で差し替える |
| GET | `/killswitch/release/confirm` | なし | 200 解除の確認モーダル（フラグメント）。発動中でなければ 409 `conflict` |
| GET | `/clients/{id}/delete/confirm`、`/clients/{id}/tokens/{token_id}/revoke/confirm` | なし | 200 確認モーダル（破壊的な操作は、サーバーが描画したモーダルの確定ボタンだけが実際の `DELETE` を送る。frontend.md §5.4）。対象が無ければ 404 |
| POST | `/approvals/{id}/allow` | なし | 200 ダイアログを閉じる指示（`HX-Trigger: closeWindow`）。`id` が失効（60 秒）・解決済みなら 410 `approval_timeout` |
| POST | `/approvals/{id}/deny` | なし | 同上（拒否） |
| POST | `/tasks/{id}/cancel` | なし | 200 タスクのフラグメント。終了済みなら現在の状態を返す |
| PUT | `/settings` | `server.port`（1024〜65535）、`killswitch.hotkey`、`screenshot.max_dimension`（256〜4096）、`lease.idle_timeout_sec`（30〜3600）、`viewer.default_quality`、`viewer.default_fps`、`audit.thumbnails_enabled`、`audit.record_text`、`audit.retention_days`、`audit.thumbs_max_bytes`、`update.apply_mode`、`log.level`、`autostart`（OS のスタートアップ登録） | 200 フォームのフラグメント。検証エラーは 422 のフラグメント（項目別のメッセージ）。`server.port` の変更は再 Listen する |
| PUT | `/agent` | `enabled`、`consent`（`enabled` を on にするときは必須のチェック）、`model`、`max_steps`（1〜500）、`max_duration_sec`（30〜7200）、`keep_screenshots`（1〜10。FR-S19）、`deny_processes`、`approve_processes`（改行区切り） | 200 フラグメント。API キー未登録で `enabled` にしようとすると 409 `agent_disabled`。同意なしは 422。有効化時は `agent.consent_at` と監査記録（FR-G14） |
| PUT | `/agent/api-key` | `api_key`（伏せ字の入力） | 200 「登録済み」のフラグメント。**値は応答・ログ・監査に出さない**。資格情報マネージャーに保存し、保存に失敗したら 500 `internal` |
| DELETE | `/agent/api-key` | なし | 200（資格情報を削除し、エージェントを無効にする。実行中のタスクは中断） |
| PUT | `/agent/fastpath` | `enabled`、`consent`（`enabled` を on にするときは必須のチェック。エージェントの `consent` とは別）、`model`（既定 `jev-1.13`。FR-S15）、`min_confidence`（0〜1、既定 0.7。FR-S16）、`max_steps`（1〜500、既定 15。FR-S17）、`excluded_labels`（改行区切り。FR-S18） | 200 フラグメント。TypeSafe API キー未登録で `enabled` にしようとすると 409 `conflict`、同意なしは 422（項目別メッセージ）。on にしたとき `agent.fastpath.consent_at`（UTC ミリ秒）を記録し、off にしたとき削除する。どちらも高速パス用の別の監査イベントを残す（FR-G22）。エージェント自体の有効・無効とは独立 |
| PUT | `/agent/fastpath/api-key` | `api_key`（伏せ字の入力） | 200 「登録済み」のフラグメント。**値は応答・ログ・監査に出さず、登録後は表示しない**。資格情報マネージャーの `puppet-remo/typesafe` に保存し、保存に失敗したら 500 `internal`。登録だけでは高速パスは有効にならない（`enabled` と同意が別に要る） |
| DELETE | `/agent/fastpath/api-key` | なし | 200（資格情報を削除し、`agent.fastpath.enabled` を off、`consent_at` を削除する。実行中のタスクは失敗させず、以後の高速ステップを行わず OpenAI で続行する） |
| GET | `/audit` | クエリ: `from`, `to`（RFC 3339）、`client_id`, `kind`, `result`, `q`（イベント名の部分一致）、`cursor`, `limit`（既定 50、最大 200） | 200 一覧フラグメント（`ts` 降順。`cursor` は最後の `id`） |
| GET | `/audit/{id}/thumbnail` | なし | 200 `image/jpeg`（`audit_thumbnails.rel_path` を `thumbs/` 配下に限定して配信。パスの走査を拒否）。無ければ 404 |
| POST | `/update/check` | なし | 200 フラグメント（新版の有無） |
| POST | `/update/apply` | なし | 200（署名検証に成功した更新を適用して再起動。リース保持中・接続中・タスク実行中は 409 `conflict`） |

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

版（`rev_*`）は、描画した表示モデルのハッシュで、一覧・パネルの隠し入力（`<input type="hidden" name="rev_…" data-poll-rev>`）が持つ。レイアウトの `#live-poll` が `hx-include="[data-poll-rev]"` で送り返す。版が同じ領域は差し替えない（入力中の許可フォームを、ポーリングで消さないため）。応答に秘密（トークンなど）は含めない。`approval` と `tasks`（M3・M4）は、この経路の対象外または後続の領域として追加する。

#### `GET /events`（SSE）

接続は 1 本で、`data` は `GET /poll` と同じボディのコンポーネントで描画した HTML フラグメント。

| event | 内容（`data`） | 更新される画面 |
|-------|---------------|---------------|
| `server-status` | サーバー状態のフラグメント（tailnet IP・ポート・tailscaled 状態） | ダッシュボード |
| `sessions` | 接続中セッション一覧のフラグメント | ダッシュボード |
| `lease` | リース保持者のフラグメント（種別付き。M2 から強制剥奪ボタンを含む） | ダッシュボード |
| `connection-requests` | サイドバーの件数バッジのフラグメント | 接続要求、トレイ通知 |
| `approval` | `{ "id": "…", "kind": "lease" \| "safety_check" \| "judge_action" }`。受信したアプリが承認ダイアログのウィンドウを開く | （ウィンドウ生成） |
| `tasks` | 実行中タスクのフラグメント（ステップ数・状態・現在の経路 `fast` / `openai` と引き継ぎ理由。M4） | ダッシュボード、タスク |
| `killswitch` | ヘッダーのキルスイッチ状態バッジのフラグメント | 全画面のヘッダー |
| `toast` | 通知メッセージ（更新の通知、DB 破損の隔離、TypeSafe が 401 を返して高速パスを無効にした通知など） | 全画面 |

承認ダイアログに表示する内容は `kind` による。`lease`: クライアント名・種別・（`agent` なら指示の要約）。`safety_check`: OpenAI の `pending_safety_checks` の内容。`judge_action`: アクションの内容、対象ウィンドウ（タイトル・プロセス名）、判定の理由。いずれも 60 秒で失効し拒否扱い。

### 7.5 M1 実装での差分

M1 の実装が本書の他の節と異なる点（段階の前倒しではなく、実装の事実）。

| 項目 | 設計 | M1 の実装 |
|------|------|----------|
| リモートルーターの提供物（§1） | `/mcp`、`/viewer`、`/ws/screen`、`/api/v1` | **`/mcp` だけ**。それ以外のパス（`/` を含む）は 404。`/viewer`・`/ws/screen`・静的ファイル・`/api/v1`（Huma）は M2 以降 |
| GUI ルーターのルート（§7.1〜§7.3） | 表のとおり | M1 にあるのは `GET /`・`/requests`・`/clients`・`/clients/{id}`・`/settings`・`/poll`・`/events`・`/killswitch`、確認モーダル（`/clients/{id}/delete/confirm`・`/clients/{id}/tokens/{token_id}/revoke/confirm`・`/killswitch/release/confirm`）、`POST /requests/{id}/approve`・`/requests/{id}/deny`・`/clients/{id}/tokens`・`/killswitch/trigger`・`/killswitch/release`、`PATCH` / `DELETE /clients/{id}`、`DELETE /clients/{id}/tokens/{token_id}`、`PUT /settings`、`/static/*`、M1.5 の起動許可アプリ（`GET /apps`・`/apps/new`・`/apps/{id}/edit`・`/apps/{id}/delete/confirm`、`POST /apps`・`/apps/pick-file`、`PUT` / `DELETE /apps/{id}`）。**未実装**（それぞれ表の段階で足す）: `/approvals/*`・`/audit*`・`/update*`・`/tasks*`・`/agent*`・`/lease/revoke`・`/clients/{id}/approval/reset` |
| `PUT /settings` の項目（§7.3） | 13 項目 | M1 は `server.port`（1024〜65535）・`killswitch.hotkey`・`screenshot.max_dimension`（256〜4096）・`lease.idle_timeout_sec`（30〜3600）・`autostart` の 5 項目だけ。他は各段階で足す |
| ホスト GUI の更新（§7.4） | SSE | ホスト GUI は **`GET /poll`**（3 秒ごと。表示中の領域が持つ隠し入力 `rev_*` の版が変わったときだけ OOB で差し替える。`toast` は `Bus` の購読を `gui.Handler` が待ち行列にし、`/poll` が取り出す）で更新する。`GET /events`（SSE）は残しているが、GUI のレイアウトは購読しない。`htmx-ext-sse` は `dist/js` に同梱するが読み込まない（未使用） |
| GUI のエラー応答（§2） | `/api/v1`・MCP は problem+json / `isError`。HTML の経路は同じ `code` と HTTP ステータスのフラグメント | GUI の対象なし（クライアント・トークン・起動許可アプリ）は 404、`data-error-code="not_found"`（§2 のコード表に無い、GUI 内のコード）。`invalid_argument`（入力の不備）は 422 のフラグメント（§2 の表の 400 ではなく、フォーム再描画の 422）。想定外は 500 `internal`（詳細はログにだけ出す） |
| 接続要求の許可（§7.2） | `pending` なら登録して `approved` | 期限切れの `pending` は一覧に出ず、許可もできない（409 `conflict`）。同じ `stable_node_id` の登録済みクライアントがあるとき（ログイン名の変更後の再許可）は、古いクライアントを置き換える（トークンは CASCADE で削除、セッションは切断、リースは解放）。このとき監査に `client.replace` を記録する。古いクライアントの切断（ツール呼び出しの完了待ち）は、他の許可・GUI の操作を止めないよう、許可の直列化のロックの外で行う。同じデバイスの `pending` を 5 秒以内に更新済みなら、DB に書かない |
| 監査（§7 の冒頭） | ホスト操作は `actor_type = host` | `client_id` は、クライアントの行がすでに無いとき（`client.delete` の直後など）NULL で記録する（`client_label` は残る） |
| MCP ツールの監査（§3.3） | — | `Control` を通らないツール（`get_screen_info`・`screenshot`・`cursor_position`・`list_windows`・`acquire_control`・`release_control`・`get_control_status`）の呼び出しは、`mcp` のツール登録で `kind = tool` として監査する。入力系などは `Control` が記録する |
| HTTP サーバー（§1） | — | tailnet 向けの `http.Server` は `ReadHeaderTimeout` 10 秒、`IdleTimeout` 120 秒、`MaxHeaderBytes` 64 KiB。`WriteTimeout` は付けない。WhoIs の LocalAPI 呼び出しは 5 秒で打ち切り、失敗は 403 `whois_failed`（fail closed） |
| MCP の keepalive（§3.1・NFR-A04） | — | 既定で ping しない（§3.1） |

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
