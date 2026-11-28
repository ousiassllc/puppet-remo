package molecules_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/web/atoms"
	"github.com/ousiassllc/puppet-remo/internal/web/molecules"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

// T3: 見出しの「段」（ガイド「アプリ内の見出しの段」・設定画面のパーツ）。見た目はタグではなくクラスで決める:
// カードの題 = section 16 SemiBold、モーダルの題 = lg 18 SemiBold（ダイアログだけ）、項目の見出し（h3）= item 15 SemiBold（クライアント名・アプリ名もこの段。#166。
// organisms_test の TestItemNameHeadingsAreH3Item）、インラインアラートの題 = sm 14 SemiBold。本文と同じ段の SemiBold を題にしない（アラートの題を 15・16 にしない）。
func TestHeadingsFollowTheGuideLadder(t *testing.T) {
	type rung struct{ size, weight string }
	ladder := func(class string) rung {
		var r rung
		for _, c := range strings.Fields(class) {
			switch c {
			case "text-sm", "text-desc", "text-item", "text-section", "text-tip", "text-base", "text-lg", "text-page-title", "text-2xs":
				r.size = strings.TrimPrefix(c, "text-")
			case "font-normal", "font-medium", "font-semibold", "font-bold":
				r.weight = strings.TrimPrefix(c, "font-")
			}
		}
		return r
	}
	card := webtest.Render(t, molecules.Card("見出し"))
	modal := webtest.Render(t, molecules.Modal(view.ConfirmModal{Title: "確認", Message: "本当に？", Label: "実行", Method: "post"}))
	alert := webtest.Render(t, atoms.Alert(atoms.AlertProps{Tone: atoms.AlertDanger, Heading: "失敗", Message: "m"}))
	reveal := webtest.Render(t, molecules.TokenReveal(view.TokenReveal{Label: "ci", Plaintext: "tok", ExpiresAt: "2026-12-01"}))
	for name, tc := range map[string]struct {
		out   string
		tags  []string
		title string
		want  rung
	}{
		"card title":   {card, []string{"h2"}, "見出し", rung{"section", "semibold"}},
		"modal title":  {modal, []string{"h2"}, "確認", rung{"lg", "semibold"}},
		"alert title":  {alert, []string{"strong"}, "失敗", rung{"sm", "semibold"}},
		"token reveal": {reveal, []string{"h3"}, "「ci」のトークンを発行しました", rung{"sm", "semibold"}},
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

// 見出し付きカード（ガイド「設定画面のパーツ」1）: 題は text-section font-semibold で題の下に線を引かない。余白は --pad-card、影なし、地 --surface・角丸 --r-card・枠は .card-edge。
func TestCardFollowsTheGuide(t *testing.T) {
	card := webtest.Render(t, molecules.Card("見出し"))
	title := webtest.Elements(t, card, "h2")[0]
	// 題は text-section font-semibold（16・600）。題の下に線は引かない（border-b なし）。
	for _, want := range []string{"text-section", "font-semibold", "text-ink", "px-(--pad-card)", "pt-(--pad-card)"} {
		if !slices.Contains(strings.Fields(title.Class), want) {
			t.Errorf("card title lacks %q: %q", want, title.Class)
		}
	}
	if strings.Contains(title.Class, "border") {
		t.Errorf("card title must have no separator line: %q", title.Class)
	}
	for _, want := range []string{`class="rounded-card card-edge bg-surface"`, `class="px-(--pad-card) pb-(--pad-card) pt-4"`} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	if strings.Contains(card, "shadow") || strings.Contains(card, "border-border") || strings.Contains(card, "rounded-lg") {
		t.Errorf("card must have no shadow, border-border or rounded-lg (the edge is .card-edge):\n%s", card)
	}
	untitled := webtest.Render(t, molecules.Card(""))
	if strings.Contains(untitled, "<h2") {
		t.Errorf("an untitled card must not render a heading:\n%s", untitled)
	}
	if !strings.Contains(untitled, `class="p-(--pad-card)"`) {
		t.Errorf("an untitled card body must use p-(--pad-card):\n%s", untitled)
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

// T2: 本文中の強調（<strong>）は app.css の base が下線で表す。題（見出し役）の <strong> には下線を付けない（リンクに見えるため）。
// 題はクラス no-underline で外す（utilities は base に勝つ）。実描画は remotedemo + Playwright の getComputedStyle で確かめた。
func TestAlertTitleHasNoUnderline(t *testing.T) {
	alert := webtest.Render(t, atoms.Alert(atoms.AlertProps{Tone: atoms.AlertDanger, Heading: "失敗", Message: "m"}))
	for _, e := range webtest.Elements(t, alert, "strong") {
		if !slices.Contains(strings.Fields(e.Class), "no-underline") {
			t.Errorf("alert title <strong> lacks no-underline: %q", e.Class)
		}
	}
	if len(webtest.Elements(t, alert, "strong")) == 0 {
		t.Fatalf("no <strong> title in:\n%s", alert)
	}
}

// T5: トークン発行直後の通知の有効期限は、値だけ Mono（周りの文は Regular）。
func TestTokenRevealExpiryIsMono(t *testing.T) {
	reveal := webtest.Render(t, molecules.TokenReveal(view.TokenReveal{Label: "ci", Plaintext: "tok", ExpiresAt: "2026-12-01"}))
	if !strings.Contains(reveal, `（有効期限: <span class="font-mono">2026-12-01</span>）`) {
		t.Errorf("expiry is not a Mono span:\n%s", reveal)
	}
}
