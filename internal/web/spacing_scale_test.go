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
