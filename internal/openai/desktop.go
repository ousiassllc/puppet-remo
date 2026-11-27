package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは関数ツールの経路（接続方式 chatgpt・chatgpt-codex。FR-G27）の、namespace desktop の関数の定義と引数の検査。
// 名前・引数・座標系・上限は MCP の低レベルツール（FR-M04〜M10）と同じ。ChatGPT 用の Model（Responses API の namespace ツール）と
// Codex の app-server（dynamicTools の namespace）が同じ定義・同じ検査を使う。

// DesktopNamespace は関数ツールをまとめる namespace の名前（FR-G26）。
const DesktopNamespace = "desktop"

// DesktopNamespaceDescription は namespace の説明（モデルへ渡す）。
const DesktopNamespaceDescription = "Operate the owner's Windows desktop. Coordinates are pixels in the latest screenshot " +
	"(the primary monitor, scaled). After your calls run, a new screenshot follows as a user message."

// 上限（FR-M07・M09・M10）。
const (
	desktopMaxScrollAmount = 50
	desktopMaxTypeRunes    = 10_000
	desktopMaxWaitMs       = 10_000
)

// FunctionOutputOK は実行した呼び出しの function_call_output（FR-G27 (4)）。
const FunctionOutputOK = `{"ok":true}`

// FunctionOutputNotExecuted は、スコープ付きのタスクでバッチの途中に対象グループが変わり、実行しなかった呼び出しの出力（FR-G27 (4)・#158）。
const FunctionOutputNotExecuted = `{"ok":false,"error":"not_executed","reason":"group_changed"}`

// DesktopTool は関数 1 つの定義。Parameters は JSON Schema（object）。
type DesktopTool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

var (
	modifiersProp = map[string]any{
		"type": "array", "description": "modifier keys held during the action",
		"items": map[string]any{"type": "string", "enum": []string{"ctrl", "shift", "alt", "win"}},
	}
	pointProp = map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"x", "y"},
		"properties": map[string]any{"x": intProp("x in screenshot pixels"), "y": intProp("y in screenshot pixels")},
	}
)

func object(required []string, props map[string]any) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": props}
}

func clickTool(name, desc string) DesktopTool {
	return DesktopTool{Name: name, Description: desc + " Omit x and y to click at the current cursor position.", Parameters: object(nil, map[string]any{
		"x": intProp("x in screenshot pixels"), "y": intProp("y in screenshot pixels"), "modifiers": modifiersProp,
	})}
}

// DesktopTools は FR-G27 の関数ツール一覧（順序は一覧の表のとおり）。呼び出し側は変更しないこと。
var DesktopTools = []DesktopTool{
	{Name: "screenshot", Description: "Take a new screenshot of the primary monitor.", Parameters: object(nil, map[string]any{})},
	{Name: "mouse_move", Description: "Move the mouse cursor to a point.", Parameters: object([]string{"x", "y"}, map[string]any{
		"x": intProp("x in screenshot pixels"), "y": intProp("y in screenshot pixels"),
	})},
	clickTool("left_click", "Left-click at a point."),
	clickTool("right_click", "Right-click at a point."),
	clickTool("middle_click", "Middle-click at a point."),
	clickTool("double_click", "Double left-click at a point."),
	{Name: "left_click_drag", Description: "Press the left button at from, move to to and release.", Parameters: object([]string{"from", "to"}, map[string]any{
		"from": pointProp, "to": pointProp, "modifiers": modifiersProp,
	})},
	{Name: "scroll", Description: "Turn the mouse wheel at a point, in notches.", Parameters: object([]string{"x", "y", "direction", "amount"}, map[string]any{
		"x": intProp("x in screenshot pixels"), "y": intProp("y in screenshot pixels"),
		"direction": map[string]any{"type": "string", "enum": []string{"up", "down", "left", "right"}},
		"amount":    map[string]any{"type": "integer", "minimum": 1, "maximum": desktopMaxScrollAmount, "description": "wheel notches"},
	})},
	{Name: "key", Description: "Press a key combination such as ctrl+shift+t or enter.", Parameters: object([]string{"keys"}, map[string]any{
		"keys": map[string]any{"type": "string", "description": "keys joined by +. Modifiers: ctrl, shift, alt, win. Names: enter, escape, tab, " +
			"backspace, delete, insert, home, end, pageup, pagedown, up, down, left, right, space, capslock, f1 to f24, or one letter, digit or symbol"},
	})},
	{Name: "type", Description: "Type Unicode text into the focused window.", Parameters: object([]string{"text"}, map[string]any{
		"text": map[string]any{"type": "string", "maxLength": desktopMaxTypeRunes},
	})},
	{Name: "wait", Description: "Wait for the given milliseconds.", Parameters: object([]string{"ms"}, map[string]any{
		"ms": map[string]any{"type": "integer", "minimum": 0, "maximum": desktopMaxWaitMs},
	})},
}

// ParseDesktopCall は desktop の関数の呼び出し（名前と JSON の引数）を検査して domain.Op にする（FR-G27 (3)）。
// screenshot は nil の Op（新しい画面を撮るだけ）。未知の名前・未知の引数・型や範囲の不正・x と y の片方だけ・未知のキーは
// domain.ErrUnsupportedAction を包んで返す（タスクは unsupported_action）。座標が画像の外かは Control が見る（invalid_coordinates）。
// エラーには引数の値（type の本文など）を含めない。座標は縮小画像の座標で、Monitor は常に domain.MonitorPrimary（FR-G06）。
func ParseDesktopCall(name string, args []byte) (domain.Op, error) {
	if len(bytes.TrimSpace(args)) == 0 {
		args = []byte("{}")
	}
	switch name {
	case "screenshot":
		var in struct{}
		return nil, decodeArgs(name, args, &in)
	case "mouse_move":
		var in struct{ X, Y *int }
		if err := decodeArgs(name, args, &in); err != nil {
			return nil, err
		}
		if in.X == nil || in.Y == nil {
			return nil, unsupported(name, "x and y are required")
		}
		return domain.MouseMove{At: domain.Point{X: *in.X, Y: *in.Y}, Monitor: domain.MonitorPrimary}, nil
	case "left_click", "right_click", "middle_click", "double_click":
		var in struct {
			X, Y      *int
			Modifiers []string
		}
		if err := decodeArgs(name, args, &in); err != nil {
			return nil, err
		}
		mods, err := desktopMods(name, in.Modifiers)
		if err != nil {
			return nil, err
		}
		var at *domain.Point
		switch {
		case in.X != nil && in.Y != nil:
			at = &domain.Point{X: *in.X, Y: *in.Y}
		case in.X != nil || in.Y != nil:
			return nil, unsupported(name, "x and y must be given together")
		}
		btn, count := domain.ButtonLeft, 1
		switch name {
		case "right_click":
			btn = domain.ButtonRight
		case "middle_click":
			btn = domain.ButtonMiddle
		case "double_click":
			count = 2
		}
		return domain.Click{At: at, Button: btn, Count: count, Mods: mods, Monitor: domain.MonitorPrimary}, nil
	case "left_click_drag":
		var in struct {
			From, To  *struct{ X, Y *int }
			Modifiers []string
		}
		if err := decodeArgs(name, args, &in); err != nil {
			return nil, err
		}
		if in.From == nil || in.To == nil || in.From.X == nil || in.From.Y == nil || in.To.X == nil || in.To.Y == nil {
			return nil, unsupported(name, "from and to need x and y")
		}
		mods, err := desktopMods(name, in.Modifiers)
		if err != nil {
			return nil, err
		}
		return domain.Drag{
			From: domain.Point{X: *in.From.X, Y: *in.From.Y}, To: domain.Point{X: *in.To.X, Y: *in.To.Y},
			Mods: mods, Monitor: domain.MonitorPrimary,
		}, nil
	case "scroll":
		var in struct {
			X, Y      *int
			Direction string
			Amount    *int
		}
		if err := decodeArgs(name, args, &in); err != nil {
			return nil, err
		}
		dir := domain.Direction(in.Direction)
		switch {
		case in.X == nil || in.Y == nil:
			return nil, unsupported(name, "x and y are required")
		case !dir.Valid():
			return nil, unsupported(name, "unknown direction")
		case in.Amount == nil || *in.Amount < 1 || *in.Amount > desktopMaxScrollAmount:
			return nil, unsupported(name, "amount must be between 1 and 50")
		}
		return domain.Scroll{At: domain.Point{X: *in.X, Y: *in.Y}, Dir: dir, Amount: *in.Amount, Monitor: domain.MonitorPrimary}, nil
	case "key":
		var in struct{ Keys *string }
		if err := decodeArgs(name, args, &in); err != nil {
			return nil, err
		}
		if in.Keys == nil {
			return nil, unsupported(name, "keys is required")
		}
		combo, err := domain.ParseKeyCombo(*in.Keys)
		if err != nil {
			return nil, unsupported(name, "unknown key")
		}
		return domain.KeyPress{Keys: combo}, nil
	case "type":
		var in struct{ Text *string }
		if err := decodeArgs(name, args, &in); err != nil {
			return nil, err
		}
		switch {
		case in.Text == nil:
			return nil, unsupported(name, "text is required")
		case utf8.RuneCountInString(*in.Text) > desktopMaxTypeRunes:
			return nil, unsupported(name, "text is longer than 10000 characters")
		}
		return domain.TypeText{Text: *in.Text}, nil
	case "wait":
		var in struct{ Ms *int }
		if err := decodeArgs(name, args, &in); err != nil {
			return nil, err
		}
		if in.Ms == nil || *in.Ms < 0 || *in.Ms > desktopMaxWaitMs {
			return nil, unsupported(name, "ms must be between 0 and 10000")
		}
		return domain.Wait{Duration: time.Duration(*in.Ms) * time.Millisecond}, nil
	}
	return nil, unsupported("function", "unknown desktop function")
}

// decodeArgs は引数を厳密に読む（未知の項目・型の違い・余分な値は対応しない呼び出し）。値はエラーに含めない。
func decodeArgs(name string, args []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return unsupported(name, "invalid arguments")
	}
	if dec.More() {
		return unsupported(name, "invalid arguments")
	}
	return nil
}

// desktopMods は修飾キーの名前を検査して Op の型にする（ctrl / shift / alt / win だけ。大文字小文字は区別しない）。
func desktopMods(name string, names []string) ([]domain.Modifier, error) {
	if len(names) == 0 {
		return nil, nil
	}
	mods := make([]domain.Modifier, 0, len(names))
	for _, n := range names {
		m := domain.Modifier(strings.ToLower(strings.TrimSpace(n)))
		if !m.Valid() {
			return nil, unsupported(name, fmt.Sprintf("unknown modifier #%d", len(mods)+1))
		}
		mods = append(mods, m)
	}
	return mods, nil
}
