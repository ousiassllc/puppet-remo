#!/usr/bin/env bash
# gh（GitHub CLI）を、sudo なしで用意する。
#
# 既に PATH にあれば何もしない。無ければ、版と SHA-256 を固定した公式のリリースを一時領域に展開する。
# 展開先は $GITHUB_PATH へ書く（GitHub Actions の外では export する行を出力する）。
set -euo pipefail

gh_version=2.95.0
gh_sha256=25d1e4729e8808c9ed3d613e96ebd3f3e44446f2d368c89d878a71a36ddb3d8c

if command -v gh >/dev/null 2>&1; then
  echo "install-gh: gh は PATH にある: $(command -v gh)"
  exit 0
fi

work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/gh.XXXXXX")
archive="gh_${gh_version}_linux_amd64.tar.gz"
curl -fsSL --retry 3 -o "$work/$archive" "https://github.com/cli/cli/releases/download/v${gh_version}/${archive}"
echo "$gh_sha256  $work/$archive" | sha256sum --check --status || {
  echo "install-gh: $archive の SHA-256 が固定値と違う" >&2
  exit 1
}
tar -xzf "$work/$archive" -C "$work"
bindir="$work/gh_${gh_version}_linux_amd64/bin"

if [[ -n ${GITHUB_PATH:-} ]]; then
  echo "$bindir" >>"$GITHUB_PATH"
else
  echo "export PATH=\"$bindir:\$PATH\""
fi
echo "install-gh: gh $gh_version を $bindir に用意した"
