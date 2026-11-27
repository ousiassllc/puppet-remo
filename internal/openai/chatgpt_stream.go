package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// このファイルは ChatGPT 経路の応答（SSE）の読み取り。終端のイベントまで読み、response.completed だけを成功とする（FR-G26）。

// 読み取りの上限。
const (
	maxSSELine   = 8 << 20  // 1 行（1 イベントの data）の上限
	maxSSEStream = 64 << 20 // 1 回の応答全体の上限
)

// errNoTerminalEvent は response.completed などの終端のイベントが来る前にストリームが終わったことを表す。
var errNoTerminalEvent = errors.New("openai: stream ended before a terminal event")

// readChatGPTStream は SSE を読み、終端のイベントの結果を返す。途中までの function_call は返さない。token はエラーの文言から伏せる。
func readChatGPTStream(r io.Reader, token string) (StepOutput, error) {
	sc := bufio.NewScanner(io.LimitReader(r, maxSSEStream))
	sc.Buffer(make([]byte, 0, 64<<10), maxSSELine)
	var name string
	var data [][]byte
	dispatch := func() (StepOutput, bool, error) {
		if len(data) == 0 {
			name = ""
			return StepOutput{}, false, nil
		}
		ev := bytes.Join(data, []byte("\n"))
		n := name
		name, data = "", nil
		return handleChatGPTEvent(n, ev, token)
	}
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			if out, done, err := dispatch(); done || err != nil {
				return out, err
			}
			continue
		}
		if line[0] == ':' { // コメント（keep-alive）
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			name = string(value)
		case "data":
			data = append(data, append([]byte(nil), value...))
		}
	}
	if err := sc.Err(); err != nil {
		return StepOutput{}, newRequestError(err, token)
	}
	// 最後の空行なしで終わったイベントも読む（終端のイベントでなければ、下の errNoTerminalEvent になる）。
	if out, done, err := dispatch(); done || err != nil {
		return out, err
	}
	return StepOutput{}, errNoTerminalEvent
}

// streamEvent は SSE の data（JSON）。error イベントは項目が直下にも error の中にも来うる。
type streamEvent struct {
	Type     string        `json:"type"`
	Response *streamResp   `json:"response"`
	Error    *streamErrObj `json:"error"`
	streamErrObj
}

type streamErrObj struct {
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
	Param   json.RawMessage `json:"param"`
	Message string          `json:"message"`
}

type streamResp struct {
	ID                string            `json:"id"`
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	Error             *streamErrObj     `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

// handleChatGPTEvent は 1 イベントを処理する。done は終端のイベント（成功か失敗）を受けたこと。
func handleChatGPTEvent(name string, data []byte, token string) (out StepOutput, done bool, err error) {
	if string(bytes.TrimSpace(data)) == "[DONE]" {
		return StepOutput{}, false, nil
	}
	var ev streamEvent
	if json.Unmarshal(data, &ev) != nil {
		return StepOutput{}, false, errors.New("openai: invalid stream event")
	}
	typ := ev.Type
	if typ == "" {
		typ = name
	}
	switch typ {
	case "response.completed":
		if ev.Response == nil {
			return StepOutput{}, true, errors.New("openai: response.completed without response")
		}
		if s := ev.Response.Status; s != "" && s != "completed" {
			return StepOutput{}, true, streamFailure(ev.Response.Error, "response "+s, token)
		}
		out, err := parseChatGPTCompleted(ev.Response)
		return out, true, err
	case "response.failed":
		var e *streamErrObj
		if ev.Response != nil {
			e = ev.Response.Error
		}
		if e == nil {
			e = ev.Error
		}
		return StepOutput{}, true, streamFailure(e, "response failed", token)
	case "response.incomplete":
		reason := ""
		if ev.Response != nil && ev.Response.IncompleteDetails != nil {
			reason = ev.Response.IncompleteDetails.Reason
		}
		msg := "response incomplete"
		if reason != "" {
			msg += ": " + truncate(redact(reason, token), maxMessageRunes)
		}
		return StepOutput{}, true, errors.New("openai: " + msg)
	case "error":
		e := ev.Error
		if e == nil {
			e = &ev.streamErrObj
		}
		return StepOutput{}, true, streamFailure(e, "stream error", token)
	}
	return StepOutput{}, false, nil
}

// streamFailure はストリームの中の失敗（response.failed・error）を分類して返す。code・param・message はトークンを伏せる。
func streamFailure(e *streamErrObj, fallback, token string) error {
	if e == nil || (e.Code == nil && e.Message == "" && e.Type == "") {
		return errors.New("openai: " + fallback)
	}
	return classifyPlan(&APIError{
		Type:    redact(e.Type, token),
		Code:    redact(jsonString(e.Code), token),
		Param:   redact(jsonString(e.Param), token),
		Message: truncate(redact(e.Message, token), maxMessageRunes),
	})
}

// parseChatGPTCompleted は response.completed の response を StepOutput へ写す。出力アイテムは原文のまま。
func parseChatGPTCompleted(res *streamResp) (StepOutput, error) {
	out := StepOutput{ResponseID: res.ID}
	if res.Usage != nil {
		out.Usage = Usage{InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens}
	}
	var text string
	for _, raw := range res.Output {
		out.Items = append(out.Items, OutputItem{Raw: raw})
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return StepOutput{}, errors.New("openai: invalid output item")
		}
		switch item.Type {
		case "function_call":
			if item.CallID == "" {
				return StepOutput{}, errors.New("openai: function_call without call_id")
			}
			if item.Namespace != DesktopNamespace {
				return StepOutput{}, unsupported("function_call", "unknown namespace")
			}
			op, err := ParseDesktopCall(item.Name, []byte(item.Arguments))
			if err != nil {
				return StepOutput{}, err
			}
			out.Calls = append(out.Calls, ToolCall{CallID: item.CallID, Name: item.Name, Op: op})
		case "message":
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					text += part.Text
				case "refusal":
					text += part.Refusal
				}
			}
		}
	}
	if len(out.Calls) == 0 {
		out.FinalText = text
	}
	return out, nil
}
