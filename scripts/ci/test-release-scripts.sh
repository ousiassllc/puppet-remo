#!/usr/bin/env bash
# 配信のスクリプト（next-version.sh・prune-nightlies.sh・release-notes.sh・publish-release.sh）のテスト。
# 一時ディレクトリに偽のリポジトリ・偽のタグ・偽の gh を作って確かめる。ネットワークも実際の Release も使わない。
#
# 使い方: scripts/ci/test-release-scripts.sh   （失敗が 1 つでもあれば終了コードは 1）
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# 利用者の git 設定（hooks・署名など）の影響を受けない。
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
unset GIT_DIR GIT_WORK_TREE

passed=0
failed=0

assert_eq() { # 名前 期待 実際
  if [[ $2 == "$3" ]]; then
    passed=$((passed + 1))
  else
    failed=$((failed + 1))
    printf 'FAIL: %s\n  期待: %s\n  実際: %s\n' "$1" "$2" "$3" >&2
  fi
}

assert_contains() { # 名前 全体 部分
  if [[ $2 == *"$3"* ]]; then
    passed=$((passed + 1))
  else
    failed=$((failed + 1))
    printf 'FAIL: %s\n  含まれるべき: %s\n  実際: %s\n' "$1" "$3" "$2" >&2
  fi
}

assert_not_contains() { # 名前 全体 部分
  if [[ $2 != *"$3"* ]]; then
    passed=$((passed + 1))
  else
    failed=$((failed + 1))
    printf 'FAIL: %s\n  含まれてはいけない: %s\n  実際: %s\n' "$1" "$3" "$2" >&2
  fi
}

# 出力（key=value の行）から key の値を取る。
kv() { sed -n "s/^$1=//p" <<<"$2"; }

new_repo() { # 名前 VERSION の中身 → リポジトリのパスを出力
  local d=$tmp/$1
  mkdir -p "$d"
  git -C "$d" init -q -b main
  git -C "$d" config user.name test
  git -C "$d" config user.email test@example.com
  printf '%s\n' "$2" >"$d/VERSION"
  git -C "$d" add VERSION
  GIT_AUTHOR_DATE=2026-10-30T00:00:00Z GIT_COMMITTER_DATE=2026-10-30T00:00:00Z git -C "$d" commit -q -m "chore: init"
  echo "$d"
}

commit() { # リポジトリ 件名 [日時] → commit の sha を出力
  local d=$1 date=${3:-2026-10-31T10:00:00Z}
  echo "$2" >>"$d/log.txt"
  git -C "$d" add -A
  GIT_AUTHOR_DATE=$date GIT_COMMITTER_DATE=$date git -C "$d" commit -q -m "$2"
  git -C "$d" rev-parse HEAD
}

short() { git -C "$1" rev-parse --short=7 "$2"; }

nv() { # リポジトリ mode sha → 標準出力（rc は $rc）
  out=$(cd "$1" && "$here/next-version.sh" "$2" "$3" 2>/dev/null) && rc=0 || rc=$?
}

# ---------------------------------------------------------------- 正式版（release）

# 初回: タグが無ければ VERSION をそのまま使う（+1 しない）。
r=$(new_repo first 0.1.0)
a=$(commit "$r" "feat: a")
nv "$r" release "$a"
assert_eq "初回: rc" 0 "$rc"
assert_eq "初回: action" build "$(kv action "$out")"
assert_eq "初回: tag" v0.1.0 "$(kv tag "$out")"
assert_eq "初回: version" 0.1.0 "$(kv version "$out")"
assert_eq "初回: app_version" 0.1.0 "$(kv app_version "$out")"
assert_eq "初回: asset" puppet-remo-0.1.0-amd64-installer.exe "$(kv asset "$out")"
assert_eq "初回: prerelease" false "$(kv prerelease "$out")"
assert_eq "初回: prev_tag は空" "" "$(kv prev_tag "$out")"

# 再実行: その commit に正式タグが既にあれば、同じ版を使う（版が進まない）。
git -C "$r" tag v0.1.0 "$a"
nv "$r" release "$a"
assert_eq "再実行: action" build "$(kv action "$out")"
assert_eq "再実行: tag" v0.1.0 "$(kv tag "$out")"
assert_eq "再実行: prev_tag は自分自身を含まない" "" "$(kv prev_tag "$out")"

# 次の push: 最新タグのパッチ +1。
b=$(commit "$r" "fix: b")
nv "$r" release "$b"
assert_eq "次の push: tag" v0.1.1 "$(kv tag "$out")"
assert_eq "次の push: prev_tag" v0.1.0 "$(kv prev_tag "$out")"

# VERSION の方が大きい: そちらを使う（マイナー・メジャーを上げる）。
echo 0.2.0 >"$r/VERSION"
c=$(commit "$r" "chore: bump minor")
nv "$r" release "$c"
assert_eq "VERSION が大きい（マイナー）: tag" v0.2.0 "$(kv tag "$out")"
echo 1.0.0 >"$r/VERSION"
d=$(commit "$r" "chore: bump major")
nv "$r" release "$d"
assert_eq "VERSION が大きい（メジャー）: tag" v1.0.0 "$(kv tag "$out")"
assert_eq "VERSION が大きい: installer の版" 1.0.0 "$(kv version "$out")"

# VERSION が +1 の候補と同じ: その版。
echo 0.1.1 >"$r/VERSION"
e=$(commit "$r" "chore: same as candidate")
nv "$r" release "$e"
assert_eq "VERSION が候補と同じ: tag" v0.1.1 "$(kv tag "$out")"

# VERSION が最新タグより小さい: 無視してパッチ +1。
echo 0.0.9 >"$r/VERSION"
f=$(commit "$r" "chore: lower")
nv "$r" release "$f"
assert_eq "VERSION が小さい: tag" v0.1.1 "$(kv tag "$out")"

# 同時実行・順序: 後の commit B が先にリリースされたら、先の commit A は skip（版の順序を逆転させない）。
r=$(new_repo order 0.1.0)
a=$(commit "$r" "feat: A")
b=$(commit "$r" "feat: B")
nv "$r" release "$b"
assert_eq "順序(B 先): B は v0.1.0" v0.1.0 "$(kv tag "$out")"
git -C "$r" tag v0.1.0 "$b"
nv "$r" release "$a"
assert_eq "順序(B 先): A は skip" skip "$(kv action "$out")"
assert_eq "順序(B 先): skip のとき他のキーを出さない" 1 "$(wc -l <<<"$out" | tr -d ' ')"
# 逆順: A が先にリリースされたら、B は次の版。
r=$(new_repo order2 0.1.0)
a=$(commit "$r" "feat: A")
b=$(commit "$r" "feat: B")
nv "$r" release "$a"
assert_eq "順序(A 先): A は v0.1.0" v0.1.0 "$(kv tag "$out")"
git -C "$r" tag v0.1.0 "$a"
nv "$r" release "$b"
assert_eq "順序(A 先): B は v0.1.1" v0.1.1 "$(kv tag "$out")"
# 同時に 2 本が、どちらもタグ作成前に計算した場合は同じタグを出す（二重の公開は publish-release.sh が防ぐ。下で確かめる）。
r=$(new_repo simul 0.1.0)
a=$(commit "$r" "feat: A")
b=$(commit "$r" "feat: B")
nv "$r" release "$a"
tag_a=$(kv tag "$out")
nv "$r" release "$b"
assert_eq "同時計算: 同じタグ" "$tag_a" "$(kv tag "$out")"

# 数字として比較する（v0.9.0 < v0.10.0）。ナイトリーのタグ・形式の違うタグは数えない。
r=$(new_repo sortv 0.1.0)
a=$(commit "$r" "feat: a")
git -C "$r" tag v0.9.0 "$a"
git -C "$r" tag v0.10.0 "$a"
git -C "$r" tag v0.99.0-rc1 "$a"
git -C "$r" tag v0.50 "$a"
git -C "$r" tag nightly-20261031-abc1234 "$a"
b=$(commit "$r" "fix: b")
nv "$r" release "$b"
assert_eq "タグの比較は数字: tag" v0.10.1 "$(kv tag "$out")"

# 正しくない VERSION は失敗する。
for bad in 0.1 v0.1.0 0.1.0-rc1 70000.0.0 0.01.0 ""; do
  r=$(new_repo "bad-${bad:-empty}" "$bad")
  a=$(commit "$r" "feat: a")
  nv "$r" release "$a"
  assert_eq "不正な VERSION '$bad' は失敗" 1 "$rc"
done
r=$(new_repo noversion 0.1.0)
git -C "$r" rm -q VERSION
a=$(commit "$r" "chore: remove VERSION")
nv "$r" release "$a"
assert_eq "VERSION が無いと失敗" 1 "$rc"
nv "$r" bogus "$a"
assert_eq "mode が不正だと失敗" 1 "$rc"

# ---------------------------------------------------------------- ナイトリー（nightly）

r=$(new_repo night 0.1.0)
a=$(commit "$r" "feat: a" 2026-10-31T10:00:00Z)
s=$(short "$r" "$a")
nv "$r" nightly "$a"
assert_eq "ナイトリー(タグ無し): tag" "nightly-20261031-$s" "$(kv tag "$out")"
assert_eq "ナイトリー(タグ無し): version" 0.1.0 "$(kv version "$out")"
assert_eq "ナイトリー: app_version" "0.1.0-nightly.20261031+$s" "$(kv app_version "$out")"
assert_eq "ナイトリー: asset" "puppet-remo-0.1.0-nightly.20261031.$s-amd64-installer.exe" "$(kv asset "$out")"
assert_eq "ナイトリー: prerelease" true "$(kv prerelease "$out")"
assert_eq "ナイトリー: action" build "$(kv action "$out")"

# 正式タグの後は「次に出る正式版」の版。
git -C "$r" tag v0.1.0 "$a"
b=$(commit "$r" "feat: b" 2026-10-31T11:00:00Z)
nv "$r" nightly "$b"
assert_eq "ナイトリー(v0.1.0 の後): version" 0.1.1 "$(kv version "$out")"
assert_eq "ナイトリー(v0.1.0 の後): prev_tag" v0.1.0 "$(kv prev_tag "$out")"
echo 0.3.0 >"$r/VERSION"
c=$(commit "$r" "chore: bump" 2026-10-31T12:00:00Z)
nv "$r" nightly "$c"
assert_eq "ナイトリー(VERSION が大きい): version" 0.3.0 "$(kv version "$out")"

# 同じ日に複数: タグは sha で区別される。同じ commit は何度でも同じタグ（日付は commit の日付）。
sb=$(short "$r" "$b")
sc=$(short "$r" "$c")
nv "$r" nightly "$b"
tag_b1=$(kv tag "$out")
nv "$r" nightly "$c"
tag_c=$(kv tag "$out")
nv "$r" nightly "$b"
assert_eq "同日: b のタグ" "nightly-20261031-$sb" "$tag_b1"
assert_eq "同日: c のタグ" "nightly-20261031-$sc" "$tag_c"
assert_eq "同日: 別の commit は別のタグ" 1 "$([[ $tag_b1 != "$tag_c" ]] && echo 1 || echo 0)"
assert_eq "同日: 同じ commit の再実行は同じタグ" "$tag_b1" "$(kv tag "$out")"

# 日付は UTC の commit 日付（タイムゾーンをまたぐ）。
d1=$(commit "$r" "feat: tz1" 2020-01-02T23:59:59+09:00)
d2=$(commit "$r" "feat: tz2" 2020-01-02T23:30:00-05:00)
nv "$r" nightly "$d1"
assert_contains "UTC 日付(+09:00 → 前日)" "$(kv tag "$out")" nightly-20200102-
nv "$r" nightly "$d2"
assert_contains "UTC 日付(-05:00 → 翌日)" "$(kv tag "$out")" nightly-20200103-

# ---------------------------------------------------------------- 古いナイトリーの片付け

# 入力の行: タグ<TAB>作成日時<TAB>isPrerelease<TAB>isDraft
tsv() { printf '%s\t%s\t%s\t%s\n' "$@"; }
prune() { printf '%s' "$1" | "$here/prune-nightlies.sh" "${2:-7}"; }

list=""
for i in 01 02 03 04 05 06 07 08 09 10; do
  list+=$(tsv "nightly-202610$i-${i}a${i}b0" "2026-10-${i}T01:00:00Z" true false)$'\n'
done
# 正式版（最も古くても対象外）・形式の違う nightly-*・プレリリースでない nightly・下書きの nightly。
list+=$(tsv v0.1.0 2026-01-01T00:00:00Z false false)$'\n'
list+=$(tsv v0.0.9 2025-12-01T00:00:00Z false false)$'\n'
list+=$(tsv v0.2.0-rc1 2025-11-01T00:00:00Z true false)$'\n'
list+=$(tsv nightly-bad 2025-10-01T00:00:00Z true false)$'\n'
list+=$(tsv nightly-20250101-abcdef0 2025-01-01T00:00:00Z false false)$'\n'
list+=$(tsv nightly-20250102-abcdef1 2025-01-02T00:00:00Z true true)$'\n'
got=$(prune "$list")
assert_eq "片付け: 古い 3 本だけ" $'nightly-20261003-03a03b0\nnightly-20261002-02a02b0\nnightly-20261001-01a01b0' "$got"
assert_not_contains "片付け: 正式版 v0.1.0 は対象外" "$got" v0.1.0
assert_not_contains "片付け: 正式版 v0.0.9 は対象外" "$got" v0.0.9
assert_not_contains "片付け: プレリリースの v0.2.0-rc1 は対象外" "$got" rc1
assert_not_contains "片付け: 形式の違う nightly-bad は対象外" "$got" nightly-bad
assert_not_contains "片付け: プレリリースでない nightly は対象外" "$got" abcdef0
assert_not_contains "片付け: 下書きの nightly は対象外" "$got" abcdef1
assert_eq "片付け: 残す本数を変える(3 本残す → 7 本削除)" 7 "$(prune "$list" 3 | wc -l | tr -d ' ')"

# 短い sha は、7 桁で曖昧なとき 7 桁より長くなる（git rev-parse --short=7）。8・12・40 桁の古いナイトリーも対象にする。
# 6 桁・41 桁・16 進でない文字は対象にしない。
long=""
long+=$(tsv nightly-20260901-1234567 2026-09-01T00:00:00Z true false)$'\n'
long+=$(tsv nightly-20260902-12345678 2026-09-02T00:00:00Z true false)$'\n'
long+=$(tsv nightly-20260903-0123456789ab 2026-09-03T00:00:00Z true false)$'\n'
long+=$(tsv "nightly-20260904-$(printf 'a%.0s' {1..40})" 2026-09-04T00:00:00Z true false)$'\n'
long+=$(tsv nightly-20260905-123456 2026-09-05T00:00:00Z true false)$'\n'
long+=$(tsv "nightly-20260906-$(printf 'a%.0s' {1..41})" 2026-09-06T00:00:00Z true false)$'\n'
long+=$(tsv nightly-20260907-123456g 2026-09-07T00:00:00Z true false)$'\n'
long+=$(tsv nightly-20260908-ABCDEF0 2026-09-08T00:00:00Z true false)$'\n'
long+=$(tsv nightly-2026090-1234567 2026-09-09T00:00:00Z true false)$'\n'
# 対象は 4 本（7・8・12・40 桁）。1 本だけ残す → 古い 3 本が出る。
assert_eq "片付け: 7〜40 桁の sha を対象にする" \
  $'nightly-20260903-0123456789ab\nnightly-20260902-12345678\nnightly-20260901-1234567' \
  "$(prune "$long" 1)"
assert_eq "片付け: 長い sha の最新を残す(40 桁の sha は残る側)" 3 "$(prune "$long" 1 | wc -l | tr -d ' ')"
assert_eq "片付け: 範囲外のタグは何本あっても消さない" "" "$(prune "$(grep -E '2026090[5-9]' <<<"$long")" 1)"
# 最後の行に改行が無い入力でも、その行を落とさない。
assert_eq "片付け: 末尾に改行が無くても処理する" "nightly-20260901-1234567" \
  "$(printf 'nightly-20260901-1234567\t2026-09-01T00:00:00Z\ttrue\tfalse\nnightly-20260902-1234567\t2026-09-02T00:00:00Z\ttrue\tfalse' | "$here/prune-nightlies.sh" 1)"

# 7 本以下なら何も消さない。正式版しか無くても何も消さない。
assert_eq "片付け: ちょうど 7 本は消さない" "" "$(prune "$(head -n 7 <<<"$list")")"
assert_eq "片付け: 正式版だけなら何も消さない" "" "$(prune "$(tsv v0.1.0 2026-01-01T00:00:00Z false false)
$(tsv v0.1.1 2026-02-01T00:00:00Z false false)")"
assert_eq "片付け: 空の入力" "" "$(prune "")"

# 作成日時が同じとき: タグ名の降順で新しい側を残す（結果が毎回同じ）。
same=""
for i in 1 2 3 4 5 6 7 8 9; do
  same+=$(tsv "nightly-20261031-aaaaaa$i" 2026-10-31T01:00:00Z true false)$'\n'
done
assert_eq "片付け: 同時刻はタグ名の降順で残す" $'nightly-20261031-aaaaaa2\nnightly-20261031-aaaaaa1' "$(prune "$same")"

out=$(prune "$list" 0 2>/dev/null) && rc=0 || rc=$?
assert_eq "片付け: 残す本数 0 は失敗" 1 "$rc"
out=$(prune "$list" abc 2>/dev/null) && rc=0 || rc=$?
assert_eq "片付け: 残す本数が数字でないと失敗" 1 "$rc"

# ---------------------------------------------------------------- リリースノート

r=$(new_repo notes 0.1.0)
a=$(commit "$r" "feat: old change")
git -C "$r" tag v0.1.0 "$a"
b=$(commit "$r" "fix: new change")
notes=$(cd "$r" && "$here/release-notes.sh" release v0.1.1 "$b" v0.1.0)
assert_contains "ノート: タグ" "$notes" "puppet-remo v0.1.1"
assert_contains "ノート: 新しい変更" "$notes" "fix: new change"
assert_not_contains "ノート: 比較元以前の変更は含めない" "$notes" "feat: old change"
notes=$(cd "$r" && "$here/release-notes.sh" nightly "nightly-x" "$b" "")
assert_contains "ノート(nightly): 比較元なしは全部" "$notes" "feat: old change"
assert_contains "ノート(nightly): プレリリースの説明" "$notes" "ナイトリー"

# ---------------------------------------------------------------- 公開（偽の gh）

# 偽の gh: 呼ばれた引数を $GH_LOG に残す。`release view` の応答は $FAKE_VIEW（none|draft|published|error）で切り替える。
mkdir -p "$tmp/fakebin"
cat >"$tmp/fakebin/gh" <<'FAKE'
#!/usr/bin/env bash
echo "$*" >>"$GH_LOG"
if [[ $1 == release && $2 == view ]]; then
  case "$FAKE_VIEW" in
    none) echo "release not found" >&2; exit 1 ;;
    error) echo "HTTP 500: server error" >&2; exit 1 ;;
    draft) printf 'true\t%s\t%s\n' "$FAKE_TARGET" "" ;;
    published) printf 'false\t%s\t%s\n' "$FAKE_TARGET" "$FAKE_ASSETS" ;;
  esac
fi
exit 0
FAKE
chmod +x "$tmp/fakebin/gh"

r=$(new_repo pub 0.1.0)
a=$(commit "$r" "feat: a")
b=$(commit "$r" "feat: b")
echo notes >"$tmp/notes.md"
echo exe >"$tmp/app.exe"
echo sum >"$tmp/app.exe.sha256"
export GH_LOG="$tmp/gh.log"

# publish <FAKE_VIEW> <sha> <prerelease> → $out, $rc, $GH_LOG
publish() {
  : >"$GH_LOG"
  export FAKE_VIEW=$1 FAKE_TARGET=${FAKE_TARGET:-$2} FAKE_ASSETS=${FAKE_ASSETS:-}
  out=$(cd "$r" && PATH="$tmp/fakebin:$PATH" "$here/publish-release.sh" v0.1.0 "$2" "$3" "title" "$tmp/notes.md" "$tmp/app.exe" "$tmp/app.exe.sha256" 2>&1) && rc=0 || rc=$?
  log=$(<"$GH_LOG")
}

# Release が無い: 下書きで作成 → 公開（正式版は latest、ナイトリーはプレリリース）。
publish none "$a" false
assert_eq "公開(新規・正式版): rc" 0 "$rc"
assert_contains "公開(新規・正式版): 下書きで作成" "$log" "release create v0.1.0 --draft --target $a"
assert_not_contains "公開(新規・正式版): プレリリースにしない" "$log" "--prerelease"
assert_contains "公開(新規・正式版): 公開して latest" "$log" "release edit v0.1.0 --draft=false --latest"
publish none "$a" true
assert_contains "公開(新規・ナイトリー): プレリリースで作成" "$log" "release create v0.1.0 --draft --prerelease --target $a"
assert_contains "公開(新規・ナイトリー): 公開" "$log" "release edit v0.1.0 --draft=false"
assert_not_contains "公開(新規・ナイトリー): latest にしない" "$log" "--latest"

# 前回の失敗の続き: 下書きが残っていて、同じ commit なら添付を上書きして公開する。
publish draft "$a" false
assert_eq "公開(下書きの続き): rc" 0 "$rc"
assert_contains "公開(下書きの続き): 添付を上書き" "$log" "release upload v0.1.0"
assert_contains "公開(下書きの続き): --clobber" "$log" "--clobber"
assert_not_contains "公開(下書きの続き): 作り直さない" "$log" "release create"
assert_contains "公開(下書きの続き): 公開" "$log" "release edit v0.1.0 --draft=false --latest"
# 下書きが別の commit を指していたら失敗（変更しない）。
FAKE_TARGET=$b publish draft "$a" false
assert_eq "公開(別 commit の下書き): 失敗" 1 "$rc"
assert_not_contains "公開(別 commit の下書き): 何も変更しない" "$log" "release edit"
unset FAKE_TARGET

# 公開済み: タグが手元にあって同じ commit、添付もそろっていれば何もしない（再実行）。
git -C "$r" tag v0.1.0 "$a"
FAKE_ASSETS="app.exe,app.exe.sha256" publish published "$a" false
assert_eq "公開(公開済み・そろっている): rc" 0 "$rc"
assert_not_contains "公開(公開済み・そろっている): 変更しない(upload)" "$log" "release upload"
assert_not_contains "公開(公開済み・そろっている): 変更しない(create)" "$log" "release create"
assert_not_contains "公開(公開済み・そろっている): 変更しない(edit)" "$log" "release edit"
# 添付が足りなければ足す。
FAKE_ASSETS="app.exe" publish published "$a" false
assert_eq "公開(公開済み・不足): rc" 0 "$rc"
assert_contains "公開(公開済み・不足): 足りない添付を足す" "$log" "release upload v0.1.0"
# 同時実行で別の commit が先に同じタグを公開していたら失敗する（二重の公開をしない）。
FAKE_ASSETS="app.exe,app.exe.sha256" publish published "$b" false
assert_eq "公開(別 commit が公開済み): 失敗" 1 "$rc"
assert_not_contains "公開(別 commit が公開済み): 何も変更しない" "$log" "release upload"
# 公開済みなのに手元にタグが無ければ失敗する。
git -C "$r" tag -d v0.1.0 >/dev/null
FAKE_ASSETS="app.exe,app.exe.sha256" publish published "$a" false
assert_eq "公開(公開済み・手元にタグ無し): 失敗" 1 "$rc"

# release view が not found 以外で失敗したら、作成に進まず失敗する。
publish error "$a" false
assert_eq "公開(view の失敗): 失敗" 1 "$rc"
assert_not_contains "公開(view の失敗): 作成しない" "$log" "release create"

# 引数が足りない・添付が無い。
out=$(cd "$r" && PATH="$tmp/fakebin:$PATH" "$here/publish-release.sh" v0.1.0 "$a" false title "$tmp/notes.md" "$tmp/missing.exe" 2>&1) && rc=0 || rc=$?
assert_eq "公開(添付ファイルが無い): 失敗" 1 "$rc"

# ----------------------------------------------------------------
# sign-installer.sh（偽の go で、鍵ファイルの扱い・引数・Secret が無いときの動きを確かめる）

mkdir -p "$tmp/signbin" "$tmp/signassets"
cat >"$tmp/signbin/go" <<'FAKE'
#!/usr/bin/env bash
# 偽の go: 引数と環境を記録し、-key のファイルの権限・中身を記録して、-out に署名の代わりの内容を書く。
log=$SIGN_LOG
printf 'args:%s\n' "$*" >>"$log"
while [[ $# -gt 0 ]]; do
  case $1 in
    -key) key=$2; shift 2 ;;
    -out) out=$2; shift 2 ;;
    *) shift ;;
  esac
done
printf 'keyfile:%s\n' "$key" >>"$log"
printf 'keymode:%s\n' "$(stat -c %a "$key")" >>"$log"
printf 'keycontent:%s\n' "$(cat "$key")" >>"$log"
printf 'password_in_env:%s\n' "${MINISIGN_PASSWORD:+yes}" >>"$log"
[[ ${FAKE_SIGN_FAIL:-} == 1 ]] && exit 1
echo fake-signature >"$out"
FAKE
chmod +x "$tmp/signbin/go"
echo installer >"$tmp/signassets/app.exe"

sign() { # 追加の環境変数は呼び出し側で VAR=... を前置する
  : >"$tmp/sign.log"
  out=$(PATH="$tmp/signbin:$PATH" SIGN_LOG="$tmp/sign.log" "$here/sign-installer.sh" "$@" 2>&1) && rc=0 || rc=$?
  slog=$(cat "$tmp/sign.log")
}

# Secret が無い: 通知を出して成功で終わる（署名を作らない・go を呼ばない）。
rm -f "$tmp/signassets/app.exe.minisig"
sign 0.1.1 "$tmp/signassets" app.exe
assert_eq "署名(Secret 無し): rc" 0 "$rc"
assert_contains "署名(Secret 無し): 通知" "$out" "::notice::"
assert_eq "署名(Secret 無し): go を呼ばない" "" "$slog"
[[ ! -e "$tmp/signassets/app.exe.minisig" ]] && ok=1 || ok=0
assert_eq "署名(Secret 無し): 署名ファイルを作らない" 1 "$ok"
# 空文字の Secret も「無い」と同じ。
MINISIGN_SECRET_KEY='' sign 0.1.1 "$tmp/signassets" app.exe
assert_eq "署名(空の Secret): rc" 0 "$rc"
assert_eq "署名(空の Secret): go を呼ばない" "" "$slog"

# Secret がある: 鍵を 0600 で一時ファイルに書き、署名ツールを呼び、終わったら鍵を消す。鍵・パスワードを出力しない。
export MINISIGN_SECRET_KEY="untrusted comment: test only
TEST-ONLY-KEY-MATERIAL"
sign 0.1.1-nightly.20261102+abc1234 "$tmp/signassets" app.exe
assert_eq "署名(Secret 有り): rc" 0 "$rc"
assert_contains "署名(Secret 有り): 版を渡す" "$slog" "-version 0.1.1-nightly.20261102+abc1234"
assert_contains "署名(Secret 有り): 出力先" "$slog" "-out $tmp/signassets/app.exe.minisig"
assert_contains "署名(Secret 有り): 鍵の権限" "$slog" "keymode:600"
assert_contains "署名(Secret 有り): 鍵の中身を渡す" "$slog" "TEST-ONLY-KEY-MATERIAL"
assert_not_contains "署名(Secret 有り): パスワード無し" "$slog" "password_in_env:yes"
assert_not_contains "署名(Secret 有り): 鍵を出力しない" "$out" "TEST-ONLY-KEY-MATERIAL"
assert_not_contains "署名(Secret 有り): 鍵を引数に出さない" "$(grep '^args:' <<<"$slog")" "TEST-ONLY-KEY-MATERIAL"
keyfile=$(sed -n 's/^keyfile://p' <<<"$slog")
[[ -n $keyfile && ! -e $keyfile && ! -e "$(dirname "$keyfile")" ]] && ok=1 || ok=0
assert_eq "署名(Secret 有り): 鍵の一時ファイルを消す" 1 "$ok"
[[ -s "$tmp/signassets/app.exe.minisig" ]] && ok=1 || ok=0
assert_eq "署名(Secret 有り): 署名ファイルができる" 1 "$ok"

# パスワードは環境変数のまま渡し、出力しない。
MINISIGN_PASSWORD=pw-test-only sign 0.1.1 "$tmp/signassets" app.exe
assert_contains "署名(パスワード): 環境に渡る" "$slog" "password_in_env:yes"
assert_not_contains "署名(パスワード): 出力しない" "$out" "pw-test-only"
assert_not_contains "署名(パスワード): 引数に出さない" "$(grep '^args:' <<<"$slog")" "pw-test-only"

# 署名ツールが失敗しても、鍵は消える。失敗は伝える。
rm -f "$tmp/signassets/app.exe.minisig"
FAKE_SIGN_FAIL=1 sign 0.1.1 "$tmp/signassets" app.exe
assert_eq "署名(ツールの失敗): 失敗" 1 "$rc"
keyfile=$(sed -n 's/^keyfile://p' <<<"$slog")
[[ -n $keyfile && ! -e $keyfile ]] && ok=1 || ok=0
assert_eq "署名(ツールの失敗): 鍵を消す" 1 "$ok"

# 引数・インストーラの不備。
sign 0.1.1 "$tmp/signassets"
assert_eq "署名(引数が足りない): 失敗" 1 "$rc"
sign 0.1.1 "$tmp/signassets" missing.exe
assert_eq "署名(インストーラが無い): 失敗" 1 "$rc"
unset MINISIGN_SECRET_KEY

# ----------------------------------------------------------------
# ワークフローのランナー: 公開リポジトリではセルフホストのジョブが動かない（組織の runner グループが公開リポジトリを許可しない）ので、
# すべてのジョブを GitHub のランナー（ubuntu-latest）で走らせる。runs-on にそれ以外を書いたら失敗させる。

repo_root=$(cd "$here/../.." && pwd)
for wf in "$repo_root"/.github/workflows/*.yml; do
  name=$(basename "$wf")
  runs_on=$(sed -n 's/^[[:space:]]*runs-on:[[:space:]]*//p' "$wf" | sort -u)
  assert_eq "ランナー($name): runs-on は ubuntu-latest だけ" "ubuntu-latest" "$runs_on"
done

# ----------------------------------------------------------------

printf '\n%d 件成功、%d 件失敗\n' "$passed" "$failed"
[[ $failed -eq 0 ]]
