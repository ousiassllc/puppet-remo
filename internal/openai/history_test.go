package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func marshalInput(t *testing.T, h *History) []byte {
	t.Helper()
	in, err := h.input()
	if err != nil {
		t.Fatalf("input: %v", err)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// callItem は computer_call の出力アイテム（原文）。
func callItem(callID string) OutputItem {
	return OutputItem{Raw: json.RawMessage(fmt.Sprintf(`{"type":"computer_call","call_id":%q,"status":"completed","actions":[]}`, callID))}
}

func TestHistoryScreenshotsNeverExceedKeep(t *testing.T) {
	for _, keep := range []int{1, 2, 3, 5} {
		h := NewHistory("t", "", pngBytes("first"), keep)
		if h.Screenshots() != 1 {
			t.Fatalf("keep=%d: initial Screenshots = %d, want 1", keep, h.Screenshots())
		}
		for i := 1; i <= 10; i++ {
			h.AddComputerOutput(fmt.Sprintf("c%d", i), pngBytes(fmt.Sprint(i)), nil)
			want := min(i+1, keep)
			if got := h.Screenshots(); got != want {
				t.Fatalf("keep=%d after %d outputs: Screenshots = %d, want %d", keep, i, got, want)
			}
		}
	}
}

func TestHistoryKeepBelowOneIsOne(t *testing.T) {
	h := NewHistory("t", "", pngBytes("first"), 0)
	h.AddComputerOutput("c1", pngBytes("1"), nil)
	if h.Screenshots() != 1 {
		t.Errorf("Screenshots = %d, want 1", h.Screenshots())
	}
}

func TestHistoryOldestScreenshotsAreDropped(t *testing.T) {
	h := NewHistory("t", "", pngBytes("first"), 2)
	h.AddComputerOutput("c1", pngBytes("1"), nil)
	h.AddComputerOutput("c2", pngBytes("2"), nil)
	// 最初のメッセージと c1 が置き換えられ、c2 と c3 が残る。
	h.AddComputerOutput("c3", pngBytes("3"), nil)

	if h.items[0].shot != nil || h.items[1].shot != nil {
		t.Error("the oldest screenshots must be replaced")
	}
	if h.items[2].shot == nil || h.items[3].shot == nil {
		t.Error("the newest screenshots must be kept")
	}
	// 置き換えたあとの要求: 最初のメッセージはテキストだけ（指示 + プレースホルダ）、c1 はプレースホルダ。
	raw := marshalInput(t, h)
	if bytes.Count(raw, []byte(OmittedScreenshot)) != 2 {
		t.Errorf("placeholders = %d in %s", bytes.Count(raw, []byte(OmittedScreenshot)), raw)
	}
	if bytes.Count(raw, []byte("input_image")) != 0 {
		t.Errorf("no input_image expected: %s", raw)
	}
}

func TestHistoryAddOutputKeepsRawBytes(t *testing.T) {
	// 空白・キー順・未知のフィールド・エスケープをそのまま保つ。
	raws := []json.RawMessage{
		json.RawMessage(`{ "type" : "reasoning", "id":"rs_1","encrypted_content":"\u00e9\/x","z":1,"a":[1, 2]}`),
		json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"hi"}]}`),
	}
	want := [][]byte{append([]byte(nil), raws[0]...), append([]byte(nil), raws[1]...)}

	h := NewHistory("t", "", nil, 3)
	h.AddOutput([]OutputItem{{Raw: raws[0]}, {Raw: raws[1]}})

	if h.Screenshots() != 0 {
		t.Errorf("Screenshots = %d, want 0 without a first shot", h.Screenshots())
	}
	var items []json.RawMessage
	raw := marshalInput(t, h)
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	for i, w := range want {
		var compact bytes.Buffer
		if err := json.Compact(&compact, w); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, compact.Bytes()) {
			t.Errorf("raw item %d changed in the request: %s", i, raw)
		}
		if !bytes.Equal(raws[i], w) {
			t.Errorf("AddOutput mutated its input %d", i)
		}
	}
}

func TestHistoryNoFirstShotUsesPlaceholder(t *testing.T) {
	raw := marshalInput(t, NewHistory("do it", "", nil, 3))
	if !bytes.Contains(raw, []byte(OmittedScreenshot)) || bytes.Contains(raw, []byte("input_image")) {
		t.Errorf("input = %s", raw)
	}
}

// 出力の欠けた computer_call・function_call と、重複した出力は、組み立て（input）の時点で ErrCallPairing（#158）。
func TestHistoryInputRejectsMissingAndDuplicateOutputs(t *testing.T) {
	funcCall := OutputItem{Raw: json.RawMessage(`{"type":"function_call","call_id":"f1","name":"click","arguments":"{}"}`)}
	cases := []struct {
		name  string
		build func() *History
		ok    bool
	}{
		{"paired", func() *History {
			h := NewHistory("t", "", nil, 3)
			h.AddOutput([]OutputItem{callItem("c1")})
			h.AddComputerOutput("c1", pngBytes("1"), nil)
			return h
		}, true},
		{"computer_call without output", func() *History {
			h := NewHistory("t", "", nil, 3)
			h.AddOutput([]OutputItem{callItem("c1")})
			return h
		}, false},
		{"duplicate output", func() *History {
			h := NewHistory("t", "", nil, 3)
			h.AddOutput([]OutputItem{callItem("c1")})
			h.AddComputerOutput("c1", pngBytes("1"), nil)
			h.AddComputerOutput("c1", pngBytes("2"), nil)
			return h
		}, false},
		{"output for another call_id", func() *History {
			h := NewHistory("t", "", nil, 3)
			h.AddOutput([]OutputItem{callItem("c1")})
			h.AddComputerOutput("c2", pngBytes("1"), nil)
			return h
		}, false},
		{"function_call without output (the ChatGPT path is not implemented)", func() *History {
			h := NewHistory("t", "", nil, 3)
			h.AddOutput([]OutputItem{funcCall})
			return h
		}, false},
		{"second call still pending", func() *History {
			h := NewHistory("t", "", nil, 3)
			h.AddOutput([]OutputItem{callItem("c1")})
			h.AddComputerOutput("c1", pngBytes("1"), nil)
			h.AddOutput([]OutputItem{callItem("c2")})
			return h
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.build().input()
			if tc.ok && err != nil {
				t.Fatalf("input: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrCallPairing) {
				t.Fatalf("input err = %v, want ErrCallPairing", err)
			}
		})
	}
}

// AddUserNote は computer_call_output の直後に role: user の input_text だけのメッセージ（画像なし）を足し、
// 保持枚数（画像）の数え方に影響しない。
func TestHistoryUserNoteFollowsOutputAsTextOnlyMessage(t *testing.T) {
	h := NewHistory("t", "", pngBytes("first"), 1)
	h.AddOutput([]OutputItem{callItem("c1")})
	h.AddComputerOutput("c1", pngBytes("1"), nil)
	h.AddUserNote("the group changed")
	if h.Screenshots() != 1 {
		t.Errorf("Screenshots = %d, want 1 (a note has no image)", h.Screenshots())
	}
	var items []map[string]any
	if err := json.Unmarshal(marshalInput(t, h), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4 (user, computer_call, output, note)", len(items))
	}
	checkCallPairing(t, items)
	note := items[3]
	if note["role"] != "user" || note["type"] != "message" {
		t.Fatalf("note = %v", note)
	}
	content := note["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["type"] != "input_text" || content[0].(map[string]any)["text"] != "the group changed" {
		t.Errorf("note content = %v", content)
	}
}

// checkCallPairing の部品の検査: 欠落・重複を見つけ、function_call_output も数える（ChatGPT の経路の実装が使う）。
func TestCheckCallPairingHelper(t *testing.T) {
	good := []map[string]any{
		{"type": "computer_call", "call_id": "a"}, {"type": "computer_call_output", "call_id": "a"},
		{"type": "function_call", "call_id": "b"}, {"type": "function_call_output", "call_id": "b", "output": `{"ok":false,"error":"not_executed","reason":"group_changed"}`},
	}
	if err := pairingProblem(good); err != nil {
		t.Errorf("good = %v", err)
	}
	if err := pairingProblem(good[:3]); err == nil {
		t.Error("function_call without output was accepted")
	}
	if err := pairingProblem(append(good, map[string]any{"type": "computer_call_output", "call_id": "a"})); err == nil {
		t.Error("duplicate output was accepted")
	}
	notOK := []map[string]any{
		{"type": "function_call", "call_id": "b"}, {"type": "function_call_output", "call_id": "b", "output": `{"ok":true}`},
	}
	if err := pairingProblem(notOK); err != nil {
		t.Errorf("a plain {ok:true} output is valid for pairing: %v", err)
	}
}
