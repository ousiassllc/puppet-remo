//go:build windows

package localexec

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processTree はジョブオブジェクト。プロセスを起こした直後にジョブへ入れ、終了のときはジョブごと終了させる
// （JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE なので、このアプリが落ちてもプロセスが残らない）。起こしてからジョブへ入れるまでの
// ごく短い間に子プロセスを作るプログラムの、その子はジョブに入らない（許可リストには、そのようなプログラムを入れない）。
type processTree struct{ job windows.Handle }

func newProcessTree() (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	return &processTree{job: job}, nil
}

func (*processTree) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

func (t *processTree) attach(cmd *exec.Cmd) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return windows.AssignProcessToJobObject(t.job, h)
}

func (t *processTree) kill(cmd *exec.Cmd) error {
	if err := windows.TerminateJobObject(t.job, 1); err != nil && cmd.Process != nil {
		return cmd.Process.Kill()
	}
	return nil
}

func (t *processTree) close() { _ = windows.CloseHandle(t.job) } // KILL_ON_JOB_CLOSE: 残った子孫もここで終わる
