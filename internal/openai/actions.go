package openai

import (
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v3/responses"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

const (
	// scrollPixelsPerNotch は OpenAI の scroll_x / scroll_y（ピクセル）を MCP の scroll.amount（ノッチ。FR-M07）に
	// 換算する固定値（100 px = 1 ノッチ。FR-G05・docs/components/overview.md §12.1 でオーナーが決定。ブラウザの 1 ノッチ
	// （WHEEL_DELTA 120）に近い値）。端数は切り上げ（0 でない移動は最低 1 ノッチ）。
	scrollPixelsPerNotch = 100
	// maxScrollNotches は domain.Scroll の Amount の上限（FR-M07: 1〜50）。超える移動は複数の Scroll に分ける。
	maxScrollNotches = 50
	// waitDuration は wait アクションの待ち時間。OpenAI の wait は時間を持たないため固定（OpenAI のサンプルと同じ 2 秒。FR-M10 の上限は 10 秒）。
	waitDuration = 2 * time.Second
)

// mapActions は computer_call の actions[] を、順序を保って domain.Op へ写す（FR-G05）。
// screenshot は捨てる（ループが各ステップの後に必ず新しい画面を返す）。
// 写像できないもの（未知の種別・back / forward ボタン・修飾キー以外の保持キー・不正なキー名・点が足りない drag）は
// domain.ErrUnsupportedAction を包んで返す。type の本文などユーザーの入力はエラーに載せない。
func mapActions(actions []responses.ComputerActionUnion) ([]domain.Op, error) {
	ops := make([]domain.Op, 0, len(actions))
	for _, a := range actions {
		got, err := mapAction(a)
		if err != nil {
			return nil, err
		}
		ops = append(ops, got...)
	}
	return ops, nil
}

func unsupported(typ, why string) error {
	return fmt.Errorf("%w: %s: %s", domain.ErrUnsupportedAction, typ, why)
}

func mapAction(a responses.ComputerActionUnion) ([]domain.Op, error) {
	at := domain.Point{X: int(a.X), Y: int(a.Y)}
	switch a.Type {
	case "click":
		btn, err := mapButton(a.Button)
		if err != nil {
			return nil, err
		}
		mods, err := mapMods(a.Type, a.Keys)
		if err != nil {
			return nil, err
		}
		return []domain.Op{domain.Click{At: &at, Button: btn, Count: 1, Mods: mods, Monitor: domain.MonitorPrimary}}, nil
	case "double_click":
		mods, err := mapMods(a.Type, a.Keys)
		if err != nil {
			return nil, err
		}
		return []domain.Op{domain.Click{At: &at, Button: domain.ButtonLeft, Count: 2, Mods: mods, Monitor: domain.MonitorPrimary}}, nil
	case "drag":
		// domain.Drag は始点と終点だけを持つ。途中の点は捨て、最初の点から最後の点へ 1 回で動かす。
		if len(a.Path) < 2 {
			return nil, unsupported(a.Type, "path needs at least 2 points")
		}
		mods, err := mapMods(a.Type, a.Keys)
		if err != nil {
			return nil, err
		}
		first, last := a.Path[0], a.Path[len(a.Path)-1]
		return []domain.Op{domain.Drag{
			From:    domain.Point{X: int(first.X), Y: int(first.Y)},
			To:      domain.Point{X: int(last.X), Y: int(last.Y)},
			Mods:    mods,
			Monitor: domain.MonitorPrimary,
		}}, nil
	case "move":
		if len(a.Keys) > 0 {
			return nil, unsupported(a.Type, "held keys are not supported")
		}
		return []domain.Op{domain.MouseMove{At: at, Monitor: domain.MonitorPrimary}}, nil
	case "scroll":
		if len(a.Keys) > 0 {
			return nil, unsupported(a.Type, "held keys are not supported")
		}
		return mapScroll(at, a.ScrollX, a.ScrollY), nil
	case "keypress":
		combo, err := mapKeys(a.Keys)
		if err != nil {
			return nil, err
		}
		return []domain.Op{domain.KeyPress{Keys: combo}}, nil
	case "type":
		return []domain.Op{domain.TypeText{Text: a.Text}}, nil
	case "wait":
		return []domain.Op{domain.Wait{Duration: waitDuration}}, nil
	case "screenshot":
		return nil, nil
	default:
		return nil, unsupported("unknown action type", fmt.Sprintf("%q", a.Type))
	}
}

// mapButton は click の button を写す。wheel は中ボタン。back / forward は FR-M05 に無いので未対応。
func mapButton(b string) (domain.Button, error) {
	switch b {
	case "left":
		return domain.ButtonLeft, nil
	case "right":
		return domain.ButtonRight, nil
	case "wheel", "middle":
		return domain.ButtonMiddle, nil
	default:
		return "", unsupported("click", fmt.Sprintf("button %q", b))
	}
}

// modifierAliases は OpenAI が返す修飾キーの表記（小文字）から domain.Modifier への対応。
var modifierAliases = map[string]domain.Modifier{
	"ctrl": domain.ModCtrl, "control": domain.ModCtrl,
	"shift": domain.ModShift,
	"alt":   domain.ModAlt, "option": domain.ModAlt,
	"win": domain.ModWin, "windows": domain.ModWin, "meta": domain.ModWin, "super": domain.ModWin, "cmd": domain.ModWin, "command": domain.ModWin,
}

// keyAliases は OpenAI が返すキー名（小文字）から domain の名前つきキーへの対応。ここに無い名前は ParseKeyCombo が検査する。
var keyAliases = map[string]string{
	"esc": "escape", "return": "enter", "del": "delete", "ins": "insert", "spacebar": "space", " ": "space",
	"arrowup": "up", "arrowdown": "down", "arrowleft": "left", "arrowright": "right",
	"pgup": "pageup", "pgdn": "pagedown", "pgdown": "pagedown",
}

// mapMods は click / drag などで押しながら動かす keys を修飾キーへ写す。修飾キー以外は未対応。
func mapMods(typ string, keys []string) ([]domain.Modifier, error) {
	var mods []domain.Modifier
	for _, k := range keys {
		m, ok := modifierAliases[strings.ToLower(k)]
		if !ok {
			return nil, unsupported(typ, "held key is not a modifier")
		}
		mods = append(mods, m)
	}
	return mods, nil
}

// mapKeys は keypress の keys（同時に押す組み合わせ）を domain.ParseKeyCombo で KeyCombo にする。
func mapKeys(keys []string) (domain.KeyCombo, error) {
	if len(keys) == 0 {
		return domain.KeyCombo{}, unsupported("keypress", "no keys")
	}
	tokens := make([]string, len(keys))
	for i, k := range keys {
		lower := strings.ToLower(k)
		if strings.Contains(k, "+") {
			return domain.KeyCombo{}, unsupported("keypress", "the + key cannot be sent")
		}
		if m, ok := modifierAliases[lower]; ok {
			tokens[i] = string(m)
		} else if n, ok := keyAliases[lower]; ok {
			tokens[i] = n
		} else {
			tokens[i] = lower
		}
	}
	combo, err := domain.ParseKeyCombo(strings.Join(tokens, "+"))
	if err != nil {
		return domain.KeyCombo{}, unsupported("keypress", "unknown key")
	}
	return combo, nil
}

// mapScroll はピクセルのスクロール量を、縦（先）→ 横の順でノッチ単位の Scroll にする（scrollPixelsPerNotch）。
// 両方 0 なら何も起こさない。
func mapScroll(at domain.Point, dx, dy int64) []domain.Op {
	var ops []domain.Op
	ops = appendScroll(ops, at, dy, domain.DirUp, domain.DirDown)
	ops = appendScroll(ops, at, dx, domain.DirLeft, domain.DirRight)
	return ops
}

func appendScroll(ops []domain.Op, at domain.Point, px int64, neg, pos domain.Direction) []domain.Op {
	dir := pos
	if px < 0 {
		dir, px = neg, -px
	}
	for notches := int((px + scrollPixelsPerNotch - 1) / scrollPixelsPerNotch); notches > 0; notches -= maxScrollNotches {
		ops = append(ops, domain.Scroll{At: at, Dir: dir, Amount: min(notches, maxScrollNotches), Monitor: domain.MonitorPrimary})
	}
	return ops
}
