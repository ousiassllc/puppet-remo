#!/usr/bin/env bash
# 古いナイトリーの Release を選ぶ。削除はしない（削除すべきタグを 1 行 1 つで標準出力に出すだけ）。
#
# 使い方: gh release list --limit 1000 --json tagName,createdAt,isPrerelease,isDraft \
#           --jq '.[] | [.tagName, .createdAt, .isPrerelease, .isDraft] | @tsv' \
#         | scripts/ci/prune-nightlies.sh [残す本数。既定 7]
#
# 標準入力は タグ<TAB>作成日時<TAB>isPrerelease<TAB>isDraft の行。
# 対象は「タグが nightly-YYYYMMDD-<短い sha 7〜40 桁の 16 進> に完全一致し、プレリリースで、下書きでない」Release だけ。
# （短い sha は git が 7 桁で曖昧なとき 7 桁より長くなるので、桁数は 7〜40 を許す。）
# 正式版（v<X.Y.Z>）や、形式の違う nightly-* は、何本あっても対象にしない。
# 作成日時の新しい順に並べ（同時刻はタグ名の降順）、残す本数より古いものを出す。
# 判定は awk ではなく bash の正規表現で行う（Ubuntu 22.04 の既定の mawk は {n} を正規表現として解釈しない）。
set -euo pipefail

keep=${1:-7}
[[ $keep =~ ^[1-9][0-9]*$ ]] || {
  echo "prune-nightlies: 残す本数は 1 以上の整数: '$keep'" >&2
  exit 1
}

tag_re='^nightly-[0-9]{8}-[0-9a-f]{7,40}$'

while IFS=$'\t' read -r tag created prerelease draft || [[ -n ${tag:-} ]]; do
  if [[ $tag =~ $tag_re && $prerelease == true && $draft == false ]]; then
    printf '%s\t%s\n' "$created" "$tag"
  fi
done |
  sort -t $'\t' -k1,1r -k2,2r |
  tail -n +"$((keep + 1))" |
  cut -f 2
