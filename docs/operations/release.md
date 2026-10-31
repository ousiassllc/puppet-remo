# 配信（ナイトリーと正式版）

Windows 向けの NSIS インストーラを、GitHub Releases に自動で公開する仕組みの運用手順です。ブランチは 2 本です。

| ブランチ | 公開されるもの | ワークフロー |
|----------|----------------|--------------|
| `develop` | ナイトリー（プレリリース） | `.github/workflows/nightly.yml` |
| `main` | 正式版 | `.github/workflows/release.yml` |

すべてのジョブはセルフホストの Linux ランナー（`[self-hosted, Linux, X64]`）で動きます。Windows の runner は使いません（`GOOS=windows` のクロスビルドと、NSIS の `makensis` を Linux で動かします）。使うトークンは `GITHUB_TOKEN`（`contents: write`）だけで、新しい Secret はありません。コード署名はしません（[NFR-O07](../requirements/non-functional.md)。SmartScreen の警告は [README](../../README.md#smartscreen-の警告について) を参照）。

## 開発の流れ

- 普段の PR は `develop` に向けます。
- `main` に入れるのは、`develop` → `main` の PR だけです（マージはマージコミット方式）。`main` への push は、すべて正式版のリリースになります。
- `develop` への push は、すべてナイトリーになります。

## ナイトリー（develop への push）

1. `develop` に push されると、`nightly.yml` が起動します。
2. インストーラを作り、プレリリースとして公開します。CI（`ci.yml`）の成功は待ちません。
3. タグは `nightly-YYYYMMDD-<短い sha 7 桁>` です。日付は commit の日付（UTC）で、同じ commit は何度実行しても同じタグになります。同じ日に複数回出ても、sha で区別されます。
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

リポジトリは private なので、Release のダウンロードにはリポジトリの読み取り権限が要ります（`gh release download <tag>`）。

## ビルドの中身

`.github/actions/build-installer`（composite action）が、ジョブの中で次を行います。

1. Go・Node を setup アクションで入れる（キャッシュは `RUNNER_TEMP`）。
2. `task`（版固定）と `wails3`（`go.mod` の版）を `go install` で入れる。`wails3` は `CGO_ENABLED=0` で入れる（cgo が有効だと Linux では gtk4・webkitgtk-6.0 が要る）。
3. `makensis` が PATH に無ければ、`apt-get download nsis nsis-common` と `dpkg -x` で `RUNNER_TEMP` に展開する（sudo 不要。[`scripts/ci/install-nsis.sh`](../../scripts/ci/install-nsis.sh)）。runner に apt が無いときは失敗するので、NSIS を runner に入れること。
4. `task windows:package RELEASE_VERSION=<X.Y.Z> APP_VERSION=<main.version>` を実行する。
5. インストーラを改名し、SHA-256 を作る。exe に `main.version` が入っていることも確かめる。

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
- 署名がないため、SmartScreen の警告が出ます。
- リポジトリの設定（ブランチ保護など）は使えない（private・Free プラン）ので、「`main` へは `develop` からの PR だけ」はルール（運用）です。
- PR 本文の `Closes #N` が Issue を閉じるのは、既定ブランチ（`main`）へのマージだけです。`develop` 向けの PR では閉じないので、`develop` → `main` の PR の本文に `Closes #N` をまとめるか、手で閉じます。
- `schedule`（定時実行）は使いません。使うなら、既定ブランチ（`main`）にあるワークフローでしか動きません。
