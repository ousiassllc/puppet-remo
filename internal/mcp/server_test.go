package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// API 仕様 §3.3 のツール一覧（M1・M1.5・M4）。
var wantTools = map[string]bool{ // 値は readOnlyHint
	"get_screen_info":    true,
	"screenshot":         true,
	"cursor_position":    true,
	"mouse_move":         false,
	"left_click":         false,
	"right_click":        false,
	"middle_click":       false,
	"double_click":       false,
	"triple_click":       false,
	"left_click_drag":    false,
	"scroll":             false,
	"key":                false,
	"type":               false,
	"wait":               true,
	"list_windows":       true,
	"focus_window":       false,
	"clipboard_read":     true,
	"clipboard_write":    false,
	"get_ui_tree":        true,
	"acquire_control":    false,
	"release_control":    false,
	"get_control_status": true,
	// M1.5
	"list_launchable_apps": true,
	"launch_app":           false,
	// M4
	"run_task":    false,
	"get_task":    true,
	"cancel_task": false,
}

// ownWindowHints は、puppet-remo 自身のウィンドウについて説明に含めるべき語（ツール名 → 語）。
var ownWindowHints = map[string][]string{
	"screenshot":      {"never captured", "own_windows"},
	"list_windows":    {"blocked", "own_window", "not captured"},
	"mouse_move":      {"own_window", "own_windows"},
	"left_click":      {"own_window", "own_windows"},
	"left_click_drag": {"own_window", "own_windows"},
	"scroll":          {"own_window", "own_windows"},
	"key":             {"own_window", "own_windows"},
	"type":            {"own_window", "own_windows"},
}

func TestInitializeAndToolList(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")

	init := cs.InitializeResult()
	if init.ServerInfo.Name != "puppet-remo" || init.ServerInfo.Version != "9.9.9-test" {
		t.Errorf("ServerInfo = %+v", init.ServerInfo)
	}
	for _, want := range []string{"scaled", "lease", "acquired implicitly", "launch_app", "list_launchable_apps", "app_id",
		"already open window forward", "reused_existing_window", "screenshot or list_windows before typing",
		"never captured in screenshots", "input_blocked", "own_window", "own_windows", "blocked: true",
		"run_task", "get_task", "cancel_task", "asynchronously", "agent_disabled", "Only one task runs at a time",
		"untrusted", "only the model's claim"} {
		if !strings.Contains(init.Instructions, want) {
			t.Errorf("Instructions lack %q: %s", want, init.Instructions)
		}
	}

	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range list.Tools {
		if tool.Annotations == nil {
			t.Errorf("%s has no annotations", tool.Name)
			continue
		}
		got[tool.Name] = tool.Annotations.ReadOnlyHint
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
		// puppet-remo 自身のウィンドウは写らないが入力は拒否される。事前の手がかり（NFR-S06）。
		for _, hint := range ownWindowHints[tool.Name] {
			if !strings.Contains(tool.Description, hint) {
				t.Errorf("%s description lacks %q: %s", tool.Name, hint, tool.Description)
			}
		}
		// 既存のウィンドウが前に出るだけのアプリがあるので、入力の前に対象を確かめるよう促す（FR-M19）。
		if tool.Name == "launch_app" {
			for _, want := range []string{"reused_existing_window", "already open window forward", "confirm the target window with a screenshot or list_windows"} {
				if !strings.Contains(tool.Description, want) {
					t.Errorf("launch_app description lacks %q: %s", want, tool.Description)
				}
			}
		}
	}
	if len(got) != len(list.Tools) {
		t.Errorf("duplicate tool names in %d tools", len(list.Tools))
	}
	for name, ro := range wantTools {
		g, ok := got[name]
		switch {
		case !ok:
			t.Errorf("tool %s is not listed", name)
		case g != ro:
			t.Errorf("tool %s readOnlyHint = %v, want %v", name, g, ro)
		}
	}
	for name := range got {
		if _, ok := wantTools[name]; !ok {
			t.Errorf("unexpected tool %s", name)
		}
	}
	// 三つのタスクのツールは、ホストの設定にかかわらず常に一覧に載る（有効・無効の判断は Agent が agent_disabled で返す）。
	for _, m4 := range []string{"run_task", "get_task", "cancel_task"} {
		if _, ok := got[m4]; !ok {
			t.Errorf("%s must be registered", m4)
		}
	}
}

// mcp は起動許可リストを書く経路（service.AppRegistry）を持たない。受け取るのは読み取りだけの Control（NFR-S20）。
func TestMCPHasNoAppRegistry(t *testing.T) {
	registry := reflect.TypeFor[service.AppRegistry]()
	for _, typ := range []reflect.Type{reflect.TypeFor[Config](), reflect.TypeFor[Server]()} {
		for f := range typ.Fields() {
			if f.Type == registry || f.Type.Implements(registry) {
				t.Errorf("%s.%s (%s) can write the allow list", typ, f.Name, f.Type)
			}
		}
	}
}

func TestLimitsMatchSpec(t *testing.T) {
	if DefaultSessionTimeout != 30*time.Minute {
		t.Errorf("DefaultSessionTimeout = %v, want 30m (API 仕様 §8.1)", DefaultSessionTimeout)
	}
	if MaxRequestBodyBytes != 1<<20 {
		t.Errorf("MaxRequestBodyBytes = %d, want 1 MiB (NFR-S09)", MaxRequestBodyBytes)
	}
	if DefaultToolTimeout != 30*time.Second {
		t.Errorf("DefaultToolTimeout = %v, want 30s (NFR-S09)", DefaultToolTimeout)
	}
	if DefaultMaxSessions != 5 {
		t.Errorf("DefaultMaxSessions = %d, want 5 (NFR-C02)", DefaultMaxSessions)
	}
}

func TestNewRequiresDependencies(t *testing.T) {
	e := newEnv(t)
	full := Config{Control: e.ctl, Leases: e.leases, Principal: testPrincipal}
	cases := map[string]func(*Config){
		"Control":   func(c *Config) { c.Control = nil },
		"Leases":    func(c *Config) { c.Leases = nil },
		"Principal": func(c *Config) { c.Principal = nil },
	}
	for name, clear := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := full
			clear(&cfg)
			if _, err := New(cfg); err == nil {
				t.Errorf("New without %s succeeded", name)
			}
		})
	}
}

// post は MCP エンドポイントへ生の JSON-RPC を POST する。
func (e *env) post(clientID string, body []byte, header map[string]string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.http.URL, bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", testAuthorization)
	if clientID != "" {
		req.Header.Set(clientHeader, clientID)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`

func TestUnauthenticatedRequestIsRejected(t *testing.T) {
	e := newEnv(t)
	resp := e.post("", []byte(initializeBody), nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestRequestBodyLimit(t *testing.T) {
	e := newEnv(t)
	small := e.post("c1", []byte(initializeBody), nil)
	if small.StatusCode != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200", small.StatusCode)
	}

	// 1 MiB を 1 バイトでも超える本文は 413（NFR-S09）。
	pad := strings.Repeat("a", MaxRequestBodyBytes)
	big := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"` +
		pad + `","version":"0"}}}`
	resp := e.post("c1", []byte(big), nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
}

func TestSessionBelongsToTheClientThatCreatedIt(t *testing.T) {
	e := newEnv(t)
	resp := e.post("c1", []byte(initializeBody), nil)
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("no Mcp-Session-Id")
	}
	ping := []byte(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)

	if got := e.post("c1", ping, map[string]string{"Mcp-Session-Id": sid}).StatusCode; got != http.StatusOK {
		t.Errorf("owner status = %d, want 200", got)
	}
	// 別のクライアントが同じセッション ID を使ってもセッションを乗っ取れない。
	if got := e.post("c2", ping, map[string]string{"Mcp-Session-Id": sid}).StatusCode; got != http.StatusForbidden {
		t.Errorf("other client status = %d, want 403", got)
	}
}

func TestSessionLimit(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxSessions = 2 })
	first := e.connect("c1")
	e.connect("c2")

	resp := e.post("c3", []byte(initializeBody), nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var problem map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem["code"] != "conflict" || problem["type"] != "urn:puppet-remo:error:conflict" {
		t.Errorf("problem = %v", problem)
	}

	// 既存のセッションの呼び出しは上限の影響を受けない。
	if res := callTool(t, first, "get_control_status", nil); res.IsError {
		t.Errorf("call on an open session failed: %s", res.text())
	}
	// セッションが閉じると、空いた枠に新しいセッションを作れる。
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(waitLimit)
	for {
		cs, err := e.dial("c3")
		if err == nil {
			_ = cs.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a slot did not free up after the session closed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSessionTimeoutReleasesTheLease(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.SessionTimeout = 100 * time.Millisecond })
	cs, err := e.dial("c1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	if res := callTool(t, cs, "acquire_control", nil); res.IsError {
		t.Fatalf("acquire_control: %s", res.text())
	}
	// アイドルのままセッションが閉じられ、リースが解放される（リースのアイドル 5 分とは独立）。
	ev := e.waitEvent(isRelease)
	if ev.Reason != domain.ReleaseDisconnect {
		t.Errorf("release reason = %q, want %q", ev.Reason, domain.ReleaseDisconnect)
	}
	if _, held := e.leases.Holder(); held {
		t.Error("the lease is still held after the session timed out")
	}
}

func TestToolCallTimeout(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ToolTimeout = 20 * time.Millisecond })
	e.ctl.blockUntilDone = true
	cs := e.connect("c1")

	res := callTool(t, cs, "cursor_position", nil)
	if got := res.errCode(t); got != domain.CodeInternal {
		t.Errorf("code = %s, want internal", got)
	}
	if !strings.Contains(res.text(), "timed out") {
		t.Errorf("text = %q", res.text())
	}
}

// callToolAsync は cursor_position を別 goroutine で呼び、結果（エラー）を返すチャネルを返す。
func callToolAsync(cs *mcpsdk.ClientSession, name string) <-chan error {
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
		defer cancel()
		_, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: name})
		done <- err
	}()
	return done
}

// キルスイッチのクローザー（CloseSessions）は、実行中のツール呼び出しの ctx を取り消して中断させ、
// 全セッションを時間内に閉じる。ツールを待つ間に ctx を食いつぶさない。
func TestCloseSessionsAbortsInFlightToolCalls(t *testing.T) {
	e := newEnv(t)
	e.ctl.blockUntilDone = true
	e.ctl.cursorEntered = make(chan struct{}, 1)
	e.ctl.cursorCtxErr = make(chan error, 1)
	busy := e.connect("busy")
	e.connect("idle")
	eventually(t, "idle session", func() bool { return len(e.srv.Sessions()) == 2 })

	call := callToolAsync(busy, "cursor_position")
	select {
	case <-e.ctl.cursorEntered:
	case <-time.After(waitLimit):
		t.Fatal("the tool call never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := e.srv.CloseSessions(ctx); err != nil {
		t.Fatalf("CloseSessions: %v", err)
	}
	select {
	case err := <-e.ctl.cursorCtxErr:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("tool ctx error = %v, want context.Canceled", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("the in-flight tool's ctx was not cancelled")
	}
	// 中断された呼び出しの終わり方（応答なしで終了）は SDK 次第なので中身は問わず、時間内に返ることだけを確かめる。
	select {
	case err := <-call:
		t.Logf("the aborted call ended with: %v", err)
	case <-time.After(waitLimit):
		t.Fatal("the aborted call never returned")
	}
	eventually(t, "all sessions to leave the table", func() bool { return len(e.srv.Sessions()) == 0 })

	// 取り消しは実行中の呼び出しだけに効く。新しく張ったセッションのツールは取り消されない。
	e.ctl.blockUntilDone = false
	e.ctl.cursorEntered = nil
	cs := e.connect("again")
	if res := callTool(t, cs, "cursor_position", nil); res.IsError {
		t.Errorf("a call after CloseSessions failed: %s", res.text())
	}
}

// ctx を無視して返らない platform 呼び出しがあっても、CloseSessions は ctx の期限で返る
// （キルスイッチの 1 秒の上限を守る）。
func TestCloseSessionsReturnsWhenContextEndsWhileAToolIsStuck(t *testing.T) {
	e := newEnv(t)
	e.ctl.cursorEntered = make(chan struct{}, 1)
	e.ctl.cursorRelease = make(chan struct{})
	busy := e.connect("busy")
	e.connect("idle")
	eventually(t, "both sessions", func() bool { return len(e.srv.Sessions()) == 2 })

	call := callToolAsync(busy, "cursor_position")
	select {
	case <-e.ctl.cursorEntered:
	case <-time.After(waitLimit):
		t.Fatal("the tool call never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := e.srv.CloseSessions(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("CloseSessions error = %v, want deadline exceeded", err)
	}
	if took := time.Since(start); took > waitLimit/2 {
		t.Errorf("CloseSessions took %v, want about the 100ms the context allowed", took)
	}
	// 止まっていた呼び出しを解放すると、閉じ切れなかったセッションも閉じる。
	close(e.ctl.cursorRelease)
	select {
	case err := <-call:
		t.Logf("the released call ended with: %v", err)
	case <-time.After(waitLimit):
		t.Fatal("the released call never returned")
	}
	eventually(t, "all sessions to leave the table", func() bool { return len(e.srv.Sessions()) == 0 })
}
