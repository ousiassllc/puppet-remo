package windows

import (
	"errors"
	"strings"
)

// 実行ファイルの場所の検査（NFR-S20 (3)「ローカルの .exe」の実行時の確認）の、OS を呼ばない判定の部分。
// Win32 の呼び出し（GetDriveType・CreateFile・GetFinalPathNameByHandle）は launcher_windows.go の verifyExecutableLocation にあり、
// ここは、その結果を受けて許可するかを決める純粋な関数だけを持つ（Linux でもテストできる）。

// driveFixed は GetDriveType が固定ディスクに返す値（DRIVE_FIXED。x/sys/windows の定数と同じ値）。
const driveFixed = 3

// errLocationNotAllowed は、実行ファイルがローカルの固定ディスク上の、シンボリックリンク・ジャンクションを経由しない
// 実体でないために起動しなかったことを表す（*platform.LaunchError の Err。パスを含まない）。
var errLocationNotAllowed = errors.New("windows: executable is not a plain file on a local fixed drive")

// isLocalFixedDrive は GetDriveType の値が固定ディスク（DRIVE_FIXED）かを返す。リムーバブル・ネットワーク（マップドドライブ）・
// CD-ROM・RAM ディスク・不明・ルートなしは許さない。
func isLocalFixedDrive(driveType uint32) bool { return driveType == driveFixed }

// finalPathMatches は、起動を指定した requested（登録されたパス）と、開いたファイルの実体の final
// （GetFinalPathNameByHandle の FILE_NAME_NORMALIZED | VOLUME_NAME_DOS の結果）が同じ場所を指すかを返す。
// final が requested と違うのは、パスの途中にシンボリックリンク・ジャンクション・マウントポイントがあるか、subst・マップドドライブの
// ドライブ文字を通しているときで、どれも「登録したパスとは別の場所のファイルを起動する」ことになるので許さない。
// 大文字小文字は区別しない。final がドライブ文字で始まらない形（UNC の \\?\UNC\…・ボリューム GUID の \\?\Volume{…}\…）も許さない。
// 8.3 形式の短い名前など、同じファイルでも綴りが違う場合は一致しない（fail closed）。
func finalPathMatches(requested, final string) bool {
	rest, ok := strings.CutPrefix(final, `\\?\`)
	if !ok || len(rest) < 3 || !isDriveLetter(rest[0]) || rest[1] != ':' || rest[2] != '\\' {
		return false
	}
	return strings.EqualFold(normalizeSeparators(requested), rest)
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
