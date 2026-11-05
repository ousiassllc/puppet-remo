package atoms_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/ousiassllc/puppet-remo/internal/web/atoms"
	"github.com/ousiassllc/puppet-remo/internal/web/molecules"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
	"github.com/ousiassllc/puppet-remo/internal/web/webtest"
)

func has(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q in:\n%s", s, out)
		}
	}
}

func hasNot(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(out, s) {
			t.Errorf("unexpected %q in:\n%s", s, out)
		}
	}
}

// デザインガイド（C2）: Tailwind の既定のパレット（slate・indigo など）と鮮やかな原色を出さない。トークンだけを使う。
func TestAtomsUseOnlyDesignTokens(t *testing.T) {
	outs := []string{
		webtest.Render(t, atoms.Button(atoms.ButtonProps{Label: "a", Variant: atoms.ButtonPrimary})),
		webtest.Render(t, atoms.Button(atoms.ButtonProps{Label: "a", Variant: atoms.ButtonDanger})),
		webtest.Render(t, atoms.Button(atoms.ButtonProps{Label: "a", Variant: atoms.ButtonSecondary})),
		webtest.Render(t, atoms.Button(atoms.ButtonProps{Label: "a", Variant: atoms.ButtonGhost})),
		webtest.Render(t, atoms.Input(atoms.InputProps{ID: "i", Name: "n", Invalid: true})),
		webtest.Render(t, atoms.Select("s", "s", nil)),
		webtest.Render(t, atoms.Checkbox("c", "c", true)),
		webtest.Render(t, atoms.Badge(atoms.BadgeOK, "ok")),
		webtest.Render(t, atoms.Alert(atoms.AlertProps{Tone: atoms.AlertDanger, Heading: "h", Message: "m"})),
		webtest.Render(t, atoms.Toast(view.Toast{Message: "m"})),
		webtest.Render(t, atoms.Spinner("sp")),
	}
	for _, out := range outs {
		hasNot(t, out, "slate-", "indigo-", "emerald-", "amber-", "rose-", "red-", "bg-white", "text-white", "bg-black", "ring-", "outline-none", "shadow-sm")
		webtest.AssertCSPCompliantHTML(t, out)
	}
}

// 状態はアイコン + 文言 + 状態色で伝える（色だけにしない）。中立のバッジ（種別・件数）にはアイコンを付けない。
func TestBadgeShowsAStateIconExceptNeutral(t *testing.T) {
	for tone, color := range map[atoms.BadgeTone]string{atoms.BadgeOK: "success", atoms.BadgeWarn: "warning", atoms.BadgeDanger: "danger"} {
		out := webtest.Render(t, atoms.Badge(tone, "状態"))
		has(t, out, "<svg", `aria-hidden="true"`, "text-"+color, "border-"+color+"/40", "状態")
		if !strings.HasSuffix(strings.TrimSpace(strings.TrimSuffix(out, "</span>")), "状態") {
			t.Errorf("the text must follow the icon: %s", out)
		}
	}
	neutral := webtest.Render(t, atoms.Badge(atoms.BadgeNeutral, "MCP"))
	hasNot(t, neutral, "<svg")
	has(t, neutral, "MCP")
}

// インラインアラート: 4 種（お知らせ・成功・注意・エラー）の状態色・アイコン・見出し・本文。閉じるボタンは Dismissible のときだけ。
func TestAlertTonesIconsAndDismiss(t *testing.T) {
	for tone, color := range map[atoms.AlertTone]string{atoms.AlertInfo: "info", atoms.AlertSuccess: "success", atoms.AlertWarning: "warning", atoms.AlertDanger: "danger"} {
		out := webtest.Render(t, atoms.Alert(atoms.AlertProps{Tone: tone, Code: "conflict", Heading: "見出し", Message: "本文"}))
		has(t, out, `role="alert"`, `data-error-code="conflict"`, `data-alert-tone="`+string(tone)+`"`, "border-"+color+"/40", "<svg", "見出し", "本文", "data-alert-message")
		hasNot(t, out, "data-clear")
	}
	out := webtest.Render(t, atoms.Alert(atoms.AlertProps{Tone: atoms.AlertDanger, Heading: "h", Message: "m", Dismissible: true}))
	has(t, out, `data-clear="#alert-root"`, `aria-label="閉じる"`, "data-ripple")
	hasNot(t, out, "data-error-code")
	// 雛形（app.ts が複製する）は role を持たない（ページに常に置くので、常時のエラーに見えないようにする）。
	tmpl := webtest.Render(t, atoms.Alert(atoms.AlertProps{Tone: atoms.AlertDanger, Heading: "h", Template: true}))
	hasNot(t, tmpl, `role="alert"`)
}

// 常設の注意書き: 4 種の状態色の枠・アイコン・本文（本文色 ink）。見出しと閉じるボタンは持たず、role などは attrs で渡す。
func TestNoticeTonesIconsAndBody(t *testing.T) {
	for tone, color := range map[atoms.AlertTone]string{atoms.AlertInfo: "info", atoms.AlertSuccess: "success", atoms.AlertWarning: "warning", atoms.AlertDanger: "danger"} {
		body := templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
			_, err := io.WriteString(w, "本文")
			return err
		})
		var sb strings.Builder
		if err := atoms.Notice(tone, atoms.Attrs{"role": "note", "data-notice": "n"}).Render(templ.WithChildren(context.Background(), body), &sb); err != nil {
			t.Fatal(err)
		}
		out := sb.String()
		has(t, out, `role="note"`, `data-notice="n"`, "border-"+color+"/40", "bg-"+color+"/10", "text-"+color, "<svg", "text-sm text-ink", ">本文</div>")
		hasNot(t, out, "<h3", "<button", "text-xs")
		webtest.AssertCSPCompliantHTML(t, out)
	}
}

// トースト: 完了の短い通知。ピル・インク地・role や閉じるボタンは持たない（3 秒で自動消去）。エラーの表示には使わない。
func TestToastIsAShortInkPill(t *testing.T) {
	out := webtest.Render(t, atoms.Toast(view.Toast{Message: "保存しました。"}))
	has(t, out, "data-toast", "rounded-full", "bg-ink", "text-bone", "min-h-10", "保存しました。")
	hasNot(t, out, "data-toast-dismiss", "data-toast-level", "data-error-code", "<button")
}

// ボタン: 4 種（主・サブ・ゴースト・危険）。SemiBold・disabled は opacity .4・高さ 40（小は 32）。押下のリップルの対象。
func TestButtonVariantsAndSizes(t *testing.T) {
	for v, want := range map[atoms.ButtonVariant]string{
		atoms.ButtonPrimary: "bg-accent text-bone", atoms.ButtonDanger: "bg-danger text-bone",
		atoms.ButtonSecondary: "border border-border", atoms.ButtonGhost: "bg-transparent text-ink",
	} {
		out := webtest.Render(t, atoms.Button(atoms.ButtonProps{Label: "操作", Variant: v}))
		has(t, out, want, "font-semibold", "disabled:opacity-40", "rounded-md", "h-10", "data-ripple", "motion-hover")
		hasNot(t, out, "font-medium", "disabled:opacity-50")
	}
	small := webtest.Render(t, atoms.Button(atoms.ButtonProps{Label: "操作", Small: true}))
	has(t, small, "h-8", "text-sm")
	hasNot(t, small, "h-10")
}

// フォームの入力: 高さ 40・--r-md・1px の --border・--surface 地・影なし。不正なときは --danger の枠。
func TestFieldsFollowTheFormSpec(t *testing.T) {
	ok := webtest.Render(t, atoms.Input(atoms.InputProps{ID: "i", Name: "n"}))
	has(t, ok, "h-10", "rounded-md", "border-border", "bg-surface", "text-base")
	hasNot(t, ok, "shadow", "border-danger")
	bad := webtest.Render(t, atoms.Input(atoms.InputProps{ID: "i", Name: "n", Invalid: true}))
	has(t, bad, "border-danger", `aria-invalid`)
	area := webtest.Render(t, atoms.Textarea(atoms.TextareaProps{ID: "t", Name: "t"}))
	has(t, area, "py-2", "rounded-md")
	hasNot(t, area, "h-10")
}

// アイコン: Iconoir の SVG（currentColor・線 1.5）。サイズは sm 16 / md 20 / lg 24 / xl 32。装飾なので読み上げない。
func TestIconIsAnIconoirSVG(t *testing.T) {
	for size, class := range map[atoms.IconSize]string{atoms.IconSM: "size-4", atoms.IconMD: "size-5", atoms.IconLG: "size-6", atoms.IconXL: "size-8"} {
		out := webtest.Render(t, atoms.Icon(atoms.IconXmark, size))
		has(t, out, "<svg", `class="shrink-0 `+class+`"`, `stroke="currentColor"`, `stroke-width="1.5"`, `viewBox="0 0 24 24"`, `aria-hidden="true"`, "<path d=")
		webtest.AssertCSPCompliantHTML(t, out)
	}
	for _, name := range []atoms.IconName{atoms.IconXmark, atoms.IconArrowLeft, atoms.IconArrowRight, atoms.IconCheckCircle, atoms.IconWarningTriangle, atoms.IconInfoCircle, atoms.IconXmarkCircle, atoms.IconEmptyPage} {
		has(t, webtest.Render(t, atoms.Icon(name, atoms.IconSM)), "<path d=")
	}
}

// スピナー: 20px の回る円 + 文言（処理中を文言でも伝える）。
func TestSpinnerHasTheCircleAndTheText(t *testing.T) {
	out := webtest.Render(t, atoms.Spinner("sp"))
	has(t, out, `id="sp"`, `role="status"`, "htmx-indicator", `class="spinner"`, "処理中…")
}

// 空状態: 破線・rounded-lg・padding 48/24・アイコン + 文言。data-empty に名前を持つ。
func TestEmptyState(t *testing.T) {
	out := webtest.Render(t, molecules.EmptyState("clients"))
	has(t, out, `data-empty="clients"`, "border-dashed", "rounded-lg", "px-6 py-12", "<svg")
}

// 確認モーダル: --z-modal・rounded-xl・影はモーダルだけ。外側（スクラム）のクリックは app.ts が閉じる（#modal の id が目印）。
func TestModalFollowsTheOverlaySpec(t *testing.T) {
	out := webtest.Render(t, molecules.Modal(view.ConfirmModal{Title: "削除しますか？", Message: "元に戻せません。", Label: "削除する", ConfirmURL: "/x", Method: "delete", Target: "#t", Swap: "outerHTML"}))
	has(t, out, `id="modal"`, `role="dialog"`, `aria-modal="true"`, "z-(--z-modal)", "bg-scrim", "rounded-xl", "shadow-modal", "motion-fade-in", "motion-rise-in")
	hasNot(t, out, "shadow-xl", "z-40")
}
