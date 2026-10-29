# アーキテクチャ設計

要件は [機能要件](../requirements/functional.md) と [非機能要件](../requirements/non-functional.md)、全体像は [プロジェクト概要](../overview.md) を参照。対象 OS は Windows のみ。

## 設計方針

- **単一の Go プロセス**がすべてを担う。ホスト GUI・Web UI・MCP・内蔵エージェント・画面キャプチャ・入力注入・永続化を 1 つの Wails アプリに内蔵する
- **フロントエンドは HALT（HTMX + Atomic Design + Lit + Templ）**。SPA を作らず、サーバー（Go）が UI を制御する。状態駆動レンダリング（操作可能なアクションだけを HTML に出す）を徹底する
- **ホスト GUI は外部から到達できない**。ホスト GUI（トークン発行・承認・設定・キルスイッチ解除）は Wails のアセットハンドラ経由でプロセス内だけで配信し、ソケットを開かない。tailnet に公開するルーターは別にする（セキュリティ上の最重要分離）
- **OS 依存部はインターフェースの背後に置く**（`platform` パッケージ）。コアは OS 非依存でテストできる（[NFR-O03](../requirements/non-functional.md)）
- **純 Go（cgo なし）**。画像は JPEG のみ（配信・サムネイルとも）
- tailnet への参加は**ホストの tailscaled** に任せ、本アプリは LocalAPI クライアントになる
- **内蔵エージェントは「別の操作主体」であり、安全機構は共通**。OpenAI の `computer` ツールが返すアクションを 1 件ずつ、MCP 直接・Web UI と同じ入力層（InputGuard → リース → キルスイッチ → 監査）に通す。モデルが生成したコードをホストで実行する方式（`exec_py` 等）は採らない。ほかに承認を省略できる経路を作らない（[NFR-S15](../requirements/non-functional.md)）
- **外部通信は GitHub（更新）と OpenAI（エージェント有効時のみ）**。OpenAI API キーは Windows 資格情報マネージャーにだけ置く

## 技術選定

| 領域 | 技術 | 選定理由・備考 |
|------|------|--------------|
| 言語 | Go | Wails / Tailscale クライアントライブラリと同じ言語。cgo を使わず Windows ビルドを単純化 |
| アプリシェル | Wails v3 | トレイ常駐・複数ウィンドウ・`AssetOptions.Handler`（`http.Handler` を WebView に配信）・`IgnoreMouseEvents` / `AlwaysOnTop` / 透過背景（オーバーレイ）・`ContentProtectionEnabled`（`WDA_EXCLUDEFROMCAPTURE`）・単一インスタンスを標準で持つ。ドキュメント上は beta 表記のため、実装時にバージョンを固定する（`go.mod` は v3.0.0 タグを確認済み） |
| HTTP ルーティング / SSR | Gin | HALT 標準 |
| JSON API | Huma | HALT 標準。Lit アイランドが使う JSON API と OpenAPI を提供 |
| テンプレート | Templ | 型安全な SSR。Atomic Design（atoms / molecules / organisms / pages / layout）で構成 |
| インタラクション | HTMX（`response-targets`、ホスト GUI のみ `sse` 拡張） | サーバー駆動の DOM 更新 |
| リッチ UI | Lit（Web Components） | ライブ画面ビューア 1 つだけ（`<remo-screen-viewer>`）。Canvas + WebSocket + 入力キャプチャのため |
| スタイル / ビルド | Tailwind CSS / esbuild | HALT 標準。成果物は `go:embed` で単一バイナリに同梱 |
| MCP | 公式 Go SDK（`github.com/modelcontextprotocol/go-sdk`） | `NewStreamableHTTPHandler` で Streamable HTTP を提供 |
| tailnet 連携 | `tailscale.com/client/local` | LocalAPI の `WhoIs`、`WatchIPNBus`（IP 変化監視）、`StatusWithoutPeers`。Windows では名前付きパイプ経由でライブラリが接続する |
| WebSocket | `github.com/coder/websocket` | context 対応・バイナリフレーム・軽量（候補。実装時に確定） |
| 永続化 | SQLite（`modernc.org/sqlite`、純 Go）+ 埋め込み SQL マイグレーション | cgo なし。WAL モード（[NFR-A06](../requirements/non-functional.md)） |
| 画面キャプチャ | DXGI Desktop Duplication（主）、GDI BitBlt（フォールバック） | DXGI は変更矩形を OS から取得でき低 CPU。失敗時は GDI へ切り替え |
| 入力注入 | Win32 `SendInput`（`golang.org/x/sys/windows`） | cgo 不要。`type` は `KEYEVENTF_UNICODE` で日本語を直接送る |
| UI Automation | COM `IUIAutomation`（`go-ole` 等） | `get_ui_tree` 用 |
| 画像エンコード | 標準 `image/jpeg` | 純 Go 制約のため JPEG のみ |
| OpenAI クライアント | `github.com/openai/openai-go/v3` | v3.90.0 で確認済み: `responses.ComputerToolParam`（`{type:"computer"}`）、`ResponseComputerToolCall` の `Actions`（`click` / `double_click` / `drag` / `move` / `scroll` / `keypress` / `type` / `wait` / `screenshot`）と `PendingSafetyChecks`、`ResponseComputerToolCallOutputItem` の `AcknowledgedSafetyChecks`。旧 `computer_use_preview`（`environment: "windows"` 等を指定する旧方式）は使わない |
| API キー保管 | Windows 資格情報マネージャー（`github.com/zalando/go-keyring` を候補。v0.2.8 を確認。cgo 不要かは実装時に確認） | [NFR-S14](../requirements/non-functional.md)。`platform.SecretStore` の背後に置く |
| Judge（判定補助） | 既定はルールベース（自前） | 外部モデル（Jev 等）は未決。TypeSafe の API に Go SDK は無く、使う場合は HTTP（`POST https://api.typesafe.ai/v1/systemone`）を直接呼ぶ |
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
    end
    subgraph Host["ホスト (Windows) — 単一プロセス puppet-remo"]
        TS["tailscaled"]
        subgraph Net["tailnet 公開面 (tailnet IP のみ Listen)"]
            RR["リモートルーター (Gin)<br/>/mcp /ws /viewer /api/v1"]
            MW["ミドルウェア<br/>Host/Origin 検証 → WhoIs → 許可リスト → Bearer(/mcp)"]
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
            JUDGE["判定 (Judge)<br/>ルールベース + 差し込み可"]
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
    S["service<br/>(Authz, Lease, Approval, Control, Agent, Judge, Streamer, Audit, Updater)"]
    R["repository<br/>(SQLite)"]
    P["platform<br/>(interface + windows 実装)"]
    TN["tailnet<br/>(LocalAPI クライアント)"]
    OA["openai<br/>(Responses API クライアント)"]
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
```

| レイヤー | 責務 | 依存してよい先 |
|---------|------|--------------|
| `domain` | クライアント、リース状態機械、承認モード、座標変換、ツール引数、エラー種別などの純粋なモデルとルール | なし |
| `repository` | SQLite へのアクセス（クライアント、トークン、設定、接続要求、監査ログ） | `domain` |
| `platform` | 画面キャプチャ・入力注入・ウィンドウ列挙・UI Automation・クリップボード・資格情報（SecretStore）・オーバーレイ補助のインターフェースと Windows 実装 | `domain` |
| `tailnet` | tailscaled 連携（IP 監視、WhoIs + 10 秒キャッシュ、Listener 管理） | `domain` |
| `openai` | OpenAI Responses API のクライアント（`computer` ツールの要求・応答、タイムアウト、キーの読み出し）。`Model` インターフェースの背後に置き、テストではモックする（[NFR-O06](../requirements/non-functional.md)） | `domain` |
| `service` | ユースケース。リース、承認、操作ツール、内蔵エージェント、Judge、画面配信、監査、更新 | `domain`、`repository`、`platform`、`tailnet`、`openai` |
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
    N->>TS: WhoIs (キャッシュ 10 秒)
    TS-->>N: ユーザー + node ID
    N->>A: 許可リスト照合
    alt 未登録
        A-->>N: 未登録
        N-->>C: 403 (接続要求を作成 / 集約、レート制限)
    else 登録済み
        alt /mcp
            N->>A: Bearer 照合 (ハッシュ・定数時間・node 束縛)
        end
        N->>N: キルスイッチ確認
        N-->>C: 業務ハンドラへ
    end
```

`/mcp` の手前には、本アプリ自前の Host / Origin 検証ミドルウェア（NFR-S02）を必ず置く。MCP Go SDK の `StreamableHTTPOptions` には localhost 向けの保護（`DisableLocalhostProtection`、`CrossOriginProtection`）があるが、本アプリは tailnet IP で Listen するため SDK の保護には依存せず、SDK 側の設定の挙動は実装時に確認する。また SDK の `SessionTimeout`（MCP セッションの寿命）とリースのアイドル解放（FR-S04、既定 5 分）は別のものとして独立に設定する。

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
        alt 承認が必要
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

- **ループの 1 ステップ**は Responses API の呼び出し 1 回。`computer_call` が返らなくなったら最終回答として `completed` にする（[FR-G04](../requirements/functional.md)）
- **画面**は MCP の `screenshot` と同じキャプチャを使い、FR-M16 の縮小規則で縮小した PNG を `detail: "original"` で送る。モデルが返す座標は縮小座標として物理座標に戻す（OpenAI のガイドが求める「縮小した場合はモデルの座標を元の座標空間に戻す」に一致）
- **すべてのアクションを 1 件ずつ検査**する。ブロック・拒否が 1 回でも起きたら、残りの `actions[]` は実行せずタスクを `failed` にする（FR-G07）
- **中断**は、実行中の OpenAI 呼び出しの `context` を取り消し、押下中のキー・ボタンを解放し、リースを解放する。キルスイッチ・リース剥奪・`cancel_task` はすべて同じ経路（FR-G10）
- **状態の永続化**: タスクは状態遷移のたびに `agent_tasks` を更新する。ステップは監査ログの行として `task_id` と連番付きで保存する（[ER](er.md)）。実行状態（ループ・リース）はメモリのみで、再起動時に `running` / `pending_approval` のタスクを `aborted` にする（NFR-A08）
- **会話の継続方式**（`previous_response_id` を使うか、全履歴を再送するか）と OpenAI 側の保存（`store`）は、実装時に OpenAI の最新仕様とデータ保持の扱い（NFR 未決事項）を確認して確定する

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
    U->>DB: 許可: クライアント登録 + 承認モード選択 / 拒否: 破棄
```

### キルスイッチ

発動経路（トレイ、ダッシュボード、グローバルホットキー）はいずれも `KillSwitch.Trigger()` を呼ぶ。処理順は、(1) 状態を `active` に永続化、(2) 全リース剥奪、(3) 実行中のエージェントのタスクを中断（OpenAI 呼び出しの取り消し、押下中入力の解放）、(4) リモートルーターのリクエストを 503 `kill_switch_active` で拒否、(5) 全 MCP・WebSocket セッションを切断、(6) オーバーレイ消去、(7) 監査記録。順序は NFR-P05（1 秒以内）を満たすため同期で行い、永続化は先頭に置いて再起動後も維持する（[NFR-S07](../requirements/non-functional.md)）。

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
- **外部判定（Jev 等）を足す場合の制約**（未決。決まるまで実装しない）: 入力はテキストと JSON（アクションの JSON、ウィンドウのタイトル・プロセス名、UI Automation の要素名）に限る。座標や数値の判定は任せない（Jev の jaggedness ページが数値・算術の精度が弱いと明記している）。画像入力は「Dreamer-ready」で `TypeSafe-Danger-Zone-Images: true` ヘッダーが必要なため使わない。プロンプトインジェクションの検知精度は公開評価で 74.1%（n=116）にとどまるため、**防御の唯一の主体にしない**。送信データと通信先は [NFR](../requirements/non-functional.md) の未決事項に従って追記する

## HALT の適用

### フロントエンド構成

```mermaid
graph TD
    subgraph GUI["ホスト GUI (Wails WebView)"]
        GP["Templ ページ + HTMX"]
        SSE["SSE 拡張 /events<br/>(状態更新を push)"]
    end
    subgraph WEB["Web UI (ブラウザ)"]
        WP["Templ ページ + HTMX"]
        LIT["Lit アイランド<br/>remo-screen-viewer"]
    end
    GP --> SSE
    WP -- "HTMX の外側に配置" --- LIT
    LIT -- "WebSocket (JSON + バイナリ)" --> RR["リモートルーター"]
    WP -- "HTML フラグメント" --> RR
    GP -- "HTML フラグメント" --> GR["GUI ルーター"]
```

- 画面の大半（ダッシュボード、クライアント管理、接続要求、設定、監査ログ、ビューアの周辺 UI、リースパネル）は **Templ + HTMX** で描画する
- **Lit は `<remo-screen-viewer>` のみ**。理由は Canvas 描画、WebSocket のバイナリフレーム、マウス・キー入力のキャプチャが必要でハイパーメディアに載せられないため（HALT の使用基準に該当）
- Lit アイランドの属性: `ws-url`（サーバーが注入）、`readonly`（操作権なしなら付与）、`monitor`、`quality`、`fps`
- **HTMX ↔ Lit 境界**: HTMX は Lit アイランドを swap しない。ビューアのツールバー（モニター・品質・FPS・クリップボード・リースボタン）は Lit の外に Templ で描画し、設定変更は `hx-post` でサーバーに送る。サーバーが WS 経由で設定を反映する。リース状態の変化は WS 制御メッセージ → Lit の CustomEvent → HTMX の `hx-trigger` でパネルを再取得する
- **状態駆動レンダリング**: リースが `idle` なら「操作権を取得」だけ、他者保持なら「強制剥奪」だけ、自分が保持なら「解放」だけを描画する。キルスイッチ中は操作系を一切描画しない。判定は Go 側で行う
- 命名規則: カスタム要素のプレフィックスは `remo`（例: `<remo-screen-viewer>`）
- 共通ライブラリ: `lib/ws.ts`（自動再接続）、`lib/api.ts`、`lib/logger.ts`（HALT 標準）。直接 `fetch()` / `new WebSocket()` は使わない

### HALT 標準からの差分

| HALT 標準 | 本プロジェクト | 理由 |
|-----------|--------------|------|
| Cookie セッション + CSRF トークン（meta タグ） | **Cookie を使わない**。識別は WhoIs、CSRF 対策は Host / Origin 検証 + カスタムヘッダー（`X-Puppet-Remo: 1`。`hx-headers` と `lib/api.ts` が付与） | tailnet 上の ID が真実の源であり、Cookie ベースの認証がそもそも無い（NFR-S03） |
| 単一の HTTP サーバー | **GUI ルーター（プロセス内）とリモートルーター（tailnet）に分離** | ホスト専用操作を構造的にネットワークから隔離するため |
| HTMX 拡張は最小限 | ホスト GUI のみ `sse` 拡張を追加 | 承認要求・リース・接続要求をほぼリアルタイムに GUI へ反映するため（NFR-P06）。Web UI は WS 経由で足りるため追加しない |
| Lit は編集系リッチ UI | ライブ画面ビューア 1 つのみ | 要件が 1 画面に限られる |
| Huma で JSON API | `/api/v1/*` は小規模（モニター一覧・状態・OpenAPI）のみ。MCP と WebSocket は Huma の対象外 | JSON API の主な利用者が Lit アイランドと運用確認に限られる |
| 静的ファイルは Gin の `Static` で配信 | `go:embed` した `dist/` を両ルーターで配信 | 単一バイナリ配布のため |

## ディレクトリ構成

```
puppet-remo/
├── cmd/puppet-remo/              # エントリーポイント (Wails アプリ起動、DI)
├── internal/
│   ├── domain/                   # クライアント、リース、承認、座標、エラー
│   ├── repository/               # SQLite (clients, tokens, settings, ...)
│   │   └── migrations/           # 埋め込み SQL マイグレーション
│   ├── platform/                 # interface 定義
│   │   └── windows/              # capture(DXGI/GDI), input, window, uia, clipboard, secret(資格情報)
│   ├── tailnet/                  # LocalAPI クライアント、Watcher、Listener 管理
│   ├── openai/                   # Responses API クライアント (Model インターフェース + 実装、モック)
│   ├── service/                  # authz, lease, approval, control, agent, judge, streamer, audit, updater
│   ├── mcp/                      # MCP サーバー (ツール定義と service 呼び出し)
│   ├── ws/                       # WebSocket プロトコル (画面フレーム・入力)
│   ├── router/                   # gui.go, remote.go
│   ├── web/
│   │   ├── handler/              # 機能別ハンドラ
│   │   ├── middleware/           # hostcheck, whois, allowlist, bearer, killswitch
│   │   ├── atoms/ molecules/ organisms/ pages/ layout/
│   └── app/                      # Wails ウィンドウ・トレイ・オーバーレイ・ホットキー
├── static/
│   └── src/
│       ├── components/
│       │   ├── screen-viewer/    # <remo-screen-viewer>
│       │   └── lib/              # api.ts, ws.ts, logger.ts
│       ├── css/
│       └── dist/                 # ビルド成果物 (go:embed)
├── build/                        # Wails パッケージング (NSIS、マニフェスト)
├── docs/
└── Taskfile.yml                  # templ / tailwind / esbuild / wails3 package
```

## ビルドと配布

| 段階 | 内容 |
|------|------|
| コード生成 | `templ generate` |
| フロントエンド | Tailwind CSS ビルド、esbuild（Lit コンポーネント）、`htmx.min.js` のコピー → `static/dist/` |
| Go ビルド | 純 Go（cgo なし）。Per-Monitor DPI Aware V2 のマニフェストを埋め込む |
| パッケージ | `wails3 package` による NSIS インストーラ（WebView2 ブートストラップ込み） |
| 配布・更新 | GitHub Releases。インストーラと署名ファイル（Ed25519）を公開し、アプリが署名検証して適用 |
| CI | GitHub Actions `windows-latest` で templ 生成の差分チェック、フロントエンドビルド、build / vet / lint / test |

## 改訂履歴

| 版 | 日付 | 変更内容 | 変更理由 |
|---|------|---------|---------|
| 1.0 | 2026-10-28 | 初版作成 | — |
| 1.1 | 2026-10-29 | 内蔵エージェント（OpenAI `computer` ツール）を追加: 構成図・レイヤー（`openai`、`Agent`、`Judge`、`SecretStore`）、タスク実行フロー、キルスイッチの手順、Judge インターフェース、技術選定（openai-go v3、資格情報）、MCP SDK の保護に関する注記。MCP フローのツール名を `left_click` に更新 | ホスト内蔵エージェントを主役にする決定 |
