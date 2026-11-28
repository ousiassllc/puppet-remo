//go:build windows

package localexec

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// ヘルパーの spawn は、起動した直後に孫プロセスを作る。止めた状態で起こしてジョブへ入れてから動かす（#279）ので、
// その孫もジョブに入り、タイムアウトで終了される（実機で流す手順は docs/operations/smoke-windows.md §4.20 X9）。
func TestRunTimeoutKillsGrandchildren(t *testing.T) {
	res, err := New().Run(context.Background(), helperSpec(2*time.Second, "spawn"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("result = %+v", res)
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(string(res.Stdout)), 10, 32)
	if err != nil {
		t.Fatalf("grandchild pid = %q", res.Stdout)
	}
	deadline := time.Now().Add(30 * time.Second)
	for processRunning(uint32(pid)) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d is still alive after the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// processRunning は pid のプロセスがまだ動いているか（開けない = もう無い）。
func processRunning(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259 // STILL_ACTIVE
}
