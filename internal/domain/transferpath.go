package domain

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ファイル転送（#264）のパスと大きさの規則。OS に依らない純粋な文字列の検査で、ホストの実ファイルには触れない
// （実体のパスがルートの外に出ないこと・リンクを含まないことの確認は platform.FileStore が行う）。

const (
	// MaxTransferPathUnits はルートからの相対パスの長さの上限（UTF-16 のコードユニット数）。ルートのパスと合わせても
	// Windows の既定の上限（260 文字）に収まりやすい長さにする。
	MaxTransferPathUnits = 200
	// maxTransferElemUnits は 1 要素（ファイル名・フォルダ名）の長さの上限（NTFS の上限）。
	maxTransferElemUnits = 255

	// MaxFileChunkBytes は put_file・get_file が 1 回で運べる最大のバイト数（base64 にする前）。
	// MCP の要求本文の上限（1 MiB。NFR-S09）に base64 と JSON の分が収まる大きさ。これより大きいファイルは offset で分けて運ぶ。
	MaxFileChunkBytes = 512 << 10
	// MaxFileListEntries は list_files が 1 回で返す最大の件数。
	MaxFileListEntries = 1000

	// DefaultFileMaxBytes・DefaultFileTotalMaxBytes は、ホストの設定が無いときの 1 ファイルの上限（100 MiB）と、置き場所全体の上限（1 GiB）。
	DefaultFileMaxBytes      int64 = 100 << 20
	DefaultFileTotalMaxBytes int64 = 1 << 30
)

// CleanTransferPath は、ファイル転送の置き場所（ルート）からの相対パス rel を検査し、区切りを "/" にそろえた正規の形を返す。
// 返す値は空でなく、各要素は 1 つ以上の文字を持つ（"a/b/c.txt"）。区切りは "/" と "\" のどちらでも受け付ける。
//
// 次のものは domain.ErrInvalidArgument で拒否する（メッセージにパスは入れない）:
//   - 空・長すぎるパス（MaxTransferPathUnits）・不正な UTF-8・C0 制御文字（NUL を含む）と DEL
//   - 絶対パス（先頭が区切り）・UNC（`\\host\...`）・デバイスパス（`\\?\`・`\\.\`）
//   - ":" を含むもの（ドライブ指定 `C:\x`・`C:x`・代替データストリーム `a.txt:s`・`a.txt::$DATA`）
//   - 空の要素（"a//b"・末尾の区切り）・"." と ".." の要素（Windows は末尾のドット・空白を取り除くので、"..."・".. " も同じ扱い）
//   - 末尾がドット・空白の要素（Windows はそれらを取り除き、別の名前と同じファイルを指すため）
//   - `<>"|?*` を含む要素・1 要素が 255 文字（UTF-16）を超えるもの
//   - 予約デバイス名の要素（CON・PRN・AUX・NUL・COM1〜9・LPT1〜9 など。拡張子が付いても、フォルダの要素でも拒否）
func CleanTransferPath(rel string) (string, error) {
	switch {
	case rel == "":
		return "", ErrInvalidArgument.WithMsg("path is required")
	case !utf8.ValidString(rel):
		return "", ErrInvalidArgument.WithMsg("path must be valid UTF-8")
	case UTF16Len(rel) > MaxTransferPathUnits:
		return "", ErrInvalidArgument.WithMsg("path is too long")
	}
	for _, r := range rel {
		switch {
		case r < 0x20 || r == 0x7f:
			return "", ErrInvalidArgument.WithMsg("path must not contain control characters")
		case r == ':':
			return "", ErrInvalidArgument.WithMsg("path must be relative: drive letters, device paths and alternate data streams (\":\") are not allowed")
		}
	}
	slashed := strings.ReplaceAll(rel, `\`, "/")
	if strings.HasPrefix(slashed, "/") {
		return "", ErrInvalidArgument.WithMsg("path must be relative to the transfer folder: absolute and UNC paths are not allowed")
	}
	for elem := range strings.SplitSeq(slashed, "/") {
		if msg := checkTransferElem(elem); msg != "" {
			return "", ErrInvalidArgument.WithMsg(msg)
		}
	}
	return slashed, nil
}

// checkTransferElem は 1 要素の検査で、不備があれば拒否の理由（パスを含まない）を返す。
func checkTransferElem(elem string) string {
	trimmed := strings.TrimRight(elem, ". ")
	switch {
	case elem == "":
		return "path must not contain empty segments"
	case trimmed == "":
		return `path must not contain "." or ".." segments`
	case trimmed != elem:
		return "path segments must not end with a dot or a space"
	case strings.ContainsAny(elem, `<>"|?*`):
		return `path segments must not contain any of <>"|?*`
	case UTF16Len(elem) > maxTransferElemUnits:
		return "a path segment is too long"
	case isReservedDeviceName(elem):
		return "path must not contain a reserved device name (CON, NUL, ...)"
	}
	return ""
}

// SplitTransferPath は CleanTransferPath を通った rel を要素に分ける。
func SplitTransferPath(rel string) ([]string, error) {
	clean, err := CleanTransferPath(rel)
	if err != nil {
		return nil, err
	}
	return strings.Split(clean, "/"), nil
}

// maxTransferRootUnits はホストが設定するルートのパスの長さの上限（UTF-16 のコードユニット数。Windows の既定の上限 260 から、相対パスの分を残す）。
const maxTransferRootUnits = 240

// ValidateTransferRoot は、ホストが設定するファイル転送のルート（置き場所）root の形を検査する。root は Clean 済みであること
// （呼び出し側が filepath.Clean を通す。末尾の区切り・".." を含まない）。満たさなければ domain.ErrInvalidArgument。
// 絶対パスで（Windows ではドライブ文字で始まるもの）、UNC・デバイスパス（`\\host\...`・`\\?\`・`\\.\`）・ドライブの直下（`C:\`）・
// 制御文字・長すぎる値は不可。ネットワーク共有は、別のホストが中身を差し替えられて「リンクを経由しない」検査が成り立たないため認めない。
func ValidateTransferRoot(root string) error {
	bad := func(msg string) error { return ErrInvalidArgument.WithMsg(msg) }
	switch {
	case root == "" || !utf8.ValidString(root):
		return bad("the transfer folder must be a non-empty path")
	case UTF16Len(root) > maxTransferRootUnits:
		return bad("the transfer folder path is too long")
	case strings.HasPrefix(root, `\\`) || strings.HasPrefix(root, "//"):
		return bad("the transfer folder must be on a local drive (UNC and device paths are not allowed)")
	case !filepath.IsAbs(root):
		return bad("the transfer folder must be an absolute path")
	case filepath.Clean(root) != root:
		return bad("the transfer folder path must be clean (no \"..\", no trailing separator)")
	case filepath.Dir(root) == root:
		return bad("the transfer folder must not be the root of a drive")
	}
	for _, r := range root {
		if r < 0x20 || r == 0x7f {
			return bad("the transfer folder path must not contain control characters")
		}
	}
	return nil
}
