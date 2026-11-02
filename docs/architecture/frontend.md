# フロントエンドアーキテクチャ設計（HALT）

全体構成は [アーキテクチャ設計](overview.md)、ルートとプロトコルは [API 仕様](../api/endpoints.md)、パッケージの責務は [コンポーネント設計](../components/overview.md) を参照。本書はフロントエンドを **HALT（HTMX + Atomic Design + Lit + Templ）** で作るための規約を定める。HALT 標準のリファレンスをベースにし、本プロジェクト固有の差分は §2 にまとめる。

## 1. 概要

### 1.1 設計思想

バックエンドファースト。判断に迷ったらサーバー側に寄せる。

- フロントエンドはアプリ（Go のプロセス）の中で動く。**SPA は作らない**
- サーバーが UI を制御し、HTMX がハイパーメディア駆動のインタラクションを担う
- リッチな操作が必要な箇所（ライブ画面）だけ Lit の Web Component で拡張する
- テンプレートは Atomic Design で構造化する
- クライアント側の状態とロジックは最小限にする。状態はサーバーが持つ
- **状態駆動レンダリング（HATEOAS）**: サーバーが現在の状態と権限に基づいて、利用可能なアクションだけを HTML に含める。ボタンが見えるなら押せる。見えないなら存在しない。`hidden` / `disabled` で隠さず、レンダリングしない。判定は Go 側で行う

### 1.2 技術スタック

| レイヤー | 技術 | 役割 |
|---------|------|------|
| サーバー | Go + Gin | ルーティングと SSR |
| JSON API | Huma | `/api/v1` の OpenAPI 3.1 生成と入出力検証（読み取り専用の小さな API のみ） |
| テンプレート | Templ | 型安全な Go の HTML テンプレート |
| インタラクション | HTMX 2.x + 拡張 `response-targets`（両 UI）。ホスト GUI の状態の更新は `GET /poll` のポーリング（§5.3） | サーバー駆動の DOM 更新 |
| リッチ UI | Lit（Web Components） | ライブ画面ビューア `<remo-screen-viewer>` のみ |
| スタイル | Tailwind CSS | Templ にはユーティリティクラス、Lit には Shadow DOM 内の `css` |
| ビルド | esbuild（TypeScript）、Tailwind CLI | 成果物を `static/dist/` に出力し、本番は `go:embed` で同梱 |
| シェル | Wails v3 | ホスト GUI の WebView。GUI ルーターを `AssetOptions.Handler` に渡す |

### 1.3 2 つの UI

同じ Templ の atoms / molecules を共有するが、**ハンドラと配信経路は共有しない**（ホスト専用の操作がリモートに露出しないことを構造で保証する）。

| UI | 利用者 | 配信経路 | 認可 | HTMX 拡張 |
|----|-------|---------|------|-----------|
| **ホスト GUI** | ホスト所有者（ホスト PC の Wails WebView） | GUI ルーター（プロセス内。ソケットなし） | プロセス内到達のみ | `response-targets` |
| **Web UI（ビューア）** | tailnet 上の別端末のブラウザ | リモートルーター（tailnet IP） | WhoIs + 許可リスト（API 仕様 §1） | `response-targets` |

```mermaid
graph TD
    subgraph GUI["ホスト GUI (Wails WebView)"]
        GP["Templ ページ + HTMX"]
        SSE["ポーリング GET /poll<br/>(3 秒ごと。OOB で状態を更新)"]
    end
    subgraph WEB["Web UI (ブラウザ)"]
        WP["Templ ページ + HTMX"]
        LIT["Lit アイランド<br/>remo-screen-viewer"]
    end
    subgraph SH["共有 (internal/web)"]
        TPL["Templ: atoms / molecules<br/>(organisms / pages は UI ごと)"]
        VM["view (表示用モデル)"]
    end
    GP --> SSE
    GP -- "HTML フラグメント" --> GR["GUI ルーター"]
    WP -- "HTML フラグメント" --> RR["リモートルーター"]
    LIT -- "WebSocket (JSON + バイナリ)" --> RR
    WP -- "HTMX の外側に配置" --- LIT
    GR --> TPL
    RR --> TPL
    TPL --> VM
```

## 2. HALT 標準からの差分

| HALT 標準 | 本プロジェクト | 理由 |
|-----------|--------------|------|
| Cookie セッション + CSRF トークン（meta タグ） | **Cookie を使わない**。識別は WhoIs、CSRF 対策は Host / Origin 検証 + カスタムヘッダー `X-Puppet-Remo: 1`（`<body hx-headers>` で全 HTMX リクエストに付与） | tailnet 上の ID が真実の源で、Cookie の認証が無い（NFR-S03） |
| 単一の HTTP サーバー | **GUI ルーター（プロセス内）とリモートルーター（tailnet）に分離** | ホスト専用の操作を構造でネットワークから隔離する |
| HTMX 拡張は最小限 | ホスト GUI は拡張を `response-targets` だけにし、状態の更新は `GET /poll` のポーリング（`hx-trigger="every 3s"` + OOB）にする。`sse` 拡張は読み込まない | 承認要求・リース・接続要求を GUI へ反映する（NFR-P06）。Wails v3.0.0 の Windows の AssetServer は応答をバッファして SSE が届かないため（§5.3）。Web UI は WebSocket で足りる |
| Lit は編集系のリッチ UI | ライブ画面ビューア 1 つのみ | 要件が 1 画面に限られる |
| Huma で JSON API（アクションも含む） | `/api/v1/*` は読み取り専用（状態・モニター・リース）のみ。**アクションはすべて HTML を返す HTMX のルート**。MCP と WebSocket は Huma の対象外 | 状態を変更する操作の利用者は HTMX だけ。JSON の変更 API は不要 |
| 静的ファイルは Gin の `Static`、本番は `max-age=31536000` | `go:embed` した `dist/` を両ルーターで配信し、**`Cache-Control: no-cache` + `ETag`（ビルド ID）**。開発は `!production` ビルドでディスクから配信（**ユーザー承認済み**） | 単一バイナリ配布。ファイル名にハッシュを付けないため、アプリの更新後に利用者のブラウザが古い JS を長期保持する事故を避ける |
| 開発は `air` + `templ generate --watch` + `bun run dev` | **`wails3 dev`**（Go の再ビルドと再起動は Wails が行う）+ `templ generate --watch` + `frontend:dev`（esbuild のウォッチ + `WAILS_VITE_PORT` での待ち受け、Tailwind のウォッチ。§8.3） | Wails の開発サーバーとホットリロードが二重にならないようにする。`wails3 dev` は `frontend:dev` が待ち受けることを前提にする（§8.3） |
| `lib/api.ts` を共通ライブラリに含める | **初版では作らない**（**ユーザー承認済み**）。Lit から HTTP を呼ぶ箇所が無いため。必要になった時点で追加する（§6.1） | 呼び出し元が無い共通コードは持たない |
| CSP は任意（HALT 標準は規定なし） | **両ルーターの全 HTML レスポンスに CSP ヘッダーを付ける**（`script-src 'self'` / `style-src 'self'`。インラインのスクリプト・スタイル・イベント属性は禁止。§7.1。NFR-S19） | API キーの入力画面を持つホスト GUI が主な対象。共有テンプレートなので Web UI も同じ規約にする |
| `lib/ws.ts` は JSON メッセージ前提 | **バイナリフレーム（画面）と JSON テキストフレーム（制御・入力）の両方を扱う**（§6.2） | `/ws/screen` の形式（API 仕様 §5） |

## 3. ディレクトリ構成

```
internal/web/
├── view/                     # Templ に渡す表示用モデル（domain から handler が組み立てる）
├── handler/
│   ├── gui/                  # GUI ルーター用: dashboard, requests, clients, tokens, approvals,
│   │                         #   apps, tasks, agent, audit, settings, update, poll(OOB), events(SSE)
│   └── remote/               # リモートルーター用: viewer, lease, clipboard, keys
├── middleware/               # csp, hostcheck, csrfheader, whois, allowlist, bearer, killswitch
├── atoms/                    # 共有: button, input, textarea, select, checkbox, badge, spinner, toast, icon, kbd
├── molecules/                # 共有: card, modal, form_field, lease_badge, status_pill, token_reveal
├── organisms/
│   ├── gui/                  # server_status_panel, session_list, lease_panel, task_panel,
│   │                         #   killswitch_button, connection_request_list, client_list, client_detail,
│   │                         #   token_list, approval_body, audit_table, audit_filter_form, settings_form,
│   │                         #   agent_settings_form, update_panel, app_list, app_form, header, sidebar
│   └── remote/               # viewer_toolbar, lease_panel, clipboard_panel, key_menu
├── pages/
│   ├── gui/                  # dashboard, requests, clients, client_detail, approval, audit, audit_detail,
│   │                         #   settings, update, tasks, task_detail, agent, apps
│   └── remote/               # viewer, pending (接続要求待ち), denied, error
└── layout/                   # gui, remote（<head>・ヘッダー・`hx-headers`・スクリプトの読み込み）

static/
├── package.json              # esbuild, tailwindcss, lit, htmx.org, htmx-ext-*, @open-wc/testing, @web/test-runner
├── esbuild.config.mjs
├── src/
│   ├── components/
│   │   ├── screen-viewer/    # remo-screen-viewer.ts（+ テスト）
│   │   └── lib/              # ws.ts, logger.ts
│   ├── app/                  # htmx-config.ts（classic スクリプト）, app.ts（グローバルのエラーリスナー・トークンのコピー）
│   └── css/app.css           # Tailwind のエントリーポイント
├── dist/                     # ビルド成果物（go:embed。.gitignore）
└── e2e/                      # Playwright（§9）
```

- `atoms` / `molecules` は 2 つの UI で**共有**し、`organisms` / `pages` は UI ごとに分ける。共有側は UI 固有の URL を持たない（URL は props で受ける）
- `handler/gui` と `handler/remote` は互いを import しない。ハンドラが使えるのは `service` と `domain`（`repository` / `platform` / `tailnet` は不可。`scripts/check-imports.sh` が検査する。[コンポーネント設計](../components/overview.md) §6・§13.1）
- `view` は Templ 用の表示モデル（例: `LeasePanelView`、`ClientRowView`）。Templ は `domain` / `service` を import せず、`view` だけを受け取る
- `go:embed` は `static/dist` を対象にする。開発ビルド（`!production`）はディスクから読む（§8.3）

## 4. Templ（Atomic Design）

### 4.1 層

| 層 | 責務 | 例 |
|----|------|----|
| atoms | 最小の UI 要素。単一の責務 | button, input, badge, spinner, toast |
| molecules | atoms の組み合わせ | form_field, modal, lease_badge, token_reveal |
| organisms | 機能単位。HTMX の `hx-*` を持つ | lease_panel, client_list, viewer_toolbar |
| pages | ページ全体 | viewer, dashboard, clients |
| layout | `<html>`・`<head>`・共通ヘッダー・スクリプト | gui, remote |

### 4.2 コンポーネントの規約

- 単純なコンポーネント（引数 3 個以下）は直接パラメータ、複雑なものは `Props` 構造体にする
- バリエーションは `const` + 独自型で型安全にする（`ButtonVariant` など）
- 子要素は `templ.Component` のスロット（`{ children... }`）で受ける
- HTML 属性のパススルーは `Attrs templ.Attributes`
- 状態の分岐・権限の判定は**ハンドラが行い、`view` に結果として渡す**。テンプレートは `if` で描画の有無を決めるだけで、権限のロジックを持たない

### 4.3 状態駆動レンダリングの例（リースパネル）

```go
// internal/web/organisms/remote/lease_panel.templ
type LeasePanelView struct {
    State     string // "idle" | "pending_approval" | "held"
    Holder    *HolderView // 種別（mcp / web / agent）・名前・タスクの概要
    Mine      bool
    CanAcquire, CanRelease, CanRevoke bool // ハンドラが LeaseManager の状態から決める。pending_approval（M3）はすべて false（承認待ちの表示だけ。操作を持たない）
}

templ LeasePanel(v LeasePanelView) {
    <section id="lease-panel"
             hx-get="/viewer/lease"
             hx-trigger="remo:lease-changed from:#viewer-root"
             hx-swap="outerHTML">
        @molecules.LeaseBadge(v.State, v.Holder)
        if v.CanAcquire {
            @atoms.Button("操作権を取得", atoms.ButtonPrimary, hx.Post("/viewer/lease/acquire"), hx.Target("#lease-panel"))
        }
        if v.CanRelease {
            @atoms.Button("解放", atoms.ButtonSecondary, hx.Post("/viewer/lease/release"), hx.Target("#lease-panel"))
        }
        if v.CanRevoke {
            @atoms.Button("強制剥奪", atoms.ButtonDanger, hx.Post("/viewer/lease/revoke"), hx.Target("#lease-panel"))
        }
    </section>
}
```

- リースが `idle` なら「取得」だけ、他者の保持中なら「強制剥奪」だけ、自分が保持なら「解放」だけを描画する
- キルスイッチ中は操作系を一切描画しない（そもそもリモートのページは 503 になる。ホスト GUI は解除ボタンだけを描画する）
- `hx.Post(...)` などは `hx-*` 属性を組み立てる補助関数（`atoms` に置く）。URL はハンドラ側（または view）が持つ文字列で、JS には書かない

## 5. HTMX パターン

### 5.1 ルートの 3 パターン

| パターン | 例 | `HX-Request` | 返却 | 登録先 |
|---------|----|-------------|------|--------|
| ページルート | `GET /viewer`、GUI の `GET /clients` | で分岐する | フルページ（`layout` 込み）またはフラグメント | Gin |
| アクションルート | `POST /viewer/lease/acquire`、`PATCH /clients/{id}`、`GET /viewer/lease` | しない | **常にフラグメント** | Gin |
| API ルート | `GET /api/v1/status` | しない | JSON（problem+json のエラー） | Huma |

- **Web UI・GUI のルートは常に HTML**。`HX-Request` の有無で JSON に切り替えない（JSON を返すのは `/api/v1` と MCP のみ。API 仕様 §1）
- 同じ URL をブラウザが直接開いても、HTMX が部分更新しても動く（ページルート）。ページルートの分岐の実装は次のとおり。

```go
func (h *Handler) Clients(c *gin.Context) {
    v := h.clientsView(c.Request.Context())
    if c.GetHeader("HX-Request") != "" {
        organisms.ClientList(v).Render(c.Request.Context(), c.Writer)
    } else {
        pages.Clients(v).Render(c.Request.Context(), c.Writer)
    }
}
```

- 状態変更のルートは `X-Puppet-Remo: 1` を必須にする（リモートのみ。API 仕様 §1 の連鎖の 2。ホスト GUI のルーターには付けない — ソケットを開かずプロセス内の WebView だけが到達できるため。前提は API 仕様 §7 の冒頭）。`<body hx-headers='{"X-Puppet-Remo":"1"}'>` で全リクエストに付与する
- ビューアのページには `X-Viewer-Id` も付ける: `<main id="viewer-root" hx-headers={ templ.JSONString(map[string]string{"X-Viewer-Id": id}) }>`（`hx-headers` は子孫へ継承され、親の値とマージされる）

### 5.2 OOB（Out of Band）スワップ

1 レスポンスで複数の DOM を更新する。**副作用の反映だけ**に使い、メインの更新には使わない。3 箇所以上の同時更新は設計の見直しのサインとする。

| 場面 | メイン（`hx-target`） | OOB |
|------|---------------------|-----|
| 接続要求を許可・拒否（GUI） | 要求の一覧 | ヘッダーの件数バッジ、トースト |
| トークンを失効（GUI） | トークン一覧 | トースト |
| キルスイッチを発動・解除（GUI） | ダッシュボード | ヘッダーの状態バッジ |
| 設定を保存（GUI） | フォーム | トースト |

### 5.3 状態の更新: ポーリング `GET /poll`（ホスト GUI のみ）

**決定**: ホスト GUI の状態（サーバー状態・セッション・リース・キルスイッチ・接続要求の件数と一覧・トースト）は、レイアウトが `GET /poll` を 3 秒ごとに呼び、応答の OOB（`hx-swap-oob`）で差し替える。SSE（`GET /events`）は GUI のレイアウトから購読しない。

理由: Wails v3.0.0 の Windows の AssetServer は、`AssetOptions.Handler` の応答をハンドラが返るまでバッファし、`http.Flusher` を持たない（`internal/assetserver/webview/responsewriter_windows.go`）。ホスト GUI は `AssetOptions.Handler` でしか配信しないため、開いたままの SSE の応答は WebView に届かない。

```html
<body hx-ext="response-targets" hx-headers=...>
  <section id="server-status">…</section>
  <section id="sessions">…</section>
  <section id="lease">…</section>
  ...
  <div id="live-poll" hx-get="/poll" hx-trigger="every 3s" hx-swap="none" hx-include="[data-poll-rev]"></div>
</body>
```

- `GET /poll` は 1 回の応答に、いま更新すべき領域の OOB のフラグメントを並べる。API 仕様 §7.4 の対応表と同じ領域・同じ描画（`ServerStatusBody` など、SSE の `data` と共通のコンポーネント）を使い、描画のロジックを二重に持たない
- `hx-swap="none"` でも OOB の差し替えは行われる。`#live-poll` は `#main` の外にあるため、ページ遷移（`hx-boost`）で消えない
- **表示中の領域だけを更新する**: 一覧・パネルのように作り直す領域（接続要求の一覧、キルスイッチのパネル）は、描画した状態の版を隠し入力（`<input type="hidden" name="rev_…" data-poll-rev>`）に持つ。`hx-include="[data-poll-rev]"` がそれを `/poll` のクエリで送り返し、サーバーは**版が変わった領域だけ**を差し替える。そのページに無い領域は版が送られないため、差し込み先の無い OOB を返さない。入力中の許可フォームなどを、ポーリングで消さないための仕組み
- ヘッダーのキルスイッチのバッジとサイドバーの件数バッジは、軽い差し替え（中身だけ）で常に返す
- トーストは、`Bus` の `toast` を `gui.Handler` が `New` で購読して溜めておき、`GET /poll` が取り出して `#toast-root` に追加する OOB にする（取り出したものは次の応答に出ない。バッファは `Bus` の購読者ごとの上限 16 件で、超えた分は古いものから捨てる）
- `approval` のイベントは DOM を更新しない。承認ダイアログのウィンドウは Go 側（`app` の `approvalDialogs` が `Bus` から購読）が開き、`approval-closed`（承認待ちの終了）で閉じる（ポーリングの対象外。M3・#68）。ダイアログの残り時間（60 秒のカウントダウン）だけは、ダイアログのページ自身が HTMX の `hx-trigger="every 1s"` で `GET /approvals/{id}` を呼んで `#approval-remaining` を差し替える（`#live-poll` は使わない。専用ウィンドウにレイアウトのポーリングは無い。Lit は使わない。§6 の基準）。許可・拒否の応答の `HX-Trigger: closeWindow` は `static/src/app/app.ts` が受け、Wails のランタイム（`/wails/runtime.js`。同一オリジンの外部スクリプト）の `Window.Close` でウィンドウを閉じる（CSP を守る。インラインスクリプト・ハンドラ属性は使わない）
- `GET /events`（SSE）は仕様のまま残す。ストリームを流せる経路で GUI を配信するとき・テストのために使える。`sse` 拡張の JS（`dist/js/sse.min.js`）も配信物に残すが、レイアウトは読み込まない
- ポーリングの間隔は 3 秒。画面が隠れている間の停止は行わない（`hx-trigger` のフィルターは eval が要るため CSP の都合で使えない。§7）

### 5.4 ローディングと遷移

サーバーが真実。応答を待つのが正しい姿勢で、Optimistic UI は使わない。スケルトンスクリーンも使わない。

| 場面 | 手法 |
|------|------|
| 部分更新 | `hx-indicator` で対象にスピナー atom を表示 |
| ページ遷移（ホスト GUI のサイドバーのみ） | `hx-boost` + `#main` への差し替え。グローバルに有効化しない |
| 二重送信の防止 | `hx-disabled-elt="this"` |
| 破壊的な操作（クライアント削除、トークン失効、キルスイッチ解除など） | サーバーが描画した確認のモーダル（molecule）を `hx-get` で開き、確定のボタンだけが実際のアクションを送る |
| ファイル選択（起動許可アプリの実行ファイル。M1.5） | 「参照…」のボタンが `hx-post="/apps/pick-file"`、`hx-target="closest [data-field=exe_path]"`（`exe_path` の入力欄を包む要素。`form_field`）、`hx-swap="outerHTML"` で送る（フォームの `hx-target` などの継承を上書きする）。`hx-vals` の `form_id`（フォームの要素 id。追加は `app-form-new`、編集は `app-form-{id}`）で、応答の入力欄の `id` を追加・編集のフォームごとに重ならない値にする。サーバーは `app-form-` で始まる安全な文字だけの値以外を `app-form-new` に置き換える。サーバーがネイティブのダイアログを開き、選ばれたパスを入れた入力欄を返す（キャンセルは 204 で差し替えない）。入力欄は手入力のままでも使える（ダイアログは補助）。ダイアログの表示中はボタンを `hx-disabled-elt` で無効にし、`hx-indicator` を出す。JS（Wails の `runtime.js` の `Dialogs`）は使わない |

**起動許可アプリ画面（M1.5。`/apps`）**: 一覧（`app_list` organism。表示名・実行ファイル・引数・作業ディレクトリ・編集・削除）と追加・編集のフォーム（`app_form` organism）をホスト GUI のサイドバーに加える。追加・編集のフォームは、実行ファイル・表示名・引数（`textarea`。1 行 1 引数）・作業ディレクトリの項目と、各項目の検証メッセージ（422 のフラグメントが入力値を残して再描画する。`hx-target-422`）を持つ。常時表示の注意書き（`cmd.exe`・`powershell.exe` などの登録は任意コードの実行を許す。NFR-S20）を置く。削除は確認モーダルを経る（上の表）。許可リストの変更はホスト所有者の操作でだけ起きるため `GET /poll` の対象にしない（操作の応答のフラグメントで一覧を更新する）。パスと引数は、ホスト GUI の HTML にだけ出し、リモートの UI（`organisms/remote`・`pages/remote`）と MCP には出さない（M2 の Web UI にも許可リストの画面は作らない）。

- ルートの応答（`handler/gui/apps.go`）: `GET /apps` は `HX-Request` なら一覧（`#app-list`）、`GET /apps/new` は追加フォーム、`GET /apps/{id}/edit` は編集フォームのフラグメント（`HX-Boosted` とそれ以外はフルページ。`/apps/{id}/edit` のフルページは一覧の該当行が編集フォームになる。対象が無ければ 404）。`POST /apps` の 200 は一覧（`#app-list` を差し替え）に、追加フォームを空に戻す OOB（`#app-form-new`）とトーストを添える。`PUT /apps/{id}` の 200 は行のカード（編集フォームを差し替え）とトースト。422 はどちらもフォーム自身（`hx-target-422="this"`）を、入力値（引数は入力どおりの複数行）と項目別のメッセージ付きで再描画する。50 件到達の 409 `conflict` と、想定外のエラーはトーストのフラグメント（`HX-Retarget: #toast-root`）。`DELETE /apps/{id}` の 200 は一覧に、モーダルを閉じる OOB とトーストを添える
- 引数は、入力欄の文字列をそのまま（LF・CRLF どちらも）`domain.ParseArgsLines` で 1 行 1 引数に分け、空行は捨てる。編集フォームは保存済みの引数を改行でつないで出す
- 実装は `organisms/app_list.templ`（`AppList`・`AppCard`）・`organisms/app_form.templ`（`AppForm`・`AppExePathField`）・`pages/apps.templ`・アトム `textarea`。ハンドラは `service.AppRegistry` を `gui.Deps.Apps`（利用者側のインターフェース `gui.Apps`）で受け、`platform` を import しない

**接続要求の履歴（`/requests/history`。FR-H03）**: 「接続要求」画面の「未処理」と「履歴」を `RequestsTabs` organism のタブで切り替える（どちらも `hx-boost`・`hx-target="#main"`・`hx-target-error="#main"`。サイドバーの強調は「接続要求」のまま）。履歴は `ConnectionRequestHistory` organism（`#request-history`）で、届いた日時・デバイス名・tailnet IP・ユーザー・結果（許可・拒否・失効・未処理のバッジ）・処理した日時の表を、読み取り専用で出す。件数が増えても重くならないよう、**1 ページ 20 件の固定のページ送り**にする（全件の読み込みも件数の上限による打ち切りもしない）。ページはクエリ `page` で、前へ・次へのリンクは `hx-get`・`hx-target="#request-history"`・`hx-swap="outerHTML"`・`hx-push-url="true"` で表だけを差し替える（`href` も持つため、JS なしや履歴の復元でも同じ URL でフルページが開く）。履歴は `#request-list` と違い `data-poll-rev` を持たず、`/poll` の対象にしない（表示中の表を勝手に差し替えない）。実装は `organisms/request_history.templ`・`pages/request_history.templ`

**ログ画面（`/logs`。FR-H13）**: サイドバーの「ログ」（`navItems`・`view.NavLogs`）。アプリの運用ログ（NFR-O01。ファイル）を、日時・レベル（`atoms.Badge`。ERROR は danger・WARN は warn・INFO は ok・DEBUG は neutral）・内容（メッセージと属性の `キー 値`）の表で新しい順に出す。`AppLog` organism（`#app-log`）が、レベルの絞り込み（「すべて」「WARN 以上」「ERROR のみ」のリンク。クエリ `level`）・表・ページ送り（クエリ `page`。1 ページ 50 件の固定。「接続要求の履歴」と同じ理由）を持ち、リンクは `hx-get`・`hx-target="#app-log"`・`hx-swap="outerHTML"`・`hx-push-url="true"` で `#app-log` だけを差し替えて URL を更新する（絞り込みのリンクはページを 1 に戻し、ページ送りは `level` を保つ）。ログファイルのパスはページ（`pages/logs.templ`）の見出しの下に出す（フラグメントには含めない）。ホストのファイルマネージャーで開くボタンは持たない（M1 の GUI にフォルダーを開く仕組みがなく、パスが分かれば足りるため。Issue #45）。読み取り専用で、`/poll` の対象ではない（手動の再読み込み。絞り込みのリンクのクリックでも最新になる）。ハンドラは `gui.Deps.Logs`（利用者側のインターフェース `gui.Logs`。実体は `app.LogReader`）で受ける。ログの文字列はすべて templ がエスケープする。ログに秘密が出ないこと（NFR-S13）は、書き込み側の `ReplaceAttr` と読み出し側の `redactString` で守る（[アーキテクチャ「運用ログと監査ログ」](overview.md)）

**監査ログ画面（`/audit`・`/audit/{id}`。FR-AU05、FR-H08。M3・#72）**: サイドバーの「監査ログ」（`navItems`・`view.NavAudit`）。ホスト GUI だけの読み取り専用の画面で、リモートのルーターには作らない。`organisms.AuditList`（`#audit-list`）が、絞り込みフォーム（`AuditFilters`。`molecules.FormField`・`atoms.Input` / `Select`。開始・終了日時は RFC 3339 のテキスト入力（`datetime-local` への変換はしない）・クライアント（`HostAdmin.Clients` の一覧。削除済みのクライアントの行は絞り込みの選択肢には出ず、一覧ではスナップショットの名前で出る）・種別・結果・イベント名の部分一致）・`ts` 降順の表（日時は `Location` のローカル時刻・クライアント・種別・イベント（詳細へのリンク）・結果バッジ（`ok` は ok、`denied` は warn、`error` は danger）・所要時間・サムネイルの有無）・「次へ（古いログ）」（`cursor` は前向きだけなので「前へ」は無い。戻るのはブラウザの履歴）を持つ。フォームと「次へ」は `hx-get`・`hx-target="#audit-list"`・`hx-swap="outerHTML"`・`hx-push-url="true"` で `#audit-list` だけを差し替えて URL を更新し、不正な値の 422 は `hx-target-422="#audit-list"` で同じ場所に項目別のメッセージ（`data-field-error`）付きで描き直す。`AuditDetail` organism は項目・引数のキー・値の表・サムネイル（`<img src="/audit/{id}/thumbnail">`。CSP の `img-src 'self'` で足りる。無いときは理由の文）を出す。ポーリング（`/poll`）・SSE の対象にしない（読み取りだけで、自動更新しない）。値は `AuditReader` が秘密を伏せたものだけで、templ が HTML エスケープする。見た目の確認用の画面は `internal/router/internal/guidemo`（スクリーンショットは `docs/screenshots/m3/`）。

**設定画面の監査ログ・更新のグループ（`/settings`。FR-S07、FR-S09。M3・#73）**: `organisms.SettingsForm` に `<fieldset>` を 2 つ足した（`data-settings-group="audit"`・`"update"`）。監査ログのグループは、常に出す注意書き（`data-notice="audit-plaintext"`。サムネイルと本文は平文で保存される・BitLocker でのドライブ全体の暗号化・サムネイルの保存の無効化の案内。NFR-S11）、`audit.thumbnails_enabled` と `audit.record_text` のチェックボックス（`audit.record_text` には常に出す警告文 `data-notice="audit-record-text"`。有効にするときの確認モーダルは作らない）、`audit.retention_days`（日）と `audit.thumbs_max_bytes`（バイト。単位は未決のため換算しない）の数値入力。更新のグループは `update.channel`（正式版 `stable` が既定 / ナイトリー）と `update.apply_mode`（`confirm` が既定）の選択（どちらも `atoms.Select`。#74 で `update.channel` を足し、説明を更新の確認の間隔・自動適用の条件に更新した）。検証は既存の項目と同じくサーバーが行い、422 で入力値と項目別のメッセージ（`data-field-error`）付きにフォームを再描画する。`log.level` は未決のため出さない。

**エージェント設定画面（`/agent`。FR-H11、FR-G14、FR-G22、NFR-S14。M4・#19）**: サイドバーの「エージェント」（`view.NavAgent`）。`pages.AgentSettings` / `AgentSettingsBoosted` が説明（既定は無効。`data-notice="agent-default-off"`）と `organisms.AgentSections`（`#agent-sections`）を包む。`AgentSections` は 2 つのカード（OpenAI のエージェント、高速パス（TypeSafe））で、それぞれ `organisms.APIKeyPanel`（キーの登録）と設定フォーム（`organisms.AgentForm`・`FastpathForm`）を持つ。2 つのフォームは `id`・`hx-put`・`hx-target="this"`・`hx-target-422="this"`・`novalidate` で自身を差し替える（`SettingsForm` と同じ規約）。**同意**は 2 つが別のチェックボックスと文言（`data-consent="openai"`・`"typesafe"`）: OpenAI は「スクリーンショット（プライマリモニターの全体）と指示・操作の履歴が OpenAI に送信される」、TypeSafe は「指示・前面のウィンドウのタイトルとプロセス名・要素の ID・役割・ラベル・有効か・直近 5 件の高速ステップの説明文が送信される。スクリーンショット・要素の値・入力した文字・キー入力・API キーは送らない」（FR-G14・G18・G22、NFR-PR04）。同意済みなら日時（`data-consent-at`）を出し、チェックを入れて描画する。**キーの入力欄**（`atoms.SecretInput`）は `type="password"`・`autocomplete="off"` で、`value` 属性を持たない（props にも値を受ける欄が無い）。パネルが受け取るのは登録の有無（真偽値）と失敗の理由だけ。削除ボタンは `hx-get` で確認モーダル（`#modal-root`）を開き、確定の `DELETE` は `#agent-sections` 全体を差し替える（有効・同意も変わるため）。JavaScript は HTMX のほかに要らない。

**更新画面（`/update`。FR-H09、FR-S09。M3・#74）**: サイドバーの「更新」（`navItems`・`view.NavUpdate`）。`pages.Update` / `UpdateBoosted` が `organisms.UpdatePanel`（`#update-panel`）を包む。パネルは現在の版・チャネル・適用方法・最後の確認（未確認なら「未確認」）・新版（版・公開日時・署名の検証結果のバッジ・リリースノートの要約）と、「今すぐ確認」（`hx-post="/update/check"`。`hx-target="#update-panel"`・`outerHTML`）・「更新する」（署名を検証できた新版があるときだけ。`hx-get="/update/apply/confirm"` で確認モーダルを `#modal-root` に出し、確定が `POST /update/apply`）。リリースノートは外部（GitHub の Release）由来なので、templ の自動エスケープのまま `<pre>`（`whitespace-pre-wrap`）に出す（`data-field="release-notes"`）。署名の検証に失敗した新版は「適用できません」のバッジと理由（`data-update="reject"`）を出し、「更新する」は出さない。確認の失敗は `data-update="error"`、署名鍵の未設定は `data-update="key-unset"`、`dev` ビルドは `data-update="not-updatable"` で、確認・適用のボタンを出さない。適用できない（リース保持中・接続中など）ときの 409 は、`HX-Retarget: #toast-root` のトーストに理由を出す（§5.2 の失敗の経路）。新版を検出したときのトーストは `Bus` の `toast`（`service.Updater` が出す）が `GET /poll` で届く。

### 5.5 HTMX 拡張の採否

| 拡張 | 採否 | 理由 |
|------|------|------|
| `response-targets` | 採用（両 UI） | エラーの表示先をステータス別に分ける |
| `sse` | **読み込まない**（`GET /events` はサーバーに残す） | Wails の AssetServer がストリームをバッファするため。状態の更新は `GET /poll`（§5.3） |
| `hx-boost` | ホスト GUI のサイドバーのみ | §5.4 |
| `preload` | 不使用 | 不要なリクエストを増やす |
| `loading-states` | 不使用 | `hx-indicator` で足りる |

### 5.6 エラーハンドリング

サーバーが UI を制御する原則はエラーにも適用する。

| 種類 | 扱い |
|------|------|
| バリデーション（422） | フォームを入力値とエラーメッセージ付きで再描画する。クライアント側のバリデーション JS は書かない。表示先は `hx-target-422` |
| ビジネスエラー（403 / 404 / 409 など。API 仕様 §2 の `code`） | エラーのフラグメントを同じステータスで返す（`data-error-code="<code>"` を付ける）。メインの領域は現状維持にして、OOB でトーストを出す |
| ページ遷移（`hx-boost`）が返した 4xx / 5xx | htmx は既定で 4xx / 5xx を差し替えないため、`hx-boost` のリンクには `hx-target-error="#main"` を付け、エラーページを `#main` に出す（`body` には付けない。アクションの失敗は `HX-Retarget` でトーストへ回す） |
| 未登録デバイス（403 `device_not_registered`） | 接続要求の待機ページ（フルページ。FR-W02） |
| 認証・認可の失敗など、ミドルウェアが拒否したもの | ページルートはエラーページ、アクションルートはエラーのフラグメント |
| ネットワーク障害・想定外の 5xx | クライアント側で唯一処理する。`htmx:responseError` / `htmx:sendError` のグローバルリスナーで汎用トーストを出す |

```html
<form hx-post="/clients/01J.../tokens"
      hx-target="#token-list"
      hx-target-422="#token-form-errors"
      hx-target-5*="#global-error">
```

- `/api/v1` のエラーは problem+json（HTML にしない）。Lit のビューアは `/api/v1` を呼ばないので HTML のエラーは受けない

## 6. Lit Web Components

### 6.1 使用基準と共通ライブラリ

**Web Component を作る基準**: リアルタイム編集、複雑なドラッグ＆ドロップ、Canvas / WebGL の描画、WebSocket によるリアルタイム同期のいずれか。HTMX で足りるもの（フォーム、一覧の更新、モーダル、承認ダイアログのカウントダウンなど）には使わない。

本プロジェクトで基準に該当するのは **ライブ画面ビューアのみ**（Canvas 描画、WebSocket のバイナリフレーム、マウス・キーの捕捉）。承認ダイアログの 60 秒のカウントダウンは、サーバーが残り時間を描画した HTMX（`hx-trigger="every 1s"` での再取得）または CSS で作り、Lit にはしない。

| ライブラリ | 状態 | 内容 |
|-----------|------|------|
| `lib/ws.ts` | 初版で作る | WebSocket のラッパー（§6.4）。Lit は必ずこれ経由で WebSocket を使い、`new WebSocket()` を直接呼ばない |
| `lib/logger.ts` | 初版で作る | レベル付きのコンソールロガー。本番はエラーと警告のみ |
| `lib/api.ts` | **初版では作らない**（ユーザー承認済み） | Lit から HTTP を呼ぶ箇所が無い（画面の更新は WebSocket と HTMX）。必要になった時点で追加する。追加する場合は、`X-Puppet-Remo` と `X-Viewer-Id` を付与し、problem+json を `ApiError` に変換し、Lit は必ずここを経由する（直接 `fetch()` を使わない） |

画面座標・キーの変換（`remo-screen-viewer` 内の入力処理）は、コンポーネントと同じディレクトリに置く（共通ライブラリには出さない）。

**命名**: カスタム要素の接頭辞は `remo`（`<remo-screen-viewer>`）。

### 6.2 `<remo-screen-viewer>`

ホスト画面をライブ表示し、操作権を持つときにマウス・キーボード入力を送る。

**属性（サーバーが注入する。クライアントは URL をハードコードしない）**

| 属性 | 型 | 内容 |
|------|----|------|
| `ws-url` | string | `ws://<host>:<port>/ws/screen?viewer_id=<ULID>`。サーバーが描画時に埋め込む |
| `readonly` | boolean | 描画時にサーバーが判定した操作権の有無（持たなければ付与する）。**初期描画のヒントにすぎず、入力の可否は決めない**（下の「入力」。HTMX はコンポーネントを swap しないので、取得・解放・剥奪で更新されない） |
| `monitor` / `quality` / `fps` | number | **初期値**。接続後はサーバーの `config` メッセージが正で、HTMX のツールバー（Lit の外）からの変更はサーバー経由で反映される |

```go
// Templ（remote/pages/viewer）
<main id="viewer-root" hx-headers={ templ.JSONString(map[string]string{"X-Viewer-Id": v.ViewerID}) }>
    @organisms.ViewerToolbar(v.Toolbar)   // Lit の外。モニター・品質・FPS・クリップボード・ショートカット
    @organisms.LeasePanel(v.Lease)        // Lit の外
    <remo-screen-viewer
        ws-url={ v.WSURL }
        readonly?={ !v.Lease.Mine }
        monitor={ strconv.Itoa(v.Monitor) }
        quality={ strconv.Itoa(v.Quality) }
        fps={ strconv.Itoa(v.FPS) }>
    </remo-screen-viewer>
</main>
```

**イベント（`bubbles: true, composed: true`）**

| イベント | `detail` | 用途 |
|---------|---------|------|
| `remo:lease-changed` | WebSocket の `lease` メッセージの内容 | HTMX がリースパネルを再取得する（`hx-trigger`） |
| `remo:error` | `{ code, message }` | 入力の拒否（`lease_required` / `input_blocked` など）。ページ側のハンドラ（`app.ts`）がトーストを出す。`message` はサーバーのドメインエラー文（英語。WebSocket の契約は変えない）なので、トーストは `code` から日本語にする（`lease_required` / `lease_held_by_other` / `lease_revoked` / `invalid_argument` / `kill_switch_active` / `desktop_unavailable` / `input_blocked` / `internal`。文言は HTMX のアクション失敗の `failureMessages` と同じ）。表に無い `code`（`invalid_coordinates` など）はサーバーの `message` を出す |

**状態とライフサイクル**

```mermaid
stateDiagram-v2
    [*] --> connecting: connectedCallback
    connecting --> open: ready 受信 + キーフレーム
    open --> reconnecting: 切断（1006 など）
    reconnecting --> open: 再接続に成功
    reconnecting --> failed: 10 回失敗
    open --> failed: 4400 / 4401 / 4429 / 4510
    open --> killswitch: 4503 または killswitch メッセージ
    open --> [*]: disconnectedCallback
    failed --> [*]
    killswitch --> [*]
```

- `connectedCallback` で `lib/ws.ts` に接続し、`disconnectedCallback` で接続・タイマー・キー押下状態を破棄する
- `connecting` / `reconnecting` / `failed` / `killswitch` の表示は、Shadow DOM 内にコンポーネント自身が描画する（HTMX は Lit の領域を swap しない）。`failed`（4401 / 4510 / 再試行の上限）と `killswitch` では、ページの再読み込みを促す文言を出す
- **再接続時はリースが失われる**（サーバーは WebSocket の切断で `web` のリースを解放する。API 仕様 §4.1）。再接続後の `ready` に現在のリース状態が入っているので、コンポーネントはそれを反映し、`remo:lease-changed` を発火して HTMX にパネルを再取得させる

**描画**

- `ready` / `config` の `width` / `height` で `canvas` のサイズを決める（ストリームの座標空間 = モニターの物理ピクセル）。CSS で表示サイズに縮小する
- バイナリフレーム（API 仕様 §5.4）を受信したら、各タイルの JPEG を `createImageBitmap` で復号し、`(x, y)` に描画する。連番（`seq`）が古い差分フレームは捨てる。キーフレーム（bit0）は全面を置き換え、連番によらず採用して連番を引き継ぐ（モニター・品質・FPS の変更でサーバーは連番を 1 から数え直すため）。復号中に新しいフレームが来ても、描画は連番の順に行う
- `width` / `height` が変わったら `canvas` を作り直し、次のキーフレームまで描画を保留する
- 閲覧のみ（非保持）でも描画は行う

**入力（操作権を持つときだけ）**

「入力が有効」とは、最後に受信した `lease`（`ready` の `lease` を含む）が `state: "held"` かつ `mine: true` であること（`pending_approval` は承認待ちの本人（`mine: true`）でも閲覧のみ。M3）。`mine: false`（他者の保持・空き・剥奪）では閲覧のみで、`mine` が `false` になった時点で押下中のキー・ボタンを送らずに捨てる（サーバーが解放する）。`readonly` 属性は描画時のサーバー判定を表す初期のヒントで、入力の可否には使わない（HTMX はコンポーネントを swap しないため、取得・解放・剥奪の後も属性は描画時のままで、これに頼ると操作権を取得しても入力が送られない）。サーバー側でも入力は常に検査される（リース・InputGuard）。

| 入力 | 送るメッセージ（API 仕様 §5.3） | 備考 |
|------|------------------------------|------|
| マウス移動 | `input.mouse` `move` | 座標は `canvas` の表示座標からストリームの物理座標へ変換（`round((clientX − rect.left) × canvas.width ÷ rect.width)`）。1 アニメーションフレームにつき最大 1 回に間引く |
| ボタン・ホイール | `input.mouse` `down` / `up` / `wheel` | `contextmenu` を抑止する。**`wheel` の `dx` / `dy` の単位は「ノッチ」**（数値。小数可。MCP `scroll.amount` と同じ単位）。ブラウザの `WheelEvent` を `deltaMode` に応じて換算する: `DOM_DELTA_PIXEL` は `delta ÷ 100`、`DOM_DELTA_LINE` は `delta ÷ 3`、`DOM_DELTA_PAGE` は `delta × 10`。符号は API 仕様の `scroll` の方向の規約に従う（`dy > 0` = 下。`WheelEvent.deltaY` と同じ）。サーバーが `ノッチ × 120`（Win32 の `WHEEL_DELTA`）を丸めて OS へ渡し、丸めの余りは次回へ繰り越す。Win32 側の符号の向きもサーバーが吸収する |
| キー | `input.key` `down` / `up`（`code`、`key`、`modifiers`） | キャンバスにフォーカスがあるときだけ捕捉し、`preventDefault` する。ブラウザが先取りするショートカットは送れない（専用メニュー。FR-W06）。フォーカスを失ったら押下中のキーすべてに `up` を送る |
| IME 確定 | `input.text` | `compositionend` の文字列を Unicode で送る。`canvas` は IME を受けないため、Shadow DOM 内の見えない `<textarea>`（`tabindex="-1"`。`css` で不可視にする）が IME とキー入力を受ける。`canvas` の `mousedown` でこの要素にフォーカスを移し（「`canvas` にフォーカスがある」とは、この要素にフォーカスがあること）、`keydown` / `keyup` / `compositionend` / `blur` はこの要素で受ける。IME の処理中のキー（`isComposing`・`keyCode` 229）は `input.key` で送らない |

### 6.3 Shadow DOM とスタイル

- Lit 側は Shadow DOM 内の `css` テンプレートリテラルで書き、外部の CSS の影響を受けない。`css` は constructable stylesheets（`adoptedStyleSheets`）として適用されるため、CSP の `style-src 'self'` のままで動く（`unsafe-inline` 不要。§7.1）
- Templ 側は Tailwind のユーティリティクラス（外部ファイルの `dist/css/app.css`）
- ダークモードは CSS カスタム変数で切り替える

### 6.4 `lib/ws.ts`

| 項目 | 仕様 |
|------|------|
| サブプロトコル | `puppet-remo.v1` |
| `binaryType` | `arraybuffer` |
| 受信 | テキストフレームは JSON をパースして `{ kind: "text", message }`、バイナリは `{ kind: "binary", data: ArrayBuffer }` として渡す |
| 送信 | `send(obj)`（JSON）。64 KiB を超えるメッセージは送らずエラーにする（NFR-S09） |
| 再接続 | 指数バックオフ（500 ms から、上限 15 s）で最大 10 回。**再接続しない閉じコード**: 4400 / 4401 / 4429 / 4503 / 4510。再接続した後は `onOpen` が再度呼ばれる。試行の数は、接続のたびにサーバーから 1 つでもメッセージを受け取ったら 0 に戻る（接続して直ぐ閉じる繰り返しは数え続けて、上限で止まる） |
| コールバック | `onOpen`、`onMessage`、`onClose(code)`、`onGiveUp` |

## 7. セキュリティとスタイル

- Cookie と認証トークンを JS から扱わない。識別はサーバーが WhoIs で行う（§2）
- `X-Puppet-Remo` と `X-Viewer-Id` は `hx-headers` が付与する。JS が組み立てない
- トークンの平文は、トークン発行のレスポンスのフラグメントに**一度だけ**表示する（`Cache-Control: no-store`。API 仕様 §7.2）。`<input readonly>` に全選択して出す。**コピーボタン**は `app.ts` がイベント委譲（`data-copy-target` 属性を持つボタンのクリック）で処理し、**ユーザーのクリック内で `navigator.clipboard.writeText` を呼ぶ**。使えない（未定義・拒否）ときは、入力欄を全選択したまま「Ctrl+C でコピーしてください」の文言を表示するフォールバックにする。**トークンを含むフラグメントのルートの要素には `hx-history="false"`** を付ける。htmx は、文書内にこの属性があるあいだ、そのページの履歴スナップショット（`sessionStorage`）を保存しない（htmx 2.0.11 の `saveCurrentPageToHistory`）ので、平文が残らず、戻る操作で再表示されない。WebView2 は `*.localhost` を安全なコンテキストとして扱うため前者が動く想定。実機での確認は §11
- `Origin` が同一のオリジンでない状態変更は拒否される（ミドルウェア。NFR-S02）。フロントエンドは Origin を偽装できない前提で、何もしない
- Templ は既定で出力を HTML エスケープする。`templ.Raw` と `templ.JSONString` 以外の生 HTML の埋め込みは禁止する
- ライブ画面の表示は、サーバーがキャプチャから除外した自ウィンドウを含まない（承認ダイアログ・オーバーレイはビューアに写らない。NFR-S06）

### 7.1 Content Security Policy（NFR-S19）

共有テンプレートを使う両ルーターの HTML に、**ミドルウェア `csp`** が次のヘッダーを付ける（`<meta>` タグでは付けない。`frame-ancestors` が meta では効かないため）。主な理由は、API キーの入力画面を持つホスト GUI の XSS 対策。

```
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'
```

- **配置**: GUI ルーター・リモートルーターの両方で、ミドルウェア連鎖の**最初**に置く（`hostcheck` などが返すエラーページ、キルスイッチの 503 ページにも付くようにする）。HTML 以外（JSON、静的ファイル）に付いても害は無いため、HTML かどうかの判定はしない
- `img-src ... data: blob:` はビューアの `canvas`（`createImageBitmap`）と小さなアイコンのため。`connect-src 'self'` は同一オリジンの `fetch`（HTMX・ポーリング）・WebSocket（`ws:`）を許可する

**規約（CSP の帰結）**

| 項目 | ルール |
|------|-------|
| スクリプト | インラインの `<script>` を書かない。すべて `dist/js/` の外部ファイル。Templ の `script` コンポーネント・`templ.JSFuncCall` など、インライン JS を出力する機能は使わない |
| スタイル | `<style>` 要素と `style=` 属性を書かない。Tailwind のクラスで表現する。Templ の `css` コンポーネント（`<style>` を出力する）は使わない。動的な値（進捗など）はクラスの切り替えか `data-*` 属性で表す。Lit は CSSOM（`element.style.x = …`）を使ってよい（`setAttribute("style", …)` は不可） |
| イベント属性 | `onclick=` など `on*` 属性を書かない。イベントは外部の JS（`app.ts`、Lit）で付ける |
| htmx の設定 | `static/src/app/htmx-config.ts` が `htmx.config.allowEval = false` と `htmx.config.includeIndicatorStyles = false` を設定する。`<meta name="htmx-config">` は使わない。`includeIndicatorStyles` を無効にするのは、htmx が既定でインラインの `<style>` を挿入するため。`.htmx-indicator` の表示切り替え（`opacity` 0 → 1）は `app.css` に Tailwind で定義する |
| htmx の属性 | `hx-on:*` と、`hx-vals` / `hx-headers` の `js:` 接頭辞を使わない。`hx-trigger` のイベントフィルター（`click[ctrlKey]` のような `[...]`）も eval が必要なため使わない。`hx-headers` の JSON 文字列（`{"X-Puppet-Remo":"1"}`）は**データ**であり使ってよい（`templ.JSONString` で出力する） |
| 読み込み順 | `layout` は `htmx.min.js` → `htmx-config.js`（classic スクリプト）→ 拡張 → `app.js`（`type="module"`）の順で読み込む。設定は htmx の初期化（`DOMContentLoaded`）より前に適用される必要がある |
| スワップされる HTML | フラグメントにも上記が適用される。ハンドラのテスト（§9）が、描画した HTML にインラインのスクリプト・スタイル・`on*` 属性が無いことを検査する |

- E2E（§9）は、すべてのページで `securitypolicyviolation` イベントと CSP のコンソールエラーを失敗として扱う。これにより htmx のバージョン更新時の回帰（インラインのスタイル・スクリプトの再混入）を検出する
- Wails の WebView 固有の確認（オリジン、Wails がインラインのスクリプトを注入しないか）は実機で行う（§11）

## 8. ビルド

### 8.1 パイプライン

```mermaid
graph LR
    T[".templ"] -- "templ generate" --> G["_templ.go"]
    TS["src/**/*.ts<br/>(components, app, htmx-config)"] -- "esbuild" --> JS["dist/js/*.js"]
    CSS["src/css/app.css"] -- "Tailwind CLI" --> C["dist/css/app.css"]
    H["htmx.org / htmx-ext-*"] -- "copy" --> JS
    G --> BIN["Go ビルド"]
    JS --> E["go:embed (production)"]
    C --> E
    E --> BIN
    BIN -- "wails3 package" --> NSIS["NSIS インストーラ"]
```

- esbuild: エントリーポイントは各 Web Component（`screen-viewer`）と `src/app/` の 2 ファイル。出力形式は下記。本番は minify、開発は sourcemap
- Tailwind CLI: `src/css/app.css` → `dist/css/app.css`（minify）。Templ のファイルをコンテンツとして走査する
- HTMX: `htmx.min.js`、`response-targets`、`sse` を `dist/js/` にコピーする（HTMX の拡張は別パッケージ）。**版の固定の手順**: 実装の着手時に `bun add htmx.org@2 htmx-ext-response-targets@2 htmx-ext-sse@2` で最新の 2.x を入れ、`bun.lock` をコミットして固定する。以降の更新は意図的に行い、E2E の CSP 違反の検査（§7.1）を通す
- esbuild の出力形式: `htmx-config.ts` は IIFE（classic スクリプト。htmx の直後に同期で読み込む）、`app.ts` と Web Component は ES モジュール。Templ の `<script>` タグは外部ファイルの参照のみ（§7.1）で、モジュールには `type="module"` を付ける
- 成果物は両ルーターが同じ `dist/` から配信する。`Cache-Control: no-cache` + `ETag`（ビルド ID。§2）

### 8.2 ビルドスクリプト（`static/package.json`）

```json
{
  "scripts": {
    "build": "bun run clean && bun run build:css && bun run build:js && bun run copy:htmx",
    "clean": "bun scripts/clean.mjs",
    "build:js": "bun esbuild.config.mjs",
    "build:css": "bun --bun tailwindcss -i src/css/app.css -o dist/css/app.css --minify",
    "copy:htmx": "bun scripts/copy-htmx.mjs",
    "dev": "bun --bun concurrently \"bun run dev:css\" \"bun run dev:js\"",
    "dev:css": "bun --bun tailwindcss -i src/css/app.css -o dist/css/app.css --watch",
    "dev:js": "bun esbuild.config.mjs --watch",
    "test": "bun --bun web-test-runner \"src/**/*.test.ts\" --node-resolve"
  }
}
```

`esbuild.config.mjs` は `--watch` のとき、ウォッチに加えて esbuild の `serve`（`servedir: "dist"`）を、環境変数 `WAILS_VITE_PORT`（`wails3 dev` が設定。既定 9245）のポートで開始する。環境変数の展開が OS のシェルに依存しないよう、ポートはシェルではなく `esbuild.config.mjs`（`process.env`）が読む。

### 8.3 開発ワークフロー（Wails 流）

`wails3 dev` が Taskfile の `dev` タスクを実行する。Go の変更の検知・再ビルド・再起動は Wails が行うため、`air` は使わない。

| プロセス | ツール | 役割 |
|---------|-------|------|
| アプリ（Go） | `wails3 dev` | `.go` の変更で再ビルド・再起動。ホスト GUI は WebView で確認 |
| Templ 生成 | `templ generate --watch` | `.templ` の変更で `_templ.go` を再生成（Wails がそれを検知して再ビルド） |
| フロントエンド | Taskfile の `frontend:dev`（`bun run dev` = esbuild のウォッチ + `serve`、Tailwind のウォッチ） | `static/dist/` を更新し、**`WAILS_VITE_PORT` で待ち受ける**（下記） |

- 開発ビルド（`!production`）の静的ファイルはディスク（`static/dist`）から配信する。ブラウザの再読み込みで反映される。ディスクの場所はカレントから親へ辿って `static/dist` を探す（`-trimpath` でソースの位置が使えないため）。別の場所は環境変数 `PUPPET_REMO_STATIC_DIR` で指す。ビルド ID（`ETag`）は `dist` 全体の内容のハッシュ（本番は起動後の最初の 1 回、開発は毎回）本番ビルド（`-tags production`。`wails3 build` / `package` が付与）は `go:embed` を使う
- **`wails3 dev` は常に `FRONTEND_DEVSERVER_URL=http://<host>:$WAILS_VITE_PORT`（`WAILS_VITE_PORT` の既定は 9245）を設定し、アプリの起動前処理（preRun）がその URL を 500 ms 間隔で 10 回確認して、何も待ち受けていなければ致命的エラーで終了する**（Wails v3.0.0 のソースで確認）。したがってこの変数を未設定にして運用することはできない。`frontend:dev` タスクは、`WAILS_VITE_PORT` で待ち受けるプロセスを起動する必要がある。本プロジェクトでは esbuild の `serve` がその役目を担う
- esbuild の `serve` は**待ち受けの確認とライブのリビルドのためのスタブ**で、GUI のページは配信しない。開発サーバーへの転送（プロキシ）は Wails の既定の `AssetFileServer` にだけ働き、**カスタムの `AssetOptions.Handler`（本アプリの GUI ルーター = Gin）には働かない**。GUI のページ・フラグメントは常に Gin のハンドラから直接配信される
- 開発時は WebView の開始 URL のポートが開発サーバーのポート（`WAILS_VITE_PORT`）になる。ページはそのオリジンに対する相対パスで GUI ルーターへ届く。CSP の `'self'` はこのオリジンを指す（§7.1。実機での確認は §11）
- `/wails/*` は Wails のランタイム用に予約する。GUI ルーターにそのパスを登録しない（ランタイムの JS は同一オリジンで配信される）
- リモートの Web UI は、ホスト上のアプリが tailnet IP で配信する。開発時は、ホストのブラウザでそのアドレスを開いて確認する

## 9. テスト戦略

レイヤーごとに手法を使い分ける。HTMX のインタラクションは E2E でカバーする。CI はセルフホストの Linux ランナー（`[self-hosted, Linux, X64]`）で動き対話デスクトップが無いため、E2E は偽の `platform` 実装で行う（[コンポーネント設計](../components/overview.md) §10）。偽の platform と偽の tailnet で OS 非依存になるため、Playwright は Linux のランナーで実行する（Chromium の導入が容易）。Windows 向けのコードは `GOOS=windows` の `go build` / `go vet` のクロスコンパイルで検査するだけで、Windows の実機は CI に使わない。実機スモークは手動のまま。Lit のテストと Playwright の E2E は Web UI を作る M2 で CI に足す（M1 の CI にはない）。

| レイヤー | 手法 | 検証内容 |
|---------|------|---------|
| Go ハンドラ（GUI・リモート） | `httptest` + HTML アサーション | ページルートの `HX-Request` 分岐（フルページに `<html>` が有る / フラグメントに無い）、アクションルートが常にフラグメント、ステータスコード、`/api/v1` が JSON、ミドルウェアの拒否（Host / Origin / ヘッダー / 未登録 / キルスイッチ）、エラーのフラグメントの `data-error-code`、**すべての HTML レスポンス（エラーページ・キルスイッチの 503 を含む）に §7.1 の CSP ヘッダーが付くこと** |
| Templ | Go のテストで `Render()` → HTML 文字列のアサーション | **状態駆動レンダリング**: 権限・状態ごとに、押せないボタンが**描画されていない**こと（`CanRevoke = false` なら強制剥奪のボタンが HTML に無い）。キルスイッチ中は操作系が無い。**CSP 適合**: 描画した HTML に、`src` の無い `<script>`、`<style>`、`style=` 属性、`on*` 属性、`hx-on` 系の属性、`js:` 接頭辞、`hx-trigger` の `[...]` フィルターが無いこと |
| Lit（`<remo-screen-viewer>`。M2） | `@open-wc/testing` + `@web/test-runner`（ヘッドレス Chromium） | タイルの描画（偽の WebSocket から画像を流す）、座標変換、連番の古いフレームの破棄、キーフレームでの置換、`lease.mine` による入力の有効・無効（`readonly` 属性が付いたままでも、`mine: true` の `lease` で入力が送られること）、フォーカスを失ったときの `up` 送信、再接続の方針（再接続しない閉じコード）、`remo:lease-changed` の発火 |
| E2E（M2） | Playwright（Chromium。Linux のセルフホストランナー） | **偽の `platform` と偽の tailnet（`Listener` / `WhoIs` のテスト用実装）で起動したテスト専用のサーバー**（ビルドタグ `e2e`。本番のバイナリには含めない）に対して、リモートの Web UI を操作する: 未登録デバイスの待機ページ → ホスト側で許可 → ビューアの表示 → 操作権の取得 → クリックが偽の injector に届く → 強制剥奪 → キルスイッチ。全ページで CSP 違反（`securitypolicyviolation`）が 0 件であること（§7.1） |
| GUI ルーターの E2E | `httptest` で足りる範囲に限る | Wails のウィンドウ自体（トレイ、承認ダイアログのウィンドウ、オーバーレイ）の動作は実機スモーク（コンポーネント設計 §10） |

テスト専用サーバーは tailnet の IP ではなくループバックで Listen するが、これは**テスト用のビルドタグ（`e2e`）に限る**（NFR-S01）。本番のビルドは常に tailnet の IP だけで Listen する。

**CI の手順**（すべて Linux のセルフホストランナー。ジョブは `go`・`windows-cross`・`frontend`・`lint`）: `go` ジョブ: gofmt → `go build` / `go vet` → import 規則 → `go test`。`windows-cross` ジョブ: `GOOS=windows` の `go build` / `go vet`（と import 規則）。`frontend` ジョブ: bun の導入（`./.github/actions/setup-bun`。sudo 不要、版と SHA-256 は `scripts/ci/install-bun.sh` に固定）→ `templ generate` の差分チェック → `bun install --frozen-lockfile` → フロントエンドのビルド（`bun run build`）→ TypeScript の型検査（`bunx tsc -p .`）→ 本番ビルド（Windows 向けを含む）→ 同梱した `dist` のテスト。`lint` ジョブ: golangci-lint（Linux と `GOOS=windows`）。M2 で、Lit のテスト（`bun run test`）と、テスト専用サーバー（`-tags e2e`）をビルドしての Playwright を足す。

## 10. 要件とのトレース

| 要件 | 担当 |
|------|------|
| FR-W01〜W09（Web UI） | `pages/remote`、`organisms/remote`、`<remo-screen-viewer>`、`handler/remote` |
| FR-H02〜H11（ホスト GUI） | `pages/gui`、`organisms/gui`、`handler/gui`（`GET /poll`）。M1 のダッシュボードは状態表示・キルスイッチ・接続要求・クライアント管理まで。GUI の強制剥奪ボタンは M2 で実装した（`organisms.LeasePanelBody` の `CanRevoke`。`POST /lease/revoke`）。実行中のエージェントタスクのパネルと中断ボタンは M4 で実装した（`organisms.RunningTasksPanel`・`organisms.TaskStatus`。`POST /tasks/{id}/cancel`。`GET /poll` の `rev_tasks`）。タスク画面（`/tasks`・`/tasks/{id}`。FR-H10）は `pages.Tasks` / `TaskDetail`・`organisms.TaskList` / `TaskDetail` |
| FR-H12（起動許可アプリ。M1.5） | `pages`（`apps.templ`。設計の `pages/gui`）、`organisms`（`app_list.templ`・`app_form.templ`。設計の `organisms/gui`）、`handler/gui`（`apps.go`: `/apps`・`/apps/pick-file`）、`atoms`（`textarea`）、`molecules`（`modal`・`form_field`）。リモートの UI には作らない |
| FR-H03・H04・H05・S05（承認モードと承認ダイアログ。M3・#68） | 接続要求の許可フォーム（`organisms.ConnectionRequestList`）の承認モード（常時承認（既定）・初回のみ承認・自動許可）、クライアントのカード（`organisms.ClientCard`）の承認モードの選択（`hx-patch`・`change` で送信。`approved_at` が NULL に戻る）と「初回のみ」の承認済み（日時）・未承認の表示・「承認を取り消す」（`POST /clients/{id}/approval/reset`。確認モーダルは挟まない）、承認ダイアログ（`pages.ApprovalDialog`・`organisms.ApprovalDialog`・`organisms.ApprovalRemaining`・`layout.Dialog`。サイドバー・ヘッダー・ポーリングの無い簡素な外枠） | `handler/gui/approvals.go`（`/approvals/{id}`・`allow`・`deny`。`gui.Deps.Approval`）。許可 / 拒否のボタンは `hx-disabled-elt="#approval-actions button"` で二重送信を防ぐ。表示する文字列はクライアント名・種別・要求の内容・残り時間だけ。`internal/router/internal/guidemo`（`-lease pending_approval`）で承認待ちとダイアログを確認でき、スクリーンショットは `docs/screenshots/m3/` |
| FR-L07（承認待ち。M3・#67） | `lease_panel` に `pending_approval` の表示（「承認待ち: <名前>（種別）」。本人には「ホストの承認を待っています」）。承認待ちの間は取得・解放・強制剥奪のボタンも `#viewer-ops` も出さない | リモートの `ViewerLeasePanel` と GUI のダッシュボードの `#lease`（`LeaseDisplay`）は、`view.ViewerLease.Pending`・`PendingMine` / `view.Lease.Pending` で承認待ちを描く（`molecules.LeasePendingBadge`）。`data-lease-state="pending_approval"`。GUI は強制剥奪のボタンを `held` のときだけ出す現行の動きのまま（承認待ちの剥奪は docs に無い）。拒否・タイムアウトは `POST /viewer/lease/acquire` の 403 `approval_denied`・408 `approval_timeout` のトースト（`HX-Retarget: #toast-root`）。`<remo-screen-viewer>` は `state !== "held"` を入力無効として扱う |
| FR-L06（強制剥奪。M2） | `lease_panel`（リモート・GUI）の状態駆動レンダリング。GUI のリースパネル（`#lease`）は M2 から `CanRevoke`（他者の `mcp` / `web` が保持中）のときだけ強制剥奪のボタンを描画する。リモートの `lease_panel` は別の子 Issue |
| NFR-S02・S03（Host / Origin、カスタムヘッダー） | `middleware`、`layout` の `hx-headers` |
| NFR-S06（自己保護） | ビューアに自ウィンドウが写らない（キャプチャ除外）。GUI に AI の入力が届かない（InputGuard） |
| NFR-P02（表示遅延） | ビューアの描画方式（§6.2）、`lib/ws.ts` |
| NFR-S19（CSP） | `middleware/csp`、`layout`（外部スクリプトのみ）、`static/src/app/htmx-config.ts`、§7.1 の規約を検査する Templ・ハンドラ・E2E のテスト |

## 11. 実機で検証する事項

設計上の決定事項はすべて確定済み（§2・§6〜§9）。ここには、Wails の WebView や実機のブラウザでなければ確認できない**検証項目**だけを置く。結果が想定と異なった場合は、該当の節（括弧内）を改訂する。

| 項目 | 検証内容 |
|------|---------|
| Wails の GUI での CSP とオリジン（§7.1・§8.3） | 本番と `wails3 dev` それぞれで、WebView の実際のオリジン（開発時は開発サーバーのポート）に対して `'self'` が効き、GUI の全ページで CSP 違反が出ないこと。Wails が HTML にインラインのスクリプト・スタイルを注入しないこと。`/wails/runtime.js` が同一オリジンで読み込まれること |
| `wails3 dev` の起動（§8.3） | `frontend:dev` の esbuild `serve` が、`FRONTEND_DEVSERVER_URL` のホスト・ポートで待ち受け、preRun の確認（500 ms × 10 回）を通過すること。その状態で WebView が GUI ルーター（Gin）のページを表示すること |
| GUI の WebView でのクリップボード（§7） | トークンのコピーボタンで `navigator.clipboard.writeText` がクリック内で成功すること。失敗したときにフォールバック（全選択 + Ctrl+C の案内）が表示されること |
| ブラウザでの WebSocket と CSP（§7.1） | `connect-src 'self'` が、利用するブラウザ（Chromium 系、Firefox、Safari）で同一オリジンの `ws://<tailnet IP>:<port>/ws/screen` を許可すること（Web UI は `ws:`。tailnet の通信は WireGuard で暗号化されるため `wss:` は使わない） |
| GUI の `GET /poll`（§5.3）の WebView での更新 | ダッシュボード・接続要求のページを開いたまま、tailnet 側から接続要求を作る・キルスイッチを発動する・セッションを張る操作を行い、約 3 秒以内に一覧・バッジ・ヘッダー・パネルが再読み込みなしで更新されること。入力中の許可フォーム（表示名・チェックボックス）がポーリングで消えないこと。**決定済み**: Wails v3.0.0 の Windows の AssetServer は応答を本文の完了までバッファし、`http.Flusher` を持たないため（`internal/assetserver/webview/responsewriter_windows.go` で確認）、SSE（`GET /events`）は WebView に届かない。ポーリングを採用した（`docs/operations/smoke-windows.md` §4.3 W3） |

## 12. M1 実装での差分

M1 はホスト GUI（GUI ルーター）だけを作る。本書にあって M1 に無い・異なる点は次のとおり。

| 項目 | 設計 | M1 の実装 |
|------|------|----------|
| リモートの UI（§1.3・§3・§6） | Web UI（`/viewer`）、Lit の `<remo-screen-viewer>`、`lib/ws.ts`、`handler/remote`、`organisms/remote`・`pages/remote` | Web UI のページ・ハンドラは M2 の #62 で作った（`/`・`/viewer`・リース・設定・クリップボード・ショートカット・待機ページ。API 仕様 §4.2・§7.5）。`<remo-screen-viewer>`・`lib/ws.ts`・`lib/logger.ts`・`src/components/` は #63 で作った（esbuild が `dist/js/screen-viewer.js` を出し、レイアウトが読み込む。`screen-viewer/` は `remo-screen-viewer.ts`・`protocol.ts`（メッセージ・バイナリフレームの復号）・`input.ts`（座標・ホイール・キーの変換））。Lit のテスト・`web-test-runner`・`e2e/`（Playwright）は作らない（下の「ビルドと CI」） |
| `internal/web` の構成（§3） | `organisms/{gui,remote}/`・`pages/{gui,remote}/`、`handler/{gui,remote}/`、`layout/{gui,remote}` | `organisms/`・`pages/` は平ら（`gui/` / `remote/` のサブディレクトリを作らない。ビューアは `organisms/viewer_*.templ`、待機・エラーは `pages/remote_notice.templ`、ページは `pages/viewer.templ`）。`handler/gui/` と `handler/remote/`（M2。偽の依存は `handler/remote/remotetest/`）。`layout/` は `gui.templ` と `remote.templ`（`Remote` がビューアの外枠、`RemoteNotice` が待機・拒否・失効・ミドルウェアのエラーの簡素な外枠）。`webtest/`（ハンドラのテスト補助）と `handler/gui/guitest/`（偽の service）を追加した。`view/` は設計どおり（ビューアは `Viewer`・`ViewerToolbar`・`ViewerLease`・`ViewerOps`・`KeyMenuItem`・`RemoteError`）。アトムは `alert`・`badge`・`button`・`checkbox`・`input`・`select`・`spinner`・`textarea`・`toast`、モレキュールは `card`・`form_field`・`lease_badge`・`modal`・`status_pill`・`token_reveal` |
| リモートのリースパネル・ビューアの組み立て（§4.3・§6.2） | `lease_panel`・`viewer_toolbar`・`clipboard_panel`・`key_menu` の各 organism を `<main id="viewer-root">` に並べる | `ViewerLeasePanel`（`#lease-panel`。`remo:lease-changed from:#viewer-root` で `GET /viewer/lease` を再取得して `outerHTML` で差し替える）・`ViewerToolbar`（`#viewer-toolbar`。`change` で `POST /viewer/settings`、422 は `hx-target-422="this"`）・`ViewerOps`（`#viewer-ops`。操作権を持つときだけ `ClipboardPanel` と `KeyMenu` を描画し、リース系の応答に OOB で添えて更新する）・`ViewerRoot`（`<remo-screen-viewer>` を HTMX の対象の外に置き、`readonly` は描画時の操作権の有無）。エラーはホスト GUI と同じく `HX-Retarget: #toast-root` のトースト。スライダー（画質・FPS）の横の数値は `app.ts` が `input` イベントで追従させる |
| GUI のページ・ハンドラ（§3） | dashboard・requests・clients・tokens・approvals・tasks・agent・audit・settings・update・poll・events | M1 は dashboard・requests・clients（詳細とトークン）・settings・poll・events・killswitch のみ（API 仕様 §7.5）。M1.5 で apps（起動許可アプリ。`handler/gui/apps.go`）を足した。監査（`handler/gui/audit.go`。`/audit`・`/audit/{id}`・`/audit/{id}/thumbnail`）は M3 の #72 で足した。設定は M3 の #73 で監査ログ・更新のグループを足した。承認（approvals。M3・#68 で実装。`handler/gui/approvals.go`。M4 で `safety_check`・`judge_action` の表示を足した）・タスク（`handler/gui/tasks.go`。M4）・更新（M3・#74）は実装済み。エージェント設定（`handler/gui/agent.go`。`/agent`・API キーの登録と削除）は M4 の #19 で足した。 |
| ホスト GUI の更新（§5.3） | `GET /poll`（本書のとおり） | 同じ。`GET /events`（SSE）は残すが GUI は購読しない。`htmx-ext-sse` は `static/package.json` に入れて `dist/js/sse.min.js` へコピーするが、レイアウトは読み込まない（未使用。ビルドの対象としては残る） |
| `static/package.json`（§8.2） | `lit`・`htmx-ext-*`・`@open-wc/testing`・`@web/test-runner`、`test` スクリプト | 依存は `htmx.org`・`htmx-ext-response-targets`・`htmx-ext-sse`・`lit`（#63。版は固定）、開発依存は `tailwindcss`・`@tailwindcss/cli`・`esbuild`・`typescript`・`concurrently`。`@open-wc/testing`・`@web/test-runner` は入れず、`test` スクリプトは無い（Lit のテストを CI に足さないというオーナーの決定。#64）。スクリプトに `clean` を足した（`build` の先頭で `dist` を消す）。esbuild のエントリーは `src/app/` の 2 ファイルと `screen-viewer`（`src/components/screen-viewer/remo-screen-viewer.ts` → `dist/js/screen-viewer.js`。ES モジュール）。`define` の `__DEBUG__`（`--watch` のとき true）で、`lib/logger.ts` の本番のログをエラー・警告だけにする |
| 開発タスク名（§8.3） | `frontend:dev` | 同じ。GUI の見た目の確認用に `task gui:demo`（`internal/router/internal/guidemo`。偽の service を loopback で配信）を足した。スクリーンショットは `docs/screenshots/m1/` |
| ビルドと CI（§8・§9） | Playwright の E2E・Lit のテスト | M1 の CI は Go のビルド・vet・lint・test・import 規則・templ 生成の差分・フロントエンドのビルド。Lit のテストと E2E（Playwright）は足さない（オーナーの決定。#64）。型検査（`bunx tsc -p .`）とビルドで `<remo-screen-viewer>` を検査する。Windows 実機の確認は [`docs/operations/smoke-windows.md`](../operations/smoke-windows.md) の手動手順 |

## 改訂履歴

| 版 | 日付 | 変更内容 | 変更理由 |
|---|------|---------|---------|
| 1.0 | 2026-10-29 | 初版作成（`overview.md` の「HALT の適用」から詳細を分離） | HALT の規約を 1 か所に集約する |
| 1.1 | 2026-10-29 | CSP（§7.1）を新設。ホイールの単位をノッチに確定（§6.2）。トークンのコピーの方式を確定（§7）。`wails3 dev` の `FRONTEND_DEVSERVER_URL` に関する記述を、ソースで確認した事実（`WAILS_VITE_PORT` での待ち受けが必須、カスタム Handler は転送されない）に訂正し、`frontend:dev` を esbuild の `serve` に確定（§8.3）。Playwright を ubuntu の別ジョブに確定（§9）。静的ファイルのキャッシュ・`lib/api.ts` を作らないことをユーザー承認済みとして確定（§2）。htmx の版の固定手順を明記（§8.1）。§11 を実機で検証する事項だけに改めた | 未決事項の決定 |
| 1.2 | 2026-10-29 | PR #2 のレビュー対応: §10 のトレースに、M1 のダッシュボードの範囲（強制剥奪ボタンは M2、`task_panel` と中断ボタンは M4）を明記 | M1 の実装者が迷わない仕様にするためのレビュー指摘の解消 |
| 1.3 | 2026-10-30 | GUI の実装（#12）に合わせて §8.3 に開発ビルドの `dist` の探し方とビルド ID の算出を追記 | 実装で確定した事項の反映 |
| 1.4 | 2026-10-30 | ホスト GUI の状態の更新を SSE から `GET /poll` のポーリング（3 秒ごと・OOB・版の隠し入力）に変更し、`sse` 拡張をレイアウトから外した（§1・§2・§5.3・§5.5・§11）。`GET /events` は残す | Wails v3.0.0 の Windows の AssetServer が `AssetOptions.Handler` の応答をバッファし、SSE が WebView に届かないため（#12） |
| 1.5 | 2026-10-30 | 開発タスク名を実在する `frontend:dev` に訂正（`dev:frontend` は誤り）。`hx-boost` のリンクの `hx-target-error` と、トークン表示の `hx-history="false"` を追記（§5.5・§7） | 実装のレビュー指摘（エラーページが表示されない・トークンの平文が履歴に残る）の反映 |
| 1.6 | 2026-10-30 | §9 の CI の記述を、`windows-latest` と Windows のジョブ・ubuntu のジョブから、Linux のセルフホストランナーと `GOOS=windows` のクロスコンパイルに更新。Lit のテストと Playwright は M2 と明記 | CI をセルフホストの Linux ランナーに統一（オーナー決定） |
| 1.7 | 2026-10-30 | §12「M1 実装での差分」を新設（リモートの UI・Lit・E2E が M2、`internal/web` の構成、GUI のページ、`htmx-ext-sse` の同梱と未使用、`static/package.json`、`gui:demo`・スクリーンショット・スモーク手順）。§3 のハンドラの依存規則に `tailnet` を追記 | M1 の実装との差分の文書化（オーナー決定） |
| 1.8 | 2026-10-30 | 起動許可アプリの画面（M1.5。FR-H12）を追加: §3 のディレクトリ構成（`apps`・`app_list`・`app_form`）、§5.4 のファイル選択のパターン（`hx-post="/apps/pick-file"`）、§5 の画面の方針（フォーム・注意書き・削除の確認・`/poll` の対象外・パスはホスト GUI だけに出す）、§10 のトレース | M1.5 の仕様追記（#16。オーナー決定 8 件） |
| 1.9 | 2026-10-30 | 起動許可アプリの画面の実装（#23）に合わせて、§5.4 のファイル選択の `hx-target` と `form_id`、§5 の `/apps` の応答（フラグメント・OOB・422・409）と構成、§10 のトレース、§12 のアトム（`textarea`）・ページを追記 | #23 の実装との整合 |
| 1.10 | 2026-11-01 | フロントエンド（`static/`）のパッケージマネージャを npm から bun に変更（§8.1・§8.2・§8.3・§9）。ロックファイルは `package-lock.json` から `bun.lock`、スクリプトは `bun run`・`bunx`、CI は `bun install --frozen-lockfile`。runner への bun の導入手順（sudo なし）を CI の中に持つ | TS/JS のパッケージマネージャは bun に固定する方針（#42） |
| 1.11 | 2026-11-01 | §5.4 に接続要求の履歴（「未処理」「履歴」のタブと、1 ページ 20 件のページ送り。固定の件数のページ分けを選んだ理由と `hx-get` の差し替え）を追加 | #44 |
| 1.12 | 2026-11-01 | §5.4 にログ画面（サイドバーの「ログ」。レベルの絞り込みと 1 ページ 50 件のページ送り、`#app-log` の差し替え、ファイルを開くボタンを持たない理由）を追加 | #45 |
| 1.13 | 2026-11-01 | §10 のトレースの GUI の強制剥奪ボタン（M2。`CanRevoke` のときだけ描画、`POST /lease/revoke`）を実装に合わせて更新 | #58 |
| 1.14 | 2026-11-01 | §12 に Web UI（`/viewer`。`handler/remote`・`layout/remote`・`organisms/viewer_*`・`pages/viewer`・待機・拒否・失効のページ）の実装と、`<remo-screen-viewer>` の成果物は #63 が出すこと、`app.ts` のスライダーの数値の追従を追記 | #62 |
| 1.15 | 2026-11-01 | §6.2 に IME とキー入力を受ける見えない入力要素、§6.4 に再接続の試行の数え直しを追記。§12 の差分表（`lit`・`lib/ws.ts`・`lib/logger.ts`・`components/`・esbuild のエントリー・Lit のテストと E2E を作らないこと）を更新 | #63 |
| 1.16 | 2026-11-01 | §6.2 の `readonly` を初期描画のヒントに限定し、入力の有効・無効は WebSocket の `lease`（`mine`）だけで決めると明記（操作権の取得後も `readonly` が残って入力が送られない不具合の修正） | #63 |
| 1.17 | 2026-11-01 | §6.2 の `remo:error` のトーストを、`code` から日本語にする（文言は HTMX のアクション失敗と揃え、表に無い `code` はサーバーの `message`。WebSocket の契約は変えない） | #63 |
| 1.18 | 2026-11-02 | §5.4 に監査ログ画面（`/audit`。絞り込み・「次へ」・詳細・サムネイル）、§12 の差分表の GUI のハンドラに `audit` を足した | #72 |
| 1.19 | 2026-11-02 | §5.4 に設定画面の監査ログ・更新のグループ（平文の注意書き・`audit.record_text` の警告・バイト単位の入力・`update.apply_mode` の選択）、§12 の差分表の GUI のハンドラに設定の M3 の項目を追記 | #73 |
| 1.20 | 2026-11-02 | §4.3 の `Can*` に承認待ちの扱い、§6.2 の「入力が有効」に `state: held` の条件、§12 に FR-L07（承認待ちのリースパネル・ダッシュボード）の実装を追記 | #67 |
| 1.21 | 2026-11-02 | §5.3 の `approval`・`approval-closed` と承認ダイアログの残り時間・`closeWindow`、§12 に FR-H03・H04・H05・S05（承認モードの選択・取消、承認ダイアログ）の実装を追記 | #68 |
| 1.22 | 2026-11-02 | §5.4 に更新画面（`/update`。#74）と、設定画面の更新のチャネル（`update.channel`）を追記 | #74 |
| 1.23 | 2026-11-02 | §5.4 にエージェント設定画面（`/agent`。#19）: 2 つの同意・伏せ字の入力欄（`atoms.SecretInput`）・キーのパネルと確認モーダル・`#agent-sections` の差し替え。§12 のトレースの GUI のページに `agent` を含める | #19 |
| 1.24 | 2026-11-02 | §10・§12 の `task_panel` と中断ボタン、タスク（`/tasks`）、承認ダイアログの `safety_check`・`judge_action` を M4 の実装（`TaskStatus` の共有部品・`#running-tasks` の `rev_tasks`・`approvalTexts`）に更新 | #19（M4 ホスト GUI・タスク） |
