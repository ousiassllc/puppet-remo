package web_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// C6: 表の行はホバーで --table-row-hover（--surface-sel）地になるが、--mute はその上で AA 4.5:1 に届かない（ライト 4.45・ダーク 4.89 のうちライトが未満。--mute-on-soft は 5.59・6.07）。
// 行の中の text-mute の文字は、行に `group` を付けて `group-hover:text-mute-on-soft` に切り替える
// （docs/architecture/frontend.md §8.4「色・ダーク（C）」）。
// 行の中で描く部品（auditThumbCell・taskStepArgs）も、行の外に text-mute を持たない前提で同じ規則を当てる。
func TestMuteTextInHoverRowsSwitchesToMuteOnSoft(t *testing.T) {
	const switched = "group-hover:text-mute-on-soft"
	mute := regexp.MustCompile(`class="[^"]*\btext-mute\b[^"]*"`)
	rowOpen := regexp.MustCompile(`<tr class="([^"]*\bhover:bg-\(--table-row-hover\)[^"]*)"`)

	// 行の中で描く部品: 先頭の templ 宣言から、次の行頭の `}` まで。
	for _, c := range []struct{ file, decl string }{
		{"organisms/audit.templ", "templ auditThumbCell("},
		{"organisms/tasks.templ", "templ taskStepArgs("},
	} {
		src := readFile(t, c.file)
		start := strings.Index(src, c.decl)
		if start < 0 {
			t.Fatalf("%s: %s が見つからない", c.file, c.decl)
		}
		body := src[start:]
		if end := strings.Index(body, "\n}\n"); end >= 0 {
			body = body[:end]
		}
		if !mute.MatchString(body) {
			t.Errorf("%s: %s に text-mute が無い（テストの前提が変わった。部品を見直す）", c.file, c.decl)
		}
		for _, m := range mute.FindAllString(body, -1) {
			if !strings.Contains(m, switched) {
				t.Errorf("%s: %s の %s に %s が無い", c.file, c.decl, m, switched)
			}
		}
	}

	// 行（<tr class="… hover:bg-(--table-row-hover) …"> から </tr> まで）の中の text-mute。
	for _, file := range []string{
		"organisms/app_log.templ", "organisms/audit.templ", "organisms/clients.templ",
		"organisms/request_history.templ", "organisms/tasks.templ",
	} {
		src := readFile(t, file)
		for _, loc := range rowOpen.FindAllStringSubmatchIndex(src, -1) {
			open := src[loc[2]:loc[3]]
			rest := src[loc[1]:]
			end := strings.Index(rest, "</tr>")
			if end < 0 {
				t.Fatalf("%s: <tr> の閉じが見つからない", file)
			}
			row := rest[:end]
			muted := mute.FindAllString(row, -1)
			if len(muted) == 0 {
				continue
			}
			line := strings.Count(src[:loc[0]], "\n") + 1
			if !regexp.MustCompile(`(^|\s)group(\s|$)`).MatchString(open) {
				t.Errorf("%s:%d: 行に text-mute があるのに class に group が無い", file, line)
			}
			for _, m := range muted {
				if !strings.Contains(m, switched) {
					t.Errorf("%s:%d: 行の中の %s に %s が無い", file, line, m, switched)
				}
			}
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
