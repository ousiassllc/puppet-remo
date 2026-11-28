//go:build windows

package localexec

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processTree はジョブオブジェクト。プロセスを止めた状態（CREATE_SUSPENDED）で起こし、ジョブへ入れてから動かす（#279）。
// 終了のときはジョブごと終了させる（JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE なので、このアプリが落ちてもプロセスが残らない）。
// 動き出す前にジョブへ入るので、起動直後に作られた子孫もジョブに入り、タイムアウトやキルスイッチで終了される。
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
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED}
}

// attach は止まっているプロセスをジョブへ入れ、主スレッドを動かす。どちらかに失敗したら error（呼び出し側がプロセスを終わらせる）。
func (t *processTree) attach(cmd *exec.Cmd) error {
	pid := uint32(cmd.Process.Pid)
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.AssignProcessToJobObject(t.job, h); err != nil {
		return err
	}
	return resumeThreads(pid)
}

// resumeThreads は CREATE_SUSPENDED で作った pid のプロセスのスレッドを動かす。
// os/exec は CreateProcess が返すスレッドのハンドルを閉じてしまうので、スレッドの一覧から pid のものを探す。
// 止まっているプロセスのスレッドは主スレッドだけだが、外から差し込まれたスレッドがあっても主スレッドが止まったままにならないよう、
// pid のスレッドをすべて再開する（止まっていないスレッドへの ResumeThread は何もしない）。pid はプロセスのハンドル（cmd.Process）を持っている間は再利用されない。
func resumeThreads(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := false
	for err := windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		if err := resumeThread(entry.ThreadID); err != nil {
			return err
		}
		resumed = true
	}
	if !resumed {
		return windows.ERROR_NOT_FOUND
	}
	return nil
}

func resumeThread(tid uint32) error {
	th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, tid)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(th) }()
	_, err = windows.ResumeThread(th)
	return err
}

// kill はジョブ（と、ジョブに入る前のプロセス）を終了させる。ジョブがまだ空のうちに呼ばれると TerminateJobObject は何も終わらせずに
// 成功するので、プロセス自身も必ず終了させる（ジョブの終了で先に終わっていても、それはエラーにしない）。
func (t *processTree) kill(cmd *exec.Cmd) error {
	if err := windows.TerminateJobObject(t.job, 1); err != nil {
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return err
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return nil
}

func (t *processTree) close() { _ = windows.CloseHandle(t.job) } // KILL_ON_JOB_CLOSE: 残った子孫もここで終わる
