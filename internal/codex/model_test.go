package codex_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/codex"
	"github.com/ousiassllc/puppet-remo/internal/codex/appserver"
	"github.com/ousiassllc/puppet-remo/internal/codex/codextest"
	"github.com/ousiassllc/puppet-remo/internal/codex/protocol"
	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/openai"
)

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

var (
	png1 = []byte("\x89PNG-first-screen")
	png2 = []byte("\x89PNG-second-screen")
)

type rig struct {
	t     *testing.T
	home  string
	work  string
	srv   *appserver.Server
	model *codex.Model

	mu    sync.Mutex
	fakes []*codextest.Fake
	procs []*codextest.Proc
}

// newRig は偽の app-server をつないだ Model を作る。setup は起動のたびに Fake を設定する。
func newRig(t *testing.T, signedIn bool, setup func(*codextest.Fake)) *rig {
	t.Helper()
	r := &rig{t: t, home: t.TempDir(), work: filepath.Join(t.TempDir(), "codex-work")}
	r.srv = appserver.New(appserver.Options{
		CodexHome: r.home, Version: "test",
		Start: func() (appserver.Process, error) {
			f := &codextest.Fake{CodexHome: r.home}
			if signedIn {
				f.SetAccount(&protocol.Account{Type: "chatgpt", Email: "user@example.invalid", PlanType: "plus"})
			}
			if setup != nil {
				setup(f)
			}
			p := codextest.Start(f)
			r.mu.Lock()
			r.fakes, r.procs = append(r.fakes, f), append(r.procs, p)
			r.mu.Unlock()
			return p, nil
		},
	})
	t.Cleanup(r.srv.Close)
	r.model = codex.NewModel(codex.ModelConfig{Server: r.srv, WorkDir: r.work})
	return r
}

func (r *rig) fake(i int) *codextest.Fake {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fakes[i]
}

func (r *rig) procCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.procs)
}

func newHistory() *openai.History { return openai.NewHistory("メモ帳を開いて", "", png1, 3) }

func step(t *testing.T, r *rig, h *openai.History) (openai.StepOutput, error) {
	t.Helper()
	return r.model.Step(ctxT(t), openai.StepInput{Model: "gpt-test", History: h, Instructions: "OPERATE THE DESKTOP"})
}

// feed は呼び出し側のループがすること: 出力アイテムと、実行結果・新しい画面を履歴へ足す。
func feed(h *openai.History, out openai.StepOutput, shot []byte) {
	h.AddOutput(out.Items)
	for _, c := range out.Calls {
		h.AddFunctionOutput(c.CallID, openai.FunctionOutputOK)
	}
	h.AddUserImage(shot, "")
}

func TestStepは道具の無効化されたスレッドを始め関数の呼び出しを返して応答し最終回答を返す(t *testing.T) {
	respCh := make(chan protocol.DynamicToolResponse, 1)
	r := newRig(t, true, func(f *codextest.Fake) {
		f.OnTurnStart = func(f *codextest.Fake, th, turn string, _ protocol.TurnStartParams) {
			resp, _, err := f.ToolCall(ctxT(t), th, turn, "call-1", "left_click", `{"x":10,"y":20}`)
			if err != nil {
				return
			}
			respCh <- resp
			f.ItemStarted(th, turn, "reasoning")
			f.AgentMessage(th, turn, "開きました")
			f.CompleteTurn(th, turn, "completed", "")
		}
	})
	h := newHistory()
	out, err := step(t, r, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Calls) != 1 || out.Calls[0].CallID != "call-1" || out.Calls[0].Name != "left_click" || out.FinalText != "" {
		t.Fatalf("out = %+v", out)
	}
	click, ok := out.Calls[0].Op.(domain.Click)
	if !ok || click.At == nil || click.At.X != 10 || click.At.Y != 20 {
		t.Fatalf("op = %#v", out.Calls[0].Op)
	}
	var item map[string]string
	if len(out.Items) != 1 || json.Unmarshal(out.Items[0].Raw, &item) != nil || item["type"] != "function_call" || item["call_id"] != "call-1" || item["namespace"] != "desktop" {
		t.Fatalf("items = %s", out.Items)
	}

	f := r.fake(0)
	// thread/start: 道具の無効化と dynamicTools。
	ts := f.Calls(protocol.MethodThreadStart)
	if len(ts) != 1 {
		t.Fatalf("thread/start calls = %d", len(ts))
	}
	var tp struct {
		Model            string                     `json:"model"`
		Ephemeral        bool                       `json:"ephemeral"`
		BaseInstructions string                     `json:"baseInstructions"`
		Cwd              string                     `json:"cwd"`
		Sandbox          string                     `json:"sandbox"`
		ApprovalPolicy   string                     `json:"approvalPolicy"`
		DynamicTools     []protocol.DynamicToolSpec `json:"dynamicTools"`
	}
	if err := json.Unmarshal(ts[0].Params, &tp); err != nil {
		t.Fatal(err)
	}
	if tp.Model != "gpt-test" || !tp.Ephemeral || tp.BaseInstructions != "OPERATE THE DESKTOP" || tp.Cwd != r.work || tp.Sandbox != "read-only" || tp.ApprovalPolicy != "never" {
		t.Errorf("thread/start = %+v", tp)
	}
	if len(tp.DynamicTools) != 1 || tp.DynamicTools[0].Type != "namespace" || tp.DynamicTools[0].Name != openai.DesktopNamespace || len(tp.DynamicTools[0].Tools) != len(openai.DesktopTools) {
		t.Fatalf("dynamicTools = %+v", tp.DynamicTools)
	}
	for i, tool := range tp.DynamicTools[0].Tools {
		if tool.Type != "function" || tool.Name != openai.DesktopTools[i].Name || tool.InputSchema == nil {
			t.Errorf("tool %d = %+v", i, tool)
		}
	}
	// turn/start: text と localImage（一時ファイルは実在し、中身は最初の画面）。
	tu := f.Calls(protocol.MethodTurnStart)
	if len(tu) != 1 {
		t.Fatalf("turn/start calls = %d", len(tu))
	}
	var up struct {
		Input          []map[string]string `json:"input"`
		ApprovalPolicy string              `json:"approvalPolicy"`
		SandboxPolicy  map[string]string   `json:"sandboxPolicy"`
	}
	if err := json.Unmarshal(tu[0].Params, &up); err != nil {
		t.Fatal(err)
	}
	if len(up.Input) != 2 || up.Input[0]["type"] != "text" || up.Input[0]["text"] != "メモ帳を開いて" || up.Input[1]["type"] != "localImage" ||
		up.ApprovalPolicy != "never" || up.SandboxPolicy["type"] != "readOnly" {
		t.Fatalf("turn/start = %+v", up)
	}
	imgPath := up.Input[1]["path"]
	if b, err := os.ReadFile(imgPath); err != nil || string(b) != string(png1) {
		t.Fatalf("image file: %q %v", b, err)
	}

	// 次の Step: 実行結果と新しい画面が contentItems で応答され、最終回答が返る。
	feed(h, out, png2)
	out2, err := step(t, r, h)
	if err != nil {
		t.Fatal(err)
	}
	if out2.FinalText != "開きました" || len(out2.Calls) != 0 {
		t.Fatalf("out2 = %+v", out2)
	}
	resp := <-respCh
	if !resp.Success || len(resp.ContentItems) != 2 ||
		resp.ContentItems[0].Type != "inputText" || resp.ContentItems[0].Text != openai.FunctionOutputOK ||
		resp.ContentItems[1].Type != "inputImage" || resp.ContentItems[1].ImageURL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(png2) {
		t.Fatalf("response = %+v", resp)
	}

	// CloseTask: スレッドの購読を外し、画像の一時ファイルを消す。2 回目は何もしない。
	r.model.CloseTask(h)
	if len(f.Calls(protocol.MethodThreadUnsubscribe)) != 1 {
		t.Error("thread/unsubscribe was not sent")
	}
	if _, err := os.Stat(imgPath); err == nil {
		t.Error("the image file must be removed")
	}
	if ents, _ := os.ReadDir(r.work); len(ents) != 0 {
		t.Errorf("the work directory must be empty: %v", ents)
	}
	r.model.CloseTask(h)
	if len(f.Calls(protocol.MethodThreadUnsubscribe)) != 1 {
		t.Error("the second CloseTask must do nothing")
	}
}

func TestStepは同時に届いた複数の呼び出しを順に返して全部に応答する(t *testing.T) {
	var wg sync.WaitGroup
	r := newRig(t, true, func(f *codextest.Fake) {
		f.OnTurnStart = func(f *codextest.Fake, th, turn string, _ protocol.TurnStartParams) {
			for _, id := range []string{"a", "b"} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, _, _ = f.ToolCall(ctxT(t), th, turn, id, "screenshot", `{}`)
				}()
			}
			wg.Wait()
			f.AgentMessage(th, turn, "done")
			f.CompleteTurn(th, turn, "completed", "")
		}
	})
	h := newHistory()
	seen := map[string]bool{}
	for range 5 {
		out, err := step(t, r, h)
		if err != nil {
			t.Fatal(err)
		}
		if out.FinalText != "" {
			if out.FinalText != "done" || !seen["a"] || !seen["b"] {
				t.Fatalf("final = %q, seen = %v", out.FinalText, seen)
			}
			r.model.CloseTask(h)
			return
		}
		for _, c := range out.Calls {
			seen[c.CallID] = true
			if c.Op != nil {
				t.Errorf("screenshot must be a nil op: %#v", c.Op)
			}
		}
		feed(h, out, png2)
	}
	t.Fatal("no final answer")
}

func TestStepは使用量の上限と認可切れを分類して返す(t *testing.T) {
	for kind, want := range map[string]error{"usageLimitExceeded": openai.ErrUsageLimit, "unauthorized": openai.ErrPlanUnauthorized} {
		t.Run(kind, func(t *testing.T) {
			r := newRig(t, true, func(f *codextest.Fake) {
				f.OnTurnStart = func(f *codextest.Fake, th, turn string, _ protocol.TurnStartParams) {
					f.CompleteTurn(th, turn, "failed", kind)
				}
			})
			h := newHistory()
			_, err := step(t, r, h)
			if !errors.Is(err, want) {
				t.Fatalf("err = %v, want %v", err, want)
			}
			if kind == "usageLimitExceeded" && errors.Is(err, openai.ErrPlanUnauthorized) {
				t.Error("classes must not overlap")
			}
			r.model.CloseTask(h)
		})
	}
}

func TestStepはそれ以外の失敗を分類なしで返す(t *testing.T) {
	r := newRig(t, true, func(f *codextest.Fake) {
		f.OnTurnStart = func(f *codextest.Fake, th, turn string, _ protocol.TurnStartParams) {
			f.CompleteTurn(th, turn, "failed", "contextWindowExceeded")
		}
	})
	_, err := step(t, r, newHistory())
	if err == nil || errors.Is(err, openai.ErrUsageLimit) || errors.Is(err, openai.ErrPlanUnauthorized) || errors.Is(err, openai.ErrEngineUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestStepは未ログインをErrPlanUnauthorizedにしスレッドを作らない(t *testing.T) {
	r := newRig(t, false, nil)
	_, err := step(t, r, newHistory())
	if !errors.Is(err, openai.ErrPlanUnauthorized) {
		t.Fatalf("err = %v", err)
	}
	if n := len(r.fake(0).Calls(protocol.MethodThreadStart)); n != 0 {
		t.Errorf("thread/start was called %d times", n)
	}
}

func TestStepはエンジンが起動できないときErrEngineUnavailableを返す(t *testing.T) {
	srv := appserver.New(appserver.Options{
		CodexHome: t.TempDir(),
		Start:     func() (appserver.Process, error) { return nil, errors.New("no engine") },
	})
	t.Cleanup(srv.Close)
	m := codex.NewModel(codex.ModelConfig{Server: srv, WorkDir: t.TempDir()})
	_, err := m.Step(ctxT(t), openai.StepInput{Model: "m", History: newHistory()})
	if !errors.Is(err, openai.ErrEngineUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestStepは道具を使う項目が始まったターンを中断してエラーにする(t *testing.T) {
	for _, itemType := range []string{"commandExecution", "fileChange", "mcpToolCall", "webSearch"} {
		t.Run(itemType, func(t *testing.T) {
			r := newRig(t, true, func(f *codextest.Fake) {
				f.OnTurnStart = func(f *codextest.Fake, th, turn string, _ protocol.TurnStartParams) {
					f.ItemStarted(th, turn, itemType)
				}
			})
			h := newHistory()
			_, err := step(t, r, h)
			if err == nil || !strings.Contains(err.Error(), itemType) {
				t.Fatalf("err = %v", err)
			}
			for _, class := range []error{openai.ErrUsageLimit, openai.ErrPlanUnauthorized, openai.ErrEngineUnavailable} {
				if errors.Is(err, class) {
					t.Errorf("the failure must be unclassified, got %v", class)
				}
			}
			if len(r.fake(0).Calls(protocol.MethodTurnInterrupt)) != 1 {
				t.Error("turn/interrupt was not sent")
			}
			r.model.CloseTask(h)
		})
	}
}

func TestStepはctxの取り消しでturnInterruptする(t *testing.T) {
	synced := make(chan struct{})
	r := newRig(t, true, func(f *codextest.Fake) {
		// ターンは何も出さない。turn/start の応答をクライアントが読み終えたことだけ、順序のある往復で確かめる
		// （応答のあとに送った要求にクライアントが答えた時点で、応答は読まれている）。
		f.OnTurnStart = func(f *codextest.Fake, _, _ string, _ protocol.TurnStartParams) {
			_, _, _ = f.Ask(ctxT(t), "ping/sync", map[string]any{})
			close(synced)
		}
	})
	ctx, cancel := context.WithCancel(ctxT(t))
	defer cancel()
	errCh := make(chan error, 1)
	h := newHistory()
	go func() {
		_, err := r.model.Step(ctx, openai.StepInput{Model: "m", History: h})
		errCh <- err
	}()
	select {
	case <-synced:
	case <-ctxT(t).Done():
		t.Fatal("turn/start was not sent")
	}
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	calls := r.fake(0).WaitFor(ctxT(t), protocol.MethodTurnInterrupt, 1)
	if len(calls) != 1 {
		t.Fatal("turn/interrupt was not sent")
	}
	var p protocol.TurnInterruptParams
	_ = json.Unmarshal(calls[0].Params, &p)
	if p.ThreadID == "" || p.TurnID == "" {
		t.Errorf("interrupt params = %+v", p)
	}
	r.model.CloseTask(h)
}

func TestStepは不正な引数の呼び出しをErrUnsupportedActionにして呼び出しを失敗で応答する(t *testing.T) {
	respCh := make(chan protocol.DynamicToolResponse, 1)
	r := newRig(t, true, func(f *codextest.Fake) {
		f.OnTurnStart = func(f *codextest.Fake, th, turn string, _ protocol.TurnStartParams) {
			resp, _, _ := f.ToolCall(ctxT(t), th, turn, "call-x", "left_click", `{"x":1}`) // y が無い
			respCh <- resp
		}
	})
	h := newHistory()
	_, err := step(t, r, h)
	if !errors.Is(err, domain.ErrUnsupportedAction) {
		t.Fatalf("err = %v", err)
	}
	if resp := <-respCh; resp.Success || len(resp.ContentItems) != 0 {
		t.Fatalf("response = %+v", resp)
	}
	r.model.CloseTask(h)
}

func TestCloseTaskは保留した呼び出しを失敗で応答しターンを中断する(t *testing.T) {
	respCh := make(chan protocol.DynamicToolResponse, 1)
	r := newRig(t, true, func(f *codextest.Fake) {
		f.OnTurnStart = func(f *codextest.Fake, th, turn string, _ protocol.TurnStartParams) {
			resp, _, _ := f.ToolCall(ctxT(t), th, turn, "call-1", "wait", `{"ms":1}`)
			respCh <- resp
		}
	})
	h := newHistory()
	out, err := step(t, r, h)
	if err != nil || len(out.Calls) != 1 {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	r.model.CloseTask(h) // 呼び出しの結果を応答しないまま終わる（中断・失敗）
	if resp := <-respCh; resp.Success {
		t.Fatalf("response = %+v", resp)
	}
	if len(r.fake(0).Calls(protocol.MethodTurnInterrupt)) != 1 {
		t.Error("turn/interrupt was not sent")
	}
}

func TestStepはプロセスの異常終了をエラーにして次のタスクで起動し直す(t *testing.T) {
	var r *rig
	r = newRig(t, true, func(f *codextest.Fake) {
		if r.procCount() == 0 {
			f.OnTurnStart = func(*codextest.Fake, string, string, protocol.TurnStartParams) {
				go r.procs[0].Crash() // 1 回目のプロセスはターンの途中で落ちる
			}
			return
		}
		f.OnTurnStart = func(f *codextest.Fake, th, turn string, _ protocol.TurnStartParams) {
			f.AgentMessage(th, turn, "ok")
			f.CompleteTurn(th, turn, "completed", "")
		}
	})
	h1 := newHistory()
	_, err := step(t, r, h1)
	if err == nil || errors.Is(err, openai.ErrEngineUnavailable) || errors.Is(err, openai.ErrUsageLimit) {
		t.Fatalf("err = %v", err)
	}
	r.model.CloseTask(h1)
	h2 := newHistory()
	out, err := step(t, r, h2)
	if err != nil || out.FinalText != "ok" {
		t.Fatalf("second task: %+v %v", out, err)
	}
	if r.procCount() != 2 {
		t.Fatalf("procs = %d, want 2", r.procCount())
	}
	r.model.CloseTask(h2)
}

func TestサーバーのCloseでプロセスが終わる(t *testing.T) {
	r := newRig(t, true, func(f *codextest.Fake) {
		f.OnTurnStart = func(f *codextest.Fake, th, turn string, _ protocol.TurnStartParams) {
			f.AgentMessage(th, turn, "ok")
			f.CompleteTurn(th, turn, "completed", "")
		}
	})
	h := newHistory()
	if _, err := step(t, r, h); err != nil {
		t.Fatal(err)
	}
	r.model.CloseTask(h)
	r.srv.Close()
	select {
	case <-r.procs[0].Done():
	default:
		t.Fatal("the process did not end")
	}
}

func TestStepは履歴に出力の無い呼び出しをErrCallPairingにする(t *testing.T) {
	respCh := make(chan protocol.DynamicToolResponse, 1)
	r := newRig(t, true, func(f *codextest.Fake) {
		f.OnTurnStart = func(f *codextest.Fake, th, turn string, _ protocol.TurnStartParams) {
			resp, _, _ := f.ToolCall(ctxT(t), th, turn, "call-1", "wait", `{"ms":1}`)
			respCh <- resp
		}
	})
	h := newHistory()
	out, err := step(t, r, h)
	if err != nil {
		t.Fatal(err)
	}
	h.AddOutput(out.Items) // function_call_output を足さずに次の Step
	if _, err := step(t, r, h); !errors.Is(err, openai.ErrCallPairing) {
		t.Fatalf("err = %v", err)
	}
	if resp := <-respCh; resp.Success {
		t.Fatalf("response = %+v", resp)
	}
	r.model.CloseTask(h)
}
