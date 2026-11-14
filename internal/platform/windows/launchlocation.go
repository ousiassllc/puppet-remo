package windows

import "strings"

// 実行ファイルの場所の検査（NFR-S20 (3)「ローカルの .exe」の実行時の確認）の、OS を呼ばない判定の部分。
// Win32 の呼び出し（GetDriveType・CreateFile・GetFinalPathNameByHandle）は launcher_windows.go の verifyExecutableLocation にあり、
// ここは、その結果を受けて許可するかを決める純粋な関数だけを持つ（Linux でもテストできる）。

// driveFixed は GetDriveType が固定ディスクに返す値（DRIVE_FIXED。x/sys/windows の定数と同じ値）。
const driveFixed = 3

// isLocalFixedDrive は GetDriveType の値が固定ディスク（DRIVE_FIXED）かを返す。リムーバブル・ネットワーク（マップドドライブ）・
// CD-ROM・RAM ディスク・不明・ルートなしは許さない。
func isLocalFixedDrive(driveType uint32) bool { return driveType == driveFixed }

// finalPathMatches は、起動を指定した requested（登録されたパス）と、開いたファイルの実体の final
// （GetFinalPathNameByHandle の FILE_NAME_NORMALIZED | VOLUME_NAME_DOS の結果）が同じ場所を指すかを返す。
// final が requested と違うのは、パスの途中にシンボリックリンク・ジャンクション・マウントポイントがあるか、subst・マップドドライブの
// ドライブ文字を通しているときで、どれも「登録したパスとは別の場所のファイルを起動する」ことになるので許さない。
// 大文字小文字は区別しない。final がドライブ文字で始まらない形（UNC の \\?\UNC\…・ボリューム GUID の \\?\Volume{…}\…）も許さない。
// 8.3 形式の短い名前など、同じファイルでも綴りが違う場合は一致しない（fail closed）。
//
// 次のどれかに当たるときも、綴りが一致していても許さない（fail closed。#172）。
//   - requested が `\\?\`・`\\.\` などのデバイス・拡張長パスの接頭辞で始まる（`/` 区切りの `//?/` も同じ）。
//     通常の登録は `C:\…` の形で、この接頭辞は Win32 のパスの正規化を通らず、検査と実際の起動で解釈が分かれうるため。
//   - requested か final に、`.` または `..` の区切り要素がある。GetFinalPathNameByHandle は正規化済みのパスを返すので final に現れれば
//     異常で、requested にあれば登録と実体の綴りが一致して見えても、どこを指すかが綴りだけでは決まらない。
//
// final の `\\?\` は GetFinalPathNameByHandle が必ず付ける接頭辞で、剥がすのはこの関数の中（driveLetterPath）。
// 呼び出し元（launcher_windows.go の verifyExecutableLocation）は API の結果をそのまま渡す。したがって、`\\?\` つきの final が
// 拒否されるのは接頭辞のせいではなく、剥がした後がドライブ文字のパスでないとき（UNC・ボリューム GUID・`\\.\` など）だけ。
func finalPathMatches(requested, final string) bool {
	if hasDevicePrefix(requested) {
		return false
	}
	rest, ok := driveLetterPath(final)
	if !ok {
		return false
	}
	if hasDotSegment(requested) || hasDotSegment(rest) {
		return false
	}
	return strings.EqualFold(normalizeSeparators(requested), rest)
}

// hasDevicePrefix は p が、2 つの区切り（`\` か `/`）に続く `?` か `.`、さらに区切りで始まる（`\\?\`・`\\.\`・`//?/` など）かを返す。
func hasDevicePrefix(p string) bool {
	return len(p) >= 4 && isSeparator(p[0]) && isSeparator(p[1]) && (p[2] == '?' || p[2] == '.') && isSeparator(p[3])
}

// hasDotSegment は p を `\` と `/` で区切った要素に、`.` か `..` がある（`C:\a\..\b`・`C:/a/./b` など）かを返す。
// `.a`・`a.`・`...` のような、`.` と `..` 以外の要素は対象にしない。
func hasDotSegment(p string) bool {
	for len(p) > 0 {
		i := strings.IndexAny(p, `\/`)
		seg := p
		if i < 0 {
			p = ""
		} else {
			seg, p = p[:i], p[i+1:]
		}
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

func isSeparator(c byte) bool { return c == '\\' || c == '/' }

// driveLetterPath は GetFinalPathNameByHandle の結果 final（`\\?\C:\…`）から `\\?\` の接頭辞を除いた、ドライブ文字つきの絶対パスを返す。
// ドライブ文字で始まらない形（UNC の \\?\UNC\…・ボリューム GUID の \\?\Volume{…}\…・接頭辞なし）は ok=false。
func driveLetterPath(final string) (string, bool) {
	rest, ok := strings.CutPrefix(final, `\\?\`)
	if !ok || len(rest) < 3 || !isDriveLetter(rest[0]) || rest[1] != ':' || rest[2] != '\\' {
		return "", false
	}
	return rest, true
}

// normalizeSeparators は "/" を "\" にし、連続する区切りを 1 つにする（Windows は同じ場所として扱う。UNC は先頭の
// 区切りが 2 つだが、ここへ来るのはドライブ文字のパスだけ）。
func normalizeSeparators(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	prevSep := false
	for i := range len(p) {
		c := p[i]
		if c == '/' {
			c = '\\'
		}
		if c == '\\' && prevSep {
			continue
		}
		prevSep = c == '\\'
		b.WriteByte(c)
	}
	return b.String()
}

func isDriveLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
