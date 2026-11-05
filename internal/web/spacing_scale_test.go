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
// （docs/architecture/frontend.md §8.5）。
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
// （docs/architecture/frontend.md §8.5）。外枠が `space-y-4`（16）や `space-y-2`（8）だと、ページごとに h1 の下の間隔が違ってしまう。
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
