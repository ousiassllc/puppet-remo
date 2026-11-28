package web_test

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// luminance は #rrggbb の相対輝度（WCAG 2.x）。
func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	if len(hex) != 7 || hex[0] != '#' {
		t.Fatalf("#rrggbb ではない: %q", hex)
	}
	var l float64
	for i, w := range []float64{0.2126, 0.7152, 0.0722} {
		v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		c := float64(v) / 255
		if c <= 0.03928 {
			c /= 12.92
		} else {
			c = math.Pow((c+0.055)/1.055, 2.4)
		}
		l += w * c
	}
	return l
}

func contrast(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := luminance(t, a), luminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// themeToken は app.css のテーマのブロック（:root・[data-theme="dark"]）から、トークンの 16 進の値を取る。
func themeToken(t *testing.T, css, block, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(block) + ` \{[^}]*?` + regexp.QuoteMeta(name) + `:\s*(#[0-9a-fA-F]{6});`).FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("app.css の %s に %s（16 進）が無い", block, name)
	}
	return strings.ToLower(m[1])
}

// #283・C6: 行の hover は専用トークン --row-hover（ライトは --bone と同じ #f5f5f4、ダークは --surface-sel）。
// 中立チップの地（--accent-soft）は --surface-sel（ライト）とほぼ同じ色で、hover の行の中で輪郭が溶けたため、行 hover は --surface-sel を使わない。
// 行の中の --mute は --row-hover の上で AA 4.5:1 を超える（ライト 4.87・ダーク 4.89）ので、行の中で --mute-on-soft へ切り替えなくてよい。
func TestRowHoverToken(t *testing.T) {
	css := readAppCSS(t)
	for _, th := range []struct {
		name, block string
		hover       string
	}{
		{"light", ":root", "#f5f5f4"},
		{"dark", `[data-theme="dark"]`, themeToken(t, css, `[data-theme="dark"]`, "--surface-sel")},
	} {
		hover := themeToken(t, css, th.block, "--row-hover")
		if hover != th.hover {
			t.Errorf("%s: --row-hover = %s, want %s", th.name, hover, th.hover)
		}
		if th.name == "light" {
			if bone := themeToken(t, css, th.block, "--bone"); hover != bone {
				t.Errorf("light: --row-hover (%s) は --bone (%s) と同じ値", hover, bone)
			}
		}
		mute := themeToken(t, css, th.block, "--mute")
		if got := contrast(t, mute, hover); got < 4.5 {
			t.Errorf("%s: --mute (%s) の --row-hover (%s) 上のコントラスト比 %.2f が AA 4.5 に届かない", th.name, mute, hover, got)
		}
		// 中立チップ（--accent-soft の地）が行 hover の地に溶けない: ライトは --surface-sel より離す。
		chip := themeToken(t, css, th.block, "--accent-soft")
		surfaceSel := themeToken(t, css, th.block, "--surface-sel")
		if th.name == "light" && contrast(t, chip, hover) <= contrast(t, chip, surfaceSel) {
			t.Errorf("light: チップ地 %s と --row-hover %s の差が --surface-sel %s 以下（輪郭が溶ける）", chip, hover, surfaceSel)
		}
	}
	// prefers-color-scheme のダークも同じ値（ダークの値は 2 か所に置く）。
	if got := strings.Count(css, "--row-hover: #1e1e21;"); got != 2 {
		t.Errorf("dark --row-hover: #1e1e21 defined %d times, want 2 ([data-theme=\"dark\"] and prefers-color-scheme)", got)
	}
}

// #283: 行の hover の地は --row-hover で、行の中の text-mute は切り替えない（group-hover:text-mute-on-soft を持たない）。
// 行は group も持たない。--accent-soft 地で hover するタスクのカード（sm 未満。taskCard）だけが、--mute-on-soft に切り替える。
func TestMuteTextInHoverRowsStaysMute(t *testing.T) {
	const switched = "group-hover:text-mute-on-soft"
	rowOpen := regexp.MustCompile(`<tr class="([^"]*\bhover:bg-\(--row-hover\)[^"]*)"`)
	rows := 0
	for _, file := range []string{
		"organisms/app_log.templ", "organisms/audit.templ", "organisms/clients.templ",
		"organisms/request_history.templ", "organisms/tasks.templ",
	} {
		src := readFile(t, file)
		for _, loc := range rowOpen.FindAllStringSubmatchIndex(src, -1) {
			rows++
			if regexp.MustCompile(`(^|\s)group(\s|$)`).MatchString(src[loc[2]:loc[3]]) {
				t.Errorf("%s:%d: 行に group が残っている（group-hover の切り替えは外した）", file, strings.Count(src[:loc[0]], "\n")+1)
			}
		}
		// ファイルの中で切り替えが残るのは taskCard（tasks.templ）だけ。
		src2 := src
		if file == "organisms/tasks.templ" {
			start := strings.Index(src, "templ taskCard(")
			end := strings.Index(src[start:], "\n}\n")
			if start < 0 || end < 0 {
				t.Fatalf("%s: templ taskCard が見つからない", file)
			}
			src2 = src[:start] + src[start+end:]
		}
		if strings.Contains(src2, switched) {
			t.Errorf("%s: 行の中に %s が残っている（--row-hover の上は --mute で AA に届く）", file, switched)
		}
		if strings.Contains(src, "hover:bg-(--table-row-hover)") {
			t.Errorf("%s: --table-row-hover は --row-hover になった", file)
		}
	}
	// 正規表現が 1 件も当たらないと、検査が空振りのまま緑になる（hover の書き方を変えたときの見落とし）。表の行は 5 ファイルに 7 行ある。
	if rows < 7 {
		t.Errorf("hover 付きの表の行が %d 行しか見つからない（7 行以上のはず。rowOpen を見直す）", rows)
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
