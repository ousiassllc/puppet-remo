# 環境構築

開発環境・CI/CD・Lint・Format・Git フックの構成をまとめる。コマンドは、このリポジトリの `Taskfile.yml`・`.github/workflows/`・`lefthook.yml` にあるものだけを書く。clone 後の最短の手順は [README](../../README.md#ビルドと開発) にある。

## 概要

| 項目 | 内容 |
|------|------|
| 言語 | Go（`go.mod` の `go` 行の版。CI は `go-version-file: go.mod` で同じ版を使う） |
| アプリシェル | Wails v3（`go.mod` で v3.0.0 に固定）。対象 OS は Windows のみ。純 Go（cgo なし）なので Linux / macOS からもクロスビルドできる |
| UI | HALT（HTMX・Atomic Design・Lit・templ）。詳細は [フロントエンド設計](../architecture/frontend.md) |
| テンプレート | templ（`go.mod` の版に固定。生成物の `_templ.go` はコミットする） |
| フロントエンドのビルド | bun（パッケージマネージャ兼ランタイム）、esbuild、Tailwind CSS CLI。成果物は `static/dist`（本番ビルドで `go:embed`） |
| タスクランナー | [Task](https://taskfile.dev/)（`Taskfile.yml`。Wails の共通タスクと Windows 向けのビルドは `build/Taskfile.yml`・`build/windows/Taskfile.yml`） |
| CI/CD | GitHub Actions。すべてのジョブをセルフホストの Linux ランナー（`[self-hosted, Linux, X64]`）で動かす |
| Lint | golangci-lint（v2。`.golangci.yml`） |
| Format | gofmt |
| Linterly | 未導入 |
| Git フック | Lefthook（`lefthook.yml`。[#41](https://github.com/ousiassllc/puppet-remo/issues/41)） |
| API ドキュメント | Huma が `/api/v1` の OpenAPI を出す設計（M2 以降。現状は未導入） |

## ディレクトリ構造

[アーキテクチャ設計](../architecture/overview.md) の「ディレクトリ構成」を参照。

## 開発環境セットアップ

### 必要なツール

| ツール | 版 | 用途と入れ方 |
|--------|----|-------------|
| Go | `go.mod` の `go` 行 | ビルド・テスト |
| [Task](https://taskfile.dev/) | CI は v3.53.1 | `task <name>`。CI の導入は `go install github.com/go-task/task/v3/cmd/task@v3.53.1` |
| [bun](https://bun.sh/) | `scripts/ci/install-bun.sh` の `bun_version`（現在 1.3.14）に固定 | フロントエンドの依存の導入とビルド。手元には公式の手順で入れる |
| templ | `go.mod` の版 | `task tools:templ`（`go install` で `go.mod` の版を `$(go env GOPATH)/bin` に入れる） |
| golangci-lint | CI は v2.15.0 | Lint。手元には [公式の手順](https://golangci-lint.run/) で入れる |
| Lefthook | v2.1.17 | Git フック。`task tools:lefthook` |
| `wails3` | `go.mod` の Wails の版 | `task dev`（開発）・`task package`（インストーラ）で使う。CI は `CGO_ENABLED=0 go install github.com/wailsapp/wails/v3/cmd/wails3@<go.mod の版>` |
| `makensis`（NSIS） | — | インストーラの作成だけに要る（`task package`） |

`$(go env GOPATH)/bin` を PATH に通しておく。

### 日常のコマンド

`task`（引数なし）でタスクの一覧を出す。主なもの:

| コマンド | 内容 |
|----------|------|
| `task build` / `task build:windows` | Go のビルド（ホスト OS / `GOOS=windows GOARCH=amd64` のクロスビルド） |
| `task vet` / `task vet:windows` | `go vet`（同上） |
| `task test` | `go test ./...` |
| `task test:race` | `go test -race ./...`（cgo が要る。CI の `go (linux)` ジョブと同じ検査） |
| `task check:imports` | レイヤー間の import 規則の検査（`scripts/check-imports.sh`） |
| `task templ:generate` | `.templ` から `_templ.go` を生成する（生成物はコミットする） |
| `task templ:watch` | `.templ` の変更で再生成し続ける（開発用） |
| `task frontend:install` | `static/` の依存を `bun.lock` のとおりに導入する（`bun install --frozen-lockfile`） |
| `task frontend:build` | `static/dist` を作る（`bun run build`。`frontend:install` を先に走らせる） |
| `task frontend:typecheck` | TypeScript の型検査（`bunx tsc -p .`） |
| `task build:production` / `task test:production` | `-tags production` のビルドと同梱物の契約テスト（どちらも `frontend:build` が先に走る） |
| `task gui:demo` | 偽の service でホスト GUI を `127.0.0.1:8899` に起動する（見た目の確認用） |
| `task windows:build` | Windows 向けの本番ビルド → `bin/puppet-remo.exe`（フロントエンドのビルド込み） |
| `task package` | NSIS インストーラを作る → `bin/puppet-remo-amd64-installer.exe`（`makensis` が要る。版は `VERSION` ファイル。`RELEASE_VERSION=` / `APP_VERSION=` で上書き） |
| `task dev` | `wails3 dev`（Windows）。別ターミナルで `task templ:watch` を動かす |
| `task tidy` | `go mod tidy` |
| `task ci` | CI と同じ検査を一通り実行する（下の「CI/CD」） |

Linux / macOS では、Windows 専用のコード（`//go:build windows`）を除いてビルド・テストできる。実機でしか確かめられない項目は [Windows 実機スモーク手順](../operations/smoke-windows.md) にまとめている。

### パッケージマネージャ（bun）

フロントエンド（`static/`）のパッケージマネージャは bun で、`npm`・`yarn`・`pnpm` は使わない（[#42](https://github.com/ousiassllc/puppet-remo/issues/42)）。

- ロックファイルは `static/bun.lock`。依存の導入は `bun install --frozen-lockfile`（ロックと `package.json` が食い違えば失敗する）。
- ビルドは `static/` で `bun run build`、型検査は `bunx tsc -p .`。いずれも `task frontend:*` から呼ぶ。
- CI は `./.github/actions/setup-bun`（中身は `scripts/ci/install-bun.sh`）で、版と SHA-256 を固定した公式のリリースを一時領域に展開する。sudo は使わない。版を上げるときは `bun_version` と `bun_sha256` を一緒に変える。

### 生成物

- `_templ.go`: templ が `.templ` から作る。コミットする。`task templ:check` は、生成したあとに `_templ.go` の差分が出ないことを確認する。
- `static/dist`: フロントエンドのビルド成果物。本番ビルド（`-tags production`）が `go:embed` で取り込む。開発ビルドはディスクから読む。

## CI/CD

GitHub Actions。すべてのジョブは、セルフホストの Linux ランナー（`[self-hosted, Linux, X64]`）で動く。Windows のランナーは使わない（Windows 向けのコードの検査は `GOOS=windows` のクロスコンパイルだけ。Windows 実機の確認は CI で行わない）。ツール（Go・bun・golangci-lint・templ）はランナーに入っている前提にせず、ジョブの中で導入する。Go・bun などのキャッシュは、ジョブ専用の一時領域（`RUNNER_TEMP`）に置く。

| ワークフロー | トリガー | 内容 |
|--------------|----------|------|
| `ci.yml`（name: CI） | `main`・`develop` への push、pull_request | 検査（下の表） |
| `nightly.yml` | `develop` への push | Windows 向け NSIS インストーラを作り、プレリリースとして GitHub Releases に公開する。最新 7 本だけ残す |
| `release.yml` | `main` の CI（`ci.yml`）が push で成功したとき（`workflow_run`） | 同じインストーラを正式版として公開する。タグ `v<X.Y.Z>` はこのワークフローが作る |

配信の版・タグの決め方と運用は [配信の運用](../operations/release.md) を参照。署名はしない。使うトークンは `GITHUB_TOKEN` だけ。

### ci.yml のジョブ

| ジョブ | 内容 |
|--------|------|
| `go (linux)` | gofmt の差分チェック、`go build ./...`、`go vet ./...`、`scripts/check-imports.sh`、`go test -race ./...` |
| `go (GOOS=windows build/vet)` | `GOOS=windows GOARCH=amd64` の `go build`・`go vet`・import 規則 |
| `frontend` | templ（`go.mod` の版）の導入 → `templ generate` の差分チェック → `bun install --frozen-lockfile` → `bun run build` → `bunx tsc -p .` → `go build -tags production ./...`（Linux と `GOOS=windows`）→ `go test -tags production ./static/...` |
| `lint` | golangci-lint v2.15.0（ホスト OS と `GOOS=windows`） |
| `release scripts` | 配信スクリプトのテスト（`scripts/ci/test-release-scripts.sh`）と `actionlint` |
| `installer (build only)` | インストーラをビルドだけして（公開しない）、ランナーでの道具（task・wails3・makensis・gh）の導入を確かめる |

`nightly.yml`・`release.yml`・`installer` ジョブは、composite action `.github/actions/build-installer`（Go・bun・task・wails3・makensis を導入し、`task windows:package` でインストーラを作って SHA-256 を付ける）を共有する。`.github/actions/setup-bun` は bun の導入だけをする。`scripts/ci/` には、版とタグの決定（`next-version.sh`）、リリースノート、公開、ナイトリーの片付け、`gh`・NSIS・bun の導入のスクリプトを置く。

### CI と同じ検査を手元で走らせる

`task ci` は、次を順に実行する。CI の `go test -race` の代わりに `go test`（`task test`）を走らせる点が CI と違う。

`fmt:check` → `templ:check` → `frontend:build` → `frontend:typecheck` → `build:production` → `test:production` → `build` → `vet` → `build:windows` → `vet:windows` → `check:imports` → `lint` → `test`

## Lint

golangci-lint v2。設定は `.golangci.yml`。

- `default: standard`（errcheck・govet・ineffassign・staticcheck・unused）に、`misspell` と `unconvert` を足している。
- `task lint` は `golangci-lint run ./...` を、ホスト OS と `GOOS=windows GOARCH=amd64` の両方で実行する。CI の `lint` ジョブも同じ 2 回を実行する。
- CI の版は v2.15.0（`ci.yml` の `golangci-lint-action` に固定）。手元の版が違うと結果が食い違うことがあるので、そろえる。
- Go 以外のフロントエンド（TypeScript）の検査は、型検査（`task frontend:typecheck`）だけで、ESLint・Biome は入れていない。

## Format

gofmt。`.golangci.yml` の `formatters` でも有効にしている。

- `task fmt`: `gofmt -w .` で整形する。
- `task fmt:check`: `gofmt -l .` に出力があれば失敗する（CI の gofmt ステップと同じ検査）。
- `.templ` の整形は templ が行う（`templ generate`）。`_templ.go` は生成物なので手で書き換えない。
- TypeScript・CSS の自動整形ツール（Prettier・Biome）は入れていない。

## Linterly

未導入。Linterly の設定ファイル・CI のステップ・Git フックは、いずれも無い。導入するかどうかが決まった時点で、この章を更新する。

## Git Hooks

Lefthook（[#41](https://github.com/ousiassllc/puppet-remo/issues/41)）。設定は `lefthook.yml`。clone したら、フックを入れる（clone ごとに一度）。手順は [README の「Git フック（Lefthook）」](../../README.md#git-フックlefthook) を参照。

```sh
task tools:lefthook   # lefthook v2.1.17 を go install する
task hooks:install    # lefthook install（.git/hooks にフックを入れる）
```

`lefthook.yml` の内容:

| フック | ジョブ（`piped: true`。順に実行し、1 つ落ちたら残りは飛ばす） | 実行する条件 |
|--------|--------------------------------------------------------------|--------------|
| pre-commit | `gofmt`（ステージした `*.go`） | `*.go` |
| | `golangci-lint run ./...`（ホスト OS） | `*.go`・`go.mod`・`go.sum`・`.golangci.yml` |
| | `scripts/check-imports.sh` | `*.go`・`go.mod`・`scripts/check-imports.sh` |
| | templ generate の差分チェック（ステージした内容と一致すること） | `*.templ`・`*_templ.go`・`go.mod` |
| | フロントエンドの型検査（`static/` で `bun install --frozen-lockfile && bunx tsc -p .`） | `static/src/*.ts`・`static/src/**/*.ts`・`static/tsconfig.json`・`static/package.json`・`static/bun.lock` |
| pre-push | `go test ./...` | 常に |

- フックを入れずに手元で実行するには `task hooks:pre-commit`（ステージ済みのファイルが対象）と `task hooks:pre-push` を使う。どちらも `lefthook run ... --no-auto-install` を呼ぶので、`.git/hooks` は書き換えない。
- pre-commit・pre-push のツール（golangci-lint・templ・bun）は PATH に要る。
- CI の残り（`go test -race`、`GOOS=windows` の build・vet・lint、本番ビルド）は pre-push では走らせない。`task ci` で確認する。
- フックは CI の代わりではない。push 前の早期検知で、CI は常に走る。

## Swagger/OpenAPI

設計では、Huma が `/api/v1` の JSON API の OpenAPI 3.1 を出す（`GET /api/v1/openapi.json`。[API 仕様](../api/endpoints.md) §6、[フロントエンド設計](../architecture/frontend.md)）。ただし現状の実装は Huma を使っておらず（`go.mod` に無い）、`/api/v1` は M2 以降に提供する（[アーキテクチャ設計](../architecture/overview.md) の「M1 実装での差分」）。

OpenAPI の範囲は `/api/v1` の JSON API だけで、MCP のツール定義は MCP の `tools/list` が正とする。Stoplight Elements などの UI や、`/swagger` のエンドポイント、`SWAGGER_ENABLED` の切り替え、spec 生成タスクは追加しない（Huma が OpenAPI を実行時に出すので、spec ファイルの生成と CI での差分チェックも要らない）。

## 改訂履歴

| 版 | 日付 | 変更内容 | 変更理由 |
|----|------|---------|---------|
| 1.0 | 2026-11-01 | 新規作成。Go・Wails v3・Task・GitHub Actions・golangci-lint・gofmt・templ・bun によるフロントエンドのビルド・Lefthook の現状を記載。Linterly は未導入と明記 | #40 |
| 1.1 | 2026-11-01 | フロントエンドの型検査の実行条件に `static/src/*.ts`（`static/src` 直下）を追加 | レビュー指摘（`**/` は直下に一致しない） |
