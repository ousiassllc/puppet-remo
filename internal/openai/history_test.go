package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

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
	raw, err := json.Marshal(h.input())
	if err != nil {
		t.Fatal(err)
	}
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
	raw, err := json.Marshal(h.input())
	if err != nil {
		t.Fatal(err)
	}
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
	raw, err := json.Marshal(NewHistory("do it", "", nil, 3).input())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(OmittedScreenshot)) || bytes.Contains(raw, []byte("input_image")) {
		t.Errorf("input = %s", raw)
	}
}
