package molecules_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/web/molecules"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

// T3: 見出しの「段」（ガイド「アプリ内の見出しの段」・設定画面のパーツ）。見た目はタグではなくクラスで決める:
// モーダルの題・カードの題 = lg 18 SemiBold。本文と同じ text-base や、タグの既定サイズ（text-2xl）にしない。
func TestHeadingsFollowTheGuideLadder(t *testing.T) {
	type rung struct{ size, weight string }
	ladder := func(class string) rung {
		var r rung
		for _, c := range strings.Fields(class) {
			switch c {
			case "text-xs", "text-sm", "text-base", "text-lg", "text-xl", "text-2xl", "text-3xl":
				r.size = strings.TrimPrefix(c, "text-")
			case "font-normal", "font-medium", "font-semibold", "font-bold":
				r.weight = strings.TrimPrefix(c, "font-")
			}
		}
		return r
	}
	card := webtest.Render(t, molecules.Card("見出し"))
	modal := webtest.Render(t, molecules.Modal(view.ConfirmModal{Title: "確認", Message: "本当に？", Label: "実行", Method: "post"}))
	for name, tc := range map[string]struct {
		out   string
		tags  []string
		title string
		want  rung
	}{
		"card title":  {card, []string{"h2"}, "見出し", rung{"lg", "semibold"}},
		"modal title": {modal, []string{"h2"}, "確認", rung{"lg", "semibold"}},
	} {
		var found *webtest.Element
		for _, e := range webtest.Elements(t, tc.out, tc.tags...) {
			if e.Text == tc.title {
				found = &e
				break
			}
		}
		if found == nil {
			t.Errorf("%s: no %v titled %q in:\n%s", name, tc.tags, tc.title, tc.out)
			continue
		}
		if got := ladder(found.Class); got != tc.want {
			t.Errorf("%s: %s is %+v, want %+v (class %q)", name, found.Tag, got, tc.want, found.Class)
		}
	}
}

// 見出し付きカード（ガイド「設定画面のパーツ」1）: 題の下に --border の線、本文 padding --s-6、影なし、地 --surface・角丸 --r-lg。
func TestCardFollowsTheGuide(t *testing.T) {
	card := webtest.Render(t, molecules.Card("見出し"))
	title := webtest.Elements(t, card, "h2")[0]
	for _, want := range []string{"border-b", "border-border", "px-6", "py-4", "leading-[1.4]"} {
		if !slices.Contains(strings.Fields(title.Class), want) {
			t.Errorf("card title lacks %q: %q", want, title.Class)
		}
	}
	for _, want := range []string{"rounded-lg", "bg-surface", `class="p-6"`} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	if strings.Contains(card, "shadow") {
		t.Errorf("card must have no shadow:\n%s", card)
	}
	if untitled := webtest.Render(t, molecules.Card("")); strings.Contains(untitled, "<h2") {
		t.Errorf("an untitled card must not render a heading:\n%s", untitled)
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
