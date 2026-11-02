package platformtest

import (
	"context"
	"strings"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.FolderOpener = (*FakeFolderOpener)(nil)

// FakeFolderOpener は platform.FolderOpener の偽の実装。ファイルマネージャーは起動せず、AddFolder したパスだけが開け
// （AcceptAny なら絶対パスならすべて）、開いたパスを Opened に記録する。パスの比較は完全一致。
type FakeFolderOpener struct {
	Recorder

	mu      sync.Mutex
	folders map[string]bool
	any     bool
	opened  []string
}

func NewFakeFolderOpener() *FakeFolderOpener { return &FakeFolderOpener{folders: map[string]bool{}} }

// AddFolder は dir に実在するフォルダーがあることにする。
func (f *FakeFolderOpener) AddFolder(dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.folders[dir] = true
}

// AcceptAny は、パスを事前に知らせなくても、絶対パスならすべて開けることにする（一時ディレクトリを使うテストなど）。
func (f *FakeFolderOpener) AcceptAny() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.any = true
}

// Opened は開いたパスを、開いた順に返す。
func (f *FakeFolderOpener) Opened() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opened...)
}

func (f *FakeFolderOpener) OpenFolder(ctx context.Context, dir string) error {
	if err := f.enter(ctx, "OpenFolder", dir); err != nil {
		return err
	}
	if dir == "" || !looksAbsolute(dir) {
		return domain.ErrInvalidArgument.WithMsg("folder path must be an absolute path")
	}
	if strings.ContainsRune(dir, 0) {
		return domain.ErrInvalidArgument.WithMsg("folder path contains a NUL character")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.folders[dir] && !f.any {
		return platform.ErrNotFound
	}
	f.opened = append(f.opened, dir)
	return nil
}
