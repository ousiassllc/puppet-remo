package typesafe

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func fixtureRequest() ChoiceRequest {
	return ChoiceRequest{
		Model:  "jev-1.13",
		Goal:   "メモ帳を開いて保存ダイアログを閉じる",
		Window: ChoiceWindow{Title: "無題 - メモ帳", Process: "notepad.exe"},
		Elements: []ChoiceElement{
			{ID: "b1c2d3e4f5a60718", Role: "button", Label: "保存", Enabled: true},
			{ID: "0a1b2c3d4e5f6071", Role: "window", Label: "エクスプローラー", Enabled: true},
		},
		History: []string{`click button "ファイル"`, `click menu_item "名前を付けて保存"`},
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// decodeAny は JSON を any に読む（map のキー順に依存せず比較するため）。
func decodeAny(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, b)
	}
	return v
}

// 要求の組み立てが、公開 OpenAPI に沿った固定の形（ゴールデン）と一致する。実 API とのスモークで
// 形式を直すときは、wire.go とこのゴールデンだけを直す。
func TestMarshalRequestMatchesGolden(t *testing.T) {
	body, allowed, err := marshalRequest(fixtureRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := decodeAny(t, body), decodeAny(t, readFixture(t, "request.json")); !reflect.DeepEqual(got, want) {
		t.Fatalf("wire body differs from testdata/request.json:\n got: %s\nwant: %s", body, readFixture(t, "request.json"))
	}
	wantAllowed := []string{"b1c2d3e4f5a60718", "0a1b2c3d4e5f6071", ReobserveChoice, AbstainChoice}
	for _, id := range wantAllowed {
		if _, ok := allowed[id]; !ok {
			t.Errorf("allowed choices lack %q", id)
		}
	}
	if len(allowed) != len(wantAllowed) {
		t.Errorf("allowed choices = %d, want %d", len(allowed), len(wantAllowed))
	}
}

// NFR-PR04・NFR-S18: 送れるのは goal / window{title,process} / elements[{id,role,label,enabled}] / history だけ。
// すべての欄を埋めた要求の本文に、これ以外のキーが現れないことを確かめる。
func TestWireBodyContainsOnlyAllowedKeys(t *testing.T) {
	req := fixtureRequest()
	req.History = []string{"a", "b", "c"}
	body, _, err := marshalRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	root, ok := decodeAny(t, body).(map[string]any)
	if !ok {
		t.Fatalf("body is not an object: %s", body)
	}

	keys := func(m map[string]any) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	assertKeys := func(path string, m map[string]any, want ...string) {
		t.Helper()
		slices.Sort(want)
		if got := keys(m); !slices.Equal(got, want) {
			t.Errorf("%s keys = %v, want %v", path, got, want)
		}
	}

	assertKeys("$", root, "model", "state", "questions")
	state := root["state"].(map[string]any)
	assertKeys("$.state", state, "goal", "window", "elements", "history")
	assertKeys("$.state.window", state["window"].(map[string]any), "title", "process")
	for i, e := range state["elements"].([]any) {
		assertKeys(fmt.Sprintf("$.state.elements[%d]", i), e.(map[string]any), "id", "role", "label", "enabled")
	}
	questions := root["questions"].(map[string]any)
	assertKeys("$.questions", questions, "next", "goal_maybe_done")
	assertKeys("$.questions.next", questions["next"].(map[string]any), "type", "instructions", "criteria")
	assertKeys("$.questions.goal_maybe_done", questions["goal_maybe_done"].(map[string]any), "type", "instructions")
	criteria := questions["next"].(map[string]any)["criteria"].(map[string]any)
	wantCriteria := []string{ReobserveChoice, AbstainChoice}
	for _, e := range req.Elements {
		wantCriteria = append(wantCriteria, e.ID)
	}
	assertKeys("$.questions.next.criteria", criteria, wantCriteria...)
}

// 型が「送れないもの」を持たないことの番人。欄が増えたら、NFR-PR04 の送信内容と照らして意図的に直す。
func TestRequestTypesHaveNoRoomForSensitiveData(t *testing.T) {
	fields := func(v any) []string {
		rt := reflect.TypeOf(v)
		out := make([]string, 0, rt.NumField())
		for f := range rt.Fields() {
			out = append(out, f.Name)
		}
		return out
	}
	for _, c := range []struct {
		name string
		v    any
		want []string
	}{
		{"ChoiceRequest", ChoiceRequest{}, []string{"Model", "Goal", "Window", "Elements", "History"}},
		{"ChoiceWindow", ChoiceWindow{}, []string{"Title", "Process"}},
		{"ChoiceElement", ChoiceElement{}, []string{"ID", "Role", "Label", "Enabled"}},
	} {
		if got := fields(c.v); !slices.Equal(got, c.want) {
			t.Errorf("%s fields = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHistoryClippedToMostRecentFive(t *testing.T) {
	history := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("step %d", i+1)
		}
		return out
	}
	for _, c := range []struct {
		name string
		in   []string
		want []string
	}{
		{"nil は空配列", nil, []string{}},
		{"5 件はそのまま", history(5), history(5)},
		{"8 件は新しい 5 件", history(8), history(8)[3:]},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := fixtureRequest()
			req.History = c.in
			body, _, err := marshalRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				State struct {
					History []string `json:"history"`
				} `json:"state"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if got.State.History == nil || !slices.Equal(got.State.History, c.want) {
				t.Fatalf("history = %#v, want %#v", got.State.History, c.want)
			}
			if len(req.History) != len(c.in) {
				t.Fatal("caller's History was modified")
			}
		})
	}
}

// 選択肢の名前になる ID は、空・メタ候補と同名・重複を送らない（黙って上書きすると別の要素を選びうる）。
func TestMarshalRequestRejectsUnusableCandidateIDs(t *testing.T) {
	for name, ids := range map[string][]string{
		"空":         {"a", ""},
		"reobserve": {"a", ReobserveChoice},
		"abstain":   {AbstainChoice},
		"重複":        {"a", "b", "a"},
	} {
		t.Run(name, func(t *testing.T) {
			req := fixtureRequest()
			req.Elements = nil
			for _, id := range ids {
				req.Elements = append(req.Elements, ChoiceElement{ID: id, Role: "button", Label: "x", Enabled: true})
			}
			if _, _, err := marshalRequest(req); !errors.Is(err, ErrBadResponse) {
				t.Fatalf("err = %v, want ErrBadResponse", err)
			}
		})
	}
}

func TestUnmarshalResponseFixture(t *testing.T) {
	_, allowed, err := marshalRequest(fixtureRequest())
	if err != nil {
		t.Fatal(err)
	}
	got, err := unmarshalResponse(readFixture(t, "response.json"), allowed)
	if err != nil {
		t.Fatal(err)
	}
	if want := (ChoiceResult{Choice: "b1c2d3e4f5a60718", Confidence: 0.91, GoalMaybeDone: 0.12}); got != want {
		t.Fatalf("result = %+v, want %+v", got, want)
	}
}

func TestUnmarshalResponseAcceptsMetaChoices(t *testing.T) {
	_, allowed, _ := marshalRequest(fixtureRequest())
	for _, meta := range []string{ReobserveChoice, AbstainChoice} {
		body := answers(fmt.Sprintf(`"choice":%q,"confidence":0.5`, meta), `"noul":0`)
		got, err := unmarshalResponse([]byte(body), allowed)
		if err != nil {
			t.Fatalf("%s: %v", meta, err)
		}
		if got.Choice != meta || got.GoalMaybeDone != 0 {
			t.Fatalf("%s: result = %+v", meta, got)
		}
	}
}

// answers は next と goal_maybe_done の答えを持つ応答本文を作る（引数は各答えの type 以外の項目）。
func answers(choiceFields, noulFields string) string {
	return `{"model":"m","answers":{"next":{"type":"choice",` + choiceFields +
		`},"goal_maybe_done":{"type":"noul",` + noulFields + `}},"usage":{"input_tokens":1,"output_tokens":1}}`
}

func TestUnmarshalResponseRejectsBadBodies(t *testing.T) {
	_, allowed, _ := marshalRequest(fixtureRequest())
	const id = "b1c2d3e4f5a60718"
	for name, body := range map[string]string{
		"空本文":                ``,
		"JSON でない":           `<html>oops</html>`,
		"配列":                 `[]`,
		"answers なし":         `{"model":"m"}`,
		"next なし":            `{"answers":{"goal_maybe_done":{"type":"noul","noul":0.1}}}`,
		"goal_maybe_done なし": `{"answers":{"next":{"type":"choice","choice":"` + id + `","confidence":0.9}}}`,
		"choice なし":          answers(`"confidence":0.9`, `"noul":0.1`),
		"confidence なし":      answers(`"choice":"`+id+`"`, `"noul":0.1`),
		"noul なし":            answers(`"choice":"`+id+`","confidence":0.9`, `"x":1`),
		"送っていない選択肢":          answers(`"choice":"not-offered","confidence":0.9`, `"noul":0.1`),
		"空の選択肢":              answers(`"choice":"","confidence":0.9`, `"noul":0.1`),
		"confidence が 1 超":   answers(`"choice":"`+id+`","confidence":1.5`, `"noul":0.1`),
		"confidence が負":      answers(`"choice":"`+id+`","confidence":-0.1`, `"noul":0.1`),
		"noul が 1 超":         answers(`"choice":"`+id+`","confidence":0.9`, `"noul":2`),
		"choice の型違い":        answers(`"choice":7,"confidence":0.9`, `"noul":0.1`),
		"type が違う":           `{"answers":{"next":{"type":"score","score":1},"goal_maybe_done":{"type":"noul","noul":0.1}}}`,
		"noul の type が違う":    `{"answers":{"next":{"type":"choice","choice":"` + id + `","confidence":0.9},"goal_maybe_done":{"type":"choice","noul":0.1}}}`,
		"null の confidence":  answers(`"choice":"`+id+`","confidence":null`, `"noul":0.1`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := unmarshalResponse([]byte(body), allowed); !errors.Is(err, ErrBadResponse) {
				t.Fatalf("err = %v, want ErrBadResponse", err)
			}
		})
	}
}

// 誤りの文面に応答本文を含めない（本文はサーバーが返すもので、何が入っているか分からない）。
func TestUnmarshalResponseErrorDoesNotEchoBody(t *testing.T) {
	_, allowed, _ := marshalRequest(fixtureRequest())
	const secret = "BODY-CANARY-123"
	_, err := unmarshalResponse([]byte(answers(`"choice":"`+secret+`","confidence":0.9`, `"noul":0.1`)), allowed)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v", err)
	}
	_, err = unmarshalResponse([]byte(`{"x":"`+secret+`"`), allowed)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v", err)
	}
}
