//go:build windows

package appserver

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createNoWindow は CREATE_NO_WINDOW。ウィンドウ版のアプリはコンソールを持たないので、これが無いと子が黒い窓を開く。
const createNoWindow = 0x08000000

// prepare は子プロセスにコンソール窓を出させない。
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
}

var (
	jobOnce sync.Once
	job     windows.Handle
	jobErr  error
)

// appJob はアプリのジョブオブジェクト（JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE）。アプリのプロセスが終わるとハンドルが閉じ、
// ジョブに入った子（app-server）は OS が終わらせる。アプリが異常終了しても子は残らない。
func appJob() (windows.Handle, error) {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			jobErr = fmt.Errorf("CreateJobObject: %w", err)
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
		}
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			_ = windows.CloseHandle(h)
			jobErr = fmt.Errorf("SetInformationJobObject: %w", err)
			return
		}
		job = h
	})
	return job, jobErr
}

// adopt は起動した子をアプリのジョブに入れる。入れられなければ失敗（呼び出し側が子を終わらせる）。
func adopt(p *os.Process) error {
	h, err := appJob()
	if err != nil {
		return err
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer func() { _ = windows.CloseHandle(ph) }()
	if err := windows.AssignProcessToJobObject(h, ph); err != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return nil
}
