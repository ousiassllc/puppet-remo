//go:build windows

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// fixtureProc は probe が起動した fixture プロセス 1 つ。名乗り（READY）の PID が、起動した子プロセスの PID と一致するまで使わない。
type fixtureProc struct {
	role  string
	cmd   *exec.Cmd
	stdin io.WriteCloser
	msgs  chan message
	done  chan struct{} // プロセスの終了で閉じる
	pid   uint32
	ready message
}

// startFixture は自分自身の exe を fixture として起動し、READY を待つ。READY の pid が起動した子の PID でなければ、その子を終了して失敗にする。
func startFixture(ctx context.Context, role string, args ...string) (*fixtureProc, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe, append([]string{"fixture", "--role=" + role}, args...)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	f := &fixtureProc{role: role, cmd: cmd, stdin: stdin, msgs: make(chan message, 256), done: make(chan struct{})}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if m, err := parseMessage(sc.Text()); err == nil {
				select {
				case f.msgs <- m:
				default: // 受け手がいなければ捨てる（READY・ACK・EVT は少数）
				}
			}
		}
		_ = cmd.Wait()
		close(f.done)
	}()
	_ = allowSetForeground(uint32(cmd.Process.Pid))

	m, err := f.waitFor(ctx, 15*time.Second, func(m message) bool { return m.Verb == "READY" })
	if err != nil {
		f.kill()
		return nil, fmt.Errorf("fixture %s: READY を受け取れなかった: %w", role, err)
	}
	pid, perr := strconv.ParseUint(m.Fields["pid"], 10, 32)
	if perr != nil || int(pid) != cmd.Process.Pid {
		f.kill()
		return nil, fmt.Errorf("fixture %s: READY の pid が起動した子プロセスと一致しない（照合失敗。撮影しない）", role)
	}
	f.pid = uint32(pid)
	f.ready = m
	return f, nil
}

var errFixtureExited = errors.New("fixture exited")

// waitFor は pred を満たす行を待つ。満たさない行は捨てる。
func (f *fixtureProc) waitFor(ctx context.Context, timeout time.Duration, pred func(message) bool) (message, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case m := <-f.msgs:
			if pred(m) {
				return m, nil
			}
		case <-f.done:
			return message{}, errFixtureExited
		case <-timer.C:
			return message{}, context.DeadlineExceeded
		case <-ctx.Done():
			return message{}, ctx.Err()
		}
	}
}

// command は CMD を送り、ACK（または ERR）を待つ。ERR は error にする。
func (f *fixtureProc) command(ctx context.Context, name string, kv ...string) (message, error) {
	if _, err := io.WriteString(f.stdin, formatMessage("CMD", name, kv...)+"\n"); err != nil {
		return message{}, err
	}
	m, err := f.waitFor(ctx, 5*time.Second, func(m message) bool { return (m.Verb == "ACK" || m.Verb == "ERR") && m.Name == name })
	if err != nil {
		return m, err
	}
	if m.Verb == "ERR" {
		return m, fmt.Errorf("fixture %s: %s: %s", f.role, name, m.Fields["reason"])
	}
	return m, nil
}

// quit は終了を頼み、少し待ってから強制終了する。
func (f *fixtureProc) quit() {
	if f == nil {
		return
	}
	_, _ = io.WriteString(f.stdin, formatMessage("CMD", "quit")+"\n")
	_ = f.stdin.Close()
	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		f.kill()
	}
}

func (f *fixtureProc) kill() {
	if f.cmd.Process != nil {
		_ = f.cmd.Process.Kill()
	}
}
