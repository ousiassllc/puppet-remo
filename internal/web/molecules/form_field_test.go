package molecules_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/ousiassllc/puppet-remo/internal/web/molecules"
)

// フォームの間隔（S2）: ラベル→入力は 8px（mb-2）、入力→補足・エラーは 4px（mt-1）。縦の間隔を一律の space-y にしない。
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
	for _, want := range []string{`class="mb-2 block text-sm text-ink"`, `class="mt-1 text-xs text-mute"`, `motion-rise-in mt-1 text-sm text-danger`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "space-y-") {
		t.Errorf("form field must not use a uniform space-y: %s", out)
	}
}

// C6・T3: 12px の --mute は 1 文の補足だけ。複数文の説明は本文色の 14px にする。
func TestFormFieldHintFollowsLength(t *testing.T) {
	render := func(hint string) string {
		var sb strings.Builder
		if err := molecules.FormField("f", "名前", hint, "").Render(context.Background(), &sb); err != nil {
			t.Fatal(err)
		}
		return sb.String()
	}
	for _, hint := range []string{"既定は 3。", "補足の 1 文", "補足の 1 文。"} {
		if out := render(hint); !strings.Contains(out, `class="mt-1 text-xs text-mute"`) {
			t.Errorf("one-sentence hint %q should be text-xs text-mute: %s", hint, out)
		}
	}
	for _, hint := range []string{"1〜10。既定は 3。", "1 文目。2 文目。", "1 文目。2 文目", "一。二。三。"} {
		out := render(hint)
		if !strings.Contains(out, `class="mt-1 text-sm text-ink"`) || strings.Contains(out, "text-mute") {
			t.Errorf("multi-sentence hint %q should be text-sm text-ink: %s", hint, out)
		}
	}
}
