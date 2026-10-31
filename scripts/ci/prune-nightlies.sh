#!/usr/bin/env bash
# 古いナイトリーの Release を選ぶ。削除はしない（削除すべきタグを 1 行 1 つで標準出力に出すだけ）。
#
# 使い方: gh release list --limit 1000 --json tagName,createdAt,isPrerelease,isDraft \
#           --jq '.[] | [.tagName, .createdAt, .isPrerelease, .isDraft] | @tsv' \
#         | scripts/ci/prune-nightlies.sh [残す本数。既定 7]
#
# 標準入力は タグ<TAB>作成日時<TAB>isPrerelease<TAB>isDraft の行。
# 対象は「タグが nightly-YYYYMMDD-<短い sha 7 桁> に完全一致し、プレリリースで、下書きでない」Release だけ。
# 正式版（v<X.Y.Z>）や、形式の違う nightly-* は、何本あっても対象にしない。
# 作成日時の新しい順に並べ（同時刻はタグ名の降順）、残す本数より古いものを出す。
set -euo pipefail

keep=${1:-7}
[[ $keep =~ ^[1-9][0-9]*$ ]] || {
  echo "prune-nightlies: 残す本数は 1 以上の整数: '$keep'" >&2
  exit 1
}

awk -F '\t' '$1 ~ /^nightly-[0-9]{8}-[0-9a-f]{7}$/ && $3 == "true" && $4 == "false" { print $2 "\t" $1 }' |
  sort -t $'\t' -k1,1r -k2,2r |
  tail -n +"$((keep + 1))" |
  cut -f 2
