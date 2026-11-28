//go:build !windows

package localfs

import "io/fs"

// isReparsePoint は Windows のリパースポイント属性の判定。Windows 以外には無い（シンボリックリンクは fs.ModeSymlink で見る）。
func isReparsePoint(fs.FileInfo) bool { return false }
