# プロジェクト概要

## 目的

自分の Windows PC（ホスト）に常駐する Wails アプリを通じて、tailnet 内の別端末から**デスクトップ全体を操作（computer-use）**できるようにする。ホストには **AI エージェントを内蔵**し、操作する側は「こういうことをして」と指示を送るだけで、ホストが OpenAI と直接通信し（**ChatGPT のプラン〔Sign in with ChatGPT。既定〕または OpenAI API キー**）、画面を見ながら実行する。操作の入口は 3 つ。

- **MCP（高レベル）**: MCP クライアント（Claude 等）が `run_task` で自然言語の指示を送る。ホスト内蔵エージェントがタスクを実行し、進捗と結果を返す
- **MCP（低レベル）**: 同じ MCP クライアントが、スクリーンショット・マウス・キーボード等のツールを直接呼んでホストを操作する
- **Web UI（ブラウザ）**: tailnet 内の別端末のブラウザでホスト画面をライブ表示し、人間が手動で操作・介入する

AI による操作（内蔵エージェント、MCP 直接）は、ホスト側のキルスイッチ・承認・オーバーレイ・監査ログで常に制御・可視化する。エージェントのスクリーンショットは OpenAI に送信される（機能は既定で無効、ホスト所有者が明示的に有効化する。ChatGPT のプランで動かすときは、ChatGPT のプランの使用量が使われる）。

## 背景

- AI エージェントの computer-use を自宅・職場の「自分の PC」に向けたいが、公開サーバーや汎用リモートデスクトップを経由せず、tailnet（WireGuard）内で完結させたい。
- ホストは Windows。ホストの `tailscaled`（Tailscale クライアント）を利用して tailnet IP で Listen し、接続元の ID を LocalAPI の WhoIs で取得して認可に使う。
- 個人利用（ホスト所有者＝操作者）が前提。複数の自分の PC を対象にし、各 PC に 1 つずつアプリを常駐させる。

## スコープ

### 含むもの

- Wails アプリ（Go バックエンド + Web フロントエンド）のホスト常駐。トレイ常駐とホスト用 GUI（承認、設定、監査ログ、クライアント管理、起動許可アプリの登録、API キー設定、ChatGPT のサインイン・解除、実行中タスクの確認・中断。段階: M1 はダッシュボード・接続要求・クライアント管理まで、M1.5 は起動許可アプリの登録・編集・削除、承認・設定・監査ログの閲覧は M3、API キー設定・ChatGPT のサインイン・解除・実行中タスクの確認・中断は M4）
- MCP サーバー（Streamable HTTP）。低レベルツール（screenshot / mouse / keyboard / ウィンドウ / クリップボード / UI ツリー / 操作リース。M1）と、ホスト GUI で登録した許可リストのアプリだけを起動する `launch_app` / `list_launchable_apps`（M1.5）と、高レベルの `run_task`（非同期のタスク実行・状態取得・中断。M4）
- ホスト内蔵エージェント: OpenAI Responses API で画面を見て操作する（`store: false`、履歴は毎回再送）。接続先（接続方式）は 2 つ。**ChatGPT のプラン（Sign in with ChatGPT。既定）**は、OAuth のアクセストークンを Bearer にして `POST https://api.openai.com/v1/responses` を呼び（API キーは不要）、ネイティブの `computer` ツールが使えないため、MCP の低レベルツールと同じ名前・引数・座標の**関数ツール**とスクリーンショットの画像入力で操作する。**OpenAI API キー**は明示的に選ぶ別の接続方式で、`computer` ツールを使う。ChatGPT の使用量の上限に達したときは、**API キーへ自動で切り替えず**タスクを止め、切り替えはホスト GUI での所有者の明示的な承認だけ。すべての入力は、どちらの経路でも MCP 直接・Web UI と同じ入力層（InputGuard・リース・Judge・監査）を通す
- 高速パス（既定は無効・別途同意）: UI Automation から作った候補表（最大 24 件）から TypeSafe の Jev が 1 手ずつ ID を選んで進め、迷ったときだけ OpenAI へエスカレーションする。完了の宣言は必ず OpenAI がスクリーンショット 1 枚で確認して行う。Jev にスクリーンショットは送らない
- Web UI（HTTP + WebSocket）。ライブ画面（JPEG 差分フレーム）と手動操作
- 認証・認可: tailnet ACL（第 1 層）、WhoIs 許可リスト、MCP 用 Bearer トークン、クライアント単位のエージェント利用許可（`agent_enabled`。M1 から GUI・API で保存・編集できるが、効果は `run_task` が入る M4 から）
- 排他リースによる操作権管理（閲覧は複数可、操作権は常に 1 セッション。保持者は MCP 直接・Web UI・内蔵エージェントのいずれか。段階: M1 は `mcp` のみ・承認なし、M2 で `web` と強制剥奪、M3 で承認モード、M4 で `agent`）
- キルスイッチ（ホストでの即時切断・実行中タスクの中断・グローバルホットキー）、操作中オーバーレイ、操作承認モード、監査ログ（段階: M1 は認証失敗・リース・ツール実行の DB への追記まで。閲覧 UI と縮小スクリーンショットは M3）
- ローカル永続化（SQLite）: 設定、クライアント、トークン、タスク、起動許可アプリ、監査ログ。OpenAI API キーと ChatGPT の OAuth トークンは Windows 資格情報マネージャーに保管
- 起動許可リスト（M1.5）: ホスト所有者がホスト GUI で登録した実行ファイル（引数・作業ディレクトリは登録時に固定）だけを、MCP の `launch_app` で起動できる。起動は `CreateProcess`（シェルを経由しない）で、キルスイッチ・操作リース・監査の下に置く。MCP が受け取る入力は `app_id` だけで、応答は `id` と表示名だけ（実行ファイルのパス・引数は出さない）。`launch_app` は起動後に前面にあったウィンドウと、既存のウィンドウが前に出ただけかの印（`reused_existing_window`）を返す。許可リストの登録・変更・削除はホスト GUI だけ
- 対象 OS: **Windows のみ**（初期スコープ）

### 含まないもの

- macOS / Linux 対応（Windows で確立後に別 Issue で検討。アーキテクチャ上は OS 依存部をインターフェースで分離しておく）
- Wails アプリを「操作する側のクライアント」として使う機能（操作側は MCP クライアントとブラウザのみ。Web UI からタスクを入力する画面も初版では作らない）
- tailnet 外（インターネット公開、Tailscale Funnel）からのアクセス
- tsnet 埋め込み（ホストの tailscaled を利用する）
- OpenAI 以外の AI プロバイダへの切り替え（内蔵エージェントの接続先は OpenAI Responses API のみ。接続方式は ChatGPT のプランと OpenAI API キーの 2 つ）。Jev は高速パスの候補選択だけに使い、実行前の判定（Judge）には使わない（Judge は決定論的なルールベース）。ChatGPT のプラン経路では高速パスを併用しない（対象外。高速パスの挙動は変えない）。ホスト 1 台で同時に保存できる ChatGPT のアカウントは 1 つ（複数アカウントの切り替えは対象外）。経路の自動切り替え（使用量の上限・認可切れ・エラーのときに API キー経路へ送ること）は行わない
- WebRTC / 動画エンコード配信
- 複数ユーザー向けのロール管理（owner / operator / viewer の分離）
- 音声、ファイル転送、双方向の自動クリップボード同期（テキストの手動送受信と MCP ツールは含む。[機能要件](requirements/functional.md) FR-W07 / FR-M13）
- ロック画面・UAC セキュアデスクトップ・Session 0 の操作（Windows の制約として対象外）
- 任意のパス・コマンドラインの起動、起動引数の呼び出し側指定、MCP / Web UI からの許可リストの登録・変更、起動済みプロセスの終了・監視（`launch_app` は起動だけ。M1.5）
- 許可リストの自動発見・インストーラ連動、Store アプリ専用 ID・`.lnk`・URL・ドキュメントの関連付けによる起動（M1.5 は実行ファイル `.exe` の `CreateProcess` のみ）

## ステークホルダー / 想定ユーザー

| 区分 | 役割 | 備考 |
|------|------|------|
| ホスト所有者（兼操作者） | ホスト PC にアプリを導入・承認・設定し、ChatGPT にサインイン（または OpenAI API キーを登録）し、手元の別端末からブラウザまたは MCP クライアントで指示・操作する | 個人。複数 PC を所有 |
| MCP クライアント（Claude 等） | `run_task` で指示を送る、または低レベルツールで直接操作する。所有者の別端末で動く | tailnet 上のデバイスから接続。AI と通信するのはクライアント側の AI（Claude 等）であり、`run_task` の実行中だけホストが OpenAI と通信する（ChatGPT のプランまたは API キー。接続方式は呼び出し側が選べない） |
| ホスト上の在席ユーザー | 画面とオーバーレイで AI 操作を監視し、必要ならキルスイッチで止める | 所有者本人であることが多い |

## システム全体像

```mermaid
graph TD
    subgraph Tailnet["tailnet (WireGuard)"]
        MCPC["MCP クライアント (Claude 等)"]
        BR["ブラウザ (Web UI)"]
        subgraph Host["ホスト (Windows)"]
            TS["tailscaled (LocalAPI: WhoIs)"]
            subgraph App["puppet-remo (Wails)"]
                SRV["HTTP サーバー<br/>(MCP / Web UI / WebSocket)"]
                CORE["コア<br/>認可・リース・承認・監査"]
                AGENT["内蔵エージェント<br/>(computer ツール / 関数ツールのループ)"]
                OS["OS アダプタ<br/>画面キャプチャ・入力注入"]
                GUI["ホスト GUI<br/>(承認・設定・ログ)"]
                DB[("SQLite")]
            end
            DESK["デスクトップ"]
        end
    end
    OAI["OpenAI API (Responses)"]
    OAUTH["auth.openai.com<br/>(Sign in with ChatGPT: 更新・失効)"]
    MCPC -- "MCP (Streamable HTTP)<br/>run_task / 低レベルツール" --> SRV
    BR -- "HTTP / WebSocket" --> SRV
    SRV -- "WhoIs" --> TS
    SRV --> CORE
    CORE --> AGENT
    AGENT -- "HTTPS (スクリーンショット送信)" --> OAI
    AGENT -. "ChatGPT 接続のときだけ<br/>(サインイン・更新・失効)" .-> OAUTH
    AGENT --> CORE
    CORE --> OS
    CORE --> DB
    CORE <--> GUI
    OS --> DESK
```

アーキテクチャの詳細は [アーキテクチャ設計](architecture/overview.md) を参照。

## ドキュメントマップ

このプロジェクトの仕様は以下のドキュメントで構成される。実装時はそれぞれ参照すること。

| ドキュメント | パス | 概要 |
|------------|------|------|
| 機能要件 | [`docs/requirements/functional.md`](requirements/functional.md) | ユースケースと機能要件（接続・認可、操作リース、MCP ツール、内蔵エージェント、Web UI、ホスト GUI、監査、設定） |
| 非機能要件 | [`docs/requirements/non-functional.md`](requirements/non-functional.md) | 脅威モデル、性能・セキュリティ（自己保護、API キー、プロンプトインジェクション対策、CSP）、可用性、プライバシー、決定事項・意図的な未設定・検証事項 |
| アーキテクチャ設計 | [`docs/architecture/overview.md`](architecture/overview.md) | 技術選定、構成図、レイヤー、GUI／リモートのルーター分離、主要フロー（高速パス含む）、画面配信、Judge、ディレクトリ構成 |
| フロントエンド設計（HALT） | [`docs/architecture/frontend.md`](architecture/frontend.md) | HTMX + Atomic Design + Lit + Templ の規約、2 つの UI（ホスト GUI / Web UI）、HTMX パターン、`<remo-screen-viewer>`、ビルド、開発ワークフロー、テスト戦略 |
| ER / データモデル | [`docs/architecture/er.md`](architecture/er.md) | SQLite のテーブル定義（clients、client_tokens、connection_requests、settings、launchable_apps、agent_tasks、audit_logs、audit_thumbnails）と保守処理 |
| API 仕様 | [`docs/api/endpoints.md`](api/endpoints.md) | MCP ツール、Web UI のルート、WebSocket のプロトコル、JSON API、ホスト GUI のルートと更新の配信（`/poll`・SSE）、エラー形式 |
| コンポーネント設計 | [`docs/components/overview.md`](components/overview.md) | パッケージの責務・依存、Go インターフェース、goroutine と状態の所有者、起動・終了順序、テスト方針（モックと実機スモーク） |
| 環境構築 | [`docs/environment/setup.md`](environment/setup.md) | 開発環境のセットアップ、CI/CD、Lint、Format、Git フック、OpenAPI の方針、Linterly の状況 |

## マイルストーン / リリース計画

目安時期は未定。対象 OS は Windows のみ。

- M1: Windows + MCP 低レベルツール（スクリーンショット・マウス・キーボード・ウィンドウ一覧とフォーカス・クリップボード・UI ツリー・操作リース。FR-M01〜M17）、tailscaled 連携（WhoIs 許可リスト + Bearer）、排他リース（保持者は MCP 直接の `mcp` のみ。承認なしで取得）、キルスイッチ、ホスト GUI の基本（ダッシュボード、接続要求、クライアント管理。クライアント別のエージェント利用可否 `agent_enabled` は保存・編集のみで効果なし。効果は M4）、監査の追記（認証失敗・リース・ツール実行）
- M1.5: 起動許可リストと MCP の `launch_app` / `list_launchable_apps`（FR-M18・M19）、ホスト GUI の起動許可アプリの登録・編集・削除（FR-H12）。起動は操作リース必須（未保持なら暗黙に取得）・キルスイッチ・監査の下に置き、承認は無い（M3 で承認モードの対象に加える）。OS の起動は `platform` の背後の `CreateProcess`
- M2: Web UI（ライブ画面・手動操作）、リースの強制剥奪（保持者種別 `web` の追加）
- M3: 承認モード（リースの `pending_approval`、承認ダイアログ）、操作中オーバーレイ、監査ログの閲覧 UI と縮小スクリーンショット
- M4: ホスト内蔵エージェント（`run_task`、接続方式 2 つ〔ChatGPT のプラン（Sign in with ChatGPT。既定）の関数ツール、OpenAI API キーの `computer` ツール〕、ChatGPT のサインイン・解除・使用量の上限の扱い、API キー管理、タスク上限・中断。リースの保持者種別 `agent` の追加）と、高速パス（TypeSafe Jev による候補選択。UI Automation の候補表、エスカレーション。API キーの接続方式のときだけ）。承認・オーバーレイ・監査（M3）を前提にするため M3 の後に置く。高速パスの品質は、固定のタスク集合で実測して確認する
  - ChatGPT のプラン経路（#49）は、現時点では仕様の追記までで、実装は未着手。原資料（OpenAI の Sign in with ChatGPT の文書）に無い点は推測で決めず、[非機能要件「ChatGPT プラン経路: 未確認」](requirements/non-functional.md)に列挙して実機・実 API で確認する。テストはモックのみで、実際の OpenAI・ChatGPT を呼ばない
  - リスクと前提: (1) ネイティブの `computer` ツールが使えないため、関数ツールでの操作の精度は API キー経路と同じとは限らない（実測で確かめる。仕様に性能の主張は書かない）。(2) この経路は OpenAI の「プレビュー」の制限（送れない項目、ツールの制限など）に依存し、原資料の変更で仕様が変わりうる。(3) 使用量の上限（Plus は 5 時間の上限が全アプリで共有される）に当たるとタスクが止まり、再開はホスト所有者の操作（再開・使用量の管理・API キーへの切り替えの承認）による。(4) 原資料の対象は、オープンソースまたはローカルでホストされるアプリ（本アプリが当たることは Issue #49 でオーナーが確認済み。公開・ライセンスは本書の範囲外）
- 将来: macOS / Linux 対応（スコープ外。別途検討）

## 用語集

| 用語 | 定義 |
|------|------|
| ホスト | 操作される Windows PC。アプリが常駐する |
| クライアント | ホストに接続する側（MCP クライアント、または Web UI を開いたブラウザ） |
| tailnet | Tailscale が構成するプライベートネットワーク |
| tailscaled | ホスト上の Tailscale デーモン。LocalAPI を提供する |
| WhoIs | LocalAPI で接続元 IP からユーザー・デバイス情報を引く機能。認可の ID 源 |
| MCP | Model Context Protocol。AI クライアントがツールを呼ぶための標準プロトコル |
| Streamable HTTP | MCP の HTTP トランスポート |
| 操作リース | 入力操作の権利。同時に 1 セッションのみが保持できる排他ロック |
| 閲覧者 | ライブ画面の閲覧のみ可能なセッション。複数同時接続可 |
| キルスイッチ | ホスト側で全クライアント接続・操作を即時遮断する機構 |
| 承認モード | 操作を許可する条件（常時承認 / 初回のみ承認 / 自動許可）をクライアント単位で設定する仕組み |
| オーバーレイ | AI 操作中であることをホスト画面に明示する表示 |
| 内蔵エージェント | ホスト内で動き、OpenAI Responses API で画面を見ながら入力を実行するループ。接続方式が OpenAI API キーなら `computer` ツール、ChatGPT のプランなら関数ツール（MCP の低レベルツールと同じ名前・引数）で操作する。`run_task` で起動する |
| 接続方式 | 内蔵エージェントの OpenAI への接続の仕方。`chatgpt`（ChatGPT のプラン。Sign in with ChatGPT。**既定**）と `openai_api_key`（OpenAI API キー）。ホスト GUI だけで選び、タスクの開始時に固定される。アプリは自動では切り替えない |
| Sign in with ChatGPT | OpenAI の、ChatGPT のアカウントでサインインして ChatGPT のプランの使用量を外のアプリから使う仕組み（OAuth）。アクセストークンを Bearer にして Responses API を呼ぶ。API キー・クライアントシークレットは要らない |
| `ext_agent_host_id` | ChatGPT のサインインで OpenAI に渡す、ホスト 1 台ごとに変わらない不透明な識別子（`urn:uuid:…`）。最初のサインインの前に作って設定に保存する。認証の資格情報ではない |
| 関数ツール | ChatGPT のプラン経路で、ネイティブの `computer` ツールの代わりにモデルへ渡す、namespace `desktop` の関数群（`screenshot`・`left_click`・`type`・`key`・`scroll` など。MCP の低レベルツールと同じ名前・引数・座標・上限。[FR-G27](requirements/functional.md)） |
| 高速パス | 内蔵エージェントの 1 手ごとの処理方式の 1 つ。アプリが UI Automation から作る候補表から、Jev が ID を 1 つ選んで進める。迷ったときは OpenAI へエスカレーションし、完了は必ず OpenAI が確認する |
| 候補表 | 高速パスで Jev に選ばせる、アプリが作る不変のアクション一覧（最大 24 件 + `reobserve` + `abstain`）。Jev は表にない操作・座標・引数を作れない |
| エスカレーション | 高速パスから OpenAI の `computer` ツールのループへ引き継ぐこと。理由は `abstain` / `low_confidence` / `no_candidates` / `invalid_choice` / `no_progress` / `goal_maybe_done` / `fast_step_limit` / `chooser_unavailable` |
| タスク（run） | 内蔵エージェントへの 1 回の指示の実行単位。排他リースを開始から終了まで保持する。状態・ステップ数・使用トークンを記録する |
| Judge | 実行前のアクションに対し「許可 / 承認へ回す / 拒否」を助言する判定インターフェース。既定はルールベース。承認を省略する方向には作用させない |
| 起動許可リスト | ホスト所有者がホスト GUI で登録した、`launch_app` で起動できるアプリの一覧（表示名・実行ファイルのパス・固定の引数・作業ディレクトリ。`launchable_apps` 表）。MCP へは `id`（`app_id`）と表示名だけを返す |

## 改訂履歴

| 版 | 日付 | 変更内容 | 変更理由 |
|----|------|---------|---------|
| 1.0 | 2026-10-28 | 新規作成 | 初版 |
| 1.1 | 2026-10-28 | ライブ画面の形式を JPEG のみに修正 | アーキテクチャ決定（純 Go・cgo なし）との整合 |
| 1.2 | 2026-10-29 | ホスト内蔵エージェント（OpenAI `computer` ツール）を主役に追加、MCP は低レベルツールと `run_task` を併存、M4 を追加 | 「ホストが AI と直接通信し、指示を受けて実行する」という要件の確認 |
| 1.3 | 2026-10-29 | ドキュメントマップを確定（全 6 文書のリンクと要約） | 仕様書の完成 |
| 1.4 | 2026-10-29 | ドキュメントマップに `docs/architecture/frontend.md` を追加（全 7 文書） | HALT の規約を新規文書に分離 |
| 1.5 | 2026-10-29 | 高速パス（Jev による候補選択）をスコープ・マイルストーン M4・用語集に追加。Jev を Judge に使わない旨を明記。OpenAI は `store: false` | 未決事項の決定 |
| 1.6 | 2026-10-29 | マイルストーンを修正（PR #2 のレビュー対応）: 排他リース（FR-L01〜L05）を M1 に、M2 を Web UI と強制剥奪（FR-L06）に変更。M1 のツール範囲を FR-M01〜M17 全件（`get_ui_tree`・`clipboard_*`・`list_windows`・`focus_window` を含む）に変更。承認モード・オーバーレイ・監査の閲覧 UI と縮小スクリーンショットは M3、`agent` のリースは M4 と明記 | M1 の実装者が迷わない仕様にするためのレビュー指摘の解消 |
| 1.7 | 2026-10-29 | スコープの「排他リース」「監査ログ」に段階（M1〜M4）を追記 | PR #2 のレビュー対応（方針 4・8） |
| 1.8 | 2026-10-29 | スコープの「ホスト用 GUI」「MCP サーバー」に段階（M1 / M3 / M4）を追記 | PR #2 のレビュー対応（方針 2・4・8） |
| 1.9 | 2026-10-30 | ドキュメントマップの API 仕様の概要を、ホスト GUI の更新の配信（`/poll`・SSE）に合わせて更新 | ホスト GUI の更新をポーリングに変更したため（#12） |
| 1.10 | 2026-10-30 | スコープの認証・認可と M1 の範囲に、`agent_enabled` が M1 では保存・編集のみで効果は M4 からであることを追記 | M1 の実装と FR-A09 の食い違いの解消（オーナー決定） |
| 1.11 | 2026-10-30 | 起動許可リストと MCP の `launch_app` / `list_launchable_apps`（M1.5）を追加: スコープ（含む・含まない）、マイルストーン M1.5、用語集、ドキュメントマップの ER の表 | M1.5 の仕様追記（#16。オーナー決定 8 件） |
| 1.12 | 2026-11-01 | 起動許可リストの説明に、`launch_app` が起動後の前面のウィンドウと `reused_existing_window` を返すことを追記 | #46 |
| 1.13 | 2026-11-01 | ドキュメントマップに環境構築（`docs/environment/setup.md`）を追加 | #40 |
| 1.14 | 2026-11-02 | 内蔵エージェントの接続方式を 2 つにした（ChatGPT のプラン〔Sign in with ChatGPT。既定〕/ OpenAI API キー）: 目的・スコープ・含まないもの・ステークホルダー・全体像・M4・リスクと前提・用語集に反映。使用量の上限でも API キーへ自動で切り替えない（所有者の明示的な承認のみ）、高速パスは併用しない、ホスト 1 台 1 アカウント | #49 |
