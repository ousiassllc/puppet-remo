// Package codex は ChatGPT のプランを Codex の app-server 経由で使う接続方式（chatgpt-codex。#247）の Model。
//
// Model は openai.Model・openai.TaskCloser を満たす。画面の操作は desktop の関数ツール（dynamicTools の namespace）で、
// app-server の item/tool/call を openai.StepOutput.Calls として呼び出し側のループへ返し、実行結果（function_call_output と
// 新しい画面）を次の Step で応答に載せる。Codex 自身の道具（シェル・ファイル変更・MCP・Web 検索など）は
// 使わせない: 起動引数の -c で切り、読み取り専用の sandbox・承認なしで、サーバーの要求は拒否し、
// それでも項目が始まったターンは中断してエラーにする。
package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/codex/appserver"
	"github.com/ousiassllc/puppet-remo/internal/codex/protocol"
	"github.com/ousiassllc/puppet-remo/internal/codex/rpc"
	"github.com/ousiassllc/puppet-remo/internal/openai"
)

// cleanupTimeout はターンの中断・スレッドの片付けなど、ベストエフォートの呼び出しの上限。
const cleanupTimeout = 5 * time.Second

// defaultInstructions は StepInput.Instructions が空のときの指示（無いと Codex 既定のコーディング用の指示になる）。
const defaultInstructions = "You operate the owner's Windows desktop through the desktop functions. Do only what the user asked."

// ModelConfig は Model の設定。
type ModelConfig struct {
	// Server は app-server のプロセス（必要になったとき起動する）。
	Server *appserver.Server
	// WorkDir は thread/start の作業ディレクトリ（アプリ専用の空のディレクトリ）。画像の一時ファイルもこの下に置く。
	WorkDir string
	Logger  *slog.Logger
}

// Model は Codex の app-server を使う openai.Model。
type Model struct {
	cfg ModelConfig

	mu    sync.Mutex
	tasks map[*openai.History]*task
}

// NewModel は Model を作る。
func NewModel(cfg ModelConfig) *Model {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Model{cfg: cfg, tasks: map[*openai.History]*task{}}
}

var (
	_ openai.Model      = (*Model)(nil)
	_ openai.TaskCloser = (*Model)(nil)
)

// pendingCall は応答を待っている item/tool/call。
type pendingCall struct {
	callID  string
	respond func(result any, rerr *rpc.Error)
}

// task は 1 つのタスク（History）に結び付いた app-server のスレッド。
type task struct {
	se       *appserver.Session
	sub      *appserver.Sub
	threadID string
	turnID   string
	imgDir   string

	pending []pendingCall // 次の Step で応答する（直前の Step が返した呼び出し）

	// ターンの状態。
	deltas    map[string]*strings.Builder
	order     []string
	final     string
	hasText   bool
	lastErr   *protocol.TurnError
	batch     []batchCall
	completed *protocol.TurnResult
}

type batchCall struct {
	params  protocol.DynamicToolCallParams
	respond func(result any, rerr *rpc.Error)
}

func (m *Model) lookup(h *openai.History) *task {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tasks[h]
}

// Step は openai.Model の実装。最初の Step でスレッドとターンを始め、以後の Step で直前の呼び出しの結果を応答する。
// 次のイベント（関数ツールの呼び出し、またはターンの終了）が来たら返る。
func (m *Model) Step(ctx context.Context, in openai.StepInput) (openai.StepOutput, error) {
	if in.History == nil {
		return openai.StepOutput{}, errors.New("codex: no history")
	}
	t := m.lookup(in.History)
	if t == nil {
		var err error
		if t, err = m.begin(ctx, in); err != nil {
			return openai.StepOutput{}, err
		}
		m.mu.Lock()
		m.tasks[in.History] = t
		m.mu.Unlock()
	} else if err := t.answer(in.History); err != nil {
		return openai.StepOutput{}, err
	}
	return m.await(ctx, t)
}

// begin は app-server を（必要なら）起動し、ログインを確かめ、スレッドを作り、最初のターンを始める。
func (m *Model) begin(ctx context.Context, in openai.StepInput) (*task, error) {
	se, err := m.cfg.Server.Ensure(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %s", openai.ErrEngineUnavailable, sanitize(err.Error()))
	}
	var acc protocol.GetAccountResult
	if err := se.Call(ctx, protocol.MethodAccountRead, protocol.GetAccountParams{RefreshToken: false}, &acc); err != nil {
		return nil, callError(ctx, se, "account/read", err)
	}
	if acc.Account == nil {
		return nil, fmt.Errorf("%w: not signed in to codex", openai.ErrPlanUnauthorized)
	}

	instructions := in.Instructions
	if instructions == "" {
		instructions = defaultInstructions
	}
	var th protocol.ThreadStartResult
	if err := se.Call(ctx, protocol.MethodThreadStart, protocol.ThreadStartParams{
		Model: in.Model, Ephemeral: true, BaseInstructions: &instructions, Cwd: m.cfg.WorkDir,
		Sandbox: protocol.SandboxReadOnly, ApprovalPolicy: protocol.ApprovalNever, DynamicTools: []protocol.DynamicToolSpec{DesktopToolSpec()},
	}, &th); err != nil {
		return nil, callError(ctx, se, "thread/start", err)
	}
	t := &task{se: se, threadID: th.Thread.ID, deltas: map[string]*strings.Builder{}}
	t.sub = se.Subscribe(t.threadID)

	text, shot := in.History.InitialRequest()
	input := []any{protocol.TextInput{Type: protocol.InputText, Text: text}}
	if len(shot) > 0 {
		path, err := m.writeImage(t, shot)
		if err != nil {
			m.release(t)
			return nil, fmt.Errorf("codex: write the screenshot: %w", err)
		}
		input = append(input, protocol.LocalImageInput{Type: protocol.InputLocalImage, Path: path})
	}
	var turn protocol.TurnStartResult
	if err := se.Call(ctx, protocol.MethodTurnStart, protocol.TurnStartParams{
		ThreadID: t.threadID, Input: input, ApprovalPolicy: protocol.ApprovalNever,
		SandboxPolicy: &protocol.SandboxPolicy{Type: protocol.PolicyReadOnly},
	}, &turn); err != nil {
		m.release(t)
		return nil, callError(ctx, se, "turn/start", err)
	}
	t.turnID = turn.Turn.ID
	return t, nil
}

// writeImage は最初の画面を、作業ディレクトリの下の専用の一時ディレクトリへ書く（CloseTask で消す）。
func (m *Model) writeImage(t *task, png []byte) (string, error) {
	if err := os.MkdirAll(m.cfg.WorkDir, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(m.cfg.WorkDir, "img-")
	if err != nil {
		return "", err
	}
	t.imgDir = dir
	p := filepath.Join(dir, "screen.png")
	if err := os.WriteFile(p, png, 0o600); err != nil {
		return "", err
	}
	return p, nil
}

// answer は直前の Step が返した呼び出しに、実行結果（function_call_output と、新しい画面）を応答する。
func (t *task) answer(h *openai.History) error {
	shot, note := h.LatestUserImage()
	for i, p := range t.pending {
		out, ok := h.FunctionOutput(p.callID)
		if !ok {
			// 出力の無い呼び出しは履歴の組み立ての誤り。残りの呼び出しは失敗で応答し、保留を残さない。
			t.failPending(t.pending[i:])
			t.pending = nil
			return fmt.Errorf("codex: %w: call_id %q has no output", openai.ErrCallPairing, p.callID)
		}
		items := []protocol.ContentItem{{Type: protocol.ContentInputText, Text: out}}
		if note != "" {
			items = append(items, protocol.ContentItem{Type: protocol.ContentInputText, Text: note})
		}
		if len(shot) > 0 {
			items = append(items, protocol.ContentItem{Type: protocol.ContentInputImage, ImageURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(shot)})
		}
		p.respond(protocol.DynamicToolResponse{ContentItems: items, Success: outputOK(out)}, nil)
	}
	t.pending = nil
	return nil
}

// outputOK は function_call_output の JSON の ok。読めなければ false。
func outputOK(out string) bool {
	var v struct {
		OK bool `json:"ok"`
	}
	return json.Unmarshal([]byte(out), &v) == nil && v.OK
}

func (t *task) failPending(ps []pendingCall) {
	for _, p := range ps {
		p.respond(protocol.DynamicToolResponse{ContentItems: []protocol.ContentItem{}}, nil)
	}
}

// await はイベントを、関数ツールの呼び出しかターンの終了が来るまで追う。
func (m *Model) await(ctx context.Context, t *task) (openai.StepOutput, error) {
	for {
		ev, ok := t.sub.Next(ctx, t.se.Done())
		if !ok {
			if ctx.Err() != nil {
				m.interrupt(t)
				return openai.StepOutput{}, ctx.Err()
			}
			return openai.StepOutput{}, fmt.Errorf("%w: the app-server ended during the turn: %s", openai.ErrEngineUnavailable, sanitize(endMessage(t.se)))
		}
		if err := m.handle(t, ev); err != nil {
			return openai.StepOutput{}, err
		}
		if t.completed == nil && len(t.batch) == 0 {
			continue
		}
		// 同時に届いている分は 1 回の Step にまとめる（待たない）。
		for t.completed == nil {
			more, ok := t.sub.TryNext()
			if !ok {
				break
			}
			if err := m.handle(t, more); err != nil {
				return openai.StepOutput{}, err
			}
		}
		if t.completed != nil {
			t.failPendingBatch()
			return m.finish(t)
		}
		return m.calls(t)
	}
}

func (t *task) failPendingBatch() {
	for _, b := range t.batch {
		b.respond(protocol.DynamicToolResponse{ContentItems: []protocol.ContentItem{}}, nil)
	}
	t.batch = nil
}

// handle は 1 つのイベントを処理する。道具を使った項目はターンを中断してエラーにする。
func (m *Model) handle(t *task, ev appserver.Event) error {
	switch ev.Method {
	case protocol.ReqDynamicToolCall:
		var p protocol.DynamicToolCallParams
		if json.Unmarshal(ev.Params, &p) != nil || ev.Respond == nil {
			if ev.Respond != nil {
				ev.Respond(protocol.DynamicToolResponse{ContentItems: []protocol.ContentItem{}}, nil)
			}
			return nil
		}
		t.batch = append(t.batch, batchCall{params: p, respond: ev.Respond})
	case protocol.NotifAgentDelta:
		var d protocol.AgentDelta
		if json.Unmarshal(ev.Params, &d) == nil {
			b := t.deltas[d.ItemID]
			if b == nil {
				b = &strings.Builder{}
				t.deltas[d.ItemID] = b
				t.order = append(t.order, d.ItemID)
			}
			b.WriteString(d.Delta)
		}
	case protocol.NotifItemStarted, protocol.NotifItemCompleted:
		var n protocol.ItemNotification
		if json.Unmarshal(ev.Params, &n) != nil {
			return nil
		}
		if !protocol.IsPlainItem(n.Item.Type) {
			m.interrupt(t)
			return fmt.Errorf("codex: the turn used a tool (%s) and was stopped; only the desktop functions are allowed", sanitize(n.Item.Type))
		}
		if ev.Method == protocol.NotifItemCompleted && n.Item.Type == protocol.ItemAgentMessage {
			if n.Item.Phase != nil && *n.Item.Phase == protocol.PhaseFinalAnswer {
				t.final, t.hasText = n.Item.Text, true
			} else if !t.hasText && n.Item.Text != "" {
				t.final = n.Item.Text // phase の無いメッセージ: final_answer が続かなければこれが答え
			}
		}
	case protocol.NotifError:
		var n protocol.ErrorNotification
		if json.Unmarshal(ev.Params, &n) == nil && !n.WillRetry && (n.TurnID == "" || n.TurnID == t.turnID) {
			e := n.Error
			t.lastErr = &e
		}
	case protocol.NotifTurnCompleted:
		var n protocol.TurnCompletedNotification
		if json.Unmarshal(ev.Params, &n) != nil || (n.Turn.ID != "" && n.Turn.ID != t.turnID) {
			return nil
		}
		turn := n.Turn
		t.completed = &turn
	}
	return nil
}

// calls は集まった呼び出しを StepOutput.Calls にする。引数の不正は domain.ErrUnsupportedAction（ParseDesktopCall）。
func (m *Model) calls(t *task) (openai.StepOutput, error) {
	batch := t.batch
	t.batch = nil
	out := openai.StepOutput{ResponseID: t.turnID}
	for i, b := range batch {
		op, err := openai.ParseDesktopCall(b.params.Tool, b.params.Arguments)
		if err != nil {
			for _, rest := range batch[i:] {
				rest.respond(protocol.DynamicToolResponse{ContentItems: []protocol.ContentItem{}}, nil)
			}
			return openai.StepOutput{}, err
		}
		raw, err := json.Marshal(map[string]string{
			"type": "function_call", "call_id": b.params.CallID, "name": b.params.Tool,
			"namespace": openai.DesktopNamespace, "arguments": string(b.params.Arguments),
		})
		if err != nil {
			return openai.StepOutput{}, err
		}
		out.Items = append(out.Items, openai.OutputItem{Raw: raw})
		out.Calls = append(out.Calls, openai.ToolCall{CallID: b.params.CallID, Name: b.params.Tool, Op: op})
		t.pending = append(t.pending, pendingCall{callID: b.params.CallID, respond: b.respond})
	}
	return out, nil
}

// finish は終了したターンの結果: 最終回答か、分類した失敗。
func (m *Model) finish(t *task) (openai.StepOutput, error) {
	turn := t.completed
	switch turn.Status {
	case protocol.TurnCompleted:
		text := strings.TrimSpace(t.final)
		if text == "" && len(t.order) > 0 {
			text = strings.TrimSpace(t.deltas[t.order[len(t.order)-1]].String())
		}
		if text == "" {
			return openai.StepOutput{}, errors.New("codex: the answer was empty")
		}
		return openai.StepOutput{ResponseID: t.turnID, FinalText: text}, nil
	case protocol.TurnFailed:
		e := turn.Error
		if e == nil {
			e = t.lastErr
		}
		return openai.StepOutput{}, turnError(e)
	default:
		return openai.StepOutput{}, fmt.Errorf("codex: the turn ended as %s", sanitize(turn.Status))
	}
}

// interrupt はターンの中断を頼む（ベストエフォート。呼び出し側の ctx は終わっているかもしれないので自分の ctx で）。
func (m *Model) interrupt(t *task) {
	if t.turnID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_ = t.se.Call(ctx, protocol.MethodTurnInterrupt, protocol.TurnInterruptParams{ThreadID: t.threadID, TurnID: t.turnID}, nil)
}

// release はスレッドの購読と一時ファイルを片付ける。
func (m *Model) release(t *task) {
	t.se.Unsubscribe(t.threadID)
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_ = t.se.Call(ctx, protocol.MethodThreadUnsubscribe, protocol.ThreadRef{ThreadID: t.threadID}, nil)
	if t.imgDir != "" {
		_ = os.RemoveAll(t.imgDir)
	}
}

// CloseTask は openai.TaskCloser の実装: タスクの終了（成功・失敗・中断のどれでも）で、保留した呼び出しを失敗で応答し、
// ターンが終わっていなければ中断し、スレッドの購読と画像の一時ファイルを片付ける。
func (m *Model) CloseTask(h *openai.History) {
	m.mu.Lock()
	t := m.tasks[h]
	delete(m.tasks, h)
	m.mu.Unlock()
	if t == nil {
		return
	}
	t.failPending(t.pending)
	t.pending = nil
	t.failPendingBatch()
	if t.completed == nil {
		m.interrupt(t)
	}
	m.release(t)
}
