package molecules_test

import (
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/web/molecules"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

// T2・T3: 発行の通知の見出しは見出し要素（h3）で、コピーの結果・失敗の案内（次の操作を伝える 2 文）は 12px にしない。
func TestTokenRevealHeadingAndCopyStatus(t *testing.T) {
	out := webtest.Render(t, molecules.TokenReveal(view.TokenReveal{Label: "ci", Plaintext: "tok", ExpiresAt: "2026-12-01"}))
	for _, want := range []string{`<h3 class="text-sm font-semibold">「ci」のトークンを発行しました</h3>`, `id="copy-status" class="text-sm text-ink"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `<p class="font-semibold">`) || strings.Contains(out, `id="copy-status" class="text-xs`) {
		t.Errorf("heading must be h3 and copy status must not be text-xs:\n%s", out)
	}
}
