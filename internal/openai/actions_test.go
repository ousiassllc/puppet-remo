package openai

import (
	"errors"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/responses"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func act(t *testing.T, js string) responses.ComputerActionUnion {
	t.Helper()
	var a responses.ComputerActionUnion
	if err := a.UnmarshalJSON([]byte(js)); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMapActions(t *testing.T) {
	pt := func(x, y int) *domain.Point { return &domain.Point{X: x, Y: y} }
	mon := domain.MonitorPrimary
	tests := []struct {
		name string
		in   string
		want []domain.Op
	}{
		{"left click", `{"type":"click","button":"left","x":1,"y":2}`,
			[]domain.Op{domain.Click{At: pt(1, 2), Button: domain.ButtonLeft, Count: 1, Monitor: mon}}},
		{"right click", `{"type":"click","button":"right","x":1,"y":2}`,
			[]domain.Op{domain.Click{At: pt(1, 2), Button: domain.ButtonRight, Count: 1, Monitor: mon}}},
		{"wheel click is middle", `{"type":"click","button":"wheel","x":1,"y":2}`,
			[]domain.Op{domain.Click{At: pt(1, 2), Button: domain.ButtonMiddle, Count: 1, Monitor: mon}}},
		{"click with modifiers", `{"type":"click","button":"left","x":1,"y":2,"keys":["CTRL","Shift"]}`,
			[]domain.Op{domain.Click{At: pt(1, 2), Button: domain.ButtonLeft, Count: 1, Mods: []domain.Modifier{domain.ModCtrl, domain.ModShift}, Monitor: mon}}},
		{"double click", `{"type":"double_click","x":3,"y":4,"keys":[]}`,
			[]domain.Op{domain.Click{At: pt(3, 4), Button: domain.ButtonLeft, Count: 2, Monitor: mon}}},
		{"drag uses first and last point", `{"type":"drag","path":[{"x":1,"y":2},{"x":50,"y":50},{"x":7,"y":8}]}`,
			[]domain.Op{domain.Drag{From: domain.Point{X: 1, Y: 2}, To: domain.Point{X: 7, Y: 8}, Monitor: mon}}},
		{"move", `{"type":"move","x":9,"y":10}`,
			[]domain.Op{domain.MouseMove{At: domain.Point{X: 9, Y: 10}, Monitor: mon}}},
		{"scroll down", `{"type":"scroll","x":5,"y":6,"scroll_x":0,"scroll_y":300}`,
			[]domain.Op{domain.Scroll{At: domain.Point{X: 5, Y: 6}, Dir: domain.DirDown, Amount: 3, Monitor: mon}}},
		{"scroll up rounds up", `{"type":"scroll","x":5,"y":6,"scroll_x":0,"scroll_y":-1}`,
			[]domain.Op{domain.Scroll{At: domain.Point{X: 5, Y: 6}, Dir: domain.DirUp, Amount: 1, Monitor: mon}}},
		{"scroll both axes", `{"type":"scroll","x":5,"y":6,"scroll_x":-200,"scroll_y":100}`, []domain.Op{
			domain.Scroll{At: domain.Point{X: 5, Y: 6}, Dir: domain.DirDown, Amount: 1, Monitor: mon},
			domain.Scroll{At: domain.Point{X: 5, Y: 6}, Dir: domain.DirLeft, Amount: 2, Monitor: mon},
		}},
		{"scroll beyond 50 notches is split", `{"type":"scroll","x":0,"y":0,"scroll_x":0,"scroll_y":12000}`, []domain.Op{
			domain.Scroll{Dir: domain.DirDown, Amount: 50, Monitor: mon},
			domain.Scroll{Dir: domain.DirDown, Amount: 50, Monitor: mon},
			domain.Scroll{Dir: domain.DirDown, Amount: 20, Monitor: mon},
		}},
		{"scroll zero does nothing", `{"type":"scroll","x":0,"y":0,"scroll_x":0,"scroll_y":0}`, nil},
		{"keypress combo", `{"type":"keypress","keys":["CTRL","SHIFT","T"]}`,
			[]domain.Op{domain.KeyPress{Keys: domain.KeyCombo{Mods: []domain.Modifier{domain.ModCtrl, domain.ModShift}, Key: "t"}}}},
		{"keypress aliases", `{"type":"keypress","keys":["ESC"]}`,
			[]domain.Op{domain.KeyPress{Keys: domain.KeyCombo{Key: "escape"}}}},
		{"keypress enter", `{"type":"keypress","keys":["ENTER"]}`,
			[]domain.Op{domain.KeyPress{Keys: domain.KeyCombo{Key: "enter"}}}},
		{"keypress arrow", `{"type":"keypress","keys":["ArrowDown"]}`,
			[]domain.Op{domain.KeyPress{Keys: domain.KeyCombo{Key: "down"}}}},
		{"type", `{"type":"type","text":"héllo"}`, []domain.Op{domain.TypeText{Text: "héllo"}}},
		{"wait", `{"type":"wait"}`, []domain.Op{domain.Wait{Duration: 2 * time.Second}}},
		{"screenshot is dropped", `{"type":"screenshot"}`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mapActions([]responses.ComputerActionUnion{act(t, tc.in)})
			if err != nil {
				t.Fatal(err)
			}
			assertOps(t, got, tc.want)
		})
	}
}

func TestMapActionsUnsupported(t *testing.T) {
	for _, js := range []string{
		`{"type":"teleport"}`,
		`{"type":"click","button":"back","x":1,"y":1}`,
		`{"type":"click","button":"forward","x":1,"y":1}`,
		`{"type":"double_click","x":1,"y":1,"keys":["a"]}`,
		`{"type":"drag","path":[]}`,
		`{"type":"keypress","keys":[]}`,
		`{"type":"keypress","keys":["+"]}`,
		`{"type":"keypress","keys":["SECRET-TEXT-NOSUCH"]}`,
	} {
		_, err := mapActions([]responses.ComputerActionUnion{act(t, js)})
		if !errors.Is(err, domain.ErrUnsupportedAction) {
			t.Errorf("%s: err = %v, want ErrUnsupportedAction", js, err)
		}
	}
}

func TestMapActionsKeepsOrder(t *testing.T) {
	got, err := mapActions([]responses.ComputerActionUnion{
		act(t, `{"type":"move","x":1,"y":1}`),
		act(t, `{"type":"screenshot"}`),
		act(t, `{"type":"type","text":"a"}`),
		act(t, `{"type":"wait"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("ops = %d, want 3", len(got))
	}
	if _, ok := got[0].(domain.MouseMove); !ok {
		t.Errorf("op 0 = %T", got[0])
	}
	if _, ok := got[1].(domain.TypeText); !ok {
		t.Errorf("op 1 = %T", got[1])
	}
	if _, ok := got[2].(domain.Wait); !ok {
		t.Errorf("op 2 = %T", got[2])
	}
}
