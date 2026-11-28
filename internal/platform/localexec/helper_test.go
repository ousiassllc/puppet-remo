package localexec

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helperArg はテストバイナリを「ヘルパープロセス」として再実行するときの第 1 引数（標準の re-exec パターン。環境変数は子へ渡らないので引数で切り替える）。
const helperArg = "__localexec_helper__"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == helperArg {
		os.Exit(helper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// helper はモードごとの振る舞い: echo OUT ERR CODE / args / env / stdin / flood N / sleep / spawn。
func helper(args []string) int {
	switch args[0] {
	case "echo":
		_, _ = fmt.Fprint(os.Stdout, args[1])
		_, _ = fmt.Fprint(os.Stderr, args[2])
		code, _ := strconv.Atoi(args[3])
		return code
	case "args":
		for _, a := range args[1:] {
			fmt.Println(a)
		}
	case "env":
		for _, kv := range os.Environ() {
			name, _, _ := strings.Cut(kv, "=")
			fmt.Println(name)
		}
	case "stdin":
		b, _ := io.ReadAll(os.Stdin)
		fmt.Print(len(b))
	case "flood":
		n, _ := strconv.Atoi(args[1])
		chunk := []byte(strings.Repeat("x", 4096))
		for left := n; left > 0; left -= len(chunk) {
			w := chunk[:min(left, len(chunk))]
			_, _ = os.Stdout.Write(w)
			_, _ = os.Stderr.Write(w)
		}
	case "sleep":
		time.Sleep(time.Hour)
	case "spawn":
		// 孫プロセス（sleep）が出力のパイプを握ったまま、親も眠る。孫の pid を最初に出す。
		child := exec.Command(os.Args[0], helperArg, "sleep")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			return 90
		}
		fmt.Println(child.Process.Pid)
		time.Sleep(time.Hour)
	}
	return 0
}

// helperSpec はヘルパープロセスを起こす指定（限度は十分に余裕を持たせる）。
func helperSpec(timeout time.Duration, args ...string) specT {
	return specT{Executable: os.Args[0], Args: append([]string{helperArg}, args...), Timeout: timeout, MaxOutputBytes: 1 << 16}
}
