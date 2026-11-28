//go:build windows

package localfs

import (
	"os"

	"golang.org/x/sys/windows"
)

// openNoFollow は path の末尾がリパースポイントのとき、その先を開かずにリンク自身を開く（FILE_FLAG_OPEN_REPARSE_POINT）。
// 開いたあとの openedIsReparsePoint が、リンク自身を開いたことを検知して拒否する。write が true なら読み書き、false なら読み取りだけ。
// create が true なら無いファイルを作る。切り詰めはしない（OPEN_ALWAYS / OPEN_EXISTING）。
func openNoFollow(path string, write, create bool) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	access := uint32(windows.GENERIC_READ)
	if write {
		access |= windows.GENERIC_WRITE
	}
	disposition := uint32(windows.OPEN_EXISTING)
	if create {
		disposition = windows.OPEN_ALWAYS
	}
	h, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// openedIsReparsePoint は開いたファイル自身が FILE_ATTRIBUTE_REPARSE_POINT を持つか（openNoFollow がリンク自身を開いた場合）。
func openedIsReparsePoint(f *os.File) (bool, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return false, err
	}
	return info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

// isSymlinkLoop は Windows には無い（リンクは openedIsReparsePoint が見つける）。
func isSymlinkLoop(error) bool { return false }
