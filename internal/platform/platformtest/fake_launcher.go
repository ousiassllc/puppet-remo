package platformtest

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.AppLauncher = (*FakeAppLauncher)(nil)

// errFakeLaunch は FakeAppLauncher が LaunchError に入れる OS のエラーの代わり（パス・引数を含まない）。
var errFakeLaunch = errors.New("fake launcher: refused")

// FakeAppLauncher は platform.AppLauncher の偽の実装。プロセスは作らず、AddExecutable したパスだけが起動でき、
// 起動のたびに新しい pid を返す。パスの比較は完全一致。
type FakeAppLauncher struct {
	Recorder

	mu       sync.Mutex
	files    map[string]bool
	dirs     map[string]bool
	denied   map[string]bool
	elevated map[string]bool
	links    map[string]string // リンク（シンボリックリンク・ジャンクション・8.3 短縮名）→ 実体のパス
	nonFixed map[string]bool   // 固定ディスク上にない（ネットワーク・リムーバブルなど）パス
	nextPID  uint32
	onLaunch func(pid uint32)
}

func NewFakeAppLauncher() *FakeAppLauncher {
	return &FakeAppLauncher{
		files: map[string]bool{}, dirs: map[string]bool{}, denied: map[string]bool{}, elevated: map[string]bool{},
		links: map[string]string{}, nonFixed: map[string]bool{}, nextPID: 1000,
	}
}

// AddExecutable は path に起動できる実行ファイルがあることにする。
func (f *FakeAppLauncher) AddExecutable(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = true
}

// AddDir は path にディレクトリがあることにする。
func (f *FakeAppLauncher) AddDir(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[path] = true
}

// DenyAccess は path の起動が OS に拒否される（access_denied）ようにする。
func (f *FakeAppLauncher) DenyAccess(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.denied[path] = true
}

// RequireElevation は path の起動に昇格が要る（elevation_required）ようにする。
func (f *FakeAppLauncher) RequireElevation(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.elevated[path] = true
}

// AddLink は link が実体 target を指すリンク（シンボリックリンク・ジャンクション・8.3 形式の短い名前）であることにする。
// Resolve は link を target に解決する（大文字小文字は区別しない）。Launch は解決済みの実体のパスで呼ばれる前提で、link では起動できない。
func (f *FakeAppLauncher) AddLink(link, target string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.links[pathKey(link)] = target
}

// MarkNonFixed は path が固定ディスク上にない（Resolve が access_denied を返す）ことにする。
func (f *FakeAppLauncher) MarkNonFixed(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nonFixed[pathKey(path)] = true
}

// OnLaunch は、起動に成功するたびに（Launch が返る直前に、内部のロックを外してから）pid つきで呼ばれる関数を設定する。
// プロセスの作成に伴うウィンドウの出現を、FakeWindows の差し替えで再現するためのもの。nil で解除する。
func (f *FakeAppLauncher) OnLaunch(fn func(pid uint32)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onLaunch = fn
}

func (f *FakeAppLauncher) Launch(ctx context.Context, spec platform.LaunchSpec) (uint32, error) {
	if err := f.enter(ctx, "Launch", spec); err != nil {
		return 0, err
	}
	if spec.ExePath == "" {
		return 0, domain.ErrInvalidArgument.WithMsg("empty executable path")
	}
	pid, err := f.start(spec)
	if err != nil {
		return 0, err
	}
	f.mu.Lock()
	fn := f.onLaunch
	f.mu.Unlock()
	if fn != nil {
		fn(pid)
	}
	return pid, nil
}

// start は起動の成否を決めて新しい pid を返す。
func (f *FakeAppLauncher) start(spec platform.LaunchSpec) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case !f.files[spec.ExePath]:
		return 0, &platform.LaunchError{Reason: domain.LaunchFileMissing, Err: errFakeLaunch}
	case f.denied[spec.ExePath]:
		return 0, &platform.LaunchError{Reason: domain.LaunchAccessDenied, Err: errFakeLaunch}
	case f.elevated[spec.ExePath]:
		return 0, &platform.LaunchError{Reason: domain.LaunchElevationRequired, Err: errFakeLaunch}
	}
	f.nextPID++
	return f.nextPID, nil
}

func (f *FakeAppLauncher) Inspect(ctx context.Context, exePath, workingDir string) error {
	if err := f.enter(ctx, "Inspect", exePath, workingDir); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.files[exePath] || (workingDir != "" && !f.dirs[workingDir]) {
		return platform.ErrNotFound
	}
	return nil
}

// Resolve は exePath を（AddLink したリンクなら実体に）解決し、AddExecutable した綴りで返す。パスの比較は大文字小文字を区別せず、
// 区切りを `\` にそろえる。ファイルが無ければ file_missing、MarkNonFixed したパスは access_denied。
func (f *FakeAppLauncher) Resolve(ctx context.Context, exePath string) (string, error) {
	if err := f.enter(ctx, "Resolve", exePath); err != nil {
		return "", err
	}
	if exePath == "" {
		return "", domain.ErrInvalidArgument.WithMsg("empty executable path")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	target := exePath
	if f.nonFixed[pathKey(target)] {
		return "", &platform.LaunchError{Reason: domain.LaunchAccessDenied, Err: errFakeLaunch}
	}
	if real, ok := f.links[pathKey(target)]; ok {
		target = real
	}
	if f.nonFixed[pathKey(target)] {
		return "", &platform.LaunchError{Reason: domain.LaunchAccessDenied, Err: errFakeLaunch}
	}
	for p := range f.files {
		if pathKey(p) == pathKey(target) {
			return p, nil
		}
	}
	return "", &platform.LaunchError{Reason: domain.LaunchFileMissing, Err: errFakeLaunch}
}

func pathKey(p string) string { return strings.ToLower(domain.NormalizePathForMatch(p)) }
