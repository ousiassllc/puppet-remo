package openai

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/openai/openai-go/v3/responses"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 中断（対象グループの変化。#158）の結果の形式を、モックの /v1/responses（httptest のループバック。実 API を呼ばない）で検査する。
// service のループが History へ足す順（AddOutput → AddComputerOutput → AddUserNote）をそのまま再現する。

func yieldHistory(callID, note string) *History {
	h := NewHistory("t", "", pngBytes("s0"), 3)
	h.AddOutput([]OutputItem{callItem(callID)})
	h.AddComputerOutput(callID, pngBytes("after-"+callID), nil)
	h.AddUserNote(note)
	return h
}

func TestStepSendsYieldNoteRightAfterTheComputerCallOutput(t *testing.T) {
	for _, note := range []string{"実行済み 0 件、残りは実行していない", "実行済み 2 件、残りは実行していない"} {
		fs := newFakeServer(t, respond(map[string]any{"type": "message", "id": "m", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": "ok", "annotations": []any{}}}}))
		h := yieldHistory("call_1", note)
		if _, err := newModel(fs, Config{}).Step(context.Background(), StepInput{Model: "m", History: h}); err != nil {
			t.Fatalf("Step: %v", err)
		}
		items := inputItems(t, decodeBody(t, fs.requests()[0].body))
		if len(items) != 4 {
			t.Fatalf("items = %d, want 4: %v", len(items), items)
		}
		checkCallPairing(t, items)
		order := []string{"message", "computer_call", "computer_call_output", "message"}
		for i, want := range order {
			if items[i]["type"] != want {
				t.Errorf("item %d type = %v, want %s", i, items[i]["type"], want)
			}
		}
		if items[2]["call_id"] != "call_1" || items[3]["role"] != "user" {
			t.Errorf("output = %v, note = %v", items[2], items[3])
		}
		content := items[3]["content"].([]any)
		part := content[0].(map[string]any)
		if len(content) != 1 || part["type"] != "input_text" || part["text"] != note {
			t.Errorf("note content = %v", content)
		}
		// 未実行を示す欄・成功を表す出力にしていない（computer_call_output は画像だけ）。
		out := items[2]["output"].(map[string]any)
		if out["type"] != "computer_screenshot" || items[2]["status"] != nil {
			t.Errorf("computer_call_output = %v", items[2])
		}
	}
}

// 出力の欠けた・重複した履歴は、API へ送る前に Step が ErrCallPairing で拒否する（サーバーに要求が届かない）。
func TestStepRejectsUnpairedHistoryBeforeSending(t *testing.T) {
	fs := newFakeServer(t, respond())
	m := newModel(fs, Config{})
	missing := NewHistory("t", "", nil, 3)
	missing.AddOutput([]OutputItem{callItem("call_1")})
	dup := yieldHistory("call_1", "n")
	dup.AddComputerOutput("call_1", pngBytes("again"), nil)
	for name, h := range map[string]*History{"missing": missing, "duplicate": dup} {
		if _, err := m.Step(context.Background(), StepInput{Model: "m", History: h}); !errors.Is(err, ErrCallPairing) {
			t.Errorf("%s: Step err = %v, want ErrCallPairing", name, err)
		}
	}
	if n := len(fs.requests()); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
}

// 中断のあとの次の応答が 5xx で失敗しても、Step は再試行しない（要求は 1 回）。
func TestStepAfterYieldIsNotRetriedOnServerError(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"message": "boom", "type": "server_error"}})
	})
	if _, err := newModel(fs, Config{}).Step(context.Background(), StepInput{Model: "m", History: yieldHistory("call_1", "n")}); err == nil {
		t.Fatal("Step succeeded")
	}
	if n := len(fs.requests()); n != 1 {
		t.Errorf("requests = %d, want 1 (no automatic retry)", n)
	}
}

// エージェントの computer ツールにクリップボード操作は無い: どのアクションもクリップボードの Op に写らない。
func TestMapActionsNeverProducesClipboardOps(t *testing.T) {
	var acts []responses.ComputerActionUnion
	for _, typ := range []string{"click", "double_click", "drag", "move", "scroll", "keypress", "type", "wait", "screenshot"} {
		a := responses.ComputerActionUnion{Type: typ, Button: "left", Text: "x"}
		if typ == "keypress" {
			a.Keys = []string{"CTRL", "C"}
		}
		if typ == "drag" {
			a.Path = []responses.ComputerActionDragPath{{X: 1, Y: 1}, {X: 5, Y: 5}}
		}
		acts = append(acts, a)
	}
	ops, err := mapActions(acts)
	if err != nil {
		t.Fatalf("mapActions: %v", err)
	}
	for _, op := range ops {
		switch op.(type) {
		case domain.ClipboardRead, domain.ClipboardWrite:
			t.Errorf("clipboard op %T produced", op)
		}
	}
}
