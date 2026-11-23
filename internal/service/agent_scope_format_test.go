package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/openai"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// 中断の結果の形式（#158）。本物の openai クライアントを、ループバックのモックの /v1/responses（httptest。外部へ接続しない。実キーを使わない）に向け、
// 次のモデル呼び出しの input（原文）を検査する。偽 platform の試験で、実 API の受理の確認ではない（computer_call_output の直後の user メッセージを
// 実 API が受理するかは [INFERENCE]。docs/operations/smoke-windows.md §3.10）。

type mockResponsesAPI struct {
	t       *testing.T
	mu      sync.Mutex
	bodies  []map[string]any
	handler func(n int) (status int, output []any)
}

func newMockResponsesAPI(t *testing.T, handler func(n int) (int, []any)) (*mockResponsesAPI, string) {
	t.Helper()
	m := &mockResponsesAPI{t: t, handler: handler}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		m.mu.Lock()
		m.bodies = append(m.bodies, body)
		n := len(m.bodies)
		m.mu.Unlock()
		status, output := m.handler(n)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = w.Write([]byte(`{"error":{"message":"boom","type":"server_error"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resp", "object": "response", "status": "completed", "output": output,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
		})
	}))
	t.Cleanup(srv.Close)
	return m, srv.URL
}

func (m *mockResponsesAPI) inputOf(n int) []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n > len(m.bodies) {
		m.t.Fatalf("request %d was not made (%d requests)", n, len(m.bodies))
	}
	raw, _ := m.bodies[n-1]["input"].([]any)
	items := make([]map[string]any, len(raw))
	for i, r := range raw {
		items[i] = r.(map[string]any)
	}
	return items
}

func (m *mockResponsesAPI) requests() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.bodies)
}

func computerCallItem(callID string, actions ...map[string]any) map[string]any {
	as := make([]any, len(actions))
	for i, a := range actions {
		as[i] = a
	}
	return map[string]any{
		"type": "computer_call", "id": "cu_" + callID, "call_id": callID, "status": "completed",
		"pending_safety_checks": []any{}, "actions": as,
	}
}

func clickAction(p domain.Point) map[string]any {
	return map[string]any{"type": "click", "button": "left", "x": p.X, "y": p.Y}
}

func finalMessageItem(text string) map[string]any {
	return map[string]any{
		"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
	}
}

// outputImage は computer_call_output の画像（PNG）を復号する。
func outputImage(t *testing.T, item map[string]any) (w, h int) {
	t.Helper()
	out, _ := item["output"].(map[string]any)
	url, _ := out["image_url"].(string)
	b64, ok := strings.CutPrefix(url, "data:image/png;base64,")
	if !ok {
		t.Fatalf("output = %v, want a PNG data URL", item["output"])
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return img.Bounds().Dx(), img.Bounds().Dy()
}

func noteText(t *testing.T, item map[string]any) string {
	t.Helper()
	if item["type"] != "message" || item["role"] != "user" {
		t.Fatalf("item = %v, want a role:user message", item)
	}
	content, _ := item["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("note content = %v, want exactly one input_text", content)
	}
	c := content[0].(map[string]any)
	if c["type"] != "input_text" {
		t.Fatalf("note part = %v", c)
	}
	return c["text"].(string)
}

func countOutputs(items []map[string]any, callID string) int {
	n := 0
	for _, it := range items {
		if it["type"] == "computer_call_output" && it["call_id"] == callID {
			n++
		}
	}
	return n
}

// 中断後の次のモデル呼び出しの input は、(a) 元の computer_call（原文のまま）、(b) その call_id の computer_call_output がちょうど 1 件（新しいグループ合成画像）、
// (c) その直後の role:user のメッセージ（実行済み E 件・残りは未実行。E = 0 のときは「実行済み 0 件」）の順。さらに後のステップでも出力は 1 件のまま、
// 実行済みの入力は再実行されない（Recorder の件数が増えない）。
func TestAgentScopedYieldResultFormatOnTheWire(t *testing.T) {
	cases := []struct {
		name        string
		firstActs   func() []map[string]any
		beforeFirst func(ae *agentEnv) // k = 1 の中断のための、撮影のあとの変化
		executed    int
		wantText    string
		wantBounds  domain.Rect // 中断後のグループ合成画像の外接矩形
	}{
		{"E = 1 (k = 2)", func() []map[string]any {
			return []map[string]any{clickAction(mp(physFileButton, boundsT)), clickAction(mp(physFileItem, boundsT))}
		}, nil, 1, "実行済み 1 件、残り 1 件は実行していない", boundsT},
		{"E = 0 (k = 1)", func() []map[string]any {
			return []map[string]any{clickAction(mp(physInT, boundsT))}
		}, func(ae *agentEnv) {
			ae.windows.Add(ownedWindow("9101", domain.Rect{X: 450, Y: 120, W: 100, H: 60}, platformtest.FakeAppPID, platformtest.FakeAppStart, false))
		}, 0, "実行済み 0 件、残り 1 件は実行していない", domain.Rect{X: 100, Y: 100, W: 450, H: 300}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ae *agentEnv
			call1 := computerCallItem("call_1", tc.firstActs()...)
			call2 := computerCallItem("call_2", clickAction(mp(physInT, boundsT)))
			var api *mockResponsesAPI
			var url string
			api, url = newMockResponsesAPI(t, func(n int) (int, []any) {
				switch n {
				case 1:
					if tc.beforeFirst != nil {
						tc.beforeFirst(ae)
					}
					return http.StatusOK, []any{call1}
				case 2:
					return http.StatusOK, []any{call2}
				}
				return http.StatusOK, []any{finalMessageItem("done")}
			})
			model := openai.New(func(_ context.Context) (string, error) { return "sk-test-placeholder", nil }, openai.Config{BaseURL: url})
			ae = newScopedEnv(t, agentOpts{model: model})
			got := ae.waitTask(t, ae.startScoped(t).ID)
			requireTaskEnd(t, ae, got, domain.TaskCompleted, "")
			if api.requests() != 3 {
				t.Fatalf("requests = %d, want 3", api.requests())
			}

			in2 := api.inputOf(2)
			if len(in2) != 4 {
				t.Fatalf("input of request 2 has %d items, want 4 (user, computer_call, computer_call_output, user note): %v", len(in2), in2)
			}
			if !reflect.DeepEqual(in2[1], jsonRoundTrip(t, call1)) { // (a) 原文のまま
				t.Errorf("computer_call changed:\n got %v\nwant %v", in2[1], call1)
			}
			if in2[2]["type"] != "computer_call_output" || in2[2]["call_id"] != "call_1" || countOutputs(in2, "call_1") != 1 { // (b)
				t.Errorf("output = %v", in2[2])
			}
			want := fitSize(tc.wantBounds.W, tc.wantBounds.H, scopedMaxDim)
			if w, h := outputImage(t, in2[2]); w != want.W || h != want.H { // 新しいグループ合成画像（モニター全体ではない）
				t.Errorf("output image = %dx%d, want the %dx%d group composite", w, h, want.W, want.H)
			}
			if text := noteText(t, in2[3]); !strings.Contains(text, tc.wantText) || !strings.Contains(text, "NOT executed") { // (c)
				t.Errorf("note = %q, want it to contain %q", text, tc.wantText)
			}

			in3 := api.inputOf(3)
			if countOutputs(in3, "call_1") != 1 || countOutputs(in3, "call_2") != 1 || len(in3) != 6 {
				t.Errorf("input of request 3 = %v, want exactly one output per call_id", in3)
			}
			if got := ae.clicks(); got != tc.executed+1 { // 中断までに実行した分 + 2 つ目の応答の click だけ（再送・再実行なし）
				t.Errorf("clicks = %d, want %d", got, tc.executed+1)
			}
		})
	}
}

func jsonRoundTrip(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// 中断のあとの次のモデル呼び出しが 5xx で失敗しても、タスクは failed で終わり、実行済みの入力は再実行されない（Recorder の件数は中断までのまま）。
func TestAgentScopedModelServerErrorAfterYieldEndsFailedWithoutReplay(t *testing.T) {
	_, url := newMockResponsesAPI(t, func(n int) (int, []any) {
		if n == 1 {
			return http.StatusOK, []any{computerCallItem("call_1", clickAction(mp(physFileButton, boundsT)), clickAction(mp(physFileItem, boundsT)))}
		}
		return http.StatusInternalServerError, nil
	})
	model := openai.New(func(_ context.Context) (string, error) { return "sk-test-placeholder", nil }, openai.Config{BaseURL: url})
	ae := newScopedEnv(t, agentOpts{model: model})
	got := ae.waitTask(t, ae.startScoped(t).ID)
	if got.Status != domain.TaskFailed || got.ErrorCode != domain.TaskErrorOpenAI {
		t.Fatalf("task = %s / %s", got.Status, got.ErrorCode)
	}
	if ae.clicks() != 1 {
		t.Errorf("clicks = %d, want 1 (no retry, no replay)", ae.clicks())
	}
}

func textAction(s string) map[string]any { return map[string]any{"type": "type", "text": s} }

// 1 つの操作の途中（キー・文字の複数回の注入の途中、クリックの移動のあと）で対象グループが変わったとき、モデルへは「未実行」ではなく
// 「一部だけ実行済み」と伝える（重複入力を招かない。実行済み 0 件とは言わない）。注入済みの入力は再送されず、監査にも partially_executed が残る。
func TestAgentScopedYieldInTheMiddleOfAnOperationIsReportedAsPartial(t *testing.T) {
	cases := []struct {
		name  string
		acts  []map[string]any
		hook  func(env func() *agentEnv) func(in platform.InputInjector) platform.InputInjector
		check func(t *testing.T, ae *agentEnv)
		// 中断までに最後まで実行した Op の数と、残り（未実行）の数
		executed, notExecuted int
	}{
		{
			name: "type interrupted after the first chunk",
			acts: []map[string]any{textAction("0123456789abcdef"), clickAction(mp(physInT, boundsT))},
			hook: func(env func() *agentEnv) func(platform.InputInjector) platform.InputInjector {
				return func(in platform.InputInjector) platform.InputInjector {
					return &afterClickInput{InputInjector: in, afterText: func(n int) {
						if n == 1 {
							env().windows.Add(ownedWindow("9301", domain.Rect{X: 450, Y: 120, W: 100, H: 60}, platformtest.FakeAppPID, platformtest.FakeAppStart, false))
						}
					}}
				}
			},
			check: func(t *testing.T, ae *agentEnv) {
				if doc := ae.app.Document(); doc != "01234567" { // 最初の 8 文字だけが届き、続きは注入されない
					t.Errorf("document = %q, want only the first chunk", doc)
				}
				if ae.clicks() != 0 || len(ae.input.CallsTo("Text")) != 1 {
					t.Errorf("clicks = %d, texts = %d", ae.clicks(), len(ae.input.CallsTo("Text")))
				}
			},
			executed: 0, notExecuted: 1,
		},
		{
			name: "click interrupted after the pointer move",
			acts: []map[string]any{clickAction(mp(physInT, boundsT)), clickAction(mp(physInT, boundsT))},
			hook: func(env func() *agentEnv) func(platform.InputInjector) platform.InputInjector {
				return func(in platform.InputInjector) platform.InputInjector {
					return &afterClickInput{InputInjector: in, afterMove: func(n int) {
						if n == 1 {
							env().windows.Add(ownedWindow("9302", domain.Rect{X: 450, Y: 120, W: 100, H: 60}, platformtest.FakeAppPID, platformtest.FakeAppStart, false))
						}
					}}
				}
			},
			check: func(t *testing.T, ae *agentEnv) {
				if ae.clicks() != 0 || len(ae.input.CallsTo("MoveTo")) != 1 { // 移動だけが届き、クリックは届かない（無害）
					t.Errorf("clicks = %d, moves = %d", ae.clicks(), len(ae.input.CallsTo("MoveTo")))
				}
			},
			executed: 0, notExecuted: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ae *agentEnv
			api, url := newMockResponsesAPI(t, func(n int) (int, []any) {
				if n == 1 {
					return http.StatusOK, []any{computerCallItem("call_1", tc.acts...)}
				}
				return http.StatusOK, []any{finalMessageItem("done")}
			})
			model := openai.New(func(_ context.Context) (string, error) { return "sk-test-placeholder", nil }, openai.Config{BaseURL: url})
			ae = newScopedEnv(t, agentOpts{model: model, wrapInput: func(in platform.InputInjector) platform.InputInjector {
				return tc.hook(func() *agentEnv { return ae })(in)
			}})
			got := ae.waitTask(t, ae.startScoped(t).ID)
			requireTaskEnd(t, ae, got, domain.TaskCompleted, "")
			tc.check(t, ae)

			in2 := api.inputOf(2)
			if len(in2) != 4 || countOutputs(in2, "call_1") != 1 {
				t.Fatalf("input of request 2 = %v", in2)
			}
			text := noteText(t, in2[3])
			if !strings.Contains(text, "PARTIALLY") || !strings.Contains(text, "一部だけ実行済み") || strings.Contains(text, "実行済み 0 件") {
				t.Errorf("note = %q, want a partial-execution notice (never 'executed 0')", text)
			}
			y := batchYields(t, ae, got.ID)
			if len(y) != 1 || y[0]["partially_executed"] != float64(1) || y[0]["executed"] != float64(tc.executed) || y[0]["not_executed"] != float64(tc.notExecuted) {
				t.Errorf("batch_yielded = %v", y)
			}
		})
	}
}

// 中断の流れでも、要求は store:false・previous_response_id なしのまま（毎回全履歴を再送する。FR-G24）。
func TestAgentScopedYieldFlowKeepsStatelessRequests(t *testing.T) {
	api, url := newMockResponsesAPI(t, func(n int) (int, []any) {
		if n == 1 {
			return http.StatusOK, []any{computerCallItem("call_1", clickAction(mp(physFileButton, boundsT)), clickAction(mp(physFileItem, boundsT)))}
		}
		if n == 2 {
			return http.StatusOK, []any{computerCallItem("call_2", clickAction(mp(physFileItem, boundsT)))}
		}
		return http.StatusOK, []any{finalMessageItem("done")}
	})
	model := openai.New(func(_ context.Context) (string, error) { return "sk-test-placeholder", nil }, openai.Config{BaseURL: url})
	ae := newScopedEnv(t, agentOpts{model: model})
	got := ae.waitTask(t, ae.startScoped(t).ID)
	requireTaskEnd(t, ae, got, domain.TaskCompleted, "")
	if len(batchYields(t, ae, got.ID)) != 1 {
		t.Fatal("no yield happened")
	}
	for i := 1; i <= api.requests(); i++ {
		api.mu.Lock()
		body := api.bodies[i-1]
		api.mu.Unlock()
		if v, ok := body["store"].(bool); !ok || v {
			t.Errorf("request %d: store = %v, want false", i, body["store"])
		}
		if _, ok := body["previous_response_id"]; ok {
			t.Errorf("request %d has previous_response_id", i)
		}
	}
}
