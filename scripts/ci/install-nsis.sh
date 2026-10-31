#!/usr/bin/env bash
# makensis（NSIS）を、sudo なしで用意する。
#
# 既に PATH にあれば何もしない。無ければ apt の deb（nsis・nsis-common）を取得して、一時領域に展開する
# （`apt-get download` と `dpkg -x` は root 権限が要らない）。展開先は次のステップから使えるように
# $GITHUB_PATH と $GITHUB_ENV（NSISDIR）へ書く。GitHub Actions の外では、export する行を出力する。
#
# apt が使えない runner では失敗する。その場合は runner に nsis を入れるか、ビルド用のイメージを使う。
set -euo pipefail

if command -v makensis >/dev/null 2>&1; then
  echo "install-nsis: makensis は PATH にある: $(command -v makensis)"
  exit 0
fi

command -v apt-get >/dev/null 2>&1 || {
  echo "install-nsis: makensis が無く、apt-get も無い。runner に NSIS を入れること" >&2
  exit 1
}

work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/nsis.XXXXXX")
(
  cd "$work"
  apt-get download nsis nsis-common
  mkdir root
  for deb in ./*.deb; do
    dpkg -x "$deb" root
  done
)

nsisdir="$work/root/usr/share/nsis"
bindir="$work/root/usr/bin"
[[ -x $bindir/makensis && -d $nsisdir ]] || {
  echo "install-nsis: 展開した deb に makensis が無い" >&2
  exit 1
}
NSISDIR="$nsisdir" "$bindir/makensis" -HDRINFO >/dev/null

if [[ -n ${GITHUB_PATH:-} && -n ${GITHUB_ENV:-} ]]; then
  echo "$bindir" >>"$GITHUB_PATH"
  echo "NSISDIR=$nsisdir" >>"$GITHUB_ENV"
else
  echo "export PATH=\"$bindir:\$PATH\" NSISDIR=\"$nsisdir\""
fi
echo "install-nsis: makensis を $bindir に用意した"
