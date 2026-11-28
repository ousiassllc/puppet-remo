package app

import (
	"encoding/json"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

type pauseStatus struct {
	Paused    bool   `json:"paused"`
	ResumesAt string `json:"resumes_at"`
}

func mcpStatus(t *testing.T, cs *mcpsdk.ClientSession) pauseStatus {
	t.Helper()
	res := callTool(t, cs, "get_control_status")
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var st pauseStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return st
}

// 組み立てた App で、ホストの実入力を検知すると MCP の入力が input_paused で断られ、get_control_status に出る。
// 自分の入力（MCP の操作）では止まらない。読み取りは止めない。設定で無効にすると再開し、検知も止まる（#270）。
func TestHostInputPausesRemoteInputEndToEnd(t *testing.T) {
	e := newEnv(t)
	_, token := e.registerClient()
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)
	cs := e.dialMCP(token)

	// 既定は有効なので、Start で検知が始まっている。
	if !e.hostInput.Watching() {
		t.Fatal("host input is not watched by default")
	}
	// 自分の入力は止まらない（検知されるのは実入力だけ。ここでは何も Emit していない）。
	if res := callTool(t, cs, "mouse_move", map[string]any{"x": 10, "y": 10}); res.IsError {
		t.Fatalf("mouse_move before host input: %+v", res.Content)
	}
	if st := mcpStatus(t, cs); st.Paused {
		t.Fatalf("status = %+v, want not paused", st)
	}
	e.input.Reset()

	e.hostInput.Emit(platform.HostInputMouse)

	res := callTool(t, cs, "mouse_move", map[string]any{"x": 20, "y": 20})
	if !res.IsError || errorCode(t, res) != domain.CodeInputPaused {
		t.Fatalf("mouse_move while paused = %+v, want input_paused", res)
	}
	if got := len(e.input.CallsTo("MoveTo")); got != 0 {
		t.Errorf("MoveTo calls while paused = %d", got)
	}
	if st := mcpStatus(t, cs); !st.Paused || st.ResumesAt == "" {
		t.Errorf("status = %+v, want paused with resumes_at", st)
	}
	// 読み取りは止めない。
	if res := callTool(t, cs, "screenshot"); res.IsError {
		t.Errorf("screenshot while paused: %+v", res.Content)
	}
	// 押下中のキー・ボタンは、停止に入ったときに離される。
	eventually(t, "held input to be released on pause", func() bool { return len(e.input.CallsTo("ReleaseAll")) > 0 })

	// 設定で無効にすると、停止は解け、検知（フック）も止まる。
	ctx := t.Context()
	s, err := e.app.settings.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HostInputPauseEnabled || s.HostInputPauseIdleSec != 5 {
		t.Fatalf("default settings = enabled %v, idle %d; want true, 5", s.HostInputPauseEnabled, s.HostInputPauseIdleSec)
	}
	s.HostInputPauseEnabled = false
	if _, err := e.app.settings.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	if e.hostInput.Watching() {
		t.Error("host input is still watched after disabling")
	}
	if res := callTool(t, cs, "mouse_move", map[string]any{"x": 30, "y": 30}); res.IsError {
		t.Errorf("mouse_move after disabling: %+v", res.Content)
	}

	// 有効に戻すと検知が再開し、待ち時間の変更も反映される。
	s.HostInputPauseEnabled, s.HostInputPauseIdleSec = true, 9
	if _, err := e.app.settings.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	if !e.hostInput.Watching() {
		t.Error("host input is not watched after enabling")
	}
	if got := e.app.hostPause.Policy(); !got.Enabled || got.Idle != 9*time.Second {
		t.Errorf("policy = %+v", got)
	}
}

// 設定で無効のまま起動したときは、検知（キーボード・マウスのフック）を始めない。
func TestHostInputIsNotWatchedWhenDisabledAtStart(t *testing.T) {
	db := openDB(t)
	if err := db.Settings().Set(t.Context(), "hostinput.pause_enabled", json.RawMessage("false")); err != nil {
		t.Fatal(err)
	}
	e := newEnvWithDB(t, db)
	e.start()
	if e.hostInput.Watching() {
		t.Error("host input is watched although the setting is off")
	}
}
