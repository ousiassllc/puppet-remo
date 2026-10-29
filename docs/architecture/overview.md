# アーキテクチャ設計

要件は [機能要件](../requirements/functional.md) と [非機能要件](../requirements/non-functional.md)、全体像は [プロジェクト概要](../overview.md) を参照。対象 OS は Windows のみ。

## 設計方針

- **単一の Go プロセス**がすべてを担う。ホスト GUI・Web UI・MCP・内蔵エージェント・画面キャプチャ・入力注入・永続化を 1 つの Wails アプリに内蔵する
- **フロントエンドは HALT（HTMX + Atomic Design + Lit + Templ）**。SPA を作らず、サーバー（Go）が UI を制御する。状態駆動レンダリング（操作可能なアクションだけを HTML に出す）を徹底する
- **ホスト GUI は外部から到達できない**。ホスト GUI（トークン発行・承認・設定・キルスイッチ解除）は Wails のアセットハンドラ経由でプロセス内だけで配信し、ソケットを開かない。tailnet に公開するルーターは別にする（セキュリティ上の最重要分離）
- **OS 依存部はインターフェースの背後に置く**（`platform` パッケージ）。コアは OS 非依存でテストできる（[NFR-O03](../requirements/non-functional.md)）
- **純 Go（cgo なし）**。画像は JPEG のみ（配信・サムネイルとも）
- tailnet への参加は**ホストの tailscaled** に任せ、本アプリは LocalAPI クライアントになる
- **内蔵エージェントは「別の操作主体」であり、安全機構は共通**。OpenAI の `computer` ツールが返すアクションを 1 件ずつ、MCP 直接・Web UI と同じ入力層（キルスイッチ → リース → InputGuard → Judge → 操作 → 監査）に通す。モデルが生成したコードをホストで実行する方式（`exec_py` 等）は採らない。ほかに承認を省略できる経路を作らない（[NFR-S15](../requirements/non-functional.md)）
- **高速パス（任意。既定は無効）は「選ぶだけ」の補助**。TypeSafe の Jev は、アプリが UI Automation から作った**不変の候補表から ID を 1 つ選ぶ**だけで、ツール名・座標・引数を生成しない。迷ったら OpenAI の `computer` ツールのループへ引き継ぎ、**タスクの完了宣言は常に OpenAI が行う**。Jev は Judge ではなく、承認を緩めることはない（[FR-G16](../requirements/functional.md)、[NFR-S17](../requirements/non-functional.md)）
- **外部通信は GitHub（更新）、OpenAI（エージェント有効時のみ）、TypeSafe（高速パス有効時のみ）の 3 つだけ**。OpenAI API キーと TypeSafe API キーは Windows 資格情報マネージャーにだけ置く。OpenAI へは常に `store: false` で送る（[FR-G24](../requirements/functional.md)、[NFR-PR03](../requirements/non-functional.md)）

## 技術選定

| 領域 | 技術 | 選定理由・備考 |
|------|------|--------------|
| 言語 | Go | Wails / Tailscale クライアントライブラリと同じ言語。cgo を使わず Windows ビルドを単純化 |
| アプリシェル | Wails v3 | トレイ常駐・複数ウィンドウ・`AssetOptions.Handler`（`http.Handler` を WebView に配信）・`IgnoreMouseEvents` / `AlwaysOnTop` / 透過背景（オーバーレイ）・`ContentProtectionEnabled`（`WDA_EXCLUDEFROMCAPTURE`）・単一インスタンスを標準で持つ。ドキュメント上は beta 表記のため `go.mod` で v3.0.0 に固定する。`wails3 dev` は常に `FRONTEND_DEVSERVER_URL=http://<host>:$WAILS_VITE_PORT`（既定 9245）を設定し、その待受を確認できないと起動に失敗する（ソースで確認済み）。デフォルトの AssetFileServer だけが開発サーバーへプロキシし、カスタム `AssetOptions.Handler`（本アプリの GUI ルーター）には適用されない。開発時は WebView の開始 URL のポートも開発サーバーのポートになる |
| HTTP ルーティング / SSR | Gin | HALT 標準 |
| JSON API | Huma | HALT 標準。Lit アイランドが使う JSON API と OpenAPI を提供 |
| テンプレート | Templ | 型安全な SSR。Atomic Design（atoms / molecules / organisms / pages / layout）で構成 |
| インタラクション | HTMX（`response-targets`、ホスト GUI のみ `sse` 拡張） | サーバー駆動の DOM 更新。CSP（`script-src 'self'` 等）下で動かすため、`allowEval=false`・`includeIndicatorStyles=false` を同梱 JS で設定し、`hx-on` と `js:` 接頭辞は使わない（[NFR-S19](../requirements/non-functional.md)、[フロントエンド設計](frontend.md)） |
| リッチ UI | Lit（Web Components） | ライブ画面ビューア 1 つだけ（`<remo-screen-viewer>`）。Canvas + WebSocket + 入力キャプチャのため |
| スタイル / ビルド | Tailwind CSS / esbuild | HALT 標準。成果物は `go:embed` で単一バイナリに同梱。`wails3 dev` が開発サーバーの待受を要求するため、`dev:frontend` タスクは esbuild の `--serve=$WAILS_VITE_PORT --servedir=static/dist`（ウォッチ付き）を起動する。これは待受の代替とライブ再ビルドのみで、GUI のページは GUI ルーターが配信する。開発時の静的ファイルはディスクから読む |
| MCP | 公式 Go SDK（`github.com/modelcontextprotocol/go-sdk` v1.8.0） | `NewStreamableHTTPHandler` で Streamable HTTP を提供。オプションの扱い（`SessionTimeout` 30 分、`MaxRequestBodyBytes` 1 MiB、`Stateless=false`、`CrossOriginProtection` 不使用など）は「ルーターの分離とリクエスト処理」を参照 |
| tailnet 連携 | `tailscale.com/client/local` | LocalAPI の `WhoIs`、`WatchIPNBus`（IP 変化監視）、`StatusWithoutPeers`。Windows では名前付きパイプ経由でライブラリが接続する |
| WebSocket | `github.com/coder/websocket` v1.8.15（ISC） | context 対応・バイナリフレーム・軽量 |
| 永続化 | SQLite（`modernc.org/sqlite`、純 Go）+ 埋め込み SQL マイグレーション | cgo なし。WAL モード（[NFR-A06](../requirements/non-functional.md)） |
| 画面キャプチャ | DXGI Desktop Duplication（主、`github.com/kirides/go-d3d` v1.0.1・MIT・cgo なし）、GDI BitBlt（フォールバック） | DXGI は変更矩形を OS から取得でき低 CPU。失敗時は GDI へ切り替え。`go-d3d` は M2 のキャプチャスパイクの結果を見て採用を確定し、スパイクが失敗した場合は自前の syscall 実装に切り替える |
| 入力注入 | Win32 `SendInput`（`golang.org/x/sys/windows`） | cgo 不要。`type` は `KEYEVENTF_UNICODE` で日本語を直接送る |
| UI Automation | COM `IUIAutomation`（`github.com/go-ole/go-ole` v1.3.0・MIT。IUIAutomation の vtable バインディングは自前で書く） | `get_ui_tree` と高速パスの候補表（スナップショット）用 |
| 画像エンコード | 標準 `image/jpeg` | 純 Go 制約のため JPEG のみ |
| OpenAI クライアント | `github.com/openai/openai-go/v3` | v3.90.0 で確認済み: `responses.ComputerToolParam`（`{type:"computer"}`）、`ResponseComputerToolCall` の `Actions`（`click` / `double_click` / `drag` / `move` / `scroll` / `keypress` / `type` / `wait` / `screenshot`）と `PendingSafetyChecks`、`ResponseComputerToolCallOutputItem` の `AcknowledgedSafetyChecks`。旧 `computer_use_preview`（`environment: "windows"` 等を指定する旧方式）は使わない |
| API キー保管 | Windows 資格情報マネージャー（`github.com/zalando/go-keyring` v0.2.8・MIT。内部で `danieljoos/wincred` v1.2.3 を使う純 Go で、cgo 不要をソースと `go list` で確認済み） | [NFR-S14](../requirements/non-functional.md)。`platform.SecretStore` の背後に置く。OpenAI キーと TypeSafe キー（ターゲット名 `puppet-remo/typesafe`）は同じ SecretStore に置く |
| 高速パス（Jev による候補選択） | TypeSafe API（`POST https://api.typesafe.ai/v1/systemone`、モデル既定 `jev-1.13`）を `internal/typesafe` から直接呼ぶ。Go SDK は無いため標準 `net/http` | 既定は無効（`agent.fastpath.enabled`）。入力はテキストと JSON のみ（スクリーンショットは送らない）。呼び出しは 5 秒タイムアウト・再試行なし。**Jev は Judge ではない**（承認を緩めない）。Judge は既定のルールベース（自前の `RuleJudge`）のまま。品質は自前のタスクで M4 に測定して確認し、仕様書には性能の主張を書かない（[FR-G16](../requirements/functional.md)） |
| ID 生成 | `github.com/oklog/ulid/v2` v2.1.2（Apache-2.0） | タスク・監査ログなどの ID。時刻順に並ぶ |
| 検討したが採らない | Cua Driver（`trycua/cua`、Rust 製、MIT） | CLI・MCP・型付き SDK 経由で OS 操作を提供するが、本アプリは同一プロセス・純 Go で InputGuard・リース・監査を一貫して制御するため `platform` 層を自前実装する |
| 自動更新 | GitHub Releases + Ed25519 署名検証 | [NFR-S10](../requirements/non-functional.md) |

## システム構成図

```mermaid
graph TD
    subgraph Clients["tailnet 上のクライアント"]
        MCPC["MCP クライアント"]
        BR["ブラウザ"]
    end
    subgraph Cloud["インターネット"]
        OAI["OpenAI API<br/>(Responses / computer ツール)"]
        TSAFE["TypeSafe API<br/>(Jev・高速パス有効時のみ)"]
    end
    subgraph Host["ホスト (Windows) — 単一プロセス puppet-remo"]
        TS["tailscaled"]
        subgraph Net["tailnet 公開面 (tailnet IP のみ Listen)"]
            RR["リモートルーター (Gin)<br/>/mcp /ws /viewer /api/v1"]
            MW["ミドルウェア<br/>Host/Origin 検証 → キルスイッチ → WhoIs → 許可リスト → Bearer(/mcp)"]
        end
        subgraph Local["プロセス内のみ (ソケットなし)"]
            GR["GUI ルーター (Gin)<br/>Wails AssetOptions.Handler"]
            WIN["Wails ウィンドウ<br/>メイン / 承認 / オーバーレイ / トレイ"]
        end
        subgraph Core["コア (service / domain)"]
            AUTHZ["認可 (Authz)"]
            LEASE["リース (LeaseManager)"]
            APPR["承認 (Approval)"]
            KILL["キルスイッチ"]
            AUDIT["監査 (Audit)"]
            TOOLS["操作ツール (Control)"]
            STREAM["画面配信 (Streamer)"]
            AGENT["内蔵エージェント (Agent)<br/>タスク実行ループ"]
            FASTPATH["高速パス (FastPath)<br/>候補表 → Jev → 1 ステップ実行"]
            JUDGE["判定 (Judge)<br/>ルールベース RuleJudge"]
            BUS["イベントバス"]
        end
        subgraph Plat["platform (Windows)"]
            CAP["Capturer<br/>DXGI / GDI"]
            INP["InputInjector + InputGuard"]
            WNDW["WindowManager"]
            UIA["UIAutomation"]
            CLIP["Clipboard"]
            SECRET["SecretStore<br/>(資格情報マネージャー)"]
        end
        DB[("SQLite")]
    end
    MCPC -- "MCP / HTTP" --> RR
    BR -- "HTTP / WebSocket" --> RR
    RR --> MW
    MW -- "WhoIs" --> TS
    MW --> AUTHZ
    RR --> TOOLS
    RR --> AGENT
    AGENT --> JUDGE
    AGENT --> TOOLS
    AGENT --> LEASE
    AGENT --> AUDIT
    AGENT --> SECRET
    AGENT -- "HTTPS (指示・スクリーンショット・履歴)" --> OAI
    AGENT --> FASTPATH
    FASTPATH --> UIA
    FASTPATH --> TOOLS
    FASTPATH --> SECRET
    FASTPATH -- "HTTPS (候補表の JSON のみ・スクリーンショットなし)" --> TSAFE
    GR --> AGENT
    RR --> STREAM
    WIN --> GR
    GR --> LEASE
    GR --> KILL
    GR --> APPR
    TOOLS --> LEASE
    LEASE --> APPR
    TOOLS --> INP
    TOOLS --> WNDW
    TOOLS --> UIA
    TOOLS --> CLIP
    STREAM --> CAP
    LEASE --> BUS
    BUS --> WIN
    TOOLS --> AUDIT
    AUTHZ --> DB
    AUDIT --> DB
    KILL --> DB
```

## レイヤー構造

HALT のレイヤードアーキテクチャを採用し、OS アダプタとプロトコル層を加える。依存は上から下への一方向のみ。逆向きの import は禁止。

```mermaid
graph TD
    H["web/handler・mcp・ws<br/>(プロトコル入口)"]
    T["web テンプレート<br/>atoms / molecules / organisms / pages"]
    S["service<br/>(Authz, Lease, Approval, Control, Agent, FastPath, Judge, Streamer, Audit, Updater)"]
    R["repository<br/>(SQLite)"]
    P["platform<br/>(interface + windows 実装)"]
    TN["tailnet<br/>(LocalAPI クライアント)"]
    OA["openai<br/>(Responses API クライアント)"]
    TY["typesafe<br/>(Chooser: Jev クライアント)"]
    D["domain<br/>(純粋なモデル・ルール)"]
    H --> S
    H --> T
    S --> R
    S --> P
    S --> TN
    S --> D
    R --> D
    P --> D
    TN --> D
    S --> OA
    OA --> D
    S --> TY
    TY --> D
```

| レイヤー | 責務 | 依存してよい先 |
|---------|------|--------------|
| `domain` | クライアント、リース状態機械、承認モード、座標変換、ツール引数、エラー種別、高速パスの候補（`Candidate` / `CandidateTable`: 役割クラスの対応表、除外、ID 導出、選択の検証）などの純粋なモデルとルール | なし |
| `repository` | SQLite へのアクセス（クライアント、トークン、設定、接続要求、監査ログ） | `domain` |
| `platform` | 画面キャプチャ・入力注入・ウィンドウ列挙・UI Automation（`get_ui_tree` と、候補表に使えるスナップショット: 役割 / ControlType・名前・有効・画面外・外接矩形・祖先パス）・クリップボード・資格情報（SecretStore）・オーバーレイ補助のインターフェースと Windows 実装 | `domain` |
| `tailnet` | tailscaled 連携（IP 監視、WhoIs + 10 秒キャッシュ、Listener 管理） | `domain` |
| `openai` | OpenAI Responses API のクライアント（`computer` ツールの要求・応答、`store: false` 固定、タイムアウト、キーの読み出し）。`Model` インターフェースの背後に置き、テストではモックする（[NFR-O06](../requirements/non-functional.md)） | `domain` |
| `typesafe` | TypeSafe（Jev）の HTTP クライアント。境界のインターフェース `Chooser`（`Choose(ctx, ChoiceRequest) (ChoiceResult, error)`）の背後に置き、テストではモックする（[NFR-O06](../requirements/non-functional.md)） | `domain` |
| `service` | ユースケース。リース、承認、操作ツール、内蔵エージェント、高速パス（`FastPath`。Agent の内部）、Judge、画面配信、監査、更新 | `domain`、`repository`、`platform`、`tailnet`、`openai`、`typesafe` |
| `web/handler`、`mcp`、`ws` | HTTP / MCP / WebSocket の入口。`repository` と `platform` を直接使わない | `service`、`domain` |
| `web` テンプレート | Templ（Atomic Design） | `domain`（表示用データ） |

## ルーターの分離とリクエスト処理

| ルーター | 配信経路 | 提供するもの | 認可 |
|---------|---------|-------------|------|
| **GUI ルーター** | Wails `AssetOptions.Handler`（プロセス内。ソケットなし） | ホスト GUI（ダッシュボード、接続要求、クライアント管理、承認ダイアログ、タスク、エージェント設定（API キー入力）、監査ログ、設定、更新）、`/events`（SSE） | プロセス内到達のみ。ネットワークからは到達不能 |
| **リモートルーター** | tailnet IP の TCP ポート（FR-S01、既定 8765） | `/mcp`、`/viewer`（Web UI）、`/ws/screen`、`/api/v1/*`、静的ファイル | ミドルウェア連鎖（下図） |

2 つのルーターは `templ` コンポーネント（atoms / molecules）を共有できるが、ハンドラは共有しない（ホスト専用操作がリモートに露出しないことを構造で保証する）。

```mermaid
sequenceDiagram
    participant C as クライアント
    participant N as リモートルーター
    participant TS as tailscaled
    participant A as Authz
    C->>N: HTTP / WS リクエスト
    N->>N: Host ヘッダー検証 (NFR-S02)
    N->>N: Origin 検証 / X-Puppet-Remo (NFR-S03)
    alt キルスイッチ発動中
        N-->>C: 503 kill_switch_active (接続要求は作らない)
    else 発動していない
        N->>TS: WhoIs (キャッシュ 10 秒)
        TS-->>N: ユーザー + node ID
        N->>A: 許可リスト照合
        alt 未登録
            A-->>N: 未登録
            N-->>C: 403 (接続要求を作成 / 集約、レート制限)
        else 登録済み
            alt /mcp
                N->>A: Bearer 照合 (NFR-S04: ハッシュの定数時間比較・node 束縛)
            end
            N-->>C: 業務ハンドラへ
        end
    end
```

`/mcp` の手前には、本アプリ自前の Host / Origin 検証ミドルウェア（NFR-S02）を必ず置く。MCP Go SDK v1.8.0 の `StreamableHTTPOptions` は次のとおり扱う（ソースで確認済み）。

| オプション | 方針 | 理由 |
|-----------|------|------|
| `DisableLocalhostProtection` | SDK の保護に依存しない | ループバックのアドレスで届いたリクエストにだけ働く。tailnet IP で Listen する本アプリでは無関係で、保護は自前の Host / Origin ミドルウェア（NFR-S02）が担う |
| `CrossOriginProtection` | 使わない | 非推奨のため |
| `SessionTimeout` | 30 分（アイドル）を明示 | 既定の 0 は「セッションを閉じない」。リースのアイドル解放（FR-S04、既定 5 分）とは独立に設定する |
| `EventStore` | v1 では使わない | ストリームの再開は提供せず、クライアントは再 initialize する |
| `MaxRequestBodyBytes` | 1 MiB を明示 | [NFR-S09](../requirements/non-functional.md) |
| `Stateless` | `false` | リース保持者をセッションに束縛するため |
| `JSONResponse` | 既定のまま | — |

## 主要フロー

### MCP ツール呼び出し（リース取得と承認を含む）

```mermaid
sequenceDiagram
    participant C as MCP クライアント
    participant M as /mcp (MCP サーバー)
    participant L as LeaseManager
    participant P as Approval
    participant G as ホスト GUI 承認ダイアログ
    participant I as InputGuard / Injector
    participant AU as Audit
    C->>M: tools/call left_click(x, y)
    M->>L: Ensure(session) (リース未保持)
    alt 他者が保持
        L-->>M: lease_held_by_other
        M-->>C: エラー
    else 取得可能
        alt 承認が必要 (M3 から。M1 は承認なしで idle → held)
            L->>P: Request(client)
            P->>G: 承認ダイアログを開く (60 秒)
            G-->>P: 許可 / 拒否 / タイムアウト
            P-->>L: 結果
        end
        L-->>M: held
        M->>I: 縮小座標 → 物理座標、InputGuard 検査
        I-->>M: 実行 or input_blocked
        M->>AU: 記録 (引数・結果・サムネイル)
        M-->>C: 結果
    end
```

### 内蔵エージェントのタスク実行

```mermaid
sequenceDiagram
    participant C as MCP クライアント
    participant M as /mcp
    participant A as Agent (TaskRunner)
    participant L as LeaseManager
    participant P as Approval / ホスト GUI
    participant J as Judge
    participant O as OpenAI API
    participant I as InputGuard / Injector
    participant AU as Audit
    C->>M: tools/call run_task(prompt)
    M->>A: Start(client, prompt)
    alt 無効 / キー未登録 / クライアント未許可
        A-->>M: agent_disabled
    else 実行可能
        A->>L: Acquire(kind=agent) (承認モードを適用)
        L->>P: 承認が必要なら 60 秒で確認
        P-->>L: 許可 / 拒否 / タイムアウト
        A-->>M: task_id, status (即時)
        M-->>C: task_id
        opt 高速パスが有効でキー・同意あり (FR-G22)
            A->>A: 高速パスのループ (下のフローチャート)。エスカレーションで OpenAI のループへ
        end
        loop ステップ上限・時間上限まで
            A->>O: Responses(computer ツール, 指示 / 画面)
            O-->>A: computer_call (actions[], pending_safety_checks)
            opt pending_safety_checks がある
                A->>P: 安全チェックの内容を表示 (60 秒)
                P-->>A: 許可 / 拒否 (拒否は failed)
            end
            loop actions[] を順に
                A->>J: Assess(action, 対象ウィンドウ)
                opt require_approval
                    J->>P: 個別承認 (60 秒)
                    P-->>J: 許可 / 拒否
                end
                A->>I: 座標変換 → InputGuard → 実行
                I-->>A: ok / input_blocked (ブロックは failed)
                A->>AU: アクション・判定・サムネイル
            end
            A->>O: computer_call_output (新しい画面, acknowledged_safety_checks)
        end
        A->>L: Release
    end
    C->>M: tools/call get_task(task_id)
    M-->>C: 状態・ステップ数・最終回答
```

- **ループの 1 ステップ**は、OpenAI ループでは Responses API の呼び出し 1 回、高速パスでは Jev 1 回 + アクション 1 件。`computer_call` が返らなくなったら最終回答として `completed` にする（[FR-G04](../requirements/functional.md)）。監査の各ステップに `route`（`fast` / `openai`）を記録する
- **画面**は MCP の `screenshot` と同じキャプチャを使い、FR-M16 の縮小規則で縮小した PNG を `detail: "original"` で送る。モデルが返す座標は縮小座標として物理座標に戻す（OpenAI のガイドが求める「縮小した場合はモデルの座標を元の座標空間に戻す」に一致）
- **すべてのアクションを 1 件ずつ検査**する。ブロック・拒否が 1 回でも起きたら、残りの `actions[]` は実行せずタスクを `failed` にする（FR-G07）
- **中断**は、実行中の OpenAI 呼び出しの `context` を取り消し、押下中のキー・ボタンを解放し、リースを解放する。キルスイッチ・リース剥奪・`cancel_task` はすべて同じ経路（FR-G10）
- **状態の永続化**: タスクは状態遷移のたびに `agent_tasks` を更新する。ステップは監査ログの行として `task_id` と連番付きで保存する（[ER](er.md)）。実行状態（ループ・リース）はメモリのみで、再起動時に `running` / `pending_approval` のタスクを `aborted` にする（NFR-A08）
- **会話の継続はステートレス**（[FR-G24](../requirements/functional.md)）: すべての Responses API 呼び出しで `store: false` を送り、`previous_response_id` は使わない。毎回、指示・これまでの出力アイテム（原文のまま）・`computer_call_output` アイテムを含む履歴を再送する。スクリーンショットは直近 `agent.keep_screenshots` 枚（既定 3、FR-S19）だけを残し、古いものはテキストのプレースホルダに置き換える。`reasoning` の暗号化コンテンツなど `include` に必要なものは、実装時のスモークテストで実 API に対して確認する
- **OpenAI 側のデータ保持**（OpenAI の文書による）: API のデータは既定で学習に使われない。不正利用監視のログは最大 30 日保持される（ゼロデータ保持の承認があれば除く）。`store` を省略または `true` にするとアプリの状態が 30 日以上保持される。画像入力は CSAM のスキャン対象で、ゼロデータ保持でも審査のために保持されうる（[NFR-PR03](../requirements/non-functional.md)）
- **スクリーンショットの大きさ**: 最大辺の既定は 1568 px（FR-S03）。16:9 では 1568×882 = 49×28 = 1,372 パッチで、文書上の拒否上限 30,000 パッチと、リサイズの予算（10,000 / 2,500 パッチ）を大きく下回る。PNG を `detail: "original"` で送る。測定に基づく調整は設定で行える

### 高速パス（Jev による 1 ステップずつの候補選択）

任意機能（`agent.fastpath.enabled`、既定 false）。タスクの先頭で、Jev が候補表から 1 件ずつ選んで実行する。**迷ったら OpenAI のループへ引き継ぎ、一度引き継いだらタスクの終了まで OpenAI のまま**（v1 では戻さない）。完了の宣言は常に OpenAI が、確認用のスクリーンショット 1 枚を見たうえで行う（[FR-G16](../requirements/functional.md)〜[FR-G23](../requirements/functional.md)）。

```mermaid
flowchart TD
    START["タスク開始 (リース取得後)"] --> EN{"高速パス有効かつ<br/>キー・同意あり?"}
    EN -- "いいえ (黙ってスキップ)" --> OAI
    EN -- "はい" --> SNAP["UI Automation スナップショット<br/>→ 候補表を構築 (domain.CandidateTable)"]
    SNAP --> EMPTY{"候補あり?"}
    EMPTY -- "なし: no_candidates" --> ESC
    EMPTY -- "あり" --> CALL["Jev に問い合わせ (Choice next + Noul goal_maybe_done)<br/>5 秒・再試行なし"]
    CALL -- "タイムアウト / ネットワーク / 5xx / 429: chooser_unavailable" --> ESC
    CALL --> CHK{"応答の検証"}
    CHK -- "表にない・重複・不正・古い ID: invalid_choice" --> ESC
    CHK -- "confidence が min_confidence 未満: low_confidence" --> ESC
    CHK -- "abstain" --> ESC
    CHK -- "goal_maybe_done が 0.5 以上: goal_maybe_done" --> ESC
    CHK -- "reobserve" --> RE{"連続 2 回以内?"}
    RE -- "はい" --> SNAP
    RE -- "いいえ: abstain" --> ESC
    CHK -- "候補 ID" --> ACT["1 アクションを実行<br/>キルスイッチ → リース → InputGuard → RuleJudge → Control → 監査"]
    ACT -- "ブロック・拒否" --> FAIL["タスクを failed にする"]
    ACT --> OBS["再観測 (UIA ツリーのハッシュ・フォアグラウンドウィンドウ)"]
    OBS --> PROG{"進捗あり?"}
    PROG -- "3 回連続でなし: no_progress" --> ESC
    PROG -- "あり" --> LIM{"高速ステップが max_steps 到達?"}
    LIM -- "はい: fast_step_limit" --> ESC
    LIM -- "いいえ" --> SNAP
    ESC["エスカレーション (handoff_reason を記録)"] --> OAI["OpenAI の computer ツールのループ<br/>最初のリクエスト: 指示 + 高速ステップの要約 + 新しいスクリーンショット"]
    OAI --> DONE["完了は OpenAI のみが宣言<br/>(確認用スクリーンショット 1 枚)"]
```

- **候補表**は `domain` の純粋なロジック（[FR-G17](../requirements/functional.md)）。フォアグラウンドウィンドウの UI Automation スナップショット（フォーカス移動は `list_windows`）から作る。v1 の候補は UIA 要素の `click`（外接矩形の中心をアプリが計算）と `focus_window`、メタ候補の `reobserve` / `abstain`。`type` / `key` / `scroll` は高速パスに載せず、必要になった時点で OpenAI へ引き継ぐ。候補は有効・画面内・ラベルが空でないネイティブ要素に限り、最大 24 件。ID は（役割クラス + 正規化したラベル + 操作可能な祖先パス）のハッシュで、要素の添字やランタイム ID は使わない。`agent.fastpath.excluded_labels`（FR-S18。既定: delete、send、purchase、close、削除、送信 など）に一致するラベルは除外する
- **Jev に送るもの**: 指示、ウィンドウのタイトルとプロセス名、候補（ID・役割・ラベル・有効）、直近 5 件の高速ステップの短い文。**送らないもの**: スクリーンショット、要素の値、クリップボード、入力テキストの本文、API キー（[FR-G18](../requirements/functional.md)、[NFR-PR04](../requirements/non-functional.md)）。Jev の回答は構築時の不変の表と照合し、一致しなければ採らない
- **共通の入力層**: 高速パスの 1 アクションも、MCP・OpenAI の経路と同じ層を通る。Judge は Jev の回答を見ず、RuleJudge が判定する。高速ステップも全体の `agent.max_steps`（50）と `agent.max_duration_sec`（600）に数える
- **縮退**（[FR-G21](../requirements/functional.md)）: 高速パスの失敗でタスクは失敗にならない。TypeSafe 呼び出しの失敗（タイムアウト・ネットワーク・5xx・429）は、そのステップを `chooser_unavailable` で OpenAI へ引き継ぐ（引き継いだタスクは最後まで OpenAI で続ける）。HTTP 401 は GUI に通知し、`agent.fastpath.enabled` を false にする（キー無効）
- **引き継ぎ理由**（`handoff_reason`）: `abstain` / `low_confidence` / `no_candidates` / `invalid_choice` / `no_progress` / `goal_maybe_done` / `fast_step_limit` / `chooser_unavailable`。引き継ぎが起きなかった場合（高速パス無効、または高速パス中にキャンセル・失敗で終了）は NULL。`agent_tasks` に `fast_steps` / `openai_steps` / `handoff_reason` を持ち、`get_task` と監査（ステップの `route`、候補数、除外数、選択 ID、confidence、goal_maybe_done の確率）に出す。入力テキストと要素の値は記録しない（[FR-G23](../requirements/functional.md)、[ER](er.md)）
- **評価**: 高速パスの品質の根拠は、Cua の小規模な自己申告テスト（8/9 対 9/9、判断の中央値が 13.6 倍速い）しかない。M4 で、固定したタスク集合を使って自前で測定し、結果が出るまで仕様に性能の主張を書かない。Jev のプロンプトインジェクション検知は一切あてにしない。敵対的なコンテンツへの弱さ（Jev の jaggedness の文書）は、固定の候補表、RuleJudge、InputGuard、ステップ上限で緩和する

### Web UI のライブ表示と手動操作

```mermaid
sequenceDiagram
    participant B as ブラウザ
    participant R as リモートルーター
    participant V as Lit remo-screen-viewer
    participant S as Streamer
    participant L as LeaseManager
    B->>R: GET /viewer (HTMX フルページ)
    R-->>B: Templ レンダリング (リース状態に応じたボタンのみ)
    B->>V: アイランドを初期化 (ws-url, 権限を属性で注入)
    V->>R: WebSocket /ws/screen
    R->>S: 閲覧者として登録 (モニター・品質・FPS)
    S-->>V: キーフレーム + 差分フレーム (JPEG タイル)
    B->>R: POST /viewer/lease/acquire (HTMX)
    R->>L: Acquire(web session)
    L-->>R: held
    R-->>B: リースパネルのフラグメント
    V->>R: 入力イベント (WS: マウス・キー)
    R->>L: 保持者検証 → InputGuard → Injector
    L-->>V: lease_changed (WS) → CustomEvent → HTMX がリースパネルを再取得
```

### 接続要求（未登録デバイス）

```mermaid
sequenceDiagram
    participant D as 未登録デバイス
    participant R as リモートルーター
    participant DB as SQLite
    participant U as ホスト GUI
    D->>R: 任意のリクエスト
    R-->>D: 403 (Web UI は「接続要求を送信しました」画面)
    R->>DB: 接続要求を upsert (device 単位に集約)
    R->>U: イベント (トレイ通知 + 接続要求画面)
    U->>DB: 許可: クライアント登録 (M3 から承認モード選択。M1 は既定 always で保存) / 拒否: 破棄
```

### キルスイッチ

発動経路（トレイ、ダッシュボード、グローバルホットキー）はいずれも `KillSwitch.Trigger()` を呼ぶ。処理順は、(1) 状態を `active` に永続化、(2) 全リース剥奪、(3) 実行中のエージェントのタスクを中断（OpenAI・TypeSafe 呼び出しの取り消し、押下中入力の解放）、(4) リモートルーターのリクエストを 503 `kill_switch_active` で拒否、(5) 全 MCP・WebSocket セッションを切断、(6) オーバーレイ消去、(7) 監査記録。順序は NFR-P05（1 秒以内）を満たすため同期で行い、永続化は先頭に置いて再起動後も維持する（[NFR-S07](../requirements/non-functional.md)）。

## 画面配信パイプライン

```mermaid
graph TD
    CAP["Capturer<br/>DXGI Desktop Duplication / GDI"] --> DIFF["差分検出<br/>DXGI 変更矩形 + タイルハッシュ"]
    DIFF --> ENC["エンコード<br/>JPEG (標準 image/jpeg)<br/>プロファイル単位で共有"]
    ENC --> FAN["ファンアウト<br/>閲覧者ごとの送信キュー"]
    FAN --> WS["WebSocket バイナリフレーム"]
    CAP --> SS["screenshot ツール<br/>縮小 → PNG/JPEG"]
```

- キャプチャ・差分検出は**モニターごとに 1 つの goroutine**。閲覧者がゼロなら停止する（FR-W09、NFR-P04）
- 差分は 64×64 px のタイル単位。変化したタイルを矩形にまとめて JPEG で送る。接続直後と一定間隔（既定 5 秒）で全面キーフレームを送る
- エンコードは (モニター, 品質, FPS) のプロファイル単位に 1 回だけ行い、同じプロファイルの閲覧者で共有する（NFR-P07）。閲覧者ごとの送信キューが詰まったら古いフレームを捨てる（遅延の累積を防ぐ）
- DXGI は `ACCESS_LOST`（ロック画面・UAC・解像度変更など）で再初期化する。復旧できなければ `desktop_unavailable` とし、GDI にフォールバックする
- `screenshot` ツールは配信とは独立に 1 回だけ撮り、縮小（FR-M16 の縮小率）して返す

## 入力注入と自己保護

- **座標変換**: 縮小座標 → `round(x ÷ 縮小率)` → モニター原点を加算 → 仮想デスクトップ座標。`SendInput` は `MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_VIRTUALDESK` で 0〜65535 に正規化して送る。プロセスは Per-Monitor DPI Aware V2 のマニフェストを持つ
- **`InputGuard`**（すべての注入の前に必ず通る。[NFR-S06](../requirements/non-functional.md)）:
  1. 注入先の座標にあるトップレベルウィンドウ、またはフォアグラウンドウィンドウが**自プロセスのもの**ならブロック（`input_blocked`）
  2. 管理者権限ウィンドウ（UIPI）が対象なら `input_blocked`
  3. キルスイッチ中・リース非保持なら拒否
  4. エージェントの入力は、上記に加えて Judge（次節）を通す。InputGuard はすべての保持者種別（`mcp` / `web` / `agent`）に同じ規則を適用する
- **自ウィンドウのキャプチャ除外**: ホスト GUI・承認ダイアログ・オーバーレイは Wails の `ContentProtectionEnabled`（`WDA_EXCLUDEFROMCAPTURE`）を有効にする。AI のスクリーンショットにも Web UI のライブ画面にも写らない
- **オーバーレイ**: モニターごとに 1 ウィンドウ。枠なし・透過背景・最前面・`IgnoreMouseEvents`（クリック透過）・タスクバー非表示。リースの保持・解放イベントで表示・消去する
- **承認ダイアログ**: 承認要求ごとに専用の Wails ウィンドウを開く（Go 側でウィンドウ生成）。内容は GUI ルーターが Templ で描画し、「許可 / 拒否」は HTMX の `hx-post` で GUI ルーターへ送る。操作は入力ガードにより AI から押せない

## Judge（判定インターフェース）

エージェントの各アクションの直前に、実行してよいかを助言させるインターフェース。

```go
type Verdict int // Allow < RequireApproval < Deny の順に厳しい

type Judge interface {
    Assess(ctx context.Context, a Action, w WindowInfo) (Verdict, error)
}
```

- **合成**: 複数の Judge は `Chain` で束ね、**最も厳しい判定**を採る。したがって後から差し込む Judge が `Allow` を返しても、他の判定を緩めることはできない（引き上げ専用）
- **fail-closed**: `error`・タイムアウト・低 confidence は `RequireApproval` に変換する。外部に到達できないときに「許可」にしない
- **既定の `RuleJudge`**: 対象ウィンドウ（InputGuard が解決したウィンドウ）のプロセス名が `agent.deny_processes` に一致すれば `Deny`、`agent.approve_processes` に一致すれば `RequireApproval`、それ以外は `Allow`（FR-G12、FR-S13）
- **Jev は Judge ではない**: Jev（TypeSafe）は高速パス（「主要フロー」の「高速パス」節）の**候補選択**にだけ使い、アクションの可否は判定しない。Judge は既定の `RuleJudge`（ルールベース）のまま。Jev の回答は Judge を通らずに実行されることはなく、Jev が承認を減らすこともない（[NFR-S17](../requirements/non-functional.md)）。外部モデルを Judge として足す案は採らない

## HALT の適用

フロントエンドは HALT で構成する。規約・ディレクトリ・HTMX パターン・Lit コンポーネント・ビルド・テストの詳細は [フロントエンド設計](frontend.md) に集約する。ここでは全体構成との接点だけを示す。

- 画面の大半（ホスト GUI、ビューアの周辺 UI、リースパネル）は **Templ + HTMX** で描画する。ページ・アクションルートは常に HTML を返し、JSON は `/api/v1` と MCP だけが返す（[API 仕様](../api/endpoints.md) §1）
- **Lit は `<remo-screen-viewer>` のみ**（Canvas 描画、WebSocket のバイナリフレーム、入力の捕捉）
- ホスト GUI は GUI ルーター（プロセス内）、Web UI はリモートルーター（tailnet）で配信し、ハンドラを共有しない

### HALT 標準からの差分（要約）

| 項目 | 本プロジェクト |
|------|--------------|
| 認証・CSRF | Cookie を使わない。WhoIs + Host / Origin 検証 + `X-Puppet-Remo` |
| サーバー | GUI ルーターとリモートルーターに分離 |
| HTMX 拡張 | `response-targets`（両 UI）、`sse`（ホスト GUI のみ） |
| JSON API | `/api/v1` は読み取り専用。アクションは HTML を返す HTMX ルート |
| 開発 | `wails3 dev` + `templ generate --watch` + esbuild / Tailwind のウォッチ（`air` は使わない）。`wails3 dev` が開発サーバーの待受を要求するため、esbuild の `--serve` を待受として起動する |
| CSP | すべての HTML レスポンスに、ミドルウェアから `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'` を送る（meta タグは使わない）。インラインのスクリプト・スタイル・イベント属性は置かない。両ルーターで共有するテンプレートに適用する（[NFR-S19](../requirements/non-functional.md)） |
| 静的ファイル | `Cache-Control: no-cache` + `ETag`（ビルド ID） |
| テスト | Go `httptest`、Templ のレンダリング、Lit は `@open-wc/testing`、E2E は Playwright（偽の platform。Windows ジョブとは別の ubuntu ジョブ） |

全項目と理由は [フロントエンド設計](frontend.md) §2 を参照。

## ディレクトリ構成

```
puppet-remo/
├── cmd/puppet-remo/              # エントリーポイント (Wails アプリ起動、DI)
├── internal/
│   ├── domain/                   # クライアント、リース、承認、座標、エラー、高速パスの候補 (Candidate / CandidateTable)
│   ├── repository/               # SQLite (clients, tokens, settings, ...)
│   │   └── migrations/           # 埋め込み SQL マイグレーション
│   ├── platform/                 # interface 定義
│   │   └── windows/              # capture(DXGI/GDI), input, window, uia, clipboard, secret(資格情報)
│   ├── tailnet/                  # LocalAPI クライアント、Watcher、Listener 管理
│   ├── openai/                   # Responses API クライアント (Model インターフェース + 実装、モック)
│   ├── typesafe/                 # TypeSafe (Jev) HTTP クライアント (Chooser インターフェース + 実装、モック)
│   ├── service/                  # authz, lease, approval, control, agent (fastpath を含む), judge, streamer, audit, updater
│   ├── mcp/                      # MCP サーバー (ツール定義と service 呼び出し)
│   ├── ws/                       # WebSocket プロトコル (画面フレーム・入力)
│   ├── router/                   # gui.go, remote.go
│   ├── web/                      # 詳細は frontend.md §3
│   │   ├── view/ handler/{gui,remote}/ middleware/
│   │   ├── atoms/ molecules/ organisms/{gui,remote}/ pages/{gui,remote}/ layout/
│   └── app/                      # Wails ウィンドウ・トレイ・オーバーレイ・ホットキー
├── static/
│   ├── package.json              # esbuild, tailwind, lit, htmx, テスト
│   ├── src/
│   │   ├── components/
│   │   │   ├── screen-viewer/    # <remo-screen-viewer>
│   │   │   └── lib/              # ws.ts, logger.ts
│   │   └── css/
│   ├── dist/                     # ビルド成果物 (go:embed)
│   └── e2e/                      # Playwright
├── build/                        # Wails パッケージング (NSIS、マニフェスト)
├── docs/
└── Taskfile.yml                  # templ / tailwind / esbuild (dev は --serve) / wails3 package
```

## ビルドと配布

| 段階 | 内容 |
|------|------|
| コード生成 | `templ generate` |
| フロントエンド | Tailwind CSS ビルド、esbuild（Lit コンポーネント）、`htmx.min.js` のコピー → `static/dist/` |
| Go ビルド | 純 Go（cgo なし）。Per-Monitor DPI Aware V2 のマニフェストを埋め込む |
| パッケージ | `wails3 package` による NSIS インストーラ（WebView2 ブートストラップ込み） |
| 配布・更新 | GitHub Releases。インストーラと署名ファイル（Ed25519）を公開し、アプリが署名検証して適用 |
| CI | GitHub Actions。`windows-latest` のジョブで templ 生成の差分チェック、フロントエンドビルド、build / vet / lint / go test、Lit のテストを実行する。Playwright の E2E は別の `ubuntu-latest` ジョブで実行する（偽の platform と偽の tailnet により OS に依存せず、Chromium を容易に使えるため。実機のスモークテストは手動）（[フロントエンド設計](frontend.md) §9） |
| 署名 | v1 では Authenticode 署名をしない（個人利用）。README に SmartScreen の回避手順を書く。更新の完全性は Ed25519 署名検証（NFR-S10）で担保する（[NFR-O07](../requirements/non-functional.md)） |

## 改訂履歴

| 版 | 日付 | 変更内容 | 変更理由 |
|---|------|---------|---------|
| 1.0 | 2026-10-28 | 初版作成 | — |
| 1.1 | 2026-10-29 | 内蔵エージェント（OpenAI `computer` ツール）を追加: 構成図・レイヤー（`openai`、`Agent`、`Judge`、`SecretStore`）、タスク実行フロー、キルスイッチの手順、Judge インターフェース、技術選定（openai-go v3、資格情報）、MCP SDK の保護に関する注記。MCP フローのツール名を `left_click` に更新 | ホスト内蔵エージェントを主役にする決定 |
| 1.2 | 2026-10-29 | 「HALT の適用」を [フロントエンド設計](frontend.md) へ分離し、差分の要約とリンクに縮小。ディレクトリ構成を更新（`lib/api.ts` を初版から除外、`e2e/` 追加）、CI にフロントエンドのテストを追加 | HALT の規約を 1 か所に集約 |
| 1.3 | 2026-10-29 | 未決事項の決定を反映: 外部通信に TypeSafe（高速パス有効時のみ）を追加、高速パス（Jev による候補選択、引き継ぎ理由、縮退）のフローとコンポーネント（`typesafe`、`FastPath`、`domain.Candidate`）を追加、Judge は RuleJudge のまま Jev を Judge にしない旨を明記、OpenAI の会話継続を `store: false` のステートレス方式に確定、技術選定のライブラリ・バージョン・ライセンス確定（go-keyring、go-ole、go-d3d、coder/websocket、ulid、MCP SDK の設定）、Wails dev の待受（esbuild `--serve`）、CSP、CI（Playwright は別の ubuntu ジョブ）、署名なしを追記 | 未決事項の決定 |
| 1.4 | 2026-10-29 | PR #2 のレビュー対応: ミドルウェア連鎖でキルスイッチを WhoIs・許可リスト・接続要求の作成より前に移動（構成図・シーケンス図）。Bearer の照合を NFR-S04 の参照に統一。承認が必要な分岐と接続要求の許可時の承認モード選択は M3 からと明記（M1 は承認なしの `idle → held`） | M1 の実装者が迷わない仕様にするためのレビュー指摘の解消 |
