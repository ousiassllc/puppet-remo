package mcp

import (
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// stepClock は手動で進める Clock。
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newPauseEnv(t *testing.T) (*env, *service.HostInputPauser, *stepClock) {
	t.Helper()
	clock := &stepClock{now: time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)}
	pauser, err := service.NewHostInputPauser(service.HostInputPauserConfig{
		Clock: clock, Policy: service.HostInputPolicy{Enabled: true, Idle: 5 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, func(c *Config) { c.HostInput = pauser })
	return e, pauser, clock
}

// get_control_status は、ホストの実入力による一時停止の状態（paused・since・resumes_at）を見せ、再開すると戻る。
func TestControlStatusReportsHostInputPause(t *testing.T) {
	e, pauser, clock := newPauseEnv(t)
	cs := e.connect("c1")

	if got := status(t, cs); got.Paused || got.PausedReason != "" || got.ResumesAt != "" || got.ResumesInMs != nil {
		t.Errorf("status before host input = %+v, want not paused", got)
	}

	pauser.NoteHostInput("mouse")
	got := status(t, cs)
	if !got.Paused || got.PausedReason != "host_input" {
		t.Fatalf("status = %+v, want paused (host_input)", got)
	}
	if got.PausedSince != "2026-11-28T09:00:00Z" || got.ResumesAt != "2026-11-28T09:00:05Z" || got.ResumesInMs == nil || *got.ResumesInMs != 5000 {
		t.Errorf("status = %+v, want since 09:00:00, resumes_at 09:00:05, 5000 ms", got)
	}
	// 停止中も、リースの状態は今までどおり（弱めない）。
	if got.State != "idle" {
		t.Errorf("lease state = %q, want idle", got.State)
	}

	clock.Advance(5 * time.Second)
	if got := status(t, cs); got.Paused || got.ResumesAt != "" {
		t.Errorf("status after the idle time = %+v, want resumed", got)
	}
}

// 停止の拒否は、ツールのエラー（code = input_paused）で、再開の目安（detail.resumes_at・retry_after_ms）が付く。
func TestPausedInputToolErrorCarriesResumeHint(t *testing.T) {
	e, pauser, _ := newPauseEnv(t)
	cs := e.connect("c1")
	pauser.NoteHostInput("keyboard")
	e.ctl.err = pauser.Check() // 実の Control が返すのと同じエラー

	res := callTool(t, cs, "type", map[string]any{"text": "x"})
	if !res.IsError || res.errCode(t) != domain.CodeInputPaused {
		t.Fatalf("type while paused = %s (error=%v), want input_paused", res.text(), res.IsError)
	}
	var body errorBody
	res.decode(t, &body)
	if body.Detail["resumes_at"] != "2026-11-28T09:00:05Z" || body.Detail["retry_after_ms"] != float64(5000) {
		t.Errorf("detail = %v, want resumes_at and retry_after_ms", body.Detail)
	}
	if body.Message == "" {
		t.Error("empty message")
	}
}

// 一時停止の設定を渡さない構成（HostInput が nil）では、paused は常に false。
func TestControlStatusWithoutHostInputIsNotPaused(t *testing.T) {
	e := newEnv(t)
	if got := status(t, e.connect("c1")); got.Paused {
		t.Errorf("status = %+v", got)
	}
}
