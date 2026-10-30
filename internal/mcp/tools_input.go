package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 入力系ツールはすべてリースが必要で、未保持なら Control が暗黙に取得する（FR-L04）。
// 範囲・文字数の検査も Control が行い、違反は invalid_argument / invalid_coordinates になる。

type mouseMoveIn struct {
	X       int  `json:"x" jsonschema:"x in scaled screenshot pixels"`
	Y       int  `json:"y" jsonschema:"y in scaled screenshot pixels"`
	Monitor *int `json:"monitor,omitempty" jsonschema:"monitor index (default: the primary monitor)"`
}

type clickIn struct {
	X         *int     `json:"x,omitempty" jsonschema:"x in scaled screenshot pixels (omit x and y to click at the current cursor position)"`
	Y         *int     `json:"y,omitempty" jsonschema:"y in scaled screenshot pixels"`
	Modifiers []string `json:"modifiers,omitempty" jsonschema:"modifier keys held during the click: ctrl, shift, alt, win"`
	Monitor   *int     `json:"monitor,omitempty" jsonschema:"monitor index (default: the primary monitor)"`
}

type dragIn struct {
	From      pointBody `json:"from" jsonschema:"where to press the left button, in scaled screenshot pixels"`
	To        pointBody `json:"to" jsonschema:"where to release the left button, in scaled screenshot pixels"`
	Modifiers []string  `json:"modifiers,omitempty" jsonschema:"modifier keys held during the drag: ctrl, shift, alt, win"`
	Monitor   *int      `json:"monitor,omitempty" jsonschema:"monitor index (default: the primary monitor)"`
}

type scrollIn struct {
	X         int    `json:"x" jsonschema:"x in scaled screenshot pixels"`
	Y         int    `json:"y" jsonschema:"y in scaled screenshot pixels"`
	Direction string `json:"direction" jsonschema:"up, down, left or right"`
	Amount    int    `json:"amount" jsonschema:"wheel notches, 1 to 50"`
	Monitor   *int   `json:"monitor,omitempty" jsonschema:"monitor index (default: the primary monitor)"`
}

type keyIn struct {
	Keys string `json:"keys" jsonschema:"keys joined by +, for example ctrl+shift+t or enter. Modifiers: ctrl, shift, alt, win. Names: enter, escape, tab, backspace, delete, insert, home, end, pageup, pagedown, up, down, left, right, space, capslock, f1 to f24, or one letter, digit or symbol"`
}

type typeIn struct {
	Text string `json:"text" jsonschema:"Unicode text to type, up to 10000 characters"`
}

type waitIn struct {
	Ms int `json:"ms" jsonschema:"milliseconds to wait, 0 to 10000"`
}

// clicks は 5 種類のクリックツール（FR-M05）の定義。
var clicks = []struct {
	name   string
	desc   string
	button domain.Button
	count  int
}{
	{"left_click", "Left-click at a point (default: the current cursor position).", domain.ButtonLeft, 1},
	{"right_click", "Right-click at a point (default: the current cursor position).", domain.ButtonRight, 1},
	{"middle_click", "Middle-click at a point (default: the current cursor position).", domain.ButtonMiddle, 1},
	{"double_click", "Double left-click at a point (default: the current cursor position).", domain.ButtonLeft, 2},
	{"triple_click", "Triple left-click at a point (default: the current cursor position).", domain.ButtonLeft, 3},
}

// addInputTools は入力系のツールを登録する: FR-M04〜M10。
func (s *Server) addInputTools() {
	addTool(s, "mouse_move", "Move the mouse cursor to a point. Needs the control lease (acquired implicitly).",
		false, func(ctx context.Context, who domain.Actor, in mouseMoveIn) (toolResult, error) {
			monitor, err := monitorArg(in.Monitor)
			if err != nil {
				return toolResult{}, err
			}
			return s.do(ctx, who, domain.MouseMove{At: domain.Point{X: in.X, Y: in.Y}, Monitor: monitor})
		})

	for _, c := range clicks {
		addTool(s, c.name, c.desc+" Needs the control lease (acquired implicitly).",
			false, func(ctx context.Context, who domain.Actor, in clickIn) (toolResult, error) {
				monitor, err := monitorArg(in.Monitor)
				if err != nil {
					return toolResult{}, err
				}
				at, err := optionalPoint(in.X, in.Y)
				if err != nil {
					return toolResult{}, err
				}
				return s.do(ctx, who, domain.Click{
					At: at, Button: c.button, Count: c.count, Mods: modifiers(in.Modifiers), Monitor: monitor,
				})
			})
	}

	addTool(s, "left_click_drag",
		"Press the left button at from, move to to and release. Needs the control lease (acquired implicitly).",
		false, func(ctx context.Context, who domain.Actor, in dragIn) (toolResult, error) {
			monitor, err := monitorArg(in.Monitor)
			if err != nil {
				return toolResult{}, err
			}
			return s.do(ctx, who, domain.Drag{
				From:    domain.Point{X: in.From.X, Y: in.From.Y},
				To:      domain.Point{X: in.To.X, Y: in.To.Y},
				Mods:    modifiers(in.Modifiers),
				Monitor: monitor,
			})
		})

	addTool(s, "scroll", "Turn the mouse wheel at a point, in notches. Needs the control lease (acquired implicitly).",
		false, func(ctx context.Context, who domain.Actor, in scrollIn) (toolResult, error) {
			monitor, err := monitorArg(in.Monitor)
			if err != nil {
				return toolResult{}, err
			}
			return s.do(ctx, who, domain.Scroll{
				At:      domain.Point{X: in.X, Y: in.Y},
				Dir:     domain.Direction(strings.ToLower(in.Direction)),
				Amount:  in.Amount,
				Monitor: monitor,
			})
		})

	addTool(s, "key", "Press a key combination such as ctrl+shift+t or enter. Needs the control lease (acquired implicitly).",
		false, func(ctx context.Context, who domain.Actor, in keyIn) (toolResult, error) {
			keys, err := domain.ParseKeyCombo(in.Keys)
			if err != nil {
				return toolResult{}, err
			}
			return s.do(ctx, who, domain.KeyPress{Keys: keys})
		})

	addTool(s, "type", "Type Unicode text, including Japanese, into the focused window. Needs the control lease (acquired implicitly).",
		false, func(ctx context.Context, who domain.Actor, in typeIn) (toolResult, error) {
			return s.do(ctx, who, domain.TypeText{Text: in.Text})
		})

	addTool(s, "wait", "Wait for the given milliseconds (0 to 10000). Does not need the control lease.",
		true, func(ctx context.Context, who domain.Actor, in waitIn) (toolResult, error) {
			// ミリ秒を Duration へ掛ける前に範囲を見る（巨大な値が桁あふれで範囲内に見えないように）。
			if in.Ms < 0 || in.Ms > int(10*time.Second/time.Millisecond) {
				return toolResult{}, domain.ErrInvalidArgument.WithMsg("ms must be between 0 and 10000")
			}
			return s.do(ctx, who, domain.Wait{Duration: time.Duration(in.Ms) * time.Millisecond})
		})
}

// do は op を Control に渡し、成功なら {"ok": true} を返す。
func (s *Server) do(ctx context.Context, who domain.Actor, op domain.Op) (toolResult, error) {
	if _, err := s.control.Do(ctx, who, op); err != nil {
		return toolResult{}, err
	}
	return okResult, nil
}

// optionalPoint は x と y の組を点にする。両方省略なら nil（現在のカーソル位置）。片方だけは不正。
func optionalPoint(x, y *int) (*domain.Point, error) {
	switch {
	case x == nil && y == nil:
		return nil, nil
	case x == nil || y == nil:
		return nil, domain.ErrInvalidArgument.WithMsg("x and y must be given together")
	}
	return &domain.Point{X: *x, Y: *y}, nil
}

// modifiers は修飾キーの文字列を Op の型にする。未知の値は Control が invalid_argument にする。
func modifiers(names []string) []domain.Modifier {
	if len(names) == 0 {
		return nil
	}
	mods := make([]domain.Modifier, len(names))
	for i, n := range names {
		mods[i] = domain.Modifier(strings.ToLower(strings.TrimSpace(n)))
	}
	return mods
}
