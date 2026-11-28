//go:build !windows

package localexec

import (
	"os/exec"
	"syscall"
)

// processTree は子孫ごと終了させるための枠。Windows 以外は新しいプロセスグループに入れ、グループごと SIGKILL する。
type processTree struct{}

func newProcessTree() (*processTree, error) { return &processTree{}, nil }

func (*processTree) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func (*processTree) attach(*exec.Cmd) error { return nil }

func (*processTree) kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

func (*processTree) close() {}
