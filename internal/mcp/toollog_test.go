package mcp

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// lockedBuffer は並行に書かれるログの出力先。
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// ツール実行の運用ログ（FR-H13）: 状態を変える操作の成功は INFO、読み取りだけのツールの成功は DEBUG（既定の INFO では出さない）、
// リース・ガードによる拒否は WARN（コード付き）。引数（座標・本文）は出さない。
func TestToolCallsAreLoggedWithSuccessAndFailure(t *testing.T) {
	var buf lockedBuffer
	e := newEnv(t, func(c *Config) {
		c.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
			Level: slog.LevelInfo,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr { // 時刻の数字が引数の確認と紛れないように外す
				if len(groups) == 0 && a.Key == slog.TimeKey {
					return slog.Attr{}
				}
				return a
			},
		}))
	})
	a, b := e.connect("a"), e.connect("b")

	if res := callTool(t, a, "mouse_move", map[string]any{"x": 123, "y": 456}); res.IsError {
		t.Fatal(res.text())
	}
	if res := callTool(t, a, "get_control_status", nil); res.IsError {
		t.Fatal(res.text())
	}
	if got := callTool(t, b, "mouse_move", map[string]any{"x": 2, "y": 2}).errCode(t); got != domain.CodeLeaseHeldByOther {
		t.Fatalf("code = %s", got)
	}

	out := buf.String()
	var succeeded, rejected []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		switch {
		case strings.Contains(line, "mcp tool succeeded"):
			succeeded = append(succeeded, line)
		case strings.Contains(line, "mcp tool was rejected"):
			rejected = append(rejected, line)
		}
	}
	if len(succeeded) != 1 || !strings.Contains(succeeded[0], "level=INFO") || !strings.Contains(succeeded[0], "tool=mouse_move") ||
		!strings.Contains(succeeded[0], "client=client-a") || !strings.Contains(succeeded[0], "took_ms=") {
		t.Errorf("success lines = %q, want exactly one INFO for mouse_move (the read-only get_control_status stays at DEBUG)", succeeded)
	}
	if len(rejected) != 1 || !strings.Contains(rejected[0], "level=WARN") || !strings.Contains(rejected[0], "tool=mouse_move") ||
		!strings.Contains(rejected[0], "code=lease_held_by_other") || !strings.Contains(rejected[0], "client=client-b") {
		t.Errorf("rejected lines = %q", rejected)
	}
	for _, leak := range []string{"123", "456", "x=2"} {
		if strings.Contains(out, leak) {
			t.Errorf("the tool arguments leaked into the log (%q):\n%s", leak, out)
		}
	}
}
