#!/usr/bin/env bash
# インストーラに minisign 形式の署名（<インストーラ>.minisig）を付ける（docs/operations/release.md「署名」）。
# 秘密鍵は GitHub Actions の Secret MINISIGN_SECRET_KEY（minisign の秘密鍵ファイルの中身）で、環境変数で受け取る。
# 鍵がパスワードで保護されているときは MINISIGN_PASSWORD（Secret）も渡す。Secret が無い（空の）ときは、通知を出して何もしない
# （署名なしのリリースは、アプリが適用を拒否するだけで、CI は緑のまま）。
#
# 使い方: scripts/ci/sign-installer.sh <main.version> <成果物のディレクトリ> <インストーラのファイル名>
#
# 鍵は一時ディレクトリへ 0600 で書き、終了時（失敗・中断を含む）に必ず消す。鍵・パスワードは標準出力・ログに出さない（set -x を使わない）。
# 署名は scripts/ci/minisign-sign（Go）が作る。
set -euo pipefail

die() {
  echo "sign-installer: $*" >&2
  exit 1
}

[[ $# -eq 3 ]] || die "使い方: $0 <main.version> <成果物のディレクトリ> <インストーラのファイル名>"
version=$1
dir=$2
asset=$3
[[ -f $dir/$asset ]] || die "インストーラが無い: $dir/$asset"

if [[ -z ${MINISIGN_SECRET_KEY:-} ]]; then
  echo "::notice::署名鍵（Secret MINISIGN_SECRET_KEY）が無いので、署名しません。このリリースはアプリが更新に使えません（docs/operations/release.md「オーナーの作業」）。"
  exit 0
fi

root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
umask 077
key=$tmp/minisign.key
printf '%s\n' "$MINISIGN_SECRET_KEY" >"$key"
chmod 600 "$key"

(cd "$root" && go run ./scripts/ci/minisign-sign -key "$key" -version "$version" -in "$dir/$asset" -out "$dir/$asset.minisig")
[[ -s $dir/$asset.minisig ]] || die "署名ファイルができていない"
