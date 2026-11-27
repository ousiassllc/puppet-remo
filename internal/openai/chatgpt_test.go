package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// canaryToken は ChatGPT のアクセストークンの代わりに使う偽物。どのエラー・ログ・出力にも現れてはならない。
const canaryToken = "eyJ-canary-access-token-DO-NOT-LEAK"

func sseEvent(typ string, payload map[string]any) string {
	payload["type"] = typ
	b, _ := json.Marshal(payload)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", typ, b)
}

func completedEvent(output ...any) string {
	return sseEvent("response.completed", map[string]any{"response": map[string]any{
		"id": "resp_1", "status": "completed", "output": output,
		"usage": map[string]any{"input_tokens": 1200, "output_tokens": 34, "total_tokens": 1234},
	}})
}

func fnCall(callID, name, args string) map[string]any {
	return map[string]any{
		"type": "function_call", "id": "fc_" + callID, "call_id": callID, "namespace": "desktop",
		"name": name, "arguments": args, "status": "completed",
	}
}

func messageItem(text string) map[string]any {
	return map[string]any{
		"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
	}
}

// streamOf は SSE のチャンクを順に書いて流し切るハンドラ（Flush しながら）。
func streamOf(chunks ...string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, c := range chunks {
			_, _ = w.Write([]byte(c))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

func errorBody(status int, code, message string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, status, map[string]any{"error": map[string]any{
			"type": "invalid_request_error", "code": code, "message": message, "param": "model",
		}})
	}
}

func newChatGPTModel(fs *fakeServer, cfg ChatGPTConfig) Model {
	cfg.BaseURL = fs.url
	return NewChatGPT(func(context.Context) (string, error) { return canaryToken, nil }, cfg)
}

func chatgptInput(t *testing.T) StepInput {
	t.Helper()
	return StepInput{
		Model:        "gpt-6.1-terra",
		Instructions: "you operate a desktop",
		History:      NewHistory("open notepad", "", pngBytes("first"), 3),
	}
}

func assertNoToken(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), canaryToken) {
		t.Fatalf("error leaks the token: %q", err.Error())
	}
}

func TestChatGPTRequestShape(t *testing.T) {
	fs := newFakeServer(t, streamOf(completedEvent(messageItem("done"))))
	m := newChatGPTModel(fs, ChatGPTConfig{})

	h := NewHistory("open notepad", "", pngBytes("first"), 3)
	h.AddOutput([]OutputItem{{Raw: json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call_1","namespace":"desktop","name":"left_click","arguments":"{\"x\":1,\"y\":2}","status":"completed"}`)}})
	h.AddFunctionOutput("call_1", FunctionOutputOK)
	h.AddUserImage(pngBytes("after"), "")
	if _, err := m.Step(context.Background(), StepInput{Model: "gpt-6.1-terra", Instructions: "be careful", History: h}); err != nil {
		t.Fatalf("Step: %v", err)
	}

	reqs := fs.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if reqs[0].path != "/responses" {
		t.Errorf("path = %q, want /responses", reqs[0].path)
	}
	if reqs[0].auth != "Bearer "+canaryToken {
		t.Errorf("Authorization = %q, want the bearer token", reqs[0].auth)
	}
	body := decodeBody(t, reqs[0].body)

	// 送る項目はこの 6 つだけ（FR-G26）。reasoning・include・parallel_tool_calls・tool_choice・temperature・
	// max_output_tokens・metadata・previous_response_id などは送らない。
	var keys []string
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"input", "instructions", "model", "store", "stream", "tools"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("request keys = %v, want %v", keys, want)
	}
	for _, banned := range []string{"reasoning", "include", "parallel_tool_calls", "tool_choice", "temperature", "max_output_tokens",
		"metadata", "previous_response_id", "background", "conversation", "max_tool_calls", "top_p", "truncation", "user", "prompt"} {
		if _, ok := body[banned]; ok {
			t.Errorf("request has %q, which must not be sent", banned)
		}
	}
	if body["store"] != false || body["stream"] != true {
		t.Errorf("store = %v, stream = %v, want false and true", body["store"], body["stream"])
	}
	if body["model"] != "gpt-6.1-terra" || body["instructions"] != "be careful" {
		t.Errorf("model = %v, instructions = %v", body["model"], body["instructions"])
	}

	// tools は namespace desktop 1 つで、その中に DesktopTools が function として並ぶ。computer ツールは無い。
	tools := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1 namespace", len(tools))
	}
	ns := tools[0].(map[string]any)
	if ns["type"] != "namespace" || ns["name"] != "desktop" {
		t.Fatalf("tool = %v, want namespace desktop", ns)
	}
	fns := ns["tools"].([]any)
	if len(fns) != len(DesktopTools) {
		t.Fatalf("functions = %d, want %d", len(fns), len(DesktopTools))
	}
	for i, f := range fns {
		fn := f.(map[string]any)
		if fn["type"] != "function" || fn["name"] != DesktopTools[i].Name {
			t.Errorf("function %d = %v, want function %s", i, fn, DesktopTools[i].Name)
		}
		if _, ok := fn["parameters"].(map[string]any); !ok {
			t.Errorf("function %s has no parameters", DesktopTools[i].Name)
		}
	}

	// input: 最初のユーザーメッセージ、function_call（原文）、function_call_output、role: user の input_image。
	items := inputItems(t, body)
	if len(items) != 4 {
		t.Fatalf("input items = %d, want 4: %v", len(items), items)
	}
	if items[0]["role"] != "user" || items[1]["type"] != "function_call" || items[1]["namespace"] != "desktop" {
		t.Errorf("unexpected leading items: %v %v", items[0], items[1])
	}
	if items[2]["type"] != "function_call_output" || items[2]["call_id"] != "call_1" || items[2]["output"] != FunctionOutputOK {
		t.Errorf("function_call_output = %v", items[2])
	}
	if items[3]["role"] != "user" {
		t.Fatalf("last item = %v, want a user message", items[3])
	}
	part := items[3]["content"].([]any)[0].(map[string]any)
	if part["type"] != "input_image" || !strings.HasPrefix(part["image_url"].(string), "data:image/png;base64,") {
		t.Errorf("last item content = %v, want an input_image data URL", part)
	}
	if strings.Contains(string(reqs[0].body), `"role":"system"`) {
		t.Error("request has a system role message, which is rejected")
	}
}

func TestChatGPTCompletedFunctionCallsInOrder(t *testing.T) {
	reasoning := map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}}
	fs := newFakeServer(t, streamOf(
		sseEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_1", "status": "in_progress"}}),
		": keep-alive\n\n",
		completedEvent(
			reasoning,
			messageItem("looking"),
			fnCall("call_a", "left_click", `{"x":10,"y":20}`),
			fnCall("call_b", "type", `{"text":"hello"}`),
			fnCall("call_c", "screenshot", `{}`),
			fnCall("call_d", "key", `{"keys":"ctrl+s"}`),
		),
	))
	out, err := newChatGPTModel(fs, ChatGPTConfig{}).Step(context.Background(), chatgptInput(t))
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if out.ResponseID != "resp_1" || out.Usage.InputTokens != 1200 || out.Usage.OutputTokens != 34 {
		t.Errorf("ResponseID = %q, Usage = %+v", out.ResponseID, out.Usage)
	}
	if len(out.Items) != 6 {
		t.Fatalf("Items = %d, want 6 (every output item, as is)", len(out.Items))
	}
	var first struct{ Type string }
	_ = json.Unmarshal(out.Items[0].Raw, &first)
	if first.Type != "reasoning" {
		t.Errorf("Items[0] = %s, want the reasoning item", out.Items[0].Raw)
	}
	if out.FinalText != "" || out.CallID != "" || len(out.Actions) != 0 {
		t.Errorf("FinalText = %q, CallID = %q, Actions = %v; a function_call response has none of them", out.FinalText, out.CallID, out.Actions)
	}
	wantCalls := []ToolCall{
		{CallID: "call_a", Name: "left_click", Op: domain.Click{At: &domain.Point{X: 10, Y: 20}, Button: domain.ButtonLeft, Count: 1, Monitor: domain.MonitorPrimary}},
		{CallID: "call_b", Name: "type", Op: domain.TypeText{Text: "hello"}},
		{CallID: "call_c", Name: "screenshot", Op: nil},
		{CallID: "call_d", Name: "key", Op: domain.KeyPress{Keys: domain.KeyCombo{Mods: []domain.Modifier{"ctrl"}, Key: "s"}}},
	}
	if !reflect.DeepEqual(out.Calls, wantCalls) {
		t.Errorf("Calls = %#v\nwant   %#v", out.Calls, wantCalls)
	}
}

func TestChatGPTFinalText(t *testing.T) {
	fs := newFakeServer(t, streamOf(completedEvent(
		messageItem("all "),
		map[string]any{"type": "message", "id": "msg_2", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "refusal", "refusal": "done"}}},
	)))
	out, err := newChatGPTModel(fs, ChatGPTConfig{}).Step(context.Background(), chatgptInput(t))
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(out.Calls) != 0 || out.FinalText != "all done" {
		t.Errorf("Calls = %v, FinalText = %q, want no calls and %q", out.Calls, out.FinalText, "all done")
	}
}

func TestChatGPTCompletedWithoutFinalNewline(t *testing.T) {
	ev := strings.TrimSuffix(completedEvent(messageItem("ok")), "\n\n")
	fs := newFakeServer(t, streamOf(ev))
	out, err := newChatGPTModel(fs, ChatGPTConfig{}).Step(context.Background(), chatgptInput(t))
	if err != nil || out.FinalText != "ok" {
		t.Errorf("Step = %q, %v; want ok", out.FinalText, err)
	}
}

func TestChatGPTUnsupportedCalls(t *testing.T) {
	cases := map[string]map[string]any{
		"未知の名前":         fnCall("c1", "format_disk", `{}`),
		"不正な引数":         fnCall("c1", "left_click", `{"x":1}`),
		"範囲外":           fnCall("c1", "scroll", `{"x":1,"y":1,"direction":"up","amount":99}`),
		"未知の namespace": func() map[string]any { c := fnCall("c1", "left_click", `{}`); c["namespace"] = "shell"; return c }(),
		"namespace なし":  func() map[string]any { c := fnCall("c1", "left_click", `{}`); delete(c, "namespace"); return c }(),
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			fs := newFakeServer(t, streamOf(completedEvent(fnCall("ok", "screenshot", `{}`), call)))
			out, err := newChatGPTModel(fs, ChatGPTConfig{}).Step(context.Background(), chatgptInput(t))
			if !errors.Is(err, domain.ErrUnsupportedAction) {
				t.Fatalf("err = %v, want ErrUnsupportedAction", err)
			}
			if len(out.Calls) != 0 {
				t.Errorf("Calls = %v, want none on failure", out.Calls)
			}
		})
	}
}

func TestChatGPTTerminalFailuresReturnNoCalls(t *testing.T) {
	partial := sseEvent("response.output_item.done", map[string]any{"item": fnCall("call_x", "left_click", `{"x":1,"y":2}`)})
	cases := map[string][]string{
		"response.failed": {partial, sseEvent("response.failed", map[string]any{"response": map[string]any{
			"id": "resp_1", "status": "failed", "error": map[string]any{"code": "server_error", "message": "boom"}}})},
		"response.incomplete": {partial, sseEvent("response.incomplete", map[string]any{"response": map[string]any{
			"id": "resp_1", "status": "incomplete", "incomplete_details": map[string]any{"reason": "max_output_tokens"}}})},
		"error イベント":       {partial, sseEvent("error", map[string]any{"code": "server_error", "message": "boom"})},
		"error（入れ子）":       {partial, sseEvent("error", map[string]any{"error": map[string]any{"code": "server_error", "message": "boom"}})},
		"終端なしの切断":          {partial, sseEvent("response.created", map[string]any{"response": map[string]any{"id": "resp_1"}})},
		"空の応答":             {""},
		"途中で切れた completed": {"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r"},
		"completed だが status が違う": {sseEvent("response.completed", map[string]any{"response": map[string]any{
			"id": "resp_1", "status": "failed", "output": []any{fnCall("call_x", "left_click", `{"x":1,"y":2}`)}}})},
	}
	for name, chunks := range cases {
		t.Run(name, func(t *testing.T) {
			fs := newFakeServer(t, streamOf(chunks...))
			out, err := newChatGPTModel(fs, ChatGPTConfig{}).Step(context.Background(), chatgptInput(t))
			if err == nil {
				t.Fatal("want an error")
			}
			if len(out.Calls) != 0 || len(out.Items) != 0 || out.FinalText != "" {
				t.Errorf("output = %+v, want empty on failure", out)
			}
			if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrUsageLimit) || errors.Is(err, ErrPlanUnauthorized) || errors.Is(err, ErrPolicyDenied) {
				t.Errorf("err = %v is classified, want a plain failure", err)
			}
		})
	}
}

func TestChatGPTStopsAtTerminalEvent(t *testing.T) {
	// 終端のあとの内容は読まない（失敗のあとに completed が続いても失敗のまま）。
	fs := newFakeServer(t, streamOf(
		sseEvent("response.failed", map[string]any{"response": map[string]any{"error": map[string]any{"code": "server_error"}}}),
		completedEvent(fnCall("call_x", "screenshot", `{}`)),
	))
	out, err := newChatGPTModel(fs, ChatGPTConfig{}).Step(context.Background(), chatgptInput(t))
	if err == nil || len(out.Calls) != 0 {
		t.Errorf("Step = %+v, %v; want a failure", out, err)
	}
}

func TestChatGPTStreamErrorClassification(t *testing.T) {
	failed := func(code string) string {
		return sseEvent("response.failed", map[string]any{"response": map[string]any{
			"id": "resp_1", "status": "failed", "error": map[string]any{"code": code, "message": "m " + canaryToken}}})
	}
	cases := []struct {
		name  string
		chunk string
		want  error
	}{
		{"failed 使用量", failed(codeUsageLimit), ErrUsageLimit},
		{"failed invalid_user", failed(codeInvalidUser), ErrPlanUnauthorized},
		{"failed ポリシー", failed(codePolicyDenied), ErrPolicyDenied},
		{"failed 対象外", failed(codeUserNotEligib), ErrPolicyDenied},
		{"error イベントの使用量", sseEvent("error", map[string]any{"code": codeUsageLimit, "message": "m"}), ErrUsageLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeServer(t, streamOf(completedPartial(), tc.chunk))
			out, err := newChatGPTModel(fs, ChatGPTConfig{}).Step(context.Background(), chatgptInput(t))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(out.Calls) != 0 {
				t.Errorf("Calls = %v, want none", out.Calls)
			}
			assertNoToken(t, err)
			var api *APIError
			if !errors.As(err, &api) || api.Code == "" {
				t.Errorf("errors.As *APIError = %v, %v; want the code kept", api, errors.As(err, &api))
			}
		})
	}
}

func completedPartial() string {
	return sseEvent("response.output_item.done", map[string]any{"item": fnCall("call_x", "left_click", `{"x":1,"y":2}`)})
}

func TestChatGPTHTTPErrorClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
		want   error // nil は分類なし（*APIError のまま）
	}{
		{"429 使用量", 429, codeUsageLimit, ErrUsageLimit},
		{"401", 401, "invalid_token", ErrPlanUnauthorized},
		{"401 コードなし", 401, "", ErrPlanUnauthorized},
		{"invalid_user", 403, codeInvalidUser, ErrPlanUnauthorized},
		{"403 ポリシー", 403, codePolicyDenied, ErrPolicyDenied},
		{"403 対象外", 403, codeUserNotEligib, ErrPolicyDenied},
		{"429 その他", 429, "rate_limit_exceeded", nil},
		{"500", 500, "server_error", nil},
		{"400", 400, "invalid_request", nil},
	}
	all := []error{ErrUsageLimit, ErrPlanUnauthorized, ErrPolicyDenied}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeServer(t, errorBody(tc.status, tc.code, "message "+canaryToken))
			_, err := newChatGPTModel(fs, ChatGPTConfig{}).Step(context.Background(), chatgptInput(t))
			assertNoToken(t, err)
			if len(fs.requests()) != 1 {
				t.Errorf("requests = %d, want 1 (no retry)", len(fs.requests()))
			}
			// API キー経路の 401（ErrUnauthorized）とは一致させない。
			if errors.Is(err, ErrUnauthorized) {
				t.Errorf("err = %v matches ErrUnauthorized", err)
			}
			for _, k := range all {
				if got := errors.Is(err, k); got != (k == tc.want) {
					t.Errorf("errors.Is(err, %v) = %v, want %v", k, got, k == tc.want)
				}
			}
			var api *APIError
			if !errors.As(err, &api) {
				t.Fatalf("err = %v, want *APIError reachable", err)
			}
			if api.Status != tc.status || api.Code != tc.code || (tc.code != "" && api.Param != "model") {
				t.Errorf("APIError = %+v, want status %d code %q param model", api, tc.status, tc.code)
			}
			if tc.status != 401 && !strings.Contains(api.Message, "message [redacted]") {
				t.Errorf("Message = %q, want the token redacted", api.Message)
			}
		})
	}
}

func TestChatGPTRequestFailureDoesNotLeakToken(t *testing.T) {
	boom := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial failed for %s", req.Header.Get("Authorization"))
	})
	m := NewChatGPT(func(context.Context) (string, error) { return canaryToken, nil }, ChatGPTConfig{Transport: boom, BaseURL: "http://127.0.0.1:1"})
	_, err := m.Step(context.Background(), chatgptInput(t))
	assertNoToken(t, err)
	if errors.Is(err, ErrPlanUnauthorized) || errors.Is(err, ErrUsageLimit) {
		t.Errorf("err = %v is classified", err)
	}
	_, err = ListChatGPTModels(context.Background(), canaryToken, ChatGPTConfig{Transport: boom})
	assertNoToken(t, err)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestChatGPTTimeout(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // クライアントが打ち切るまで終端のイベントを送らない
	})
	m := newChatGPTModel(fs, ChatGPTConfig{Timeout: 100 * time.Millisecond})
	start := time.Now()
	out, err := m.Step(context.Background(), chatgptInput(t))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	assertNoToken(t, err)
	if len(out.Calls) != 0 {
		t.Errorf("Calls = %v", out.Calls)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %v, want about the timeout", d)
	}
}

func TestChatGPTTokenErrors(t *testing.T) {
	m := NewChatGPT(func(context.Context) (string, error) { return "", fmt.Errorf("refresh: %w", ErrPlanUnauthorized) }, ChatGPTConfig{BaseURL: "http://127.0.0.1:1"})
	if _, err := m.Step(context.Background(), chatgptInput(t)); !errors.Is(err, ErrPlanUnauthorized) {
		t.Errorf("err = %v, want the token error to be reachable", err)
	}
	m = NewChatGPT(func(context.Context) (string, error) { return "", nil }, ChatGPTConfig{BaseURL: "http://127.0.0.1:1"})
	if _, err := m.Step(context.Background(), chatgptInput(t)); err == nil {
		t.Error("empty token: want an error")
	}
}

func TestChatGPTStepRejectsBadInput(t *testing.T) {
	fs := newFakeServer(t, streamOf(completedEvent(messageItem("x"))))
	m := newChatGPTModel(fs, ChatGPTConfig{})
	if _, err := m.Step(context.Background(), StepInput{History: NewHistory("t", "", nil, 1)}); err == nil {
		t.Error("empty model: want an error")
	}
	// 出力の欠けた function_call は送る前に ErrCallPairing。
	h := NewHistory("t", "", nil, 1)
	h.AddOutput([]OutputItem{{Raw: json.RawMessage(`{"type":"function_call","call_id":"c1","namespace":"desktop","name":"screenshot","arguments":"{}"}`)}})
	if _, err := m.Step(context.Background(), StepInput{Model: "m", History: h}); !errors.Is(err, ErrCallPairing) {
		t.Errorf("err = %v, want ErrCallPairing", err)
	}
	if n := len(fs.requests()); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
}

func TestChatGPTDoesNotFollowRedirects(t *testing.T) {
	other := newFakeServer(t, streamOf(completedEvent(messageItem("x"))))
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		http.Redirect(w, r, other.url+"/responses", http.StatusTemporaryRedirect)
	})
	_, err := newChatGPTModel(fs, ChatGPTConfig{}).Step(context.Background(), chatgptInput(t))
	assertNoToken(t, err)
	if n := len(other.requests()); n != 0 {
		t.Errorf("redirect target got %d requests, want 0", n)
	}
}

func TestListChatGPTModels(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, 200, map[string]any{"data": []any{
			map[string]any{"slug": "gpt-6.1-terra", "display_name": "GPT 6.1 Terra", "visibility": "list"},
			map[string]any{"slug": "hidden-model", "display_name": "Hidden", "visibility": "hide"},
			map[string]any{"slug": "gpt-6.1-sol", "display_name": "GPT 6.1 Sol", "visibility": "list"},
			map[string]any{"id": "only-id", "visibility": "list"},
			map[string]any{"slug": "no-visibility", "display_name": "x"},
			map[string]any{"slug": "named", "visibility": "list"},
		}})
	})
	got, err := ListChatGPTModels(context.Background(), canaryToken, ChatGPTConfig{BaseURL: fs.url})
	if err != nil {
		t.Fatalf("ListChatGPTModels: %v", err)
	}
	want := []PlanModelInfo{
		{Slug: "gpt-6.1-terra", DisplayName: "GPT 6.1 Terra"},
		{Slug: "gpt-6.1-sol", DisplayName: "GPT 6.1 Sol"},
		{Slug: "only-id", DisplayName: "only-id"},
		{Slug: "named", DisplayName: "named"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("models = %+v, want %+v", got, want)
	}
	reqs := fs.requests()
	if len(reqs) != 1 || reqs[0].path != "/models" || reqs[0].auth != "Bearer "+canaryToken {
		t.Errorf("requests = %+v, want one GET /models with the bearer", reqs)
	}
}

func TestListChatGPTModelsEmpty(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, 200, map[string]any{"data": []any{}})
	})
	got, err := ListChatGPTModels(context.Background(), canaryToken, ChatGPTConfig{BaseURL: fs.url})
	if err != nil || len(got) != 0 {
		t.Errorf("models = %v, %v; want empty", got, err)
	}
}

func TestListChatGPTModelsErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
		want   error
	}{
		{"401", 401, "", ErrPlanUnauthorized},
		{"429", 429, codeUsageLimit, ErrUsageLimit},
		{"403", 403, codePolicyDenied, ErrPolicyDenied},
		{"500", 500, "server_error", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeServer(t, errorBody(tc.status, tc.code, "m "+canaryToken))
			_, err := ListChatGPTModels(context.Background(), canaryToken, ChatGPTConfig{BaseURL: fs.url})
			assertNoToken(t, err)
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if errors.Is(err, ErrUnauthorized) {
				t.Errorf("err = %v matches ErrUnauthorized", err)
			}
			var api *APIError
			if !errors.As(err, &api) || api.Status != tc.status {
				t.Errorf("errors.As = %v, want status %d", api, tc.status)
			}
		})
	}
	if _, err := ListChatGPTModels(context.Background(), "", ChatGPTConfig{}); err == nil {
		t.Error("empty token: want an error")
	}
}

func TestListChatGPTModelsTimeout(t *testing.T) {
	fs := newFakeServer(t, func(_ http.ResponseWriter, r *http.Request, _ int) { <-r.Context().Done() })
	_, err := ListChatGPTModels(context.Background(), canaryToken, ChatGPTConfig{BaseURL: fs.url, Timeout: 100 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
	assertNoToken(t, err)
}
