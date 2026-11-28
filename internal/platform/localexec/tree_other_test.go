//go:build !windows

package localexec

import (
	"context"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunTimeoutKillsGrandchildren(t *testing.T) {
	res, err := New().Run(context.Background(), helperSpec(500*time.Millisecond, "spawn"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("result = %+v", res)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(res.Stdout)))
	if err != nil {
		t.Fatalf("grandchild pid = %q", res.Stdout)
	}
	// SIGKILL のあと、孫が刈り取られる（init が回収する）まで、上限つきで待つ。
	deadline := time.Now().Add(30 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d is still alive after the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
