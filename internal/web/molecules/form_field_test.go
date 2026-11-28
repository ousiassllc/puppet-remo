package molecules_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/ousiassllc/puppet-remo/internal/web/atoms"
	"github.com/ousiassllc/puppet-remo/internal/web/molecules"
)

// フォームの間隔（S2）: ラベル→入力は 6px（--gap-label。mb-(--gap-label)）、入力→補足・エラーは 4px（mt-1）。縦の間隔を一律の space-y にしない。
func TestFormFieldSpacing(t *testing.T) {
	input := templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, `<input id="f">`)
		return err
	})
	var sb strings.Builder
	if err := molecules.FormField("f", "名前", "ヒント", "エラー").Render(templ.WithChildren(context.Background(), input), &sb); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{`class="mb-(--gap-label) block text-sm text-ink"`, `class="mt-1 text-desc text-mute"`, `motion-rise-in mt-1 text-sm text-danger`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "space-y-") {
		t.Errorf("form field must not use a uniform space-y: %s", out)
	}
}

// C6・T3: 13px の --mute（text-desc）は 1 文の補足だけ。複数文の説明は本文色の 14px にする。
func TestFormFieldHintFollowsLength(t *testing.T) {
	render := func(hint string) string {
		var sb strings.Builder
		if err := molecules.FormField("f", "名前", hint, "").Render(context.Background(), &sb); err != nil {
			t.Fatal(err)
		}
		return sb.String()
	}
	for _, hint := range []string{"既定は 3。", "補足の 1 文", "補足の 1 文。"} {
		if out := render(hint); !strings.Contains(out, `class="mt-1 text-desc text-mute"`) {
			t.Errorf("one-sentence hint %q should be text-desc text-mute: %s", hint, out)
		}
	}
	for _, hint := range []string{"1〜10。既定は 3。", "1 文目。2 文目。", "1 文目。2 文目", "一。二。三。"} {
		out := render(hint)
		if !strings.Contains(out, `class="mt-1 text-sm text-ink"`) || strings.Contains(out, "text-mute") {
			t.Errorf("multi-sentence hint %q should be text-sm text-ink: %s", hint, out)
		}
	}
}

// WCAG 1.3.1・3.3.1: ヒントとエラー文は id を持ち、同じ id の入力欄が aria-describedby で指す。ヒント・エラーのない欄は何も付けない。
func TestFormFieldDescribesTheControl(t *testing.T) {
	render := func(hint, errMsg string, control templ.Component) string {
		var sb strings.Builder
		ctx := templ.WithChildren(context.Background(), control)
		if err := molecules.FormField("f", "名前", hint, errMsg).Render(ctx, &sb); err != nil {
			t.Fatal(err)
		}
		return sb.String()
	}
	input := atoms.Input(atoms.InputProps{ID: "f", Name: "f", Invalid: true})
	both := render("ヒント", "エラー", input)
	for _, want := range []string{`aria-describedby="f-hint f-error"`, `<p id="f-hint"`, `role="alert" data-field-error="f" id="f-error"`} {
		if !strings.Contains(both, want) {
			t.Errorf("missing %q in:\n%s", want, both)
		}
	}
	if out := render("ヒント", "", input); !strings.Contains(out, `aria-describedby="f-hint"`) || strings.Contains(out, "f-error") {
		t.Errorf("hint only: %s", out)
	}
	if out := render("", "エラー", input); !strings.Contains(out, `aria-describedby="f-error"`) || strings.Contains(out, "f-hint") {
		t.Errorf("error only: %s", out)
	}
	if out := render("", "", input); strings.Contains(out, "aria-describedby") {
		t.Errorf("no hint or error must not describe the control: %s", out)
	}
	// 入力欄の atom 全部が受け取る。id が違う欄（<output> のすぐ隣の別の入力など）には付けない。
	for name, c := range map[string]templ.Component{
		"textarea": atoms.Textarea(atoms.TextareaProps{ID: "f", Name: "f"}),
		"range":    atoms.Range(atoms.RangeProps{ID: "f", Name: "f"}),
		"secret":   atoms.SecretInput("f", "f", false),
		"select":   atoms.Select("f", "f", nil),
		"selectw":  atoms.SelectWith(atoms.SelectProps{ID: "f", Name: "f", Invalid: true}),
	} {
		if out := render("ヒント", "エラー", c); !strings.Contains(out, `aria-describedby="f-hint f-error"`) {
			t.Errorf("%s must be described: %s", name, out)
		}
	}
	if out := render("ヒント", "エラー", atoms.Input(atoms.InputProps{ID: "other", Name: "o"})); strings.Contains(out, "aria-describedby") {
		t.Errorf("a control with another id must not be described: %s", out)
	}
}
