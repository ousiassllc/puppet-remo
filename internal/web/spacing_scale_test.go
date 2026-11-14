package web_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
	const h1 = `<h1 class="text-3xl`
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

// T3・#166: カード状の枠の見出しは molecules.Card の題（lg 18 SemiBold）にそろえる。見出し（h1〜h4）に直書きの `text-2xl`・`text-xl` を
// 残さない（ページの題は h1 の `text-3xl`、項目名の見出しは h3 の `text-base`）。32px・24px の見出しがカードの題 18px と並んで見える不揃いを防ぐ。
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
