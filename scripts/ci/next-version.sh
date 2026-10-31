#!/usr/bin/env bash
# 配信（docs/operations/release.md）で、ある commit をどの版・タグで出すかを決める。
#
# 使い方: scripts/ci/next-version.sh <release|nightly> <sha>   （リポジトリの中で実行。タグを全部取得済みであること）
#
# 標準出力に key=value の行を出す（$GITHUB_OUTPUT にそのまま追記できる）。診断は標準エラー出力。
#   action       build（出す）または skip（何もしない。skip のときは action 以外を出さない）
#   tag          Release のタグ。正式版は v<X.Y.Z>、ナイトリーは nightly-YYYYMMDD-<短い sha>
#   version      インストーラに入る X.Y.Z（数字 3 つ。NSIS の VIProductVersion の制約）
#   app_version  exe の main.version。正式版は X.Y.Z、ナイトリーは X.Y.Z-nightly.YYYYMMDD+<短い sha>
#   asset        Release に添付するインストーラのファイル名
#   prerelease   true（ナイトリー）または false（正式版）
#   prev_tag     リリースノートの比較元にする直近の正式タグ（無ければ空）
#
# 版の決め方（正式版・ナイトリー共通。ナイトリーは「次に出る正式版」の X.Y.Z を使う）:
#   次の版 = 最新の正式タグ v<X.Y.Z> のパッチ +1。ただし、リポジトリ直下の VERSION（X.Y.Z）の値のほうが大きければ VERSION の値。
#   正式タグがまだ無ければ VERSION の値をそのまま使う。
#   メジャー・マイナーを上げるときは、VERSION を書き換える。
#
# 正式版（release）だけの規則:
#   - その commit に正式タグが既にある（再実行）なら、そのタグの版を再利用する（版を進めない）。
#   - その commit を含む正式タグが他にある（後の commit が先にリリースされた）なら skip する（版の順序を逆転させない）。
#
# ナイトリーの日付は commit の日付（UTC）にする。別の日に再実行しても、同じ commit は同じタグになる。
set -euo pipefail

die() {
  echo "next-version: $*" >&2
  exit 1
}

[[ $# -eq 2 ]] || die "使い方: $0 <release|nightly> <sha>"
mode=$1
case "$mode" in
  release | nightly) ;;
  *) die "mode は release か nightly: '$mode'" ;;
esac
sha=$(git rev-parse --verify --quiet "$2^{commit}") || die "commit が見つからない: $2"
short=$(git rev-parse --short=7 "$sha")

semver_re='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
tag_re='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'

# Windows のファイル版は各部分が 16 ビット（0〜65535）。
valid_semver() {
  [[ $1 =~ $semver_re ]] || return 1
  ((BASH_REMATCH[1] <= 65535 && BASH_REMATCH[2] <= 65535 && BASH_REMATCH[3] <= 65535))
}

# 正式タグ（v<X.Y.Z>）だけを、引数の git tag のオプションで列挙する。
release_tags() {
  { git tag --list 'v[0-9]*' "$@" || true; } | { grep -E "$tag_re" || true; } | sort -V
}

file_version=$(git show "$sha:VERSION" 2>/dev/null | tr -d '[:space:]') || file_version=""
valid_semver "$file_version" || die "VERSION は X.Y.Z（各 0〜65535 の数字）で書く: '$file_version'"

max_semver() {
  printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n 1
}

# 次に出す正式版の X.Y.Z。
next_release_version() {
  local latest
  latest=$(release_tags | tail -n 1)
  if [[ -z $latest ]]; then
    echo "$file_version"
    return
  fi
  latest=${latest#v}
  local major minor patch
  IFS=. read -r major minor patch <<<"$latest"
  max_semver "$major.$minor.$((patch + 1))" "$file_version"
}

if [[ $mode == release ]]; then
  own=$(release_tags --points-at "$sha" | tail -n 1)
  if [[ -n $own ]]; then
    echo "next-version: $short には $own が既にある。再実行として同じ版を使う" >&2
    tag=$own
    version=${own#v}
  else
    later=$(release_tags --contains "$sha" | head -n 1)
    if [[ -n $later ]]; then
      echo "next-version: $short を含む正式タグ $later が既にある。順序を逆転させないため何もしない" >&2
      echo "action=skip"
      exit 0
    fi
    version=$(next_release_version)
    tag=v$version
  fi
  app_version=$version
  asset="puppet-remo-$version-amd64-installer.exe"
  prerelease=false
else
  date=$(TZ=UTC git show -s --format=%cd --date=format-local:%Y%m%d "$sha")
  version=$(next_release_version)
  tag="nightly-$date-$short"
  app_version="$version-nightly.$date+$short"
  asset="puppet-remo-$version-nightly.$date.$short-amd64-installer.exe"
  prerelease=true
fi

prev_tag=$(release_tags | grep -vxF "$tag" | tail -n 1 || true)

echo "action=build"
echo "tag=$tag"
echo "version=$version"
echo "app_version=$app_version"
echo "asset=$asset"
echo "prerelease=$prerelease"
echo "prev_tag=$prev_tag"
