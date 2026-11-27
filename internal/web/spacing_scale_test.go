package web_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// S2: 余白は 4px 基準（4/8/12/16/24/32/48/64）。2px（`*-0.5`）などスケール外の値は、どの templ でも使わない
// （docs/architecture/frontend.md §8.4）。
func TestTemplsDoNotUseOffScaleSpacing(t *testing.T) {
	offScale := regexp.MustCompile(`\b(?:-?(?:m|p)[trblxy]?|gap(?:-[xy])?|space-[xy]|inset(?:-[xy])?|top|right|bottom|left)-(?:0\.5|1\.5|2\.5|3\.5|5|7)\b`)
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".templ") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if m := offScale.FindString(line); m != "" {
				t.Errorf("%s:%d: off-scale spacing %q", path, i+1, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// S2: ページの見出し（h1）の下は 24px（`--gap-h1`）。ページの外枠は `space-y-6`（24）で、h1 の直前の行で開く
// （docs/architecture/frontend.md §8.4）。外枠が `space-y-4`（16）や `space-y-2`（8）だと、ページごとに h1 の下の間隔が違ってしまう。
func TestPageH1HasGapH1BelowIt(t *testing.T) {
	const h1 = `<h1 class="text-page-title`
	found := 0
	err := filepath.WalkDir("pages", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".templ") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			if !strings.Contains(line, h1) {
				continue
			}
			found++
			if i == 0 || !strings.Contains(lines[i-1], `space-y-6"`) {
				t.Errorf("%s:%d: the page wrapper above the h1 must use space-y-6 (24px below the heading)", path, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no page h1 found; the test is looking in the wrong place")
	}
}

// T3（ガイド d430361）: アプリ画面のページタイトルは 28px / 700 / --ink（`text-page-title font-bold text-ink`）。サイズは app.css の
// --text-page-title（1.75rem）で 1 か所に持ち、テンプレートに 28px を直書きしない。Bold（700）はこの h1 だけで、ほかの見出しには使わない。
// 据え置く h1: 承認ダイアログの題（lg 18 SemiBold。モーダルの題と同じ段）・ビューアの sr-only・待機ページの `.pg-t`（承認済みの逸脱）。
func TestPageTitleIsTwentyEightBold(t *testing.T) {
	css := readAppCSS(t)
	if !regexp.MustCompile(`(?m)^\s*--text-page-title:\s*1\.75rem;`).MatchString(css) {
		t.Error("app.css must define --text-page-title as 1.75rem (28px)")
	}
	const pageTitle = `class="text-page-title font-bold text-ink"`
	h1 := regexp.MustCompile(`<h1\b[^>]*>`)
	kept := []string{`class="text-lg font-semibold text-ink"`, `class="sr-only"`, `class="pg-t"`}
	pageH1 := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".templ") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "font-bold") && !strings.Contains(line, "text-page-title") {
				t.Errorf("%s:%d: font-bold (700) is only for the page title (text-page-title): %s", path, i+1, strings.TrimSpace(line))
			}
			for _, tag := range h1.FindAllString(line, -1) {
				switch {
				case strings.Contains(tag, pageTitle):
					pageH1++
				case slices.ContainsFunc(kept, func(k string) bool { return strings.Contains(tag, k) }):
				default:
					t.Errorf("%s:%d: h1 must be the page title (%s) or one of the kept h1s: %s", path, i+1, pageTitle, tag)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pageH1 != 16 {
		t.Errorf("found %d page-title h1s, want 16 (clients, audit and tasks have a list and a detail page)", pageH1)
	}
}

// T3・#166: カード状の枠の見出しは molecules.Card の題（lg 18 SemiBold）にそろえる。見出し（h1〜h4）に直書きの `text-2xl`・`text-xl` を
// 残さない（ページの題は h1 の `text-page-title`、項目名の見出しは h3 の `text-base`）。32px・24px の見出しがカードの題 18px と並んで見える不揃いを防ぐ。
func TestNoHardcodedLargeHeadingsInTemplates(t *testing.T) {
	heading := regexp.MustCompile(`<h[1-6]\b[^>]*class="[^"]*\btext-(2xl|xl)\b`)
	found := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".templ") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		found++
		for i, line := range strings.Split(string(src), "\n") {
			if heading.MatchString(line) {
				t.Errorf("%s:%d: a heading must not use text-2xl / text-xl; use molecules.Card (card title) or h3 text-base (item name): %s", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no .templ file found; the test is looking in the wrong place")
	}
}

// #166: 見える h2 は molecules（Card の題・Modal の題）だけが出す。organisms・pages に直書きの `<h2`（枠付きの題）を置かない
// （題付きの枠は molecules.Card）。見せない h2（`sr-only`。項目が並ぶ場所の階層用）は対象外。
func TestVisibleH2OnlyInMolecules(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".templ") || strings.HasPrefix(path, "molecules"+string(filepath.Separator)) {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "<h2") && !strings.Contains(line, `class="sr-only"`) {
				t.Errorf("%s:%d: a visible h2 title must come from molecules.Card: %s", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// S1: 素の CSS と Lit の `css` の角丸も、リテラルの `4px` / `9999px` ではなくトークン（`--radius-*`）で書く。
func TestStylesUseRadiusTokens(t *testing.T) {
	literal := regexp.MustCompile(`border-radius:\s*[0-9]`)
	err := filepath.WalkDir(filepath.Join("..", "..", "static", "src"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || (!strings.HasSuffix(path, ".css") && !strings.HasSuffix(path, ".ts")) {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if literal.MatchString(line) {
				t.Errorf("%s:%d: use var(--radius-*) instead of a literal border-radius: %s", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// S2: ボタン列・インラインは 8（--gap-inline = gap-2）、フォームの項目 → 項目は 16（--gap-field = space-y-4）。12（gap-3・space-y-3）は、
// ガイドが決めた間隔のどれにも当たらないので、templ に残さない（#208）。対象外は、ボタン列でも項目間でもない Notice/Alert のアイコンと本文の間と、
// 空の状態（縦に積むアイコン・文・操作の組）。
func TestTemplsDoNotUseTwelvePixelGapsForButtonRowsAndFields(t *testing.T) {
	twelve := regexp.MustCompile(`\b(?:gap(?:-[xy])?|space-[xy])-3\b`)
	allowed := map[string]bool{"atoms/alert.templ": true, "molecules/empty_state.templ": true}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".templ") || allowed[filepath.ToSlash(path)] {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if m := twelve.FindString(line); m != "" {
				t.Errorf("%s:%d: 12px spacing %q (use gap-2 for button rows / space-y-4 for fields)", path, i+1, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
