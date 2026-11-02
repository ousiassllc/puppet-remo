package main

import (
	"context"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/service/agenttest"
)

// デモのトークンは NFR-S04 の形（prm_ + base64url の 32 バイト）でなければ、Bearer 検証が malformed で弾く。
func TestDemoTokenHasTheTokenFormat(t *testing.T) {
	rest, ok := strings.CutPrefix(demoToken, "prm_")
	if !ok || len(demoToken) != 47 {
		t.Fatalf("demoToken = %q (%d chars), want prm_ + 43 chars", demoToken, len(demoToken))
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(rest)
	if err != nil || len(raw) != 32 {
		t.Fatalf("demoToken body: %d bytes, err %v; want 32 bytes of strict base64url", len(raw), err)
	}
}

func TestDemoModelFollowsTheScriptPerHistory(t *testing.T) {
	m := &demoModel{safetyCheck: true, delay: time.Millisecond}
	run := func(h *agenttest.History, want ...string) {
		t.Helper()
		for i, w := range want {
			out, err := m.Step(context.Background(), agenttest.StepInput{Model: "demo", History: h})
			if err != nil {
				t.Fatalf("step %d: %v", i+1, err)
			}
			got := out.CallID
			if out.FinalText != "" {
				got = "final"
			}
			if got != w {
				t.Fatalf("step %d = %q, want %q", i+1, got, w)
			}
			if i == 1 && len(out.SafetyChecks) != 1 {
				t.Errorf("step 2 safety checks = %d, want 1", len(out.SafetyChecks))
			}
		}
	}
	run(newHistory(), "call_demo_1", "call_demo_2", "final")
	run(newHistory(), "call_demo_1", "call_demo_2", "final") // 次のタスクの履歴は台本の頭から
}

func TestDemoModelStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&demoModel{delay: time.Hour}).Step(ctx, agenttest.StepInput{History: newHistory()}); err == nil {
		t.Fatal("a cancelled context must stop the step")
	}
}

func TestTailnetFacadeReportsTheFakeLocalAddr(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	f := tailnetFacade{Listener: ln, local: "100.64.0.1:8765"}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := f.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	client, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	c, ok := <-accepted
	if !ok {
		t.Fatal("Accept failed")
	}
	defer func() { _ = c.Close() }()
	if got := c.LocalAddr().String(); got != "100.64.0.1:8765" {
		t.Fatalf("LocalAddr = %s, want the fake tailnet address", got)
	}
}

func newHistory() *agenttest.History { return agenttest.NewHistory() }
