package appserver

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/codex/protocol"
)

// Process は起動した app-server: Server が必要とするもの。
type Process interface {
	// Stdin は JSON-RPC の要求を書く先、Stdout は応答・通知を読む元。
	Stdin() io.Writer
	Stdout() io.Reader
	// Done はプロセスが終了したとき閉じる。
	Done() <-chan struct{}
	// ExitInfo は終わり方（利用者向け。終了前は ""）。
	ExitInfo() string
	// Stop はプロセスを終わらせる（先に標準入力を閉じる）。消えるか、強制終了するまで待って戻る。
	Stop()
}

// Launch は本物のプロセスの起動のしかた。
type Launch struct {
	// Exe は app-server の実行ファイル（カタログに固定した公式のもの。呼び出し側が engine から得る）。サブコマンドは取らない。
	Exe string
	// CodexHome はプロセスの CODEX_HOME: アプリ専用の、存在するディレクトリ。
	CodexHome string
	// WorkDir はプロセスの（空の）作業ディレクトリ。
	WorkDir string
	Logger  *slog.Logger
}

// DropEnv は、アプリの環境のうち、app-server のログインのしかたを変えてしまう変数。アプリはブラウザのログインだけを使うので、
// 利用者が環境に持つトークンやキーは渡さない。CODEX_HOME はアプリ専用の値で上書きし、CODEX_SQLITE_HOME は外す。
var DropEnv = []string{"CODEX_ACCESS_TOKEN", "CODEX_API_KEY", "OPENAI_API_KEY", "CODEX_HOME", "CODEX_SQLITE_HOME"}

// Args は起動引数: -c の設定（資格情報は OS の保管場所のみ・道具の無効化）、そして標準入出力の待ち受け。
func (l Launch) Args() []string {
	args := []string{"-c", protocol.ConfigCredentialsStore + `="` + protocol.CredentialsKeyring + `"`}
	for _, s := range protocol.ToolSettings {
		args = append(args, "-c", s)
	}
	return append(args, "--listen", "stdio://")
}

// Environ は子プロセスの環境: base から DropEnv を（大文字小文字を区別せずに）外し、CODEX_HOME だけをアプリ専用の値にする。
// それ以外（プロキシ・証明書・PATH など）はそのまま渡す。
func Environ(base []string, codexHome string) []string {
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, d := range DropEnv {
			if strings.EqualFold(name, d) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return append(out, "CODEX_HOME="+codexHome)
}

// Start は本物のプロセスを起動する（コンソール窓を出さず、アプリと一緒に終わる）。
func (l Launch) Start() (Process, error) {
	if l.Exe == "" {
		return nil, errors.New("no executable")
	}
	logger := l.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	cmd := exec.Command(l.Exe, l.Args()...)
	cmd.Dir = l.WorkDir
	cmd.Env = Environ(os.Environ(), l.CodexHome)
	return startChild(cmd, logger)
}

// child は標準入出力をパイプにした子プロセス。Wait が出力を閉じる前に読み切れるよう、exec のパイプではなく
// os.Pipe のファイルを使う。標準エラーは末尾だけをメモリに持つ。
type child struct {
	cmd    *exec.Cmd
	in     *os.File // 子の標準入力への書き込み側
	out    *os.File // 子の標準出力の読み取り側
	done   chan struct{}
	tail   *tailBuffer
	exit   atomic.Int64
	logger *slog.Logger
}

func startChild(cmd *exec.Cmd, logger *slog.Logger) (*child, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create pipes: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, fmt.Errorf("create pipes: %w", err)
	}
	c := &child{cmd: cmd, in: inW, out: outR, done: make(chan struct{}), tail: &tailBuffer{max: 4096}, logger: logger}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, c.tail
	prepare(cmd)
	err = cmd.Start()
	_ = inR.Close() // 子の側の端は、起動したらこちらでは要らない
	_ = outW.Close()
	if err != nil {
		_ = inW.Close()
		_ = outR.Close()
		return nil, err
	}
	if err := adopt(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = inW.Close()
		_ = outR.Close()
		return nil, fmt.Errorf("bind to the app: %w", err)
	}
	go func() {
		err := cmd.Wait()
		code := cmd.ProcessState.ExitCode()
		if err != nil && code == 0 {
			code = -1
		}
		c.exit.Store(int64(code))
		close(c.done)
	}()
	return c, nil
}

func (c *child) Stdin() io.Writer      { return c.in }
func (c *child) Stdout() io.Reader     { return c.out }
func (c *child) Done() <-chan struct{} { return c.done }

func (c *child) ExitInfo() string {
	select {
	case <-c.done:
	default:
		return ""
	}
	return exitText(int(c.exit.Load()), c.tail.String())
}

const (
	// stopGrace は標準入力を閉じてから、強制終了するまでの待ち。
	stopGrace = 5 * time.Second
	// killWait は強制終了のあと、終了を待つ上限。
	killWait = 5 * time.Second
)

func (c *child) Stop() {
	_ = c.in.Close()
	select {
	case <-c.done:
	case <-time.After(stopGrace):
		_ = c.cmd.Process.Kill()
		select {
		case <-c.done:
		case <-time.After(killWait):
		}
	}
	_ = c.out.Close()
}

// tailBuffer は書かれたものの末尾 max バイトだけを持つ。
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
