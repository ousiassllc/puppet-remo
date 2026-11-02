#!/usr/bin/env bash
# bun を、sudo なしで用意する。フロントエンド（static/）のパッケージマネージャ兼ランタイム。
#
# PATH に固定の版（bun_version）の bun が既にあれば何もしない。無ければ、版と SHA-256 を固定した公式のリリースを
# 一時領域に展開する。展開先は $GITHUB_PATH へ書く（GitHub Actions の外では export する行を出力する）。
# 版を上げるときは、bun_version と bun_sha256（リリースの SHASUMS256.txt の bun-linux-x64.zip の値）を一緒に変える。
set -euo pipefail

bun_version=1.3.14
bun_sha256=951ee2aee855f08595aeec6225226a298d3fea83a3dcd6465c09cbccdf7e848f

if command -v bun >/dev/null 2>&1 && command -v bunx >/dev/null 2>&1 && [[ $(bun --version) == "$bun_version" ]]; then
  echo "install-bun: bun $bun_version は PATH にある: $(command -v bun)"
  exit 0
fi

work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/bun.XXXXXX")
archive="bun-linux-x64.zip"
curl -fsSL --retry 3 -o "$work/$archive" "https://github.com/oven-sh/bun/releases/download/bun-v${bun_version}/${archive}"
echo "$bun_sha256  $work/$archive" | sha256sum --check --status || {
  echo "install-bun: $archive の SHA-256 が固定値と違う" >&2
  exit 1
}
if command -v unzip >/dev/null 2>&1; then
  unzip -q "$work/$archive" -d "$work"
else
  python3 -m zipfile -e "$work/$archive" "$work"
fi
bindir="$work/bun-linux-x64"
chmod +x "$bindir/bun"
# 公式のリリースは bun の 1 本だけで、bunx は無い。bun は argv[0] が bunx のときに bunx として動くので、シンボリックリンクを置く。
ln -s bun "$bindir/bunx"
[[ $("$bindir/bun" --version) == "$bun_version" ]] || {
  echo "install-bun: 展開した bun の版が $bun_version ではない" >&2
  exit 1
}

if [[ -n ${GITHUB_PATH:-} ]]; then
  echo "$bindir" >>"$GITHUB_PATH"
else
  echo "export PATH=\"$bindir:\$PATH\""
fi
echo "install-bun: bun $bun_version を $bindir に用意した"
