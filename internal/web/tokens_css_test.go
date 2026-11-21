package web_test

import (
	"io/fs"
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
		"--bp-sm", "--bp-md", "--bp-lg", "--bp-xl",
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

// S2: ラベルと入力欄のすき間 --gap-label はガイドの 6px（#171）。--s-* のスケールには 6 が無いので px で直に持つ。
// 入力欄の上に積むラベル（<label ... block>）は、このトークン（mb-(--gap-label)）で入力欄との間をあける。mb-2（8px）などに戻ると 2px ずれるので、templ を走査して検出する。
func TestLabelGapIsSixPixelsAndLabelsUseIt(t *testing.T) {
	if css := readAppCSS(t); !regexp.MustCompile(`(?m)^\s*--gap-label:\s*6px;`).MatchString(css) {
		t.Error("app.css must define --gap-label as 6px (the guide's label-to-field gap)")
	}
	stacked := regexp.MustCompile(`<label [^>]*\bblock\b[^>]*>`)
	found := 0
	err := filepath.WalkDir(filepath.Join("..", "web"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".templ") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			for _, m := range stacked.FindAllString(line, -1) {
				found++
				if !strings.Contains(m, "mb-(--gap-label)") {
					t.Errorf("%s:%d: a stacked label (block) must use mb-(--gap-label) for the gap to its field: %s", path, i+1, m)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no stacked <label> found; the test is looking in the wrong place")
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

// M3: スピナーなど処理中表示は reduced-motion でも維持する（ガイド Feedback 節・check-criteria M3）。
// app.css の .spinner と Lit のビューアの .spinner は、prefers-reduced-motion: reduce で回転を止めない。
func TestSpinnerKeepsSpinningUnderReducedMotion(t *testing.T) {
	stopped := regexp.MustCompile(`prefers-reduced-motion:\s*reduce\)\s*\{\s*\.spinner\s*\{[^}]*animation:\s*none`)
	viewer, err := os.ReadFile(filepath.Join("..", "..", "static", "src", "components", "screen-viewer", "remo-screen-viewer.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{"app.css": readAppCSS(t), "remo-screen-viewer.ts": string(viewer)} {
		if stopped.MatchString(src) {
			t.Errorf("%s must not stop the .spinner rotation under prefers-reduced-motion", name)
		}
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

// O10・#203: forced-colors（ハイコントラスト）では背景色が Canvas に置き換わり、ON の地（--accent）とチェック（--bone）が同じ色になって ON が見えなくなる。
// システムカラーは強制色でも残るので、ON の枠・地を Highlight、チェックを HighlightText にする。
func TestCheckedCheckboxSurvivesForcedColors(t *testing.T) {
	css := readAppCSS(t)
	if !regexp.MustCompile(`(?s)@media \(forced-colors: active\) \{\s*input\[type="checkbox"\]:checked \{[^}]*border-color: Highlight;[^}]*background-color: Highlight;[^}]*\}\s*input\[type="checkbox"\]:checked::before \{[^}]*background-color: HighlightText;`).MatchString(css) {
		t.Error("the checked checkbox needs Highlight / HighlightText under @media (forced-colors: active)")
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

// M5（#201）: モバイルナビのドロワー・スクラムと .htmx-indicator は、出現が行き先の状態（開いた・処理中）の transition、退出が既定の側の transition。
// 出現は --motion-slow（インジケーターは --motion-fade-in）・--ease-out、退出は --motion-fade-out（240ms）・--ease-in。--motion-base（ホバー用）は使わない。
func TestDrawerScrimAndIndicatorUseEnterAndExitTokens(t *testing.T) {
	css := readAppCSS(t)
	rule := func(selector string) string {
		t.Helper()
		m := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(selector) + ` \{([^}]*)\}`).FindStringSubmatch(css)
		if m == nil {
			t.Fatalf("app.css has no rule for %s", selector)
		}
		return m[1]
	}
	collapse := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	for selector, want := range map[string]string{
		".nav-drawer":                     "transition: opacity var(--motion-fade-out) var(--ease-in), transform var(--motion-fade-out) var(--ease-in), visibility 0s linear var(--motion-fade-out);",
		"body[data-nav-open] .nav-drawer": "transition: opacity var(--motion-slow) var(--ease-out), transform var(--motion-slow) var(--ease-out), visibility 0s linear 0s;",
		".nav-scrim":                      "transition: opacity var(--motion-fade-out) var(--ease-in), visibility 0s linear var(--motion-fade-out);",
		"body[data-nav-open] .nav-scrim":  "transition: opacity var(--motion-slow) var(--ease-out), visibility 0s linear 0s;",
		".htmx-indicator":                 "transition: opacity var(--motion-fade-out) var(--ease-in);",
		".htmx-request.htmx-indicator":    "transition: opacity var(--motion-fade-in) var(--ease-out);",
	} {
		if got := collapse(rule(selector)); !strings.Contains(got, want) {
			t.Errorf("%s must declare %q, got: %s", selector, want, got)
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
	// forced-colors では背景が OS の色になって表の文字に重なるので、枠で区切る。
	if !regexp.MustCompile(`(?s)@media \(forced-colors: active\) \{\s*\.tip \{[^}]*border: 1px solid CanvasText;`).MatchString(css) {
		t.Error(".tip needs a CanvasText border under @media (forced-colors: active)")
	}
	if !strings.Contains(hover, "400ms") || strings.Contains(focus, "400ms") {
		t.Errorf("the 400ms show delay applies to hover only (focus is immediate):\nhover: %s\nfocus: %s", hover, focus)
	}
}

// S5: ブレークポイントは --bp-*（640/768/1024/1280）で、Tailwind の @theme の --breakpoint-*、app.ts の matchMedia と同じ値。
// Tailwind の既定に暗黙に頼らず、@media は var() を取れないので、同じ値を並べて書き、ここで一致を検査する。
func TestBreakpointTokensMatchTheTailwindThemeAndAppTS(t *testing.T) {
	css := readAppCSS(t)
	for name, px := range map[string]string{"sm": "640", "md": "768", "lg": "1024", "xl": "1280"} {
		for _, prefix := range []string{"--bp-", "--breakpoint-"} {
			if !regexp.MustCompile(`(?m)^\s*` + prefix + name + `:\s*` + px + `px;`).MatchString(css) {
				t.Errorf("app.css must define %s%s as %spx", prefix, name, px)
			}
		}
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "static", "src", "app", "app.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `matchMedia("(min-width: 768px)")`) {
		t.Error("app.ts must use md (--bp-md = 768px) for the desktop width")
	}
}

// O3: トーストの高さ・表示時間・端からの距離は --toast-h 40・--toast-duration 3s・--toast-offset 24px（--s-6）。
// toast.templ・chrome.templ は値を直書きせずトークンで参照し（h-(--toast-h)・bottom-(--toast-offset)）、app.ts は --toast-duration を読む。
// CSS を読めないとき（テストなど）の app.ts の既定 3000ms も、トークンの 3s と同じ値に固定する。
func TestToastTokensAreTheGuideValuesAndSyncedWithAppTS(t *testing.T) {
	css := readAppCSS(t)
	for _, want := range []string{`--toast-h: 40px;`, `--toast-duration: 3s;`, `--toast-offset: var(--s-6);`} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css must define %s", want)
		}
	}
	if !regexp.MustCompile(`(?m)^\s*--s-6:\s*24px;`).MatchString(css) {
		t.Error("--toast-offset (--s-6) must be 24px")
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "static", "src", "app", "app.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`TOAST_LIFETIME_FALLBACK_MS = 3000;`, `getPropertyValue("--toast-duration")`} {
		if !strings.Contains(string(src), want) {
			t.Errorf("app.ts must contain %q (the toast lifetime follows --toast-duration, 3s)", want)
		}
	}
}

// O4: Lit ビューアの .sourcebar・.banner の z-index は、--z-* のトークン（--z-dropdown）で書く。数値の直書き（z-index: 1）に戻さない。
func TestViewerLocalStackingUsesZTokens(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "static", "src", "components", "screen-viewer", "remo-screen-viewer.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if bare := regexp.MustCompile(`z-index:\s*[0-9]`).FindString(string(src)); bare != "" {
		t.Errorf("remo-screen-viewer.ts must not hardcode z-index (%q); use var(--z-*)", bare)
	}
	if n := strings.Count(string(src), "z-index: var(--z-dropdown);"); n != 2 {
		t.Errorf("want .sourcebar and .banner to use z-index: var(--z-dropdown), found %d", n)
	}
}

// T1・T3（#209）: 文中・値のインライン code は 1 つの書き方で、app.css の `code` ルールが決める（Mono・--accent-soft 地・--r-sm・左右 4px・
// 大きさは親に従う・文字色 --ink）。テンプレートの <code> には、チップの見た目のクラス（text-xs・px-*・py-*・bg-*・rounded-*・text-*・font-*）を付けない。
// 付けると画面ごとに大きさ・余白が分かれ、本文の中で 12px に縮む。折り返し・レイアウトのクラス（break-all・min-w-0 など）は付けてよい。
func TestInlineCodeHasOneStyleFromAppCSS(t *testing.T) {
	css := readAppCSS(t)
	rule := regexp.MustCompile(`(?s)\n\s*code\s*\{([^}]*)\}`).FindStringSubmatch(css)
	if rule == nil {
		t.Fatal("app.css must define a global `code` rule")
	}
	for _, want := range []string{
		"border-radius: var(--radius-sm);", "background-color: var(--accent-soft);", "padding-inline: var(--s-1);",
		"color: var(--ink);", "font-family: var(--font-mono);", "font-size: inherit;",
	} {
		if !strings.Contains(rule[1], want) {
			t.Errorf("app.css `code` rule lacks %q:\n%s", want, rule[1])
		}
	}

	codeOpen := regexp.MustCompile(`<code\b[^>]*>`)
	classAttr := regexp.MustCompile(`class="([^"]*)"`)
	styled := regexp.MustCompile(`^(text-(xs|sm|base|lg|xl|2xl|3xl|2xs|ink|mute)|px-|py-|p-|bg-|rounded|font-|border)`)
	found := 0
	err := filepath.WalkDir(filepath.Join("..", "web"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".templ") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			for _, tag := range codeOpen.FindAllString(line, -1) {
				found++
				m := classAttr.FindStringSubmatch(tag)
				if m == nil {
					continue
				}
				for _, c := range strings.Fields(m[1]) {
					if styled.MatchString(c) {
						t.Errorf("%s:%d: <code> must not carry the chip style %q (app.css `code` rule owns it): %s", path, i+1, c, tag)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no <code> found; the test is looking in the wrong place")
	}
}

// T1: --font-mono は JetBrains Mono を先頭に、和文のフォールバックとして Noto Sans JP を挟む（承認済みの逸脱。docs §8.4 の T の行に理由）。
func TestFontMonoKeepsTheApprovedJapaneseFallback(t *testing.T) {
	want := `--font-mono: "JetBrains Mono", "Noto Sans JP", ui-monospace, monospace;`
	if !strings.Contains(readAppCSS(t), want) {
		t.Errorf("app.css must define %s", want)
	}
}

// T1・T5: 文（日本語の文言）の中に素のまま埋めた `{ 値 }` を残さない（#205・#210・#213〜#215・#217 と同じ種類の漏れの再発防止）。
// 文中の値は、識別子（パス・ID・ターゲット名・IP・ショートカット）なら `<code>`、日時・数値・件数・バージョンなら `<span class="font-mono">` で包む。
// 値だけの要素（`<td>{ v }</td>`）は要素の class で決める別の規則なので対象外。この検査は「同じテキストノードに、`{ 値 }` と日本語・英単語の文言が一緒にある」ものを拾う。
// 包まないまま残してよいのは、値が名前・ラベル・文章そのもの（識別子でも数値でもない）の次の表だけで、1 件ごとに理由を書く。
func TestSentenceEmbeddedValuesAreWrapped(t *testing.T) {
	// キーは「ファイル名: 式」。値が人が付けた名前・ラベル・Go 側で組み立てた文言のもの。
	plain := map[string]string{
		"agent.templ: p.Provider":     "プロバイダーの製品名（OpenAI など）で、識別子ではない",
		"clients.templ: c.Approved":   "承認モードの日本語のラベル",
		"clients.templ: c.LoginName":  "利用者の名前。表の同じ列（ユーザー）も Regular",
		"requests.templ: r.LoginName": "同上",
		"dashboard.templ: v.Source":   "発動元の日本語のラベル（ホスト GUI・ホットキー）",
		"token_reveal.templ: v.Label": "利用者が付けたトークンの名前",
	}
	// Go の文（if・for・} else {・@コンポーネント・コメント）の行は、テキストノードから外す。
	goLine := regexp.MustCompile(`^(if |for |switch |case |default:|\} else|\}$|@|\{\{|//)`)
	tag := regexp.MustCompile(`<[a-zA-Z/!][^>]*>`)
	expr := regexp.MustCompile(`\{ [^{}]*\}`)
	words := regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}A-Za-z]`)
	found := 0
	used := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".templ") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var body []string // templ ブロックの中の行だけ
		in := false
		for _, line := range strings.Split(string(src), "\n") {
			switch {
			case strings.HasPrefix(line, "templ "):
				in = true
			case line == "}":
				in = false
			case in && !goLine.MatchString(strings.TrimSpace(line)):
				body = append(body, line)
			default:
				if in {
					body = append(body, "<go>") // Go の行はテキストノードの区切りにする
				}
			}
		}
		text := strings.Join(body, "\n")
		prev := 0
		last := ""
		nodes := map[string]string{} // テキストノード → 直前のタグ
		var order []string
		for _, loc := range tag.FindAllStringIndex(text, -1) {
			node := text[prev:loc[0]]
			if expr.MatchString(node) {
				nodes[node] = last
				order = append(order, node)
			}
			prev, last = loc[1], text[loc[0]:loc[1]]
		}
		for _, node := range order {
			found++
			if strings.HasPrefix(nodes[node], "<title") {
				continue
			}
			if !words.MatchString(expr.ReplaceAllString(node, "")) {
				continue // 値だけ（または「 / 」「…」のような記号だけをはさむ）
			}
			for _, e := range expr.FindAllString(node, -1) {
				key := filepath.Base(path) + ": " + strings.TrimSpace(strings.Trim(e, "{}"))
				if _, ok := plain[key]; ok {
					used[key] = true
					continue
				}
				t.Errorf("%s: 文中に素のまま埋めた値 %s がある（識別子は <code>、日時・数値・件数は <span class=\"font-mono\"> で包む。名前・ラベルなら plain の表に理由つきで足す）:\n%s", path, e, strings.TrimSpace(node))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no `{ value }` text found; the test is looking in the wrong place")
	}
	for key := range plain {
		if !used[key] {
			t.Errorf("plain の表の %q は、もう文中に無い。表から外す", key)
		}
	}
}
