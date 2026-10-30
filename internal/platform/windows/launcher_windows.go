//go:build windows

package windows

import (
	"context"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.AppLauncher = (*Launcher)(nil)

// Launcher は platform.AppLauncher の Windows 実装（CreateProcess。FR-M19、NFR-S20）。状態を持たず、どの goroutine からも呼べる。
//
//   - ExePath を lpApplicationName に明示する（lpCommandLine 先頭の解釈による検索パスの曖昧さを避ける）。
//     コマンドラインは buildCommandLine が「引用した ExePath + Args」で組む。
//   - シェルを経由しない（cmd.exe・ShellExecute を使わない）。トークンは本アプリのもので、昇格しない。
//     昇格が必要な実行ファイルは ERROR_ELEVATION_REQUIRED で失敗し（UAC のダイアログは出ない）、elevation_required になる。
//   - 環境変数は本アプリのものを引き継ぐ。作業ディレクトリが空なら ExePath のあるディレクトリ。
//   - ハンドルは継承しない。プロセス・スレッドのハンドルは作成後すぐに閉じる（リークしない。起動後のプロセスは管理しない）。
//   - ジョブオブジェクトには入れない（本アプリの終了・キルスイッチで、起動したプロセスは終了しない）。
type Launcher struct{}

// NewLauncher は AppLauncher の実装を作る。
func NewLauncher() *Launcher { return &Launcher{} }

// Launch は spec.ExePath を新しいプロセスとして起動し、そのプロセス ID を返す。
func (*Launcher) Launch(ctx context.Context, spec platform.LaunchSpec) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !filepath.IsAbs(spec.ExePath) {
		return 0, domain.ErrInvalidArgument.WithMsg("executable path is not absolute")
	}
	line, err := buildCommandLine(spec.ExePath, spec.Args)
	if err != nil {
		return 0, err
	}
	dir := spec.WorkingDir
	if dir == "" {
		dir = filepath.Dir(spec.ExePath)
	}
	exe, err := windows.UTF16PtrFromString(spec.ExePath)
	if err != nil {
		return 0, domain.ErrInvalidArgument.WithMsg("executable path contains a NUL character")
	}
	cmd, err := windows.UTF16FromString(line) // CreateProcessW が書き換えうるので、書き込み可能なバッファで渡す
	if err != nil {
		return 0, domain.ErrInvalidArgument.WithMsg("command line contains a NUL character")
	}
	cwd, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, domain.ErrInvalidArgument.WithMsg("working directory contains a NUL character")
	}

	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(exe, &cmd[0], nil, nil, false, 0, nil, cwd, &si, &pi); err != nil {
		return 0, newLaunchError(err)
	}
	_ = windows.CloseHandle(pi.Thread)
	_ = windows.CloseHandle(pi.Process)
	return pi.ProcessId, nil
}

// Inspect は exePath が存在する通常ファイルか、workingDir（空でなければ）が存在するディレクトリかを調べる。無ければ platform.ErrNotFound。
func (*Launcher) Inspect(ctx context.Context, exePath, workingDir string) error {
	return inspectApp(ctx, exePath, workingDir)
}
