package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// canaryKey は API キーの代わりに使う文字列。どのエラー・ログ・出力にも現れてはならない。
const canaryKey = "sk-canary-0123456789-DO-NOT-LEAK"

type recorded struct {
	auth string
	path string
	body []byte
}

// fakeServer は Responses API の偽物。handle が応答を決め、受け取った要求を記録する。
type fakeServer struct {
	t    *testing.T
	mu   sync.Mutex
	reqs []recorded
	url  string
}

func newFakeServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, n int)) *fakeServer {
	t.Helper()
	fs := &fakeServer{t: t}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fs.mu.Lock()
		fs.reqs = append(fs.reqs, recorded{auth: r.Header.Get("Authorization"), path: r.URL.Path, body: body})
		n := len(fs.reqs)
		fs.mu.Unlock()
		handle(w, r, n)
	}))
	t.Cleanup(srv.Close)
	fs.url = srv.URL
	return fs
}

func (fs *fakeServer) requests() []recorded {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]recorded(nil), fs.reqs...)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// respond は completed の応答（output と usage つき）を返すハンドラ。
func respond(output ...any) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "resp_1", "object": "response", "status": "completed",
			"output": output,
			"usage":  map[string]any{"input_tokens": 1200, "output_tokens": 34, "total_tokens": 1234},
		})
	}
}

func newModel(fs *fakeServer, cfg Config) Model {
	cfg.BaseURL = fs.url
	return New(func(context.Context) (string, error) { return canaryKey, nil }, cfg)
}

func pngBytes(tag string) []byte { return []byte("\x89PNG-" + tag) }

func computerCall(callID string, actions []any, checks ...any) map[string]any {
	if checks == nil {
		checks = []any{}
	}
	return map[string]any{
		"type": "computer_call", "id": "cu_1", "call_id": callID, "status": "completed",
		"pending_safety_checks": checks, "actions": actions,
	}
}

func decodeBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	return m
}

func inputItems(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("input is not a list: %#v", body["input"])
	}
	items := make([]map[string]any, len(raw))
	for i, r := range raw {
		items[i] = r.(map[string]any)
	}
	return items
}

func TestStepRequestShape(t *testing.T) {
	fs := newFakeServer(t, respond(map[string]any{
		"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": "done", "annotations": []any{}}},
	}))
	m := newModel(fs, Config{})

	// 出力アイテムの原文（空白・キー順・未知のフィールドも含む）。そのまま送られること。
	reasoning := json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque", "x_unknown": {"a": 1}}`)
	call := json.RawMessage(`{"type":"computer_call","id":"cu_1","call_id":"call_1","status":"completed","pending_safety_checks":[],"actions":[{"type":"click","button":"left","x":10,"y":20}]}`)

	h := NewHistory("open notepad", "fast: focused window", pngBytes("first"), 3)
	h.AddOutput([]OutputItem{{Raw: reasoning}, {Raw: call}})
	h.AddComputerOutput("call_1", pngBytes("s1"), nil)

	if _, err := m.Step(context.Background(), StepInput{Model: "gpt-test", History: h}); err != nil {
		t.Fatalf("Step: %v", err)
	}
	reqs := fs.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if reqs[0].path != "/responses" {
		t.Errorf("path = %q", reqs[0].path)
	}
	if reqs[0].auth != "Bearer "+canaryKey {
		t.Errorf("Authorization header not sent as Bearer key")
	}
	body := decodeBody(t, reqs[0].body)
	if body["store"] != false {
		t.Errorf("store = %#v, want false", body["store"])
	}
	if _, ok := body["previous_response_id"]; ok {
		t.Error("previous_response_id must not be sent")
	}
	if _, ok := body["include"]; ok {
		t.Error("include must be omitted by default")
	}
	if body["model"] != "gpt-test" {
		t.Errorf("model = %#v", body["model"])
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 || fmt.Sprint(tools[0]) != "map[type:computer]" {
		t.Errorf("tools = %#v, want [{type:computer}]", body["tools"])
	}

	items := inputItems(t, body)
	if len(items) != 4 {
		t.Fatalf("input items = %d, want 4: %s", len(items), reqs[0].body)
	}
	// 1: 最初のユーザーメッセージ（指示・要約・画像）。
	if items[0]["role"] != "user" {
		t.Errorf("first item role = %#v", items[0]["role"])
	}
	content := items[0]["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("user content parts = %d, want 3", len(content))
	}
	if p := content[0].(map[string]any); p["type"] != "input_text" || p["text"] != "open notepad" {
		t.Errorf("instruction part = %#v", p)
	}
	if p := content[1].(map[string]any); p["type"] != "input_text" || p["text"] != "fast: focused window" {
		t.Errorf("handoff part = %#v", p)
	}
	img := content[2].(map[string]any)
	if img["type"] != "input_image" || img["detail"] != "original" || !strings.HasPrefix(img["image_url"].(string), "data:image/png;base64,") {
		t.Errorf("image part = %#v", img)
	}
	// 2・3: 出力アイテムは原文のまま（バイト列が要求本文にそのまま入る）。
	// （SDK が JSON を詰めるので、空白だけは除く。キーの順序・値・未知のフィールドはそのまま。）
	for _, raw := range []json.RawMessage{reasoning, call} {
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(reqs[0].body, compact.Bytes()) {
			t.Errorf("output item not sent verbatim: %s", raw)
		}
	}
	// 4: computer_call_output（画像つき）。
	out := items[3]
	if out["type"] != "computer_call_output" || out["call_id"] != "call_1" {
		t.Errorf("computer_call_output = %#v", out)
	}
	if _, ok := out["acknowledged_safety_checks"]; ok {
		t.Error("acknowledged_safety_checks must be omitted when nothing is acknowledged")
	}
	shot := out["output"].(map[string]any)
	if shot["type"] != "computer_screenshot" || !strings.HasPrefix(shot["image_url"].(string), "data:image/png;base64,") {
		t.Errorf("output = %#v", shot)
	}
}

func TestStepKeepsOnlyNewestScreenshots(t *testing.T) {
	fs := newFakeServer(t, respond())
	m := newModel(fs, Config{})

	h := NewHistory("task", "", pngBytes("first"), 2)
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("call_%d", i)
		h.AddOutput([]OutputItem{{Raw: json.RawMessage(fmt.Sprintf(`{"type":"computer_call","call_id":%q}`, id))}})
		h.AddComputerOutput(id, pngBytes(id), nil)
	}
	if _, err := m.Step(context.Background(), StepInput{Model: "gpt-test", History: h}); err != nil {
		t.Fatalf("Step: %v", err)
	}
	items := inputItems(t, decodeBody(t, fs.requests()[0].body))

	var images, placeholders int
	for _, it := range items {
		switch it["type"] {
		case "computer_call_output":
			switch o := it["output"].(type) {
			case map[string]any:
				images++
			case string:
				if o != OmittedScreenshot {
					t.Errorf("placeholder = %q", o)
				}
				placeholders++
			default:
				t.Errorf("output = %#v", o)
			}
		case "message":
			for _, p := range it["content"].([]any) {
				p := p.(map[string]any)
				if p["type"] == "input_image" {
					images++
				}
				if p["text"] == OmittedScreenshot {
					placeholders++
				}
			}
		}
	}
	// 最初のメッセージ + 4 つの computer_call_output = 画像 5 枚のうち、新しい 2 枚だけ画像つき。
	if images != 2 || placeholders != 3 {
		t.Fatalf("images = %d placeholders = %d, want 2 and 3", images, placeholders)
	}
	// 新しい 2 枚は末尾の 2 つの computer_call_output。
	last := items[len(items)-1]["output"]
	prev := items[len(items)-3]["output"]
	if _, ok := last.(map[string]any); !ok {
		t.Errorf("newest output lost its image: %#v", last)
	}
	if _, ok := prev.(map[string]any); !ok {
		t.Errorf("second newest output lost its image: %#v", prev)
	}
}

func TestStepInclude(t *testing.T) {
	fs := newFakeServer(t, respond())
	m := newModel(fs, Config{Include: []string{"reasoning.encrypted_content"}})
	if _, err := m.Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)}); err != nil {
		t.Fatalf("Step: %v", err)
	}
	body := decodeBody(t, fs.requests()[0].body)
	inc, _ := body["include"].([]any)
	if len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %#v", body["include"])
	}
}

func TestStepAcknowledgedSafetyChecksEchoed(t *testing.T) {
	fs := newFakeServer(t, respond())
	m := newModel(fs, Config{})
	h := NewHistory("t", "", pngBytes("a"), 3)
	h.AddOutput([]OutputItem{{Raw: json.RawMessage(`{"type":"computer_call","call_id":"c1"}`)}})
	h.AddComputerOutput("c1", pngBytes("b"), []SafetyCheck{
		{ID: "sc_1", Code: "malicious_instructions", Message: "looks bad"},
		{ID: "sc_2"},
	})
	if _, err := m.Step(context.Background(), StepInput{Model: "m", History: h}); err != nil {
		t.Fatalf("Step: %v", err)
	}
	items := inputItems(t, decodeBody(t, fs.requests()[0].body))
	acks := items[len(items)-1]["acknowledged_safety_checks"].([]any)
	if len(acks) != 2 {
		t.Fatalf("acknowledged_safety_checks = %#v", acks)
	}
	first := acks[0].(map[string]any)
	if first["id"] != "sc_1" || first["code"] != "malicious_instructions" || first["message"] != "looks bad" {
		t.Errorf("first ack = %#v", first)
	}
	second := acks[1].(map[string]any)
	if second["id"] != "sc_2" || len(second) != 1 {
		t.Errorf("second ack = %#v", second)
	}
}

func TestStepParsesComputerCall(t *testing.T) {
	reasoning := map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}}
	call := computerCall("call_9", []any{
		map[string]any{"type": "click", "button": "left", "x": 100, "y": 200},
		map[string]any{"type": "screenshot"},
		map[string]any{"type": "type", "text": "hello"},
		map[string]any{"type": "keypress", "keys": []string{"CTRL", "A"}},
	}, map[string]any{"id": "sc_7", "code": "malicious_instructions", "message": "check this"})
	fs := newFakeServer(t, respond(reasoning, call))
	m := newModel(fs, Config{})

	out, err := m.Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if out.CallID != "call_9" || out.ResponseID != "resp_1" || out.FinalText != "" {
		t.Errorf("out = %+v", out)
	}
	if out.Usage != (Usage{InputTokens: 1200, OutputTokens: 34}) {
		t.Errorf("usage = %+v", out.Usage)
	}
	if len(out.SafetyChecks) != 1 || out.SafetyChecks[0] != (SafetyCheck{ID: "sc_7", Code: "malicious_instructions", Message: "check this"}) {
		t.Errorf("safety checks = %+v", out.SafetyChecks)
	}
	at := domain.Point{X: 100, Y: 200}
	want := []domain.Op{
		domain.Click{At: &at, Button: domain.ButtonLeft, Count: 1, Monitor: domain.MonitorPrimary},
		domain.TypeText{Text: "hello"},
		domain.KeyPress{Keys: domain.KeyCombo{Mods: []domain.Modifier{domain.ModCtrl}, Key: "a"}},
	}
	assertOps(t, out.Actions, want)

	// 出力アイテムは原文のまま、順序も保つ。
	if len(out.Items) != 2 {
		t.Fatalf("items = %d", len(out.Items))
	}
	for i, item := range []map[string]any{reasoning, call} {
		var got map[string]any
		if err := json.Unmarshal(out.Items[i].Raw, &got); err != nil {
			t.Fatalf("item %d is not JSON: %v", i, err)
		}
		if got["type"] != item["type"] || got["id"] != item["id"] {
			t.Errorf("item %d = %s", i, out.Items[i].Raw)
		}
	}
}

func TestStepParsesLegacySingleAction(t *testing.T) {
	call := map[string]any{
		"type": "computer_call", "id": "cu_1", "call_id": "c1", "status": "completed",
		"pending_safety_checks": []any{},
		"action":                map[string]any{"type": "move", "x": 5, "y": 6},
	}
	fs := newFakeServer(t, respond(call))
	out, err := newModel(fs, Config{}).Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	assertOps(t, out.Actions, []domain.Op{domain.MouseMove{At: domain.Point{X: 5, Y: 6}, Monitor: domain.MonitorPrimary}})
}

func TestStepFinalText(t *testing.T) {
	fs := newFakeServer(t, respond(map[string]any{
		"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
		"content": []any{
			map[string]any{"type": "output_text", "text": "All ", "annotations": []any{}},
			map[string]any{"type": "output_text", "text": "done.", "annotations": []any{}},
		},
	}))
	out, err := newModel(fs, Config{}).Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if out.CallID != "" || out.FinalText != "All done." || len(out.Actions) != 0 || len(out.Items) != 1 {
		t.Errorf("out = %+v", out)
	}
}

func TestStepComputerCallWithoutActions(t *testing.T) {
	fs := newFakeServer(t, respond(computerCall("c1", []any{map[string]any{"type": "screenshot"}})))
	out, err := newModel(fs, Config{}).Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)})
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if out.CallID != "c1" || len(out.Actions) != 0 {
		t.Errorf("a screenshot-only call must still be a computer_call with no ops: %+v", out)
	}
}

func TestStepUnsupportedAction(t *testing.T) {
	for name, action := range map[string]map[string]any{
		"unknown type":   {"type": "teleport", "x": 1},
		"back button":    {"type": "click", "button": "back", "x": 1, "y": 2},
		"held non-mod":   {"type": "click", "button": "left", "x": 1, "y": 2, "keys": []string{"a"}},
		"short drag":     {"type": "drag", "path": []any{map[string]any{"x": 1, "y": 2}}},
		"unknown key":    {"type": "keypress", "keys": []string{"NOSUCHKEY"}},
		"moving w/ keys": {"type": "move", "x": 1, "y": 2, "keys": []string{"ctrl"}},
	} {
		t.Run(name, func(t *testing.T) {
			fs := newFakeServer(t, respond(computerCall("c1", []any{
				map[string]any{"type": "click", "button": "left", "x": 1, "y": 1},
				action,
			})))
			_, err := newModel(fs, Config{}).Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)})
			if !errors.Is(err, domain.ErrUnsupportedAction) {
				t.Fatalf("err = %v, want ErrUnsupportedAction", err)
			}
		})
	}
}

func TestStepResponseFailedStatus(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": "resp_1", "object": "response", "status": "failed", "output": []any{},
			"error": map[string]any{"code": "server_error", "message": "boom " + canaryKey},
		})
	})
	_, err := newModel(fs, Config{}).Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)})
	if err == nil || errors.Is(err, domain.ErrUnsupportedAction) {
		t.Fatalf("err = %v, want a plain error", err)
	}
}

func TestStepHTTPErrorsDoNotLeakKey(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantMsg    bool
		wantType   string
		wantCode   string
		headerLeak bool
	}{
		{name: "401", status: 401, wantMsg: false, wantType: "invalid_request_error", wantCode: "invalid_api_key"},
		{name: "429", status: 429, wantMsg: true, wantType: "invalid_request_error", wantCode: "invalid_api_key"},
		{name: "500", status: 500, wantMsg: true, wantType: "invalid_request_error", wantCode: "invalid_api_key"},
		{name: "503", status: 503, wantMsg: true, wantType: "invalid_request_error", wantCode: "invalid_api_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
				writeJSON(w, tc.status, map[string]any{"error": map[string]any{
					"message": "Incorrect API key provided: " + canaryKey + ". slow down",
					"type":    "invalid_request_error", "code": "invalid_api_key",
				}})
			})
			_, err := newModel(fs, Config{}).Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)})
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v (%T), want *APIError", err, err)
			}
			if apiErr.Status != tc.status || apiErr.Type != tc.wantType || apiErr.Code != tc.wantCode {
				t.Errorf("apiErr = %+v", apiErr)
			}
			if (apiErr.Message != "") != tc.wantMsg {
				t.Errorf("message = %q, wantMsg = %v", apiErr.Message, tc.wantMsg)
			}
			assertNoKey(t, err)
			if n := len(fs.requests()); n != 1 {
				t.Errorf("requests = %d, want exactly 1 (no retries)", n)
			}
		})
	}
}

func TestStepNonJSONErrorBody(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		http.Error(w, "<html>bad gateway "+canaryKey+"</html>", http.StatusBadGateway)
	})
	_, err := newModel(fs, Config{}).Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 502 || apiErr.Message != "" {
		t.Fatalf("err = %v", err)
	}
	assertNoKey(t, err)
}

func TestStepTimeout(t *testing.T) {
	release := make(chan struct{})
	fs := newFakeServer(t, func(_ http.ResponseWriter, r *http.Request, _ int) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	m := newModel(fs, Config{Timeout: 50 * time.Millisecond})
	_, err := m.Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	assertNoKey(t, err)
	if n := len(fs.requests()); n != 1 {
		t.Errorf("requests = %d, want 1 (no retries)", n)
	}
}

func TestStepContextCancelAborts(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	fs := newFakeServer(t, func(_ http.ResponseWriter, r *http.Request, _ int) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	m := newModel(fs, Config{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := m.Step(ctx, StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context canceled", err)
		}
		assertNoKey(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Step did not return after cancel")
	}
}

func TestStepKeyIsReadPerCall(t *testing.T) {
	fs := newFakeServer(t, respond())
	var calls int
	m := New(func(context.Context) (string, error) {
		calls++
		return fmt.Sprintf("%s-%d", canaryKey, calls), nil
	}, Config{BaseURL: fs.url})
	for range 2 {
		if _, err := m.Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)}); err != nil {
			t.Fatalf("Step: %v", err)
		}
	}
	reqs := fs.requests()
	if reqs[0].auth != "Bearer "+canaryKey+"-1" || reqs[1].auth != "Bearer "+canaryKey+"-2" {
		t.Errorf("keys not read per call: %q, %q", reqs[0].auth, reqs[1].auth)
	}
}

func TestStepKeyFuncFailure(t *testing.T) {
	fs := newFakeServer(t, respond())
	sentinel := errors.New("no key registered")
	m := New(func(context.Context) (string, error) { return "", sentinel }, Config{BaseURL: fs.url})
	_, err := m.Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap the key error", err)
	}
	if n := len(fs.requests()); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}

	m = New(func(context.Context) (string, error) { return "", nil }, Config{BaseURL: fs.url})
	if _, err := m.Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)}); err == nil {
		t.Error("an empty key must be an error")
	}
}

func TestStepRequiresModelAndHistory(t *testing.T) {
	fs := newFakeServer(t, respond())
	m := newModel(fs, Config{})
	if _, err := m.Step(context.Background(), StepInput{History: NewHistory("t", "", nil, 3)}); err == nil {
		t.Error("empty model must be an error")
	}
	if _, err := m.Step(context.Background(), StepInput{Model: "m"}); err == nil {
		t.Error("nil history must be an error")
	}
}

func TestStepIgnoresEnvironment(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("OPENAI_API_KEY", "sk-from-env")
	t.Setenv("OPENAI_ORG_ID", "org-env")
	fs := newFakeServer(t, respond())
	if _, err := newModel(fs, Config{}).Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)}); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if got := fs.requests()[0].auth; got != "Bearer "+canaryKey {
		t.Errorf("environment leaked into the request: %q", got)
	}
}

// failingTransport は RoundTrip の失敗の文言にキー（Authorization）を含める。safeTransport がそれを伏せること。
type failingTransport struct{}

func (failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("dial failed while sending %q", req.Header.Get("Authorization"))
}

func TestSafeTransportRedactsAuthorization(t *testing.T) {
	m := New(func(context.Context) (string, error) { return canaryKey, nil }, Config{
		BaseURL: "http://openai.invalid", Transport: failingTransport{},
	})
	_, err := m.Step(context.Background(), StepInput{Model: "m", History: NewHistory("t", "", pngBytes("a"), 3)})
	if err == nil {
		t.Fatal("want an error")
	}
	assertNoKey(t, err)
	if !strings.Contains(err.Error(), "dial failed") {
		t.Errorf("error lost its cause: %v", err)
	}
}

func assertNoKey(t *testing.T, err error) {
	t.Helper()
	for _, s := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		if strings.Contains(s, canaryKey) || strings.Contains(s, "Bearer") {
			t.Fatalf("error leaks the API key: %s", s)
		}
	}
}

func assertOps(t *testing.T, got, want []domain.Op) {
	t.Helper()
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if len(got) != len(want) || string(gj) != string(wj) {
		t.Fatalf("ops =\n %s\nwant\n %s", gj, wj)
	}
	for i := range got {
		if fmt.Sprintf("%T", got[i]) != fmt.Sprintf("%T", want[i]) {
			t.Errorf("op %d type = %T, want %T", i, got[i], want[i])
		}
	}
}
