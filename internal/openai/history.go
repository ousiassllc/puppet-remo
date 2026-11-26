package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
)

// OmittedScreenshot は、保持枚数を超えた古いスクリーンショットの代わりに送るテキスト（FR-G24）。
const OmittedScreenshot = "[screenshot omitted]"

type itemKind int

const (
	kindUser           itemKind = iota // 最初のユーザーメッセージ
	kindOutput                         // モデルの出力アイテム（原文）
	kindComputerOutput                 // computer_call_output
	kindUserNote                       // role: user の input_text だけのメッセージ（AddUserNote。画像なし）
)

// ErrCallPairing は履歴の computer_call / function_call と、その call_id の出力が 1 対 1 になっていない（出力の欠落・重複）。
// 履歴の組み立て（input）が返す。API は出力の欠けた call_id を拒否するので、送る前に止める（#158）。
var ErrCallPairing = errors.New("openai: every call needs exactly one output with the same call_id")

// historyItem は History の 1 要素。shot が nil のスクリーンショットは送らず、OmittedScreenshot で置き換える。
type historyItem struct {
	kind itemKind

	raw json.RawMessage // kindOutput

	texts []string // kindUser: 指示、高速パスの要約（あれば）。kindUserNote: 本文

	callID string        // kindComputerOutput
	ack    []SafetyCheck // kindComputerOutput

	shot []byte // kindUser・kindComputerOutput の PNG。保持枚数を超えると nil になる
}

// History はステートレス継続の履歴（FR-G24）。run が 1 タスクにつき 1 つ持ち、run の goroutine だけが触る（並行利用は不可）。
type History struct {
	items []historyItem
	keep  int
}

// NewHistory は最初のユーザーメッセージ（指示、高速パスの文章要約（無ければ空）、最初のスクリーンショット）から履歴を作る。
// keepScreenshots は画像つきで残す最新のスクリーンショットの枚数（agent.keep_screenshots。FR-S19）。1 未満は 1 として扱う。
// firstShot は PNG。呼び出し後は変更しないこと（コピーしない）。
func NewHistory(instruction, handoffSummary string, firstShot []byte, keepScreenshots int) *History {
	if keepScreenshots < 1 {
		keepScreenshots = 1
	}
	texts := []string{instruction}
	if handoffSummary != "" {
		texts = append(texts, handoffSummary)
	}
	h := &History{keep: keepScreenshots}
	h.items = append(h.items, historyItem{kind: kindUser, texts: texts, shot: nonEmpty(firstShot)})
	return h
}

// AddOutput は応答の出力アイテムを、変更せずそのまま追記する（Raw はコピーしない）。
func (h *History) AddOutput(items []OutputItem) {
	for _, it := range items {
		h.items = append(h.items, historyItem{kind: kindOutput, raw: it.Raw})
	}
}

// AddComputerOutput は computer_call_output を追加し、直近 keep 枚を超える古いスクリーンショットを
// プレースホルダのテキスト（OmittedScreenshot）へ置き換える。ack は所有者が許可した安全チェック（無ければ nil）。
// shot は PNG。呼び出し後は変更しないこと（コピーしない）。
func (h *History) AddComputerOutput(callID string, shot []byte, ack []SafetyCheck) {
	h.items = append(h.items, historyItem{kind: kindComputerOutput, callID: callID, ack: ack, shot: nonEmpty(shot)})
	kept := 0
	for i := len(h.items) - 1; i >= 0; i-- {
		if h.items[i].shot == nil {
			continue
		}
		kept++
		if kept > h.keep {
			h.items[i].shot = nil
		}
	}
}

// AddUserNote は role: user の input_text だけのメッセージ（画像なし）を追加する。computer_call_output に未実行を示す欄が無いので、
// 中断（対象グループの変化）をモデルへ伝えるのに使う。直前の computer_call_output の直後に置くこと（呼び出し側の規則）。
func (h *History) AddUserNote(text string) {
	h.items = append(h.items, historyItem{kind: kindUserNote, texts: []string{text}})
}

// callEnvelope はモデルの出力アイテム（原文）から種別と call_id だけを読む。
type callEnvelope struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
}

// validate は、すべての computer_call・function_call に、call_id が一致する出力がちょうど 1 件あることを確かめる
// （欠落・重複は ErrCallPairing）。出力は computer_call_output だけを数える（function_call_output は ChatGPT の経路の実装で足す）。
func (h *History) validate() error {
	outputs := map[string]int{}
	for i := range h.items {
		if h.items[i].kind == kindComputerOutput {
			outputs[h.items[i].callID]++
		}
	}
	for id, n := range outputs {
		if n > 1 {
			return fmt.Errorf("%w: call_id %q has %d outputs", ErrCallPairing, id, n)
		}
	}
	for i := range h.items {
		if h.items[i].kind != kindOutput {
			continue
		}
		var env callEnvelope
		if json.Unmarshal(h.items[i].raw, &env) != nil {
			continue
		}
		if (env.Type == "computer_call" || env.Type == "function_call") && outputs[env.CallID] != 1 {
			return fmt.Errorf("%w: %s %q has %d outputs", ErrCallPairing, env.Type, env.CallID, outputs[env.CallID])
		}
	}
	return nil
}

// Screenshots は画像つきで残っているスクリーンショットの枚数（keep 以下）。
func (h *History) Screenshots() int {
	n := 0
	for i := range h.items {
		if h.items[i].shot != nil {
			n++
		}
	}
	return n
}

// LatestScreenshot は画像つきで残っている最新のスクリーンショット（PNG）。無ければ nil（テスト・デモが、モデルに渡った画像を確かめるのに使う）。
func (h *History) LatestScreenshot() []byte {
	for i := len(h.items) - 1; i >= 0; i-- {
		if h.items[i].shot != nil {
			return h.items[i].shot
		}
	}
	return nil
}

func nonEmpty(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

// input は Responses API の input を組み立てる。出力アイテムは Raw のまま（再エンコードしない）。
// call_id の出力が欠けた・重複した履歴は ErrCallPairing で拒否する（validate）。
func (h *History) input() (responses.ResponseInputParam, error) {
	if err := h.validate(); err != nil {
		return nil, err
	}
	in := make(responses.ResponseInputParam, 0, len(h.items))
	for i := range h.items {
		it := &h.items[i]
		switch it.kind {
		case kindUser:
			in = append(in, userMessage(it))
		case kindOutput:
			in = append(in, param.Override[responses.ResponseInputItemUnionParam](it.raw))
		case kindComputerOutput:
			in = append(in, computerOutput(it))
		case kindUserNote:
			in = append(in, userNote(it))
		}
	}
	return in, nil
}

func userNote(it *historyItem) responses.ResponseInputItemUnionParam {
	content := make(responses.ResponseInputMessageContentListParam, 0, len(it.texts))
	for _, t := range it.texts {
		content = append(content, responses.ResponseInputContentParamOfInputText(t))
	}
	return responses.ResponseInputItemUnionParam{OfInputMessage: &responses.ResponseInputItemMessageParam{
		Content: content,
		Role:    "user",
		Type:    "message",
	}}
}

func userMessage(it *historyItem) responses.ResponseInputItemUnionParam {
	content := make(responses.ResponseInputMessageContentListParam, 0, len(it.texts)+1)
	for _, t := range it.texts {
		content = append(content, responses.ResponseInputContentParamOfInputText(t))
	}
	if it.shot != nil {
		content = append(content, responses.ResponseInputContentUnionParam{OfInputImage: &responses.ResponseInputImageParam{
			Detail:   responses.ResponseInputImageDetailOriginal,
			ImageURL: param.NewOpt(pngDataURL(it.shot)),
		}})
	} else {
		content = append(content, responses.ResponseInputContentParamOfInputText(OmittedScreenshot))
	}
	return responses.ResponseInputItemUnionParam{OfInputMessage: &responses.ResponseInputItemMessageParam{
		Content: content,
		Role:    "user",
		Type:    "message",
	}}
}

func computerOutput(it *historyItem) responses.ResponseInputItemUnionParam {
	out := responses.ResponseInputItemComputerCallOutputParam{CallID: it.callID}
	if it.shot != nil {
		out.Output = responses.ResponseComputerToolCallOutputScreenshotParam{ImageURL: param.NewOpt(pngDataURL(it.shot))}
	} else {
		// 画像をテキストのプレースホルダに置き換えた形（JSON の文字列）。実 API が受理するかは実 API のスモークで確認する（§12.2）。
		out.Output = param.Override[responses.ResponseComputerToolCallOutputScreenshotParam](placeholderOutput)
	}
	for _, c := range it.ack {
		a := responses.ResponseInputItemComputerCallOutputAcknowledgedSafetyCheckParam{ID: c.ID}
		if c.Code != "" {
			a.Code = param.NewOpt(c.Code)
		}
		if c.Message != "" {
			a.Message = param.NewOpt(c.Message)
		}
		out.AcknowledgedSafetyChecks = append(out.AcknowledgedSafetyChecks, a)
	}
	return responses.ResponseInputItemUnionParam{OfComputerCallOutput: &out}
}

var placeholderOutput = json.RawMessage(`"` + OmittedScreenshot + `"`)

func pngDataURL(png []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}
