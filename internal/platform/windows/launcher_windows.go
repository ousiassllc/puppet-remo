//go:build windows

package windows

import (
	"context"
	"errors"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.AppLauncher = (*Launcher)(nil)

// errLocationNotAllowed は、実行ファイルがローカルの固定ディスク上の、シンボリックリンク・ジャンクションを経由しない
// 実体でないために起動しなかったことを表す（*platform.LaunchError の Err。パスを含まない）。
var errLocationNotAllowed = errors.New("windows: executable is not a plain file on a local fixed drive")

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
	if err := verifyExecutableLocation(spec.ExePath); err != nil {
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

// verifyExecutableLocation は exePath が、ローカルの固定ディスク上の、シンボリックリンク・ジャンクション・マップドドライブ・
// subst を経由しない実体であることを確かめる（NFR-S20 (3)）。
//
//   - ドライブのルートの GetDriveType が DRIVE_FIXED 以外（ネットワーク・リムーバブルなど）なら拒否する。
//   - ファイルを開き（CreateFile は途中のリンクをたどる）、GetFinalPathNameByHandle が返す実体のパスが、登録されたパスと一致しなければ
//     拒否する（finalPathMatches）。UNC に解決される場合もここで拒否される。
//   - 拒否は access_denied（原因は errLocationNotAllowed。パスを含まない）。ファイルが開けない失敗は CreateProcess と同じ分類
//     （存在しなければ file_missing）。
//
// 検査と CreateProcess の間にファイルが差し替えられうる（TOCTOU）ことは防げない。登録先のディレクトリを書き換えられる者は、
// 許可リストに無い実行ファイルを置いて起動させられるため、NFR-S20 の残存リスクとして残る。
func verifyExecutableLocation(exePath string) error {
	final, err := finalExecutablePath(exePath)
	if err != nil {
		return err
	}
	if !finalPathMatches(exePath, final) {
		return &platform.LaunchError{Reason: domain.LaunchAccessDenied, Err: errLocationNotAllowed}
	}
	return nil
}

// finalExecutablePath は exePath を開いて、GetFinalPathNameByHandle が返す実体のパス（`\\?\` つき）を返す。
// 先に、exePath のドライブのルートが固定ディスク（DRIVE_FIXED）であることを確かめる（ネットワーク・リムーバブルなどは access_denied）。
// ファイルが開けない失敗は CreateProcess と同じ分類（存在しなければ file_missing）。
func finalExecutablePath(exePath string) (string, error) {
	root, err := windows.UTF16PtrFromString(filepath.VolumeName(exePath) + `\`)
	if err != nil {
		return "", domain.ErrInvalidArgument.WithMsg("executable path contains a NUL character")
	}
	if !isLocalFixedDrive(windows.GetDriveType(root)) {
		return "", &platform.LaunchError{Reason: domain.LaunchAccessDenied, Err: errLocationNotAllowed}
	}
	path, err := windows.UTF16PtrFromString(exePath)
	if err != nil {
		return "", domain.ErrInvalidArgument.WithMsg("executable path contains a NUL character")
	}
	h, err := windows.CreateFile(path, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return "", newLaunchError(err)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0) // FILE_NAME_NORMALIZED | VOLUME_NAME_DOS
		if err != nil {
			return "", newLaunchError(err)
		}
		if int(n) < len(buf) {
			return windows.UTF16ToString(buf[:n]), nil
		}
		buf = make([]uint16, n+1) // n は終端の NUL を含む必要な長さ
	}
}

// Resolve は exePath の実体のパス（リンク・8.3 短縮名を解決した、固定ディスク上のドライブ文字つきの絶対パス）を返す。
// 実体のドライブが固定ディスクでない（UNC・ボリューム GUID に解決される、リンク先が別のリムーバブルなど）ときは access_denied。
func (*Launcher) Resolve(ctx context.Context, exePath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(exePath) {
		return "", domain.ErrInvalidArgument.WithMsg("executable path is not absolute")
	}
	final, err := finalExecutablePath(exePath)
	if err != nil {
		return "", err
	}
	resolved, ok := driveLetterPath(final)
	if !ok {
		return "", &platform.LaunchError{Reason: domain.LaunchAccessDenied, Err: errLocationNotAllowed}
	}
	root, err := windows.UTF16PtrFromString(resolved[:3])
	if err != nil || !isLocalFixedDrive(windows.GetDriveType(root)) {
		return "", &platform.LaunchError{Reason: domain.LaunchAccessDenied, Err: errLocationNotAllowed}
	}
	return resolved, nil
}

// Inspect は exePath が存在する通常ファイルか、workingDir（空でなければ）が存在するディレクトリかを調べる。無ければ platform.ErrNotFound。
func (*Launcher) Inspect(ctx context.Context, exePath, workingDir string) error {
	return inspectApp(ctx, exePath, workingDir)
}
