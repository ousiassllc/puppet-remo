#!/usr/bin/env bash
# Release の本文（Markdown）を標準出力に出す。
#
# 使い方: scripts/ci/release-notes.sh <release|nightly> <tag> <sha> [比較元の正式タグ]
# 変更の一覧は、比較元のタグから <sha> までのコミット（マージコミットを除く）の件名。最大 50 件。
set -euo pipefail

[[ $# -ge 3 && $# -le 4 ]] || {
  echo "使い方: $0 <release|nightly> <tag> <sha> [prev_tag]" >&2
  exit 1
}
kind=$1
tag=$2
sha=$3
prev=${4:-}

echo "## puppet-remo $tag"
echo
case "$kind" in
  release) echo "正式版（main）です。" ;;
  nightly) echo "ナイトリー（develop）です。試験用のプレリリースで、正式版ではありません。" ;;
  *)
    echo "kind は release か nightly: '$kind'" >&2
    exit 1
    ;;
esac
echo
echo "- commit: \`$sha\`"
echo "- インストーラ: \`puppet-remo-*-amd64-installer.exe\`（SHA-256 は \`.sha256\` ファイル）"
echo "- Authenticode 署名はありません。初回の実行で SmartScreen の警告が出ます（README を参照）"
echo
echo "### 変更"
echo
range=$sha
[[ -n $prev ]] && range="$prev..$sha"
git log --no-merges -n 50 --format='- %s (%h)' "$range"
