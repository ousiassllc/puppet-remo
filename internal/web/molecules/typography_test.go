package molecules_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/web/molecules"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

// T3: カードの見出し（h2）はスケールの 2xl（32）、狭い面のモーダルの見出しは lg（18）。本文と同じ text-base にしない。
func TestHeadingsFollowTheSizeScale(t *testing.T) {
	var sb strings.Builder
	if err := molecules.Card("見出し").Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	card := sb.String()
	modal := webtest.Render(t, molecules.Modal(view.ConfirmModal{Title: "確認", Message: "本当に？", Label: "実行", Method: "post"}))
	for name, tc := range map[string]struct{ out, want string }{
		"card h2":  {card, `<h2 class="border-b border-border px-6 py-3 text-2xl font-semibold text-ink">`},
		"modal h2": {modal, `<h2 id="modal-title" class="text-lg font-semibold text-ink">`},
	} {
		if !strings.Contains(tc.out, tc.want) {
			t.Errorf("%s: missing %q in:\n%s", name, tc.want, tc.out)
		}
	}
}

// T5: 発行直後のトークンの平文は、文字の判読（0 と O、l と 1）が要るので JetBrains Mono で出す。
func TestTokenRevealPlaintextIsMono(t *testing.T) {
	out := webtest.Render(t, molecules.TokenReveal(view.TokenReveal{Label: "ci", Plaintext: "tok", ExpiresAt: "2026-12-01"}))
	input := regexp.MustCompile(`<input[^>]*id="issued-token"[^>]*>`).FindString(out)
	if !strings.Contains(input, "font-mono") {
		t.Errorf("token plaintext input must use font-mono: %q", input)
	}
}

// S5: 確認モーダルのスクラムにもガター（モバイル 16・デスクトップ 24）を持たせ、狭い幅でパネルが画面の端に接しない。
func TestModalScrimHasGutter(t *testing.T) {
	out := webtest.Render(t, molecules.Modal(view.ConfirmModal{Title: "確認", Message: "本当に？", Label: "実行", Method: "post"}))
	if !strings.Contains(out, `bg-scrim p-4 md:p-6"`) {
		t.Errorf("modal scrim lacks the p-4 md:p-6 gutter:\n%s", out)
	}
}
