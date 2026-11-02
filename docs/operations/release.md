# 配信（ナイトリーと正式版）

Windows 向けの NSIS インストーラを、GitHub Releases に自動で公開する仕組みの運用手順です。ブランチは 2 本です。

| ブランチ | 公開されるもの | ワークフロー |
|----------|----------------|--------------|
| `develop` | ナイトリー（プレリリース） | `.github/workflows/nightly.yml` |
| `main` | 正式版 | `.github/workflows/release.yml` |

すべてのジョブはセルフホストの Linux ランナー（`[self-hosted, Linux, X64]`）で動きます。Windows の runner は使いません（`GOOS=windows` のクロスビルドと、NSIS の `makensis` を Linux で動かします）。使うトークンは `GITHUB_TOKEN`（`contents: write`）と、更新用の署名の Secret（`MINISIGN_SECRET_KEY`・任意で `MINISIGN_PASSWORD`。[署名](#署名更新用)）です。Secret が無くても CI は緑のまま（署名だけを省いて公開します）。Authenticode のコード署名はしません（[NFR-O07](../requirements/non-functional.md)。SmartScreen の警告は [README](../../README.md#smartscreen-の警告について) を参照）。

## 開発の流れ

- 普段の PR は `develop` に向けます。
- `main` に入れるのは、`develop` → `main` の PR だけです（マージはマージコミット方式）。`main` への push は、すべて正式版のリリースになります。
- `develop` への push は、すべてナイトリーになります。

## ナイトリー（develop への push）

1. `develop` に push されると、`nightly.yml` が起動します。
2. インストーラを作り、プレリリースとして公開します。CI（`ci.yml`）の成功は待ちません。
3. タグは `nightly-YYYYMMDD-<短い sha>` です。短い sha は通常 7 桁で、git が曖昧と判断したときだけ長くなります（片付けは 7〜40 桁を対象にします）。日付は commit の日付（UTC）で、同じ commit は何度実行しても同じタグになります。同じ日に複数回出ても、sha で区別されます。
4. 最新 7 本だけ残し、それより古いナイトリーの Release とタグは削除します。対象は、タグが上の形に完全に一致するプレリリースだけです。正式版は対象になりません（[`scripts/ci/prune-nightlies.sh`](../../scripts/ci/prune-nightlies.sh)）。「7 本」は Release の数なので、1 日に 7 回 push があれば、その日のうちに入れ替わります。
5. 同時に 2 本は走りません（`concurrency`）。待ちは 1 件しか保持されないので、続けて push したときの間の commit は出ないことがあります。最後の commit が、それまでの変更をすべて含みます。

## 正式版（main への push）

1. `main` に push されると、まず `ci.yml`（CI）が走ります。
2. CI が**成功**したときだけ、`release.yml` が `workflow_run` で起動します。失敗・キャンセルのときは何もしません。対象の commit は `workflow_run` の `head_sha` です。
3. インストーラを作り、正式版として公開します。タグ `v<X.Y.Z>` と Release は、このワークフローが作ります。**人はタグを打ちません。** 手動実行（`workflow_dispatch`）も使いません。
4. すでに後の commit が先にリリースされていた場合（その commit を含む正式タグがある）は、版の順序を逆転させないため何もしません。

`paths-ignore` は付けていません。docs だけの変更でも、`main` への push はリリースになります。

### 版の決め方

リポジトリ直下の `VERSION`（`X.Y.Z`）と、最新の正式タグで決まります（[`scripts/ci/next-version.sh`](../../scripts/ci/next-version.sh)）。

- 次の版 = 最新の正式タグ `vX.Y.Z` の**パッチ +1**。ただし、`VERSION` の値のほうが大きければ `VERSION` の値を使います。
- 正式タグがまだ無ければ、`VERSION` の値をそのまま使います（+1 しません。最初のリリースは `v0.1.0`）。
- **マイナー・メジャーを上げるとき**は、`develop` で `VERSION` を書き換えます（例: `0.2.0`）。次の正式版がその版になり、以降はその版を起点にパッチが +1 されます。
- `VERSION` の書式は `X.Y.Z`（各 0〜65535 の数字）です。NSIS の `VIProductVersion` の制約で、`-rc1` などの接尾辞は付けられません。
- `VERSION` は、インストーラの版（`-DINFO_PRODUCTVERSION`）と exe のファイル情報（`info.json` の版）の唯一の持ち主です。CI（`task windows:package` の `RELEASE_VERSION=`）が、ここから作ります。`build/config.yml` の `info.version` はビルドでは使われません（`wails3 update build-assets` 用）。

| 出すもの | インストーラの版（X.Y.Z） | exe の `main.version` | タグ |
|----------|---------------------------|------------------------|------|
| 正式版 | 決めた版 | `X.Y.Z` | `vX.Y.Z` |
| ナイトリー | 「次に出る正式版」の版（上と同じ計算） | `X.Y.Z-nightly.YYYYMMDD+<sha7>` | `nightly-YYYYMMDD-<sha7>` |

### 同時実行・再実行

- 同時に 2 本は走りません（`concurrency`）。順序が入れ替わっても、上の「何もしない」規則で版は逆転しません。
- 失敗して再実行すると、その commit にすでにタグがあれば同じ版を使い、版は進みません。
- Release は、下書きで作って添付してから公開します。タグは公開の時点で作られるので、途中で失敗してもタグは残りません。残った下書きは、再実行で続きから公開されます。公開済みで添付が足りないときは、足します。
- 同じタグを別の commit が公開済みなら、失敗にします（別の commit の Release を壊さないため）。

## 添付ファイル

| ファイル | 内容 |
|----------|------|
| `puppet-remo-<X.Y.Z>-amd64-installer.exe` | 正式版のインストーラ |
| `puppet-remo-<X.Y.Z>-nightly.<YYYYMMDD>.<sha7>-amd64-installer.exe` | ナイトリーのインストーラ |
| 上記の `.sha256` | `sha256sum` 形式。同じ commit でもインストーラのバイト列は毎回少し違う（NSIS が時刻を入れる）ので、Release に載せた値で確かめる |
| 上記の `.minisig` | 更新用の署名（minisign 形式。[署名](#署名更新用)）。署名の Secret があるときだけ付く |

アプリの更新は、Release を**認証なし**で取得します。そのため、リポジトリ（Release）は**公開**にする必要があります（[オーナーの作業](#オーナーの作業署名鍵と公開)）。リポジトリが private の間は、アプリの更新の確認が失敗する（運用ログに残り、アプリは止まらない）だけです。人が private の Release をダウンロードするときは、リポジトリの読み取り権限が要ります（`gh release download <tag>`）。

## 署名（更新用）

アプリの自動更新（FR-H09・[NFR-S10](../requirements/non-functional.md)）は、Release のインストーラを、アプリに埋め込んだ公開鍵で検証してから適用します。署名は [minisign](https://jedisct1.github.io/minisign/) 形式（Ed25519）です。

| 項目 | 内容 |
|------|------|
| 署名するもの | インストーラのバイト列全体（minisign の事前ハッシュ形式） |
| 署名ファイル | `<インストーラのファイル名>.minisig`（インストーラと同じ Release に添付） |
| 版の束縛 | 署名の trusted comment に `version=<main.version> file=<インストーラのファイル名>` を入れる。アプリは、Release の版・ファイル名と一致しない署名を拒否する（署名済みの古いインストーラを新しい Release へ付け替えるロールバックを防ぐ） |
| 公開鍵 | リポジトリの [`internal/service/updater_pubkey.pub`](../../internal/service/updater_pubkey.pub)（`go:embed`。鍵 ID `4CF72995B5EA6E65`）。ファイルが空・コメント（`#`）だけの間は、アプリは更新の確認だけ行い、**適用しない**（fail closed。画面に「署名鍵が未設定」と出る） |
| 秘密鍵 | GitHub Actions の Secret `MINISIGN_SECRET_KEY`（minisign の秘密鍵ファイルの中身）。パスワード付きの鍵なら `MINISIGN_PASSWORD` も。リポジトリ・ログには置かない |
| 署名のステップ | `nightly.yml`・`release.yml` が、ビルドのあと・公開の前に `scripts/ci/sign-installer.sh` を実行する。Secret を一時ファイル（`0600`）に書き、[`scripts/ci/minisign-sign`](../../scripts/ci/minisign-sign/main.go)（Go。アプリの検証と同じライブラリ）で署名し、終了時に必ず消す。**Secret が無いときは通知（`::notice::`）だけ出して署名を省き、CI は緑のまま**（署名のないリリースは、アプリが適用しない） |
| 鍵の生成 | CI では生成しない（オーナーが手元で行う。[次の節](#オーナーの作業署名鍵と公開)） |

### 更新のチャネル・版の比較・確認の間隔

- **チャネル**（設定 `update.channel`）: `stable`（既定。正式版だけ）と `nightly`（ナイトリーも追う）。設定画面の「更新のチャネル」で切り替える。
  - `stable`: GitHub の `GET /repos/ousiassllc/puppet-remo/releases/latest`（プレリリースを含まない最新の正式版）を使う。
  - `nightly`: `GET /repos/ousiassllc/puppet-remo/releases` の直近 30 件から、正式版・ナイトリーのうち最も新しい版を選ぶ。
- **版の比較**（`domain.CompareVersions`）: `X.Y.Z` の数を先に比べる。同じ `X.Y.Z` では**正式版がナイトリーより新しい**（semver の前リリースと同じ）。ナイトリー同士は日付（`YYYYMMDD`）で比べ、**同じ日は新しくない**（`sha7` は順序を持たない）。
- **適用しないもの**: 現行より古い版（ダウングレード。`downgrade_rejected`）・同じ版（新版なし）。`stable` では、実行中が正式版・ナイトリーのどちらでも、現行より古い正式版へは切り替わらない。`dev` ビルド（`main.version` が `dev`）は、確認も適用もしない。
- **確認の間隔**: 起動の 1 分後に 1 回、以後 6 時間ごと（`service.DefaultUpdateStartDelay`・`DefaultUpdateInterval`）と、GUI の「今すぐ確認」。新版を見つけたら、**署名の検証まで済ませて**（インストーラを取得して検証する。同じ版は取得し直さない）から、トースト・画面で知らせる。オフライン・非公開のリポジトリ（404・401・403）・不正な応答は、運用ログ（URL・ヘッダーは出さない）に残して、次の確認まで待つ。
- **適用**: 署名を適用の直前にもう一度検証し、NSIS のインストーラを `/S`（サイレント）で起動してから、アプリを終了する。インストーラは既定で全ユーザー向け（Program Files）で UAC の確認が出る。リース保持中・承認待ち・接続中・タスク実行中は適用しない（409）。設定 `update.apply_mode` が `auto_idle` なら、これらが無くなった時点（30 秒ごとに確認）で自動的に適用する。
- **取得の制限**: HTTPS だけ。ダウンロードのリダイレクトは GitHub の配信元（`github.com`・`*.githubusercontent.com`）だけ。インストーラは 300 MiB、署名は 4 KiB まで。

### オーナーの作業（署名鍵と公開）

署名を有効にするために、**リポジトリのオーナーが 1 度だけ**行う作業です（CI・実装者は鍵を作りません）。済むまでは、署名のないリリースが出るだけで、アプリは更新を適用しません。

1. **鍵を作る**（手元の信頼できる PC で。[minisign](https://jedisct1.github.io/minisign/) を入れる）:

   ```sh
   # パスワードなし（Secret は MINISIGN_SECRET_KEY だけで足りる）
   minisign -G -W -p puppet-remo.pub -s puppet-remo.key
   # パスワード付き（推奨。Secret に MINISIGN_PASSWORD も要る）
   minisign -G -p puppet-remo.pub -s puppet-remo.key
   ```

2. **公開鍵をリポジトリに置く**（済み。鍵 ID `4CF72995B5EA6E65`）: `puppet-remo.pub` の中身で `internal/service/updater_pubkey.pub` を**置き換える**（2 行: `untrusted comment: …` と鍵の行。プレースホルダのコメントは消す）。`develop` へ PR で入れる。このファイルを含むビルドから、アプリは署名を検証できる。**以前の版のアプリには公開鍵が無いので、鍵を入れた最初の版は手動でインストールする**。
3. **秘密鍵を Secret に登録する**: リポジトリの Settings → Secrets and variables → Actions に、`MINISIGN_SECRET_KEY`（`puppet-remo.key` の中身そのまま。2 行）を登録する。パスワード付きの鍵なら `MINISIGN_PASSWORD` も登録する。`puppet-remo.key` は、登録したら安全な場所（パスワードマネージャーなど）に保管し、リポジトリ・チャットには置かない。
4. **リポジトリ（Release）を公開にする**: アプリは認証なしで Release を取得する。リポジトリが private のままでは、更新の確認が失敗する。
5. **確かめる**: `develop` に push してナイトリーが出たら、Release に `.minisig` が付いていることを確かめ、[Windows 実機スモーク手順](smoke-windows.md) §4.13 の更新の手順を実施する。

鍵を作り直したとき（漏えいなど）は、新しい公開鍵を入れた版を手動でインストールし直す必要がある（古い公開鍵のアプリは、新しい鍵の署名を検証できない）。

## ビルドの中身

`.github/actions/build-installer`（composite action）が、ジョブの中で次を行います。

1. Go を setup アクションで、bun を [`./.github/actions/setup-bun`](../../.github/actions/setup-bun/action.yml)（[`scripts/ci/install-bun.sh`](../../scripts/ci/install-bun.sh)。版と SHA-256 を固定して `RUNNER_TEMP` に展開する。sudo 不要）で入れる（キャッシュは `RUNNER_TEMP`）。
2. `task`（版固定）と `wails3`（`go.mod` の版）を `go install` で入れる。`wails3` は `CGO_ENABLED=0` で入れる（cgo が有効だと Linux では gtk4・webkitgtk-6.0 が要る）。
3. `makensis` が PATH に無ければ、`apt-get download nsis nsis-common` と `dpkg -x` で `RUNNER_TEMP` に展開する（sudo 不要。[`scripts/ci/install-nsis.sh`](../../scripts/ci/install-nsis.sh)）。runner に apt が無いときは失敗するので、NSIS を runner に入れること。
4. `task windows:package RELEASE_VERSION=<X.Y.Z> APP_VERSION=<main.version>` を実行する。
5. インストーラを改名し、SHA-256 を作る。exe に `main.version` が入っていることも確かめる。

このあと、各ワークフローが署名のステップ（[`scripts/ci/sign-installer.sh`](../../scripts/ci/sign-installer.sh)）を実行してから公開します。
ビルド時に、WebView2 のブートストラッパーを Microsoft から取得します（`wails3 generate webview2bootstrapper`）。runner から `go.microsoft.com` への外向きの通信が要ります。公開に使う `gh` が runner に無ければ、版と SHA-256 を固定して取得します（[`scripts/ci/install-gh.sh`](../../scripts/ci/install-gh.sh)）。

`ci.yml` の `installer (build only)` ジョブが、PR の段階で同じ手順をビルドだけ（公開しない）実行して、runner での道具の導入を確かめます。

## ローカルで確かめる

```sh
# 配信スクリプトのテスト（偽のリポジトリ・偽のタグ・偽の gh。ネットワークは使わない）
./scripts/ci/test-release-scripts.sh

# ある commit が、いま出るとしたらどの版・タグか（リポジトリの中で。タグは取得済みであること）
./scripts/ci/next-version.sh release HEAD
./scripts/ci/next-version.sh nightly HEAD

# ワークフローの検査
actionlint

# インストーラを作る（makensis と wails3 が要る。版は VERSION ファイル、上書きは RELEASE_VERSION= / APP_VERSION=）
task windows:package
```

## 既知の制約

- Windows 実機でのインストール・起動は、この仕組みでは確かめていません（[Windows 実機スモーク手順](smoke-windows.md) §4.7）。
- Authenticode の署名がないため、SmartScreen の警告が出ます（更新用の minisign 署名とは別です）。
- リポジトリの設定（ブランチ保護など）は使えない（private・Free プラン）ので、「`main` へは `develop` からの PR だけ」はルール（運用）です。
- PR 本文の `Closes #N` が Issue を閉じるのは、既定ブランチ（`main`）へのマージだけです。`develop` 向けの PR では閉じないので、`develop` → `main` の PR の本文に `Closes #N` をまとめるか、手で閉じます。
- `schedule`（定時実行）は使いません。使うなら、既定ブランチ（`main`）にあるワークフローでしか動きません。
