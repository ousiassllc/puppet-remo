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

// S3: モーダルの影はガイドの 1 値（0 1px 2px rgba(0,0,0,.06)）で、ライト・ダークで変えない。
// 色は :root の --shadow-modal-color 1 か所だけで、ダークの再定義（[data-theme="dark"]・prefers-color-scheme）は置かない。
func TestModalShadowColorIsASingleValue(t *testing.T) {
	css := readAppCSS(t)
	if got := strings.Count(css, "--shadow-modal-color:"); got != 1 {
		t.Errorf("--shadow-modal-color defined %d times, want 1 (no per-theme override)", got)
	}
	if !regexp.MustCompile(`(?s):root \{[^}]*--shadow-modal-color: rgb\(0 0 0 / 0\.06\);`).MatchString(css) {
		t.Error("--shadow-modal-color must be rgb(0 0 0 / 0.06) in :root")
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

// C3・C4: 不正な入力欄（aria-invalid="true"）のフォーカスの輪は --danger の 40%。
func TestInvalidFieldFocusRingIsDanger(t *testing.T) {
	css := readAppCSS(t)
	if !regexp.MustCompile(`\[aria-invalid="true"\]:focus-visible\s*\{\s*box-shadow: 0 0 0 2px color-mix\(in srgb, var\(--danger\) 40%, transparent\);`).MatchString(css) {
		t.Error("app.css must give [aria-invalid=\"true\"]:focus-visible a --danger 40% ring")
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
