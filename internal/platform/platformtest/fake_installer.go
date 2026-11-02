package platformtest

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.Installer = (*FakeInstaller)(nil)

// FakeInstaller は platform.Installer の偽の実装。インストーラは起動せず、AddInstaller したパスだけが起動でき、
// 起動したパスを Started に記録する。パスの比較は完全一致。
type FakeInstaller struct {
	Recorder

	mu      sync.Mutex
	files   map[string]bool
	any     bool
	started []string
	onRun   func(path string)
}

func NewFakeInstaller() *FakeInstaller { return &FakeInstaller{files: map[string]bool{}} }

// AddInstaller は path に実在するインストーラがあることにする。
func (f *FakeInstaller) AddInstaller(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = true
}

// AcceptAny は、パスを事前に知らせなくても（検証済みのインストーラを一時ディレクトリに置く Updater のテストなど）、
// 形（絶対パスの .exe）が正しいパスをすべて起動できることにする。
func (f *FakeInstaller) AcceptAny() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.any = true
}

// OnRun は、起動に成功するたびに（Run が返る直前に、内部のロックを外してから）path つきで呼ばれる関数を設定する。nil で解除する。
func (f *FakeInstaller) OnRun(fn func(path string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onRun = fn
}

// Started は起動に成功したパスを、起動した順に返す。
func (f *FakeInstaller) Started() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.started...)
}

func (f *FakeInstaller) Run(ctx context.Context, path string) error {
	if err := f.enter(ctx, "Run", path); err != nil {
		return err
	}
	if path == "" || !looksAbsolute(path) || !strings.EqualFold(filepath.Ext(path), ".exe") {
		return domain.ErrInvalidArgument.WithMsg("installer path must be an absolute path to an .exe file")
	}
	f.mu.Lock()
	if !f.files[path] && !f.any {
		f.mu.Unlock()
		return platform.ErrNotFound
	}
	f.started = append(f.started, path)
	fn := f.onRun
	f.mu.Unlock()
	if fn != nil {
		fn(path)
	}
	return nil
}

// looksAbsolute は path が Windows 形式（ドライブ付き・ルート始まり）か POSIX 形式の絶対パスかを返す
// （偽の実装は Linux でも Windows のパスで試すため、filepath.IsAbs を使わない）。
func looksAbsolute(path string) bool {
	if strings.HasPrefix(path, `/`) || strings.HasPrefix(path, `\`) {
		return true
	}
	return len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}
