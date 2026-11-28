package web_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// #184: 枠付きのカード（`rounded-card card-edge bg-surface` の枠）の内側の余白は、直書きの数値（`p-4` など）でなく、ガイドのトークンで書く。
// 項目カード（クライアント・アプリ・接続要求・詳細の枠）は設定画面のパーツ 1 の本文と同じ `p-(--pad-card)`（22）、
// 絞り込み・ツールバーのような小さい枠と、見出し付きカードの中に入れ子になった拒否リストの行（表示・編集とも）は `p-(--pad-card-sm)`（16）。docs/architecture/frontend.md §8.4 の「項目カードの余白」。
func TestBorderedSurfaceFramesUsePadCardTokens(t *testing.T) {
	rawPadding := regexp.MustCompile(`rounded-card card-edge bg-surface[^"]*\bp-[0-9]`)
	found := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".templ") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "rounded-card card-edge bg-surface p-(--pad-card") {
				found++
			}
			if strings.Contains(line, "data-denied={ row.ID }") && !strings.Contains(line, "p-(--pad-card-sm)") {
				t.Errorf("%s:%d: a denied-list row is nested in a Card, so it uses p-(--pad-card-sm): %s", path, i+1, strings.TrimSpace(line))
			}
			if rawPadding.MatchString(line) {
				t.Errorf("%s:%d: use p-(--pad-card) / p-(--pad-card-sm) instead of a literal padding: %s", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no framed card with a pad-card token found; the test is looking in the wrong place")
	}
}
