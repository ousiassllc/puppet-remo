package typesafe

// このファイルは POST /v1/systemone の JSON の組み立てと解釈だけを持つ（HTTP・キー・エラーの分類は chooser.go）。
//
// 形式の根拠: TypeSafe の公開 OpenAPI（https://api.typesafe.ai/openapi.json、version 0.2.0。
// https://docs.typesafe.ai/api/system-one）。
//   - 要求: {model, state, questions{名前: {type, instructions, criteria}}}。state は文字列・オブジェクト・配列の
//     いずれでもよく、ここでは FR-G18 の状態オブジェクトを置く。
//   - Choice 質問: criteria は「選択肢の名前 → いつ選ぶかの説明」（1〜255 件）。
//   - Noul 質問: criteria は任意。
//   - 応答: {model, answers{質問の名前: ...}, usage}。Choice の答えは {type:"choice", choice, confidence, probabilities}、
//     Noul の答えは {type:"noul", noul}（はい の確率 0〜1）。
//
// 実 API との突き合わせ（スモーク。docs/components/overview.md §12.2）は未実施。
// confidence の意味と、質問・説明文の英語の言い回しが実際の判断品質に効くかは実測で確かめる。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
)

// 質問の名前。応答の answers のキーでもある。
const (
	questionNext          = "next"
	questionGoalMaybeDone = "goal_maybe_done"
)

type wireRequest struct {
	Model     string                  `json:"model"`
	State     wireState               `json:"state"`
	Questions map[string]wireQuestion `json:"questions"`
}

// wireState は FR-G18 の状態。ここに無い欄は送られない。
type wireState struct {
	Goal     string        `json:"goal"`
	Window   wireWindow    `json:"window"`
	Elements []wireElement `json:"elements"`
	History  []string      `json:"history"`
}

type wireWindow struct {
	Title   string `json:"title"`
	Process string `json:"process"`
}

type wireElement struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
}

type wireQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type wireResponse struct {
	Answers map[string]wireAnswer `json:"answers"`
}

// wireAnswer は Choice と Noul の答えの和。欠けた項目を 0 と区別するためポインターにする。
type wireAnswer struct {
	Type       string   `json:"type"`
	Choice     *string  `json:"choice"`
	Confidence *float64 `json:"confidence"`
	Noul       *float64 `json:"noul"`
}

const (
	nextInstructions = "Decide the single next UI step toward state.goal. " +
		"state.window is the foreground window, state.elements lists the operable candidates, and state.history " +
		"lists the most recent steps already taken (oldest first). " +
		"Pick the candidate whose operation makes the most progress. " +
		"Pick " + ReobserveChoice + " if the screen has probably not finished updating or you need a fresh look. " +
		"Pick " + AbstainChoice + " if no candidate clearly fits or you are unsure."
	reobserveDescription = "No candidate is right yet; the screen may not have finished updating, so look at it again."
	abstainDescription   = "Cannot decide with confidence: no candidate clearly fits the goal, or the goal needs typing, keys or scrolling."
	goalDoneInstructions = "Judging only from state.history and state.elements, has state.goal probably already been achieved?"
)

// marshalRequest は ChoiceRequest を要求本文にする。Elements の ID は Choice の選択肢の名前になるので、
// 空・メタ候補と同名・重複のものは拒否する（黙って上書きすると、別の要素を選んだことになりうる）。
// 同時に、回答に許す選択肢の名前の集合を返す。
func marshalRequest(req ChoiceRequest) ([]byte, map[string]struct{}, error) {
	criteria := make(map[string]string, len(req.Elements)+2)
	elements := make([]wireElement, 0, len(req.Elements))
	for _, e := range req.Elements {
		if e.ID == "" || e.ID == ReobserveChoice || e.ID == AbstainChoice {
			return nil, nil, fmt.Errorf("%w: invalid candidate ID in request", ErrBadResponse)
		}
		if _, dup := criteria[e.ID]; dup {
			return nil, nil, fmt.Errorf("%w: duplicate candidate ID in request", ErrBadResponse)
		}
		criteria[e.ID] = describeElement(e)
		elements = append(elements, wireElement(e))
	}
	criteria[ReobserveChoice] = reobserveDescription
	criteria[AbstainChoice] = abstainDescription

	history := req.History
	if len(history) > maxHistory {
		history = history[len(history)-maxHistory:]
	}

	w := wireRequest{
		Model: req.Model,
		State: wireState{
			Goal:     req.Goal,
			Window:   wireWindow{Title: req.Window.Title, Process: req.Window.Process},
			Elements: elements,
			History:  append([]string{}, history...),
		},
		Questions: map[string]wireQuestion{
			questionNext:          {Type: "choice", Instructions: nextInstructions, Criteria: criteria},
			questionGoalMaybeDone: {Type: "noul", Instructions: goalDoneInstructions},
		},
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // < > & をエスケープしない（本文を小さく保つ。どちらも正しい JSON）
	if err := enc.Encode(w); err != nil {
		return nil, nil, fmt.Errorf("typesafe: encode request: %w", err)
	}

	allowed := make(map[string]struct{}, len(criteria))
	for name := range criteria {
		allowed[name] = struct{}{}
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), allowed, nil
}

// describeElement は候補の「いつ選ぶか」の短い説明。要素の値は持たないので、役割とラベルだけで作る。
func describeElement(e ChoiceElement) string {
	if e.Role == "window" {
		return fmt.Sprintf("Bring the window titled %q to the foreground.", e.Label)
	}
	return fmt.Sprintf("Click the %s labeled %q.", e.Role, e.Label)
}

// unmarshalResponse は 200 の応答本文を ChoiceResult にする。必要な項目が無い・型や範囲が違う・
// 送っていない選択肢を返したときは ErrBadResponse（本文は誤りの文面に含めない）。
func unmarshalResponse(body []byte, allowed map[string]struct{}) (ChoiceResult, error) {
	var w wireResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return ChoiceResult{}, fmt.Errorf("%w: invalid JSON", ErrBadResponse)
	}

	next, ok := w.Answers[questionNext]
	if !ok || next.Type != "choice" || next.Choice == nil || next.Confidence == nil {
		return ChoiceResult{}, fmt.Errorf("%w: missing %q choice answer", ErrBadResponse, questionNext)
	}
	if _, ok := allowed[*next.Choice]; !ok {
		return ChoiceResult{}, fmt.Errorf("%w: choice is not one of the offered options", ErrBadResponse)
	}
	if !unitInterval(*next.Confidence) {
		return ChoiceResult{}, fmt.Errorf("%w: confidence out of range", ErrBadResponse)
	}

	done, ok := w.Answers[questionGoalMaybeDone]
	if !ok || done.Type != "noul" || done.Noul == nil {
		return ChoiceResult{}, fmt.Errorf("%w: missing %q noul answer", ErrBadResponse, questionGoalMaybeDone)
	}
	if !unitInterval(*done.Noul) {
		return ChoiceResult{}, fmt.Errorf("%w: %s out of range", ErrBadResponse, questionGoalMaybeDone)
	}

	return ChoiceResult{Choice: *next.Choice, Confidence: *next.Confidence, GoalMaybeDone: *done.Noul}, nil
}

func unitInterval(v float64) bool { return !math.IsNaN(v) && v >= 0 && v <= 1 }
