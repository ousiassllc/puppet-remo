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
