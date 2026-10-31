#!/usr/bin/env bash
# インストーラを GitHub Release に公開する（gh と GH_TOKEN が要る）。再実行しても壊れない形にする。
#
# 使い方: scripts/ci/publish-release.sh <tag> <sha> <prerelease:true|false> <タイトル> <本文ファイル> <添付ファイル>...
#
# 手順: 下書きの Release を <sha> を指す形で作り（添付も同時）、最後に公開する。タグは公開の時点で作られるので、
# 途中で失敗してもタグは残らない。
#   - Release が無い     → 下書きで作成し、公開する
#   - 下書きが残っている → 添付を上書きして公開する（前回の失敗の続き）
#   - 公開済み           → 添付が全部そろっていれば何もしない。足りなければ上書きで足す
# 既存の Release のタグ・下書きの対象が <sha> と違うときは、別の commit の Release を壊さないため失敗にする。
set -euo pipefail

die() {
  echo "publish-release: $*" >&2
  exit 1
}

[[ $# -ge 6 ]] || die "使い方: $0 <tag> <sha> <prerelease:true|false> <タイトル> <本文ファイル> <添付ファイル>..."
tag=$1
sha=$2
prerelease=$3
title=$4
notes=$5
shift 5
files=("$@")
[[ $prerelease == true || $prerelease == false ]] || die "prerelease は true か false: '$prerelease'"
[[ -f $notes ]] || die "本文ファイルが無い: $notes"
names=()
for f in "${files[@]}"; do
  [[ -f $f ]] || die "添付ファイルが無い: $f"
  names+=("$(basename "$f")")
done

err=$(mktemp)
trap 'rm -f "$err"' EXIT
if view=$(gh release view "$tag" --json isDraft,targetCommitish,assets \
  --jq '[.isDraft, .targetCommitish, ([.assets[].name] | join(","))] | @tsv' 2>"$err"); then
  exists=true
elif grep -qi 'not found' "$err"; then
  exists=false
else
  cat "$err" >&2
  die "$tag の Release を調べられない"
fi

publish() {
  if [[ $prerelease == true ]]; then
    gh release edit "$tag" --draft=false
  else
    gh release edit "$tag" --draft=false --latest
  fi
}

if [[ $exists == false ]]; then
  if [[ $prerelease == true ]]; then
    gh release create "$tag" --draft --prerelease --target "$sha" --title "$title" --notes-file "$notes" "${files[@]}"
  else
    gh release create "$tag" --draft --target "$sha" --title "$title" --notes-file "$notes" "${files[@]}"
  fi
  publish
  echo "publish-release: $tag を公開した"
  exit 0
fi

IFS=$'\t' read -r is_draft target have <<<"$view"
if [[ $is_draft == true ]]; then
  [[ $target == "$sha" ]] || die "$tag の下書きは別の commit ($target) を指している。期待は $sha"
  gh release upload "$tag" "${files[@]}" --clobber
  publish
  echo "publish-release: 残っていた下書き $tag を公開した"
  exit 0
fi

# 公開済み。タグは取得済みの手元のタグで確かめる。
local_sha=$(git rev-parse --verify --quiet "refs/tags/$tag^{commit}") || die "$tag は公開済みだが、手元にタグが無い（別の実行が作った可能性）。取得し直して再実行する"
[[ $local_sha == "$sha" ]] || die "$tag は別の commit ($local_sha) を指している。期待は $sha"
missing=()
for n in "${names[@]}"; do
  [[ ",$have," == *",$n,"* ]] || missing+=("$n")
done
if ((${#missing[@]} == 0)); then
  echo "publish-release: $tag は公開済みで、添付もそろっている。何もしない"
  exit 0
fi
gh release upload "$tag" "${files[@]}" --clobber
echo "publish-release: 公開済みの $tag に足りない添付 (${missing[*]}) を足した"
