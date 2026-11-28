package openai

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestParseDesktopCallValid(t *testing.T) {
	pt := func(x, y int) *domain.Point { return &domain.Point{X: x, Y: y} }
	cases := []struct {
		name string
		fn   string
		args string
		want domain.Op
	}{
		{"screenshot は nil の Op", "screenshot", `{}`, nil},
		{"screenshot の空の引数", "screenshot", ``, nil},
		{"mouse_move", "mouse_move", `{"x":10,"y":20}`, domain.MouseMove{At: domain.Point{X: 10, Y: 20}, Monitor: domain.MonitorPrimary}},
		{"left_click 座標つき", "left_click", `{"x":1,"y":2}`, domain.Click{At: pt(1, 2), Button: domain.ButtonLeft, Count: 1, Monitor: domain.MonitorPrimary}},
		{"left_click 座標なしは現在位置", "left_click", `{}`, domain.Click{Button: domain.ButtonLeft, Count: 1, Monitor: domain.MonitorPrimary}},
		{"right_click", "right_click", `{"x":0,"y":0}`, domain.Click{At: pt(0, 0), Button: domain.ButtonRight, Count: 1, Monitor: domain.MonitorPrimary}},
		{"middle_click", "middle_click", `{}`, domain.Click{Button: domain.ButtonMiddle, Count: 1, Monitor: domain.MonitorPrimary}},
		{"double_click", "double_click", `{"x":5,"y":6}`, domain.Click{At: pt(5, 6), Button: domain.ButtonLeft, Count: 2, Monitor: domain.MonitorPrimary}},
		{"click の修飾キー（大文字小文字・空白を許す）", "left_click", `{"x":1,"y":2,"modifiers":["Ctrl"," shift "]}`,
			domain.Click{At: pt(1, 2), Button: domain.ButtonLeft, Count: 1, Mods: []domain.Modifier{"ctrl", "shift"}, Monitor: domain.MonitorPrimary}},
		{"left_click_drag", "left_click_drag", `{"from":{"x":1,"y":2},"to":{"x":3,"y":4},"modifiers":["alt"]}`,
			domain.Drag{From: domain.Point{X: 1, Y: 2}, To: domain.Point{X: 3, Y: 4}, Mods: []domain.Modifier{"alt"}, Monitor: domain.MonitorPrimary}},
		{"scroll", "scroll", `{"x":9,"y":8,"direction":"down","amount":3}`,
			domain.Scroll{At: domain.Point{X: 9, Y: 8}, Dir: "down", Amount: 3, Monitor: domain.MonitorPrimary}},
		{"scroll の上限", "scroll", `{"x":0,"y":0,"direction":"left","amount":50}`,
			domain.Scroll{At: domain.Point{}, Dir: "left", Amount: 50, Monitor: domain.MonitorPrimary}},
		{"key", "key", `{"keys":"ctrl+shift+t"}`, domain.KeyPress{Keys: domain.KeyCombo{Mods: []domain.Modifier{"ctrl", "shift"}, Key: "t"}}},
		{"key 単独", "key", `{"keys":"enter"}`, domain.KeyPress{Keys: domain.KeyCombo{Key: "enter"}}},
		{"type", "type", `{"text":"こんにちは"}`, domain.TypeText{Text: "こんにちは"}},
		{"type の上限（10000 文字）", "type", `{"text":"` + strings.Repeat("あ", 10_000) + `"}`, domain.TypeText{Text: strings.Repeat("あ", 10_000)}},
		{"wait", "wait", `{"ms":250}`, domain.Wait{Duration: 250 * time.Millisecond}},
		{"wait の 0", "wait", `{"ms":0}`, domain.Wait{}},
		{"wait の上限", "wait", `{"ms":10000}`, domain.Wait{Duration: 10 * time.Second}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDesktopCall(tc.fn, []byte(tc.args))
			if err != nil {
				t.Fatalf("ParseDesktopCall: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("op = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestParseDesktopCallRejects(t *testing.T) {
	cases := []struct {
		name string
		fn   string
		args string
	}{
		{"未知の関数", "format_disk", `{}`},
		{"desktop. 付きの名前", "desktop.left_click", `{"x":1,"y":2}`},
		{"空の名前", "", `{}`},
		{"JSON でない", "left_click", `not json`},
		{"オブジェクトでない", "left_click", `[1,2]`},
		{"余分な値", "left_click", `{"x":1,"y":2} {}`},
		{"screenshot の未知のキー", "screenshot", `{"monitor":1}`},
		{"mouse_move の x のみ", "mouse_move", `{"x":1}`},
		{"mouse_move の y のみ", "mouse_move", `{"y":1}`},
		{"mouse_move の引数なし", "mouse_move", `{}`},
		{"mouse_move の型違い", "mouse_move", `{"x":"1","y":2}`},
		{"mouse_move の小数", "mouse_move", `{"x":1.5,"y":2}`},
		{"mouse_move の未知のキー", "mouse_move", `{"x":1,"y":2,"z":3}`},
		{"left_click の x のみ", "left_click", `{"x":1}`},
		{"left_click の y のみ", "left_click", `{"y":1}`},
		{"left_click の未知のキー", "left_click", `{"x":1,"y":2,"button":"left"}`},
		{"left_click の未知の修飾キー", "left_click", `{"modifiers":["super"]}`},
		{"left_click の修飾キーの型", "left_click", `{"modifiers":"ctrl"}`},
		{"double_click の x のみ", "double_click", `{"x":1}`},
		{"drag の from なし", "left_click_drag", `{"to":{"x":1,"y":2}}`},
		{"drag の to の y なし", "left_click_drag", `{"from":{"x":1,"y":2},"to":{"x":1}}`},
		{"drag の from の x なし", "left_click_drag", `{"from":{"y":2},"to":{"x":1,"y":2}}`},
		{"drag の未知のキー", "left_click_drag", `{"from":{"x":1,"y":2},"to":{"x":1,"y":2},"path":[]}`},
		{"drag の点の未知のキー", "left_click_drag", `{"from":{"x":1,"y":2,"z":0},"to":{"x":1,"y":2}}`},
		{"scroll の amount が 0", "scroll", `{"x":1,"y":1,"direction":"up","amount":0}`},
		{"scroll の amount が 51", "scroll", `{"x":1,"y":1,"direction":"up","amount":51}`},
		{"scroll の amount が負", "scroll", `{"x":1,"y":1,"direction":"up","amount":-1}`},
		{"scroll の amount なし", "scroll", `{"x":1,"y":1,"direction":"up"}`},
		{"scroll の方向が未知", "scroll", `{"x":1,"y":1,"direction":"diagonal","amount":1}`},
		{"scroll の方向なし", "scroll", `{"x":1,"y":1,"amount":1}`},
		{"scroll の x のみ", "scroll", `{"x":1,"direction":"up","amount":1}`},
		{"key の keys なし", "key", `{}`},
		{"key の未知のキー名", "key", `{"keys":"ctrl+nonexistentkey"}`},
		{"key の未知の修飾キー", "key", `{"keys":"super+a"}`},
		{"key の型違い", "key", `{"keys":["a"]}`},
		{"type の text なし", "type", `{}`},
		{"type が 10001 文字", "type", `{"text":"` + strings.Repeat("あ", 10_001) + `"}`},
		{"type の型違い", "type", `{"text":1}`},
		{"wait の ms が負", "wait", `{"ms":-1}`},
		{"wait の ms が 10001", "wait", `{"ms":10001}`},
		{"wait の ms なし", "wait", `{}`},
		{"wait の未知のキー", "wait", `{"ms":1,"seconds":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDesktopCall(tc.fn, []byte(tc.args))
			if !errors.Is(err, domain.ErrUnsupportedAction) {
				t.Fatalf("err = %v, want ErrUnsupportedAction", err)
			}
			if got != nil {
				t.Errorf("op = %#v, want nil on error", got)
			}
		})
	}
}

// エラーには引数の値（入力した文字列など）を含めない。
func TestParseDesktopCallErrorHasNoArgumentValues(t *testing.T) {
	secret := "hunter2-secret-text"
	for _, tc := range []struct{ fn, args string }{
		{"type", `{"text":"` + secret + `","extra":1}`},
		{"type", `{"text":"` + strings.Repeat(secret, 1000) + `"}`},
		{"key", `{"keys":"ctrl+` + secret + `"}`},
		{"left_click", `{"modifiers":["` + secret + `"]}`},
		{"scroll", `{"x":1,"y":1,"direction":"` + secret + `","amount":1}`},
	} {
		_, err := ParseDesktopCall(tc.fn, []byte(tc.args))
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("%s: err = %v, want an error without the argument value", tc.fn, err)
		}
	}
}

// DesktopTools の定義と ParseDesktopCall の名前が食い違わない（定義した関数は、最小の正しい引数で読める）。
func TestDesktopToolsMatchParser(t *testing.T) {
	minimal := map[string]string{
		"screenshot": `{}`, "mouse_move": `{"x":1,"y":1}`, "left_click": `{}`, "right_click": `{}`, "middle_click": `{}`, "double_click": `{}`,
		"left_click_drag": `{"from":{"x":1,"y":1},"to":{"x":2,"y":2}}`, "scroll": `{"x":1,"y":1,"direction":"up","amount":1}`,
		"key": `{"keys":"a"}`, "type": `{"text":"a"}`, "wait": `{"ms":1}`,
	}
	if len(minimal) != len(DesktopTools) {
		t.Fatalf("minimal args cover %d functions, DesktopTools has %d", len(minimal), len(DesktopTools))
	}
	for _, tool := range DesktopTools {
		args, ok := minimal[tool.Name]
		if !ok {
			t.Errorf("no minimal args for %s", tool.Name)
			continue
		}
		if _, err := ParseDesktopCall(tool.Name, []byte(args)); err != nil {
			t.Errorf("%s: %v", tool.Name, err)
		}
		if tool.Parameters["type"] != "object" {
			t.Errorf("%s: parameters type = %v, want object", tool.Name, tool.Parameters["type"])
		}
	}
}
