package web_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readAppCSS(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "static", "src", "css", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	return string(src)
}

// S1・S2: ガイドの名前付きトークン（--r-*・--s-*・--gap-*・--pad-card*・--menu-*・--toast-*・--icon-*・--table-*）は
// app.css に定義がある。docs・コメントが名前で説明するトークン（--gap-h1・--gap-h2 など）が実在することを保証する。
func TestAppCSSDefinesTheGuideNamedTokens(t *testing.T) {
	css := readAppCSS(t)
	for _, name := range []string{
		"--r-none", "--r-sm", "--r-md", "--r-lg", "--r-xl", "--r-full",
		"--s-1", "--s-2", "--s-3", "--s-4", "--s-6", "--s-8", "--s-12", "--s-16",
		"--gap-menu", "--menu-item-h", "--menu-pad-x", "--gap-option",
		"--gap-grid", "--gap-grid-lg", "--gap-grid-dense",
		"--gap-label", "--gap-help", "--gap-field",
		"--gap-h1", "--gap-h2", "--gap-h3", "--gap-heading-body", "--gap-para",
		"--pad-card", "--pad-card-sm", "--gap-card-section", "--gap-inline",
		"--gap-section", "--gap-section-sm",
		"--tip-pad-y", "--tip-pad-x", "--z-popup", "--sb-size", "--sb-thumb", "--sb-thumb-hover", "--sb-thumb-active",
		"--toast-h", "--icon-sm", "--icon-md", "--icon-lg", "--icon-xl",
		"--table-pad-y", "--table-pad-x", "--table-pad-y-compact", "--table-pad-x-compact",
		"--table-head-size", "--table-body-size", "--table-row-hover", "--table-divider",
	} {
		if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `:`).MatchString(css) {
			t.Errorf("app.css does not define %s", name)
		}
	}
}

// S1・S2: Tailwind の --radius-*・--spacing は、ガイドの名前付きトークンを var() で参照する（値の定義は 1 か所）。
func TestTailwindThemeReferencesTheNamedTokens(t *testing.T) {
	css := readAppCSS(t)
	for _, want := range []string{
		"--spacing: var(--s-1);",
		"--radius-sm: var(--r-sm);", "--radius-md: var(--r-md);", "--radius-lg: var(--r-lg);",
		"--radius-xl: var(--r-xl);", "--radius-full: var(--r-full);",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css @theme must contain %q", want)
		}
	}
}

// S3: モーダルの影は 0 1px 2px で、色だけテーマで変える（ガイド「モーダルの影」: ライト 6%・ダーク 40%）。
// 色は --shadow-modal-color で、:root（ライト）と、ダークの 2 ブロック（[data-theme="dark"]・prefers-color-scheme）に同じ値を置く。
func TestModalShadowColorFollowsTheTheme(t *testing.T) {
	css := readAppCSS(t)
	if got := strings.Count(css, "--shadow-modal-color:"); got != 3 {
		t.Errorf("--shadow-modal-color defined %d times, want 3 (:root and the two dark blocks)", got)
	}
	for name, re := range map[string]string{
		":root (light) 6%":                        `(?s):root \{[^}]*--shadow-modal-color: rgb\(0 0 0 / 0\.06\);`,
		`[data-theme="dark"] 40%`:                 `(?s)\[data-theme="dark"\] \{[^}]*--shadow-modal-color: rgb\(0 0 0 / 0\.4\);`,
		"prefers-color-scheme: dark (system) 40%": `(?s)@media \(prefers-color-scheme: dark\) \{[^}]*--shadow-modal-color: rgb\(0 0 0 / 0\.4\);`,
	} {
		if !regexp.MustCompile(re).MatchString(css) {
			t.Errorf("--shadow-modal-color must be set in %s", name)
		}
	}
	if !strings.Contains(css, "--shadow-modal: 0 1px 2px var(--shadow-modal-color);") {
		t.Error("--shadow-modal must reference --shadow-modal-color")
	}
	if !regexp.MustCompile(`@theme inline \{[^}]*--shadow-modal:`).MatchString(css) {
		t.Error("--shadow-modal must be in @theme inline so that the utility keeps the variable")
	}
}

// C4: 状態色の枠・地はガイドの式（in srgb の 40% / 10%）で混色する名前付きの色（-line・-wash）。Tailwind の /40・/10 は in oklab になる。
func TestStatusLineAndWashColorsMixInSRGB(t *testing.T) {
	css := readAppCSS(t)
	for _, tone := range []string{"success", "warning", "danger", "info"} {
		for suffix, pct := range map[string]string{"line": "40", "wash": "10"} {
			want := "--color-" + tone + "-" + suffix + ": color-mix(in srgb, var(--" + tone + ") " + pct + "%, transparent);"
			if !strings.Contains(css, want) {
				t.Errorf("app.css must contain %q", want)
			}
		}
	}
}

// C4: 危険ボタンの枠（45%）も in srgb の名前付きの色にする（Tailwind の /45 は in oklab）。
func TestDangerButtonBorderMixesInSRGB(t *testing.T) {
	css := readAppCSS(t)
	want := "--color-danger-line-strong: color-mix(in srgb, var(--danger) 45%, transparent);"
	if !strings.Contains(css, want) {
		t.Errorf("app.css must contain %q", want)
	}
}

// C3: 離散コントロール（ボタン・チェックボックス・スライダーのつまみ）のフォーカスは 3px の --focus の実線（offset 2px）。
// --ring（--focus の 40%）の halo は背景に溶けて 3:1 に届かないので、単独の目印に使わない（ガイド「フォーカスリング」）。
func TestDiscreteControlFocusRingIsASolidFocusOutline(t *testing.T) {
	css := readAppCSS(t)
	for name, pattern := range map[string]string{
		"checkbox":     `input\[type="checkbox"\]:focus-visible \{\s*outline: 3px solid var\(--focus\);\s*outline-offset: 2px;`,
		"range thumb":  `input\[type="range"\]:focus-visible::-webkit-slider-thumb \{\s*outline: 3px solid var\(--focus\);\s*outline-offset: 2px;`,
		"range (moz)":  `input\[type="range"\]:focus-visible::-moz-range-thumb \{\s*outline: 3px solid var\(--focus\);\s*outline-offset: 2px;`,
		"button, link": `button:focus-visible,\s*\[data-ripple\]:focus-visible \{\s*outline-width: 3px;`,
	} {
		if !regexp.MustCompile(pattern).MatchString(css) {
			t.Errorf("app.css must give the %s :focus-visible a 3px solid --focus outline", name)
		}
	}
	if strings.Contains(css, "box-shadow: 0 0 0 3px var(--ring)") {
		t.Error("app.css must not use the --ring halo as the only focus indicator")
	}
}

// M1: スピナーの回転はガイドの 0.9s を :root の --spinner-duration 1 か所で持ち、.spinner はそれを参照する（独自の秒数を直書きしない）。
func TestSpinnerDurationIsOneToken(t *testing.T) {
	css := readAppCSS(t)
	if got := strings.Count(css, "--spinner-duration: 0.9s;"); got != 1 {
		t.Errorf("--spinner-duration: 0.9s defined %d times, want 1", got)
	}
	if !strings.Contains(css, "animation: motion-spin var(--spinner-duration) linear infinite;") {
		t.Error(".spinner must use var(--spinner-duration)")
	}
	if strings.Contains(css, "0.8s linear") {
		t.Error("app.css must not hard-code a spinner duration")
	}
}

// C8: 暗幕（--scrim）は大きな面なので、ダークでも純黒にせず --bone（#050505）系にする（ライトは 0.4、ダークは 0.6）。
func TestScrimIsNeverPureBlack(t *testing.T) {
	css := readAppCSS(t)
	if got := strings.Count(css, "--scrim: rgb(5 5 5 / 0.4);"); got != 1 {
		t.Errorf("light --scrim defined %d times, want 1", got)
	}
	if got := strings.Count(css, "--scrim: rgb(5 5 5 / 0.6);"); got != 2 {
		t.Errorf("dark --scrim defined %d times, want 2 ([data-theme=\"dark\"] and prefers-color-scheme)", got)
	}
	if strings.Contains(css, "--scrim: rgb(0 0 0") {
		t.Error("--scrim must not be pure black")
	}
}

// I1・I2: チェックボックスのチェックと select の矢印は、自作の形（clip-path・グラデーション）ではなく Iconoir（check・nav-arrow-down。線 1.5・丸い端）の SVG を data URI で出す。
// select の矢印は疑似要素を持てず色を変数にできないので、線の色は --mute の値（ライト #6b6b6e・ダーク #8b8b8e）で持つ。
func TestCSSIconsAreIconoir(t *testing.T) {
	css := readAppCSS(t)
	for _, bad := range []string{"clip-path", "linear-gradient(45deg", "linear-gradient(135deg"} {
		if strings.Contains(css, bad) {
			t.Errorf("app.css must not draw icons with %s", bad)
		}
	}
	for name, pattern := range map[string]string{
		"--icon-check":         `--icon-check: url\("data:image/svg\+xml,[^"]*stroke-width='1\.5' stroke-linecap='round' stroke-linejoin='round'[^"]*d='M5 13L9 17L19 7'`,
		"--select-arrow-light": `--select-arrow-light: url\("data:image/svg\+xml,[^"]*stroke='%236b6b6e' stroke-width='1\.5'[^"]*d='M6 9L12 15L18 9'`,
		"--select-arrow-dark":  `--select-arrow-dark: url\("data:image/svg\+xml,[^"]*stroke='%238b8b8e' stroke-width='1\.5'[^"]*d='M6 9L12 15L18 9'`,
	} {
		if !regexp.MustCompile(pattern).MatchString(css) {
			t.Errorf("app.css must define %s as an Iconoir data URI", name)
		}
	}
	if !regexp.MustCompile(`input\[type="checkbox"\]::before \{[^}]*width: var\(--icon-sm\);[^}]*mask: var\(--icon-check\)`).MatchString(css) {
		t.Error("the checkbox check must be the --icon-check mask at --icon-sm")
	}
	if !regexp.MustCompile(`select \{[^}]*background-image: var\(--select-arrow\);[^}]*background-size: var\(--icon-sm\)`).MatchString(css) {
		t.Error("the select arrow must be --select-arrow at --icon-sm")
	}
	// ダークの矢印は [data-theme="dark"] と prefers-color-scheme の 2 か所で切り替える。
	if got := strings.Count(css, "--select-arrow: var(--select-arrow-dark);"); got != 2 {
		t.Errorf("--select-arrow switched to dark %d times, want 2", got)
	}
	if got := strings.Count(css, "--select-arrow: var(--select-arrow-light);"); got != 1 {
		t.Errorf("--select-arrow defaults to light %d times, want 1", got)
	}
}

// O7: フォーム部品（チェックボックス・スライダー）の無効は、入力欄・選択欄（disabled:opacity-50）と同じ opacity .5。ボタンだけ .4。
func TestFormControlsDisabledOpacityIsHalf(t *testing.T) {
	css := readAppCSS(t)
	for _, sel := range []string{`input\[type="checkbox"\]:disabled`, `input\[type="range"\]:disabled`} {
		if !regexp.MustCompile(sel + ` \{\s*opacity: 0\.5;`).MatchString(css) {
			t.Errorf("app.css must give %s opacity 0.5", sel)
		}
	}
}

// C6: --mute はライトが #6b6b6e（--bone・--surface 上で AA 4.5:1）、ダークが #8b8b8e。
// ダークの値は [data-theme="dark"] と prefers-color-scheme の 2 か所に置く。
func TestMuteTokenIsSplitByTheme(t *testing.T) {
	css := readAppCSS(t)
	if got := strings.Count(css, "--mute: #6b6b6e;"); got != 1 {
		t.Errorf("light --mute: #6b6b6e defined %d times, want 1", got)
	}
	if got := strings.Count(css, "--mute: #8b8b8e;"); got != 2 {
		t.Errorf("dark --mute: #8b8b8e defined %d times, want 2 ([data-theme=\"dark\"] and prefers-color-scheme)", got)
	}
	if !regexp.MustCompile(`(?s):root \{[^}]*--mute: #6b6b6e;`).MatchString(css) {
		t.Error("light --mute must be in :root")
	}
	if !regexp.MustCompile(`(?s)\[data-theme="dark"\] \{[^}]*--mute: #8b8b8e;`).MatchString(css) {
		t.Error("dark --mute must be in [data-theme=\"dark\"]")
	}
	if !regexp.MustCompile(`(?s):root:not\(\[data-theme="light"\]\) \{[^}]*--mute: #8b8b8e;`).MatchString(css) {
		t.Error("dark --mute must be in the prefers-color-scheme: dark :root:not([data-theme=\"light\"]) block")
	}
}

// C6: プレースホルダーは --mute（Tailwind の既定は文字色の 50% 混色）。
func TestPlaceholderUsesMuteToken(t *testing.T) {
	css := readAppCSS(t)
	if !regexp.MustCompile(`input::placeholder,\s*textarea::placeholder\s*\{\s*color: var\(--mute\);\s*opacity: 1;`).MatchString(css) {
		t.Error("app.css must set input/textarea ::placeholder to var(--mute) with opacity 1")
	}
}

// C3・C4: 入力系のフォーカスは 2px の --focus の実線（offset -1px。枠も --focus）。不正な入力欄（aria-invalid="true"）は枠が --danger のまま、
// リングだけ枠の外側（offset 1px）に出す（ガイド「フォーカスリング」）。--ring の halo だけを目印にしない。
func TestInputFocusRingIsASolidFocusOutline(t *testing.T) {
	css := readAppCSS(t)
	if !regexp.MustCompile(`textarea:focus-visible \{\s*outline: 2px solid var\(--focus\);\s*outline-offset: -1px;\s*border-color: var\(--focus\);`).MatchString(css) {
		t.Error("app.css must give inputs a 2px solid --focus outline at offset -1px with a --focus border")
	}
	if !regexp.MustCompile(`\[aria-invalid="true"\]:focus-visible\s*\{\s*border-color: var\(--danger\);\s*outline-offset: 1px;`).MatchString(css) {
		t.Error("app.css must keep the --danger border and move the ring outside (offset 1px) for [aria-invalid=\"true\"]:focus-visible")
	}
}

// M1・M5: 確認モーダルの出現は --motion-slow（280ms）・--ease-out。退出は #modal（スクラム）の data-leaving で --motion-fade-out。
func TestModalMotionUsesTheSlowBand(t *testing.T) {
	css := readAppCSS(t)
	for name, pattern := range map[string]string{
		".motion-modal-in":               `\.motion-modal-in \{\s*animation: motion-fade-in var\(--motion-slow\) var\(--ease-out\) both;`,
		".motion-modal-in[data-leaving]": `\.motion-modal-in\[data-leaving\] \{\s*animation: motion-fade-out var\(--motion-fade-out\) var\(--ease-in\) both;`,
		".motion-modal-rise-in":          `\.motion-modal-rise-in \{\s*animation: motion-rise-in var\(--motion-slow\) var\(--ease-out\) both;`,
	} {
		if !regexp.MustCompile(pattern).MatchString(css) {
			t.Errorf("app.css must define %s with the --motion-slow band", name)
		}
	}
}

// #169: ツールチップ（atoms.Tooltip）はホバーでもキーボードのフォーカスでも出る（ガイド「ツールチップ」）。padding は --tip-pad-*、角丸は --r-sm、
// 表示の遅延はホバーだけ 400ms（フォーカスは即時）。隠れている間は visibility: hidden で、非対話（pointer-events: none）。
func TestTooltipShowsOnHoverAndFocusVisible(t *testing.T) {
	css := readAppCSS(t)
	rule := func(selector string) string {
		t.Helper()
		m := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(selector) + ` \{([^}]*)\}`).FindStringSubmatch(css)
		if m == nil {
			t.Fatalf("app.css has no rule for %s", selector)
		}
		return m[1]
	}
	base := rule(".tip")
	for _, want := range []string{"visibility: hidden;", "pointer-events: none;", "padding: var(--tip-pad-y) var(--tip-pad-x);", "border-radius: var(--radius-sm);", "background: var(--ink);", "color: var(--bone);", "font-size: var(--text-xs);", "z-index: var(--z-dropdown);"} {
		if !strings.Contains(base, want) {
			t.Errorf(".tip lacks %q:\n%s", want, base)
		}
	}
	hover, focus := rule(".tip-host:hover > .tip"), rule(".tip-host:focus-visible > .tip")
	for name, r := range map[string]string{"hover": hover, "focus-visible": focus} {
		if !strings.Contains(r, "visibility: visible;") || !strings.Contains(r, "motion-fade-in var(--motion-fade-in)") {
			t.Errorf("tooltip on %s must become visible with the fade-in:\n%s", name, r)
		}
	}
	if !strings.Contains(hover, "400ms") || strings.Contains(focus, "400ms") {
		t.Errorf("the 400ms show delay applies to hover only (focus is immediate):\nhover: %s\nfocus: %s", hover, focus)
	}
}
