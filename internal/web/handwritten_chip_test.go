package web_test

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// O8: 種別・状態・件数の小さなラベル（チップ・ピル）は共通の atoms.Badge で作り、templ に手書きしない。
// 角丸 sm / full の指定は、Badge（atoms/badge.templ）以外では次の部品だけが持つ（docs/architecture/frontend.md §8.4「手書きのチップ・ピル」）。
// 新しい手書きのチップを足したくなったら、Badge の tone で足りないかを先に見て、本当に別部品ならここに理由つきで足す。
func TestChipsAndPillsAreDrawnByBadge(t *testing.T) {
	allowed := map[string]string{
		"atoms/badge.templ":       "Badge 本体",
		"atoms/button.templ":      "ボタン本体（ピル）",
		"atoms/toast.templ":       "トースト（ink 地の通知。ピルだが状態のラベルではない）",
		"organisms/app_log.templ": "ログの絞り込みチップ（押せるリンク。ラベルではなく操作の部品）",
		"organisms/chrome.templ":  "サイドバーの承認待ちの件数（accent 地の数字だけの丸。ナビ項目の添え物）",
		"organisms/tasks.templ":   "サムネイル画像の角（ラベルではない）",
	}
	shape := regexp.MustCompile(`\brounded-(?:sm|full)\b`)
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".templ") {
			return err
		}
		if _, ok := allowed[filepath.ToSlash(path)]; ok {
			return nil
		}
		for i, line := range strings.Split(readFile(t, path), "\n") {
			if shape.MatchString(line) {
				t.Errorf("%s:%d: 手書きのチップ・ピル（rounded-sm / rounded-full）。atoms.Badge を使う", path, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
