//go:build windows

package localfs

import (
	"io/fs"
	"syscall"
)

// isReparsePoint は fi が FILE_ATTRIBUTE_REPARSE_POINT を持つか（シンボリックリンク・ジャンクション・OneDrive のプレースホルダーなど。
// Go の Lstat は種類によって ModeSymlink を立てないので、属性そのものを見る）。属性が読めない（想定外の Sys の型）ときは、安全側で true。
func isReparsePoint(fi fs.FileInfo) bool {
	attrs, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return true
	}
	return attrs.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
