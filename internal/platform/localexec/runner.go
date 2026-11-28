package localexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// waitDelay は、プロセスを終了させたあと、出力のパイプが閉じるのを待つ上限（孫プロセスがパイプを握り続けても Run が返るように）。
const waitDelay = 2 * time.Second

// Runner は platform.CommandRunner の実装。状態を持たない。
type Runner struct{}

var _ platform.CommandRunner = Runner{}

// New は Runner を返す。
func New() Runner { return Runner{} }

// allowedEnv は子プロセスへ引き継ぐ環境変数の名前（大文字・小文字は区別しない）。トークンなどの秘密を含みうる他の変数は渡さない。
var allowedEnv = []string{
	"SystemRoot", "SystemDrive", "windir", "ComSpec", "PATH", "PATHEXT", "TEMP", "TMP", "TMPDIR",
	"USERPROFILE", "USERNAME", "LOCALAPPDATA", "APPDATA", "ProgramData",
	"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "CommonProgramFiles", "CommonProgramFiles(x86)", "CommonProgramW6432",
	"COMPUTERNAME", "OS", "PROCESSOR_ARCHITECTURE", "PROCESSOR_ARCHITEW6432", "NUMBER_OF_PROCESSORS", "HOME", "LANG",
}

// filterEnv は environ（"NAME=value"）から allowedEnv の名前だけを残す。
func filterEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			continue
		}
		for _, a := range allowedEnv {
			if strings.EqualFold(name, a) {
				out = append(out, kv)
				break
			}
		}
	}
	return out
}

// capBuffer は先頭の limit バイトだけ保持し、超えた分は捨てて truncated を立てる io.Writer（読み続けるので、書き手のプロセスは止まらない）。
type capBuffer struct {
	limit     int
	buf       []byte
	truncated bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := c.limit - len(c.buf); room < n {
		c.truncated = true
		p = p[:max(room, 0)]
	}
	c.buf = append(c.buf, p...)
	return n, nil
}

func (Runner) Run(ctx context.Context, spec platform.CommandSpec) (platform.CommandResult, error) {
	if spec.Executable == "" || spec.Timeout <= 0 || spec.MaxOutputBytes <= 0 {
		return platform.CommandResult{}, domain.ErrInvalidArgument.WithMsg("command spec requires an executable, a timeout and an output limit")
	}
	tree, err := newProcessTree()
	if err != nil {
		return platform.CommandResult{}, domain.ErrCommandStartFailed
	}
	defer tree.close()

	runCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, spec.Executable, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = filterEnv(os.Environ())
	stdout, stderr := &capBuffer{limit: spec.MaxOutputBytes}, &capBuffer{limit: spec.MaxOutputBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr // Stdin は nil（空の入力）
	cmd.Cancel = func() error { return tree.kill(cmd) }
	cmd.WaitDelay = waitDelay
	tree.prepare(cmd)

	start := time.Now()
	if err := cmd.Start(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return platform.CommandResult{}, ctxErr
		}
		return platform.CommandResult{}, domain.ErrCommandStartFailed // OS のエラーにはパスが入るので載せない
	}
	if err := tree.attach(cmd); err != nil {
		// 子孫ごと終了させる準備ができなかった（Windows では、止めたまま起こしたプロセスをジョブへ入れて動かせなかった）: 動かさずに終わらせる。
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return platform.CommandResult{}, ctxErr // 準備の間に呼び出し側が取り消した
		}
		return platform.CommandResult{}, domain.ErrCommandStartFailed
	}
	waitErr := cmd.Wait()
	res := platform.CommandResult{
		Stdout: stdout.buf, Stderr: stderr.buf, StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated,
		Duration: time.Since(start),
	}
	if err := ctx.Err(); err != nil {
		return platform.CommandResult{}, err // 呼び出し側が取り消した（キルスイッチ・切断など）
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		res.TimedOut, res.ExitCode = true, -1
		return res, nil
	}
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil, errors.Is(waitErr, exec.ErrWaitDelay):
		res.ExitCode = cmd.ProcessState.ExitCode()
	case errors.As(waitErr, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		return platform.CommandResult{}, domain.ErrCommandStartFailed
	}
	return res, nil
}
