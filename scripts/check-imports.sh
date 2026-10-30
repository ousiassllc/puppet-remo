#!/usr/bin/env bash
# レイヤー間の import 規則を検査する（docs/architecture/overview.md「レイヤー構造」、
# docs/components/overview.md §1「依存の規則」）。
#
# internal/<layer>/... の各パッケージが、直接 import してよい他レイヤーだけを import していることを
# `go list` で確認する。テストファイルの import も対象にする。
# 同じレイヤー内（例: platform → platform/windows）は常に許可する。
# まだ存在しないレイヤーは検査しない。
#
# 使い方: scripts/check-imports.sh   （GOOS=windows でも実行できる）
set -euo pipefail

cd "$(dirname "$0")/.."
module=$(go list -m)
prefix="${module}/internal/"

# レイヤー: 直接 import してよい他レイヤー（空白区切り。なしは "-"）
rules=(
  "domain:-"
  "repository:domain"
  "platform:domain"
  "tailnet:domain"
  "openai:domain"
  "typesafe:domain"
  "service:domain repository platform tailnet openai typesafe"
  "mcp:domain service"
  "ws:domain service"
  "web:domain service"
  "router:domain service web mcp ws"
  "app:domain repository service router web mcp ws platform tailnet"
)

status=0
for rule in "${rules[@]}"; do
  layer=${rule%%:*}
  allowed=" ${rule#*:} "
  [[ -d "internal/${layer}" ]] || continue

  # 各行: <パッケージ> <import>...（本体・内部テスト・外部テストの import をまとめる）
  while read -r pkg imports; do
    for imp in $imports; do
      [[ "$imp" == "${prefix}"* ]] || continue
      target=${imp#"${prefix}"}
      target=${target%%/*}
      [[ "$target" == "$layer" ]] && continue
      if [[ "$allowed" != *" ${target} "* ]]; then
        echo "import 規則違反: ${pkg#"${module}"/} は internal/${target} を import している（${layer} から ${target} への依存は禁止）" >&2
        status=1
      fi
    done
  done < <(go list -e -f '{{.ImportPath}} {{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}' "./internal/${layer}/..." 2>/dev/null)
done

if [[ $status -eq 0 ]]; then
  echo "import 規則: OK"
fi
exit $status
