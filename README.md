# puppet-remo

自分の Windows PC（ホスト）に常駐する [Wails](https://wails.io/) アプリです。tailnet（Tailscale）内の別端末から、MCP（Model Context Protocol）経由でこの PC のデスクトップ全体を操作（computer-use）できるようにします。個人利用（ホスト所有者＝操作者）が前提で、対象 OS は Windows のみです。

- ホストの `tailscaled` を使い、tailnet の IP だけで Listen します（既定のポートは 8765。設定画面で変更できます）。
- 接続元の ID は Tailscale LocalAPI の WhoIs で取得し、許可リストと MCP 用の Bearer トークンで認可します。
- キルスイッチ（グローバルホットキー `Ctrl+Alt+Shift+K`・トレイ・ダッシュボード）で、操作をホスト側から即座に止められます。
- ホスト用の GUI はアプリ内のウィンドウとトレイに常駐し、ネットワークには公開しません。

現在は M1〜M4（MCP の低レベルツール・ホスト GUI・キルスイッチ・接続要求、アプリ起動許可リスト、Web ビューア、承認・監査、内蔵エージェントと高速パス）まで実装済みです。内蔵エージェント（OpenAI の `computer` ツール）と高速パス（TypeSafe）は既定で無効で、ホスト GUI での明示的な同意と有効化が必要です。OpenAI・TypeSafe の API キーは Windows の資格情報マネージャーにだけ保存し、設定ファイル・ログ・監査には書きません。全体の範囲とマイルストーンは [プロジェクト概要](docs/overview.md) を参照してください。

## インストール

1. 事前に、ホスト PC で Tailscale にログインし、`tailscaled` を動かしておきます。
2. インストーラ（`puppet-remo-amd64-installer.exe`）を実行します。WebView2 ランタイムが無い環境では、インストーラが導入します。
3. スタートメニューの「puppet-remo」から起動します。ウィンドウを閉じてもトレイに常駐します。終了はトレイメニューの「終了」です。

### SmartScreen の警告について

インストーラと実行ファイルには **Authenticode 署名を付けていません**（初版・個人利用。NFR-O07）。そのため初回の実行時に Windows SmartScreen の警告（「Windows によって PC が保護されました」）が出ます。

1. 警告の画面で **「詳細情報」** をクリックします。
2. 表示される **「実行」** をクリックします。

配布物の入手元を確認してから実行してください。更新ファイルの完全性は、署名（Ed25519。NFR-S10）で検証します。

## 推奨する tailnet ACL（NFR-S12）

アプリは tailnet の IP だけで待ち受けますが、tailnet 側の ACL でも、接続元を自分のデバイスに、宛先をこのアプリのポートに絞ることを推奨します（多層防御の第 1 層）。次は tailnet のポリシーファイル（ACL）の例です。タグ名・ポートは自分の環境に置き換えてください。

```json
{
  "tagOwners": {
    "tag:puppet-host":    ["autogroup:admin"],
    "tag:puppet-clients": ["autogroup:admin"]
  },
  "acls": [
    {
      "action": "accept",
      "src":    ["tag:puppet-clients"],
      "dst":    ["tag:puppet-host:8765"]
    }
  ]
}
```

- ホスト PC に `tag:puppet-host`、操作に使う自分の端末（MCP クライアントやブラウザを動かす端末）に `tag:puppet-clients` を付けます。
- ポートは、アプリの設定で変えたポートに合わせます。
- 上の例は、他の通信を許可する既存のルールを置き換えるものではありません。既存のポリシーにこのルールを加え、ほかの端末からホストのポートへ届かないことを確認してください。

アプリ側でも、未登録のデバイスからの接続は、ホストの GUI で承認するまで拒否されます。

## データの保存場所と BitLocker（NFR-S11）

データ（SQLite のデータベース、ログ、WebView2 のユーザーデータ）は `%LOCALAPPDATA%\puppet-remo\` に保存し、NTFS ACL で現在のユーザーだけが読み書きできるようにします。アンインストールしてもデータは残ります。完全に消すときは、このフォルダーを手で削除してください。

- 同じユーザー権限で動く悪意あるプロセスは、保護の対象外です。
- スクリーンショットのサムネイルなど、画面の内容を保存する機能は、**平文**でディスクに書きます。ディスクの紛失・盗難に備えて、**BitLocker によるドライブ全体の暗号化を有効にしてください**。
- 保存したくないときは、設定でサムネイルの保存を無効にできます（FR-AU02）。
- OpenAI などの API キーは、このフォルダーには置きません（Windows 資格情報マネージャーに保存します）。

## 起動許可アプリ（M1.5。NFR-S20）

MCP の `launch_app` で起動できるのは、ホスト所有者が許可リストに登録したアプリだけです。

- **登録できるのはホストの GUI（起動許可アプリ画面）だけ**です。MCP クライアント・Web UI・リモートからは、登録・変更・削除できません。
- MCP クライアントが `list_launchable_apps` で見られるのは `id` と表示名だけで、`launch_app` に渡せるのは `app_id` だけです。実行ファイルのパス・引数・作業ディレクトリは、MCP からは見えず、指定もできません。
- 起動のたびに登録時と同じ規則（絶対パスのローカルの `.exe` など）を確かめ、満たさない行は起動しません。

残存リスクと、登録するときの注意:

- **`cmd.exe`・`powershell.exe`・`wscript.exe`・`mshta.exe` などのコマンドインタープリターやスクリプトホストを登録しないでください。** 登録すると、引数を固定していても、実質的に任意のコードの実行を許すことになります。シェルや拡張機能を持つエディターも同様です。
- 本アプリは、こうしたプログラムを拒否するブロックリストを持ちません。何を登録するかは、ホスト所有者の判断です。
- **ユーザーが書き込める場所（ダウンロード・`%TEMP%`・ユーザーのプロファイル配下など）の `.exe` を登録しないでください。** ファイルを差し替えられると、登録したのとは別のプログラムが起動します。起動のたびに確かめるのはパスの形と存在だけで、ファイルの内容や署名は確かめません。
- 起動のたびに、実行ファイルがローカルの固定ディスク上の実体であること（ネットワーク・マップドドライブ・`subst`・シンボリックリンク・ジャンクションを経由しないこと）も確かめます。登録した場所を差し替えられる者がいれば、確認と起動の間に別のファイルへ入れ替えられる余地は残ります。
- 起動の監査は、プロセスを作ったあとに非同期で記録します。記録のキューが満杯のとき・書き込みに失敗したときは、起動は成立したまま監査に残りません。その場合は運用ログに Error（`audit entry dropped`・`audit append failed`）が出ます。
- 起動したあとのプロセスは、本アプリの管理外です。終了も監視もしません（本アプリを終了しても、キルスイッチを発動しても、起動済みのプロセスは残ります）。

## 配布（ナイトリーと正式版）

Windows 向けのインストーラ（NSIS）は、GitHub Actions が GitHub Releases に自動で公開します。`develop` への push はナイトリー（プレリリース。最新 7 本だけ残る）、`main` への push は正式版です。版は `VERSION` ファイルと最新の正式タグで決まり、人はタグを打ちません。仕組みと運用は [配信の運用](docs/operations/release.md) を参照してください。

## ビルドと開発

必要なもの: Go（`go.mod` の `go` 行の版）、[bun](https://bun.sh/)（フロントエンドのパッケージマネージャ。版は `scripts/ci/install-bun.sh` に固定）、[Task](https://taskfile.dev/)、[templ](https://templ.guide/)（`task tools:templ` で `go.mod` の版を導入）、[golangci-lint](https://golangci-lint.run/)（版は CI と同じ v2.15.0）、[Lefthook](https://lefthook.dev/)（Git フック。`task tools:lefthook` で導入）。インストーラの作成には `makensis`、開発には `wails3` が要ります。フロントエンドの依存は `task frontend:install`（`bun install --frozen-lockfile`）で入ります。

開発環境・CI/CD・Lint・Format・Git フックの構成は [環境構築](docs/environment/setup.md) にまとめています。

```sh
task               # 利用できるタスクの一覧
task build         # Go のビルド（ホスト OS）
task build:windows # Windows 向けのビルド（クロスビルドの確認）
task windows:build # Windows 向けの本番ビルド（フロントエンドのビルド込み）
task test          # テスト
task lint          # golangci-lint（ホスト OS と Windows）
task templ:generate # .templ から _templ.go を生成（生成物はコミットする）
task frontend:build # static/dist を作る（esbuild・Tailwind CSS・htmx）
task ci            # CI と同じ検査を一通り実行
task package       # NSIS インストーラを作る（版は VERSION ファイル。RELEASE_VERSION= で上書き）
task dev           # wails3 dev で開発する（Windows。別ターミナルで task templ:watch）
```

Linux / macOS では、Windows 専用のコードを除いてビルド・テストできます（`GOOS=windows go build ./...` でクロスビルドも確認できます）。実機でしか確かめられない項目は、[Windows 実機スモーク手順](docs/operations/smoke-windows.md) にまとめています。

### Git フック（Lefthook）

コミットとプッシュの前に、CI で落ちる検査を手元で先に走らせます。設定は [`lefthook.yml`](lefthook.yml) です。clone したら、フックを入れてください（clone ごとに一度）。

```sh
task tools:lefthook   # lefthook を導入する（go install github.com/evilmartians/lefthook/v2@v2.1.17。$(go env GOPATH)/bin を PATH に通す）
task hooks:install    # Git フックを入れる（lefthook install と同じ）
```

- pre-commit: ステージしたファイルに関係する検査だけを順に走らせます（落ちたらコミットされず、残りは飛ばします）。
  - gofmt（ステージした `*.go`。差分があれば失敗）
  - golangci-lint（ホスト OS）と `scripts/check-imports.sh`（`*.go`・`go.mod` などの変更時）
  - templ generate の差分チェック（`.templ`・`_templ.go` の変更時。生成物がステージした内容と一致すること）
  - フロントエンドの型検査（`static/` の変更時。`bun install --frozen-lockfile` のあと `bunx tsc -p .`）
- pre-push: `go test ./...`。

フックを入れずに手元で実行する（`.git/hooks` には触れません）には `task hooks:pre-commit`（ステージ済みのファイルが対象）と `task hooks:pre-push` を使います。CI の残り（`go test -race`、`GOOS=windows` の build・vet・lint、本番ビルド）は `task ci` で確認します。

## ドキュメント

| 文書 | 内容 |
|------|------|
| [プロジェクト概要](docs/overview.md) | 目的・範囲・マイルストーン |
| [機能要件](docs/requirements/functional.md) / [非機能要件](docs/requirements/non-functional.md) | 要件（FR-* / NFR-*） |
| [アーキテクチャ設計](docs/architecture/overview.md) | 構成・レイヤー・セキュリティ |
| [フロントエンド設計](docs/architecture/frontend.md) | HALT（HTMX・Atomic Design・Lit・templ）、ビルド |
| [データモデル](docs/architecture/er.md) | ER 図・スキーマ |
| [コンポーネント設計](docs/components/overview.md) | パッケージの責務とインターフェース |
| [API 仕様](docs/api/endpoints.md) | MCP・ルート・エラー形式 |
| [Windows 実機スモーク手順](docs/operations/smoke-windows.md) | CI で動かせない項目の手動確認 |
| [配信の運用](docs/operations/release.md) | ナイトリー・正式版の配信、版・タグの決め方 |
| [環境構築](docs/environment/setup.md) | 開発環境のセットアップ、CI/CD、Lint、Format、Git フック |
