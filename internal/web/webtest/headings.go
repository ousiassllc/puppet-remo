package webtest

import (
	"strings"
	"testing"
)

// Level は h1〜h6 の数字を返す（見出しでなければ 0）。
func (e Element) Level() int {
	if len(e.Tag) == 2 && e.Tag[0] == 'h' && e.Tag[1] >= '1' && e.Tag[1] <= '6' {
		return int(e.Tag[1] - '0')
	}
	return 0
}

// AssertHeadingOutline はページの見出し（h1〜h6。legend は見出しではない）の階層を検査する。
// h1 がちょうど 1 つ・最初の見出しが h1・下げるときは 1 段ずつ（h1 → h3 の飛びがない）。
// 上げる（h3 → h2）のは次の節に移るだけなので許す。親の無い h3 が h2 の前に出る「逆転」は、最初の規則（飛びなし）に当たる。
// 見出しのレベル列（「1 2 3 3 2」のような文字列）を返す。
func AssertHeadingOutline(t testing.TB, body string) string {
	t.Helper()
	heads := Elements(t, body)
	var levels []string
	ones, prev := 0, 0
	for _, h := range heads {
		lv := h.Level()
		levels = append(levels, string(rune('0'+lv)))
		if lv == 1 {
			ones++
		}
		if lv > prev+1 {
			t.Errorf("heading level jumps h%d -> h%d at %q (levels so far: %s)", prev, lv, h.Text, strings.Join(levels, " "))
		}
		prev = lv
	}
	if ones != 1 {
		t.Errorf("want exactly one h1, got %d (levels: %s)", ones, strings.Join(levels, " "))
	}
	return strings.Join(levels, " ")
}
