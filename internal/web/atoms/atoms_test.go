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
		has(t, out, "<svg", `aria-hidden="true"`, "text-"+color, "border-"+color+"-line", "状態")
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
		has(t, out, `role="alert"`, `data-error-code="conflict"`, `data-alert-tone="`+string(tone)+`"`, "border-"+color+"-line", "<svg", "見出し", "本文", `<p class="mt-2 text-ink" data-alert-message>`)
		hasNot(t, out, "data-clear")
		// 通知の帯は --r-md（8）。--r-lg（12）はカードで、同じ部品群の Notice と角を揃える（S1）。
		has(t, out, "rounded-md")
		hasNot(t, out, "rounded-lg")
	}
	out := webtest.Render(t, atoms.Alert(atoms.AlertProps{Tone: atoms.AlertDanger, Heading: "h", Message: "m", Dismissible: true}))
	has(t, out, `data-clear="#alert-root"`, `aria-label="閉じる"`, "data-ripple")
	// 閉じるボタンはゴースト（アイコン）ボタン: 32px の正方形・--r-md・ホバーで --accent-soft 地（S1。丸にしない）。
	has(t, out, "size-8", "hover:bg-accent-soft")
	hasNot(t, out, "rounded-full")
	hasNot(t, out, "data-error-code")
	// 雛形（app.ts が複製する）は role を持たない（ページに常に置くので、常時のエラーに見えないようにする）。
	tmpl := webtest.Render(t, atoms.Alert(atoms.AlertProps{Tone: atoms.AlertDanger, Heading: "h", Template: true}))
	hasNot(t, tmpl, `role="alert"`)
}

// 題なしのインラインアラート: 状態色は左の線（3px）だけ。ほかの 3 辺は --border 1px・地は --surface。アイコンは状態色・本文は ink。
// 見出しと閉じるボタンは持たず、role などは attrs で渡す。
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
		has(t, out, `role="note"`, `data-notice="n"`, "border border-border", "border-l-3", "border-l-"+color, "bg-surface", "rounded-md", "px-4 py-3", "text-"+color, "<svg", "text-sm text-ink", `class="min-w-0 flex-1 space-y-2"`, ">本文</div>")
		hasNot(t, out, "<h3", "<button", "text-xs", "bg-"+color+"-wash", "border-"+color+"-line")
		webtest.AssertCSPCompliantHTML(t, out)
	}
}

// 操作の結果として現れる Notice は Alert・FormField のエラーと同じフェードイン（M5）。常設の Notice は静かなまま。
func TestNoticeAnimatedRisesInOnlyWhenAsked(t *testing.T) {
	body := templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, "本文")
		return err
	})
	render := func(c templ.Component) string {
		var sb strings.Builder
		if err := c.Render(templ.WithChildren(context.Background(), body), &sb); err != nil {
			t.Fatal(err)
		}
		return sb.String()
	}
	attrs := atoms.Attrs{"role": "alert", "data-notice": "n"}
	animated := render(atoms.NoticeAnimated(atoms.AlertDanger, attrs))
	has(t, animated, `class="motion-rise-in flex items-start gap-3`, `role="alert"`, `data-notice="n"`, "border-l-danger", "<svg", ">本文</div>")
	hasNot(t, render(atoms.Notice(atoms.AlertDanger, attrs)), "motion-")
}

// トースト: 完了の短い通知。ピル・インク地・role や閉じるボタンは持たない（3 秒で自動消去）。エラーの表示には使わない。
func TestToastIsAShortInkPill(t *testing.T) {
	out := webtest.Render(t, atoms.Toast(view.Toast{Message: "保存しました。"}))
	has(t, out, "data-toast", "rounded-full", "bg-ink", "text-bone", "min-h-10", "保存しました。")
	hasNot(t, out, "data-toast-dismiss", "data-toast-level", "data-error-code", "<button")
}

// ボタン: 4 種（主・サブ・ゴースト・危険）。SemiBold・disabled は opacity .4・高さ 40（小は 32）。押下のリップルの対象。
// 危険は塗りでなく、透明地・--danger の文字・--danger 45% の枠（in srgb の --color-danger-line-strong。O7。主より弱い見た目）。
func TestButtonVariantsAndSizes(t *testing.T) {
	for v, want := range map[atoms.ButtonVariant]string{
		atoms.ButtonPrimary: "bg-accent text-bone", atoms.ButtonDanger: "border border-danger-line-strong bg-transparent text-danger hover:bg-danger-wash",
		atoms.ButtonSecondary: "border border-border", atoms.ButtonGhost: "bg-transparent text-ink",
	} {
		out := webtest.Render(t, atoms.Button(atoms.ButtonProps{Label: "操作", Variant: v}))
		has(t, out, want, "font-semibold", "disabled:opacity-40", "rounded-md", "h-10", "data-ripple", "motion-hover")
		hasNot(t, out, "font-medium", "disabled:opacity-50")
		hasNot(t, out, "bg-danger text-bone", "danger/45", "danger/10")
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
	// 無効（disabled）の見た目は opacity .5（O7）。入力・複数行・選択・秘密の入力が同じ指定を共有する。
	sel := webtest.Render(t, atoms.Select("s", "s", []atoms.SelectOption{{Value: "a", Label: "A"}}))
	for _, out := range []string{ok, area, sel} {
		has(t, out, "disabled:opacity-50", "disabled:cursor-not-allowed")
	}
}

// 読み取り専用の --bone 地は readonly 属性のある要素だけ。:read-only は select・range にも一致するので使わない（使うと select の地が変わる）。
func TestReadonlyBackgroundNeedsTheReadonlyAttribute(t *testing.T) {
	ok := webtest.Render(t, atoms.Input(atoms.InputProps{ID: "i", Name: "n"}))
	sel := webtest.Render(t, atoms.Select("s", "s", nil))
	area := webtest.Render(t, atoms.Textarea(atoms.TextareaProps{ID: "t", Name: "t"}))
	for _, out := range []string{ok, sel, area} {
		has(t, out, "[&amp;[readonly]]:bg-bone", "bg-surface")
		hasNot(t, out, "read-only:")
	}
	// 選択欄は標準の矢印を消す（app.css の select）ので、矢印の分の右 padding 36 を持つ。
	has(t, sel, "pr-9")
}

// スライダー（P1）: 1 行入力欄の枠（高さ・border・背景・padding）を持たず、幅いっぱいに置く。
func TestRangeHasNoFieldFrame(t *testing.T) {
	out := webtest.Render(t, atoms.Range(atoms.RangeProps{ID: "r", Name: "r", Value: "70", Invalid: true, Attrs: atoms.Attrs{"min": "1", "max": "100"}}))
	has(t, out, `type="range"`, `value="70"`, `min="1"`, `max="100"`, "w-full", "aria-invalid")
	hasNot(t, out, "h-10", "border", "bg-surface", "px-3", "rounded-md")
	webtest.AssertCSPCompliantHTML(t, out)
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
	has(t, out, `id="modal"`, `role="dialog"`, `aria-modal="true"`, "z-(--z-modal)", "bg-scrim", "rounded-xl", "shadow-modal", "motion-modal-in", "motion-modal-rise-in")
	// M5: モーダルの出現は --motion-slow（280ms）の専用クラス。出現 320ms の motion-fade-in・motion-rise-in は使わない。
	hasNot(t, out, "motion-fade-in", "motion-rise-in")
	hasNot(t, out, "shadow-xl", "z-40")
}

// O7: 確認モーダルの確定ボタンは、破壊的な操作（Destructive）だけ危険（--danger）。そうでない確認は主。既定の見た目は持たない（Destructive を付けなければ主）。
func TestModalConfirmButtonFollowsDestructive(t *testing.T) {
	base := view.ConfirmModal{Title: "確認", Message: "本当に？", Label: "実行", ConfirmURL: "/x", Method: "post", Target: "#t", Swap: "outerHTML"}
	destructive := base
	destructive.Destructive = true
	out := webtest.Render(t, molecules.Modal(destructive))
	has(t, out, "border border-danger-line-strong bg-transparent text-danger")
	hasNot(t, out, "bg-accent text-bone")
	out = webtest.Render(t, molecules.Modal(base))
	has(t, out, "bg-accent text-bone", ">実行<")
	hasNot(t, out, "text-danger", "border-danger")
}

// T5: Mono を指定した入力欄だけ font-mono（識別子・トークンの判読用）。既定は Sora のまま。
func TestInputMonoIsOptIn(t *testing.T) {
	has(t, webtest.Render(t, atoms.Input(atoms.InputProps{ID: "i", Name: "n", Mono: true})), "font-mono", "h-10")
	hasNot(t, webtest.Render(t, atoms.Input(atoms.InputProps{ID: "i", Name: "n"})), "font-mono")
}

// #169: ツールチップは、フォーカスできる出す側（tabindex="0"）から、aria-describedby で role="tooltip" の本文につなぐ（ホバーのほかキーボードでも出る）。
// 標準の title 属性は使わない。本文は隠れていても DOM に残る（読み上げ用）。
func TestTooltipLinksTheTriggerToTheTooltip(t *testing.T) {
	out := webtest.Render(t, atoms.Tooltip("tip-1", "ステップ 3 に付く行"))
	has(t, out, `class="tip-host" tabindex="0" aria-describedby="tip-1"`, `<span role="tooltip" id="tip-1" class="tip">ステップ 3 に付く行</span>`)
	hasNot(t, out, "title=", "<button", "<a ")
}
