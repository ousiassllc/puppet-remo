package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 操作中オーバーレイの配置（#86）。FR-H07: 画面の中身は隠さず、縁の細い枠と札だけを出す。

func layoutMonitors() map[string]domain.Monitor {
	return map[string]domain.Monitor{
		"fhd":           {Index: 0, Origin: domain.Point{}, Size: domain.Size{W: 1920, H: 1080}, DPI: 96},
		"4k-150%":       {Index: 1, Origin: domain.Point{X: 1920, Y: 0}, Size: domain.Size{W: 3840, H: 2160}, DPI: 144},
		"left-negative": {Index: 2, Origin: domain.Point{X: -2560, Y: -200}, Size: domain.Size{W: 2560, H: 1440}, DPI: 192},
		"no-dpi":        {Index: 3, Origin: domain.Point{X: 0, Y: 1080}, Size: domain.Size{W: 1280, H: 720}},
	}
}

func rectEnd(r domain.Rect) (int, int) { return r.X + r.W, r.Y + r.H }

func intersects(a, b domain.Rect) bool {
	ax, ay := rectEnd(a)
	bx, by := rectEnd(b)
	return a.X < bx && b.X < ax && a.Y < by && b.Y < ay
}

// 枠はモニターの中に収まり、互いに重ならず、真ん中（枠の太さだけ内側の矩形）には 1 px もかからない。
// 全体の面積もモニターのごく一部（FR-H07「中身を隠さない」。全面のウィンドウに戻る回帰を拾う）。
func TestOverlayBarsLeaveTheCenterUncovered(t *testing.T) {
	for name, m := range layoutMonitors() {
		t.Run(name, func(t *testing.T) {
			bars := overlayBars(m)
			b := overlayPx(m, overlayBorderDIP)
			inner := domain.Rect{X: m.Origin.X + b, Y: m.Origin.Y + b, W: m.Size.W - 2*b, H: m.Size.H - 2*b}
			screen := domain.Rect{X: m.Origin.X, Y: m.Origin.Y, W: m.Size.W, H: m.Size.H}
			area := 0
			for i, r := range bars {
				if r.Empty() {
					t.Fatalf("bar %d is empty: %+v", i, r)
				}
				if intersects(r, inner) {
					t.Errorf("bar %d %+v covers the center %+v", i, r, inner)
				}
				rx, ry := rectEnd(r)
				sx, sy := rectEnd(screen)
				if r.X < screen.X || r.Y < screen.Y || rx > sx || ry > sy {
					t.Errorf("bar %d %+v is outside the monitor %+v", i, r, screen)
				}
				for j := i + 1; j < len(bars); j++ {
					if intersects(r, bars[j]) {
						t.Errorf("bars %d and %d overlap: %+v %+v", i, j, r, bars[j])
					}
				}
				area += r.W * r.H
			}
			if got := float64(area) / float64(m.Size.W*m.Size.H); got > 0.02 {
				t.Errorf("bars cover %.2f%% of the monitor, want a thin frame (<= 2%%)", got*100)
			}
		})
	}
}

// 枠の太さは DPI に比例する（100% で 4 px、150% で 6 px、200% で 8 px。DPI 未設定は 100%）。
func TestOverlayBorderScalesWithDPI(t *testing.T) {
	ms := layoutMonitors()
	for name, want := range map[string]int{"fhd": 4, "4k-150%": 6, "left-negative": 8, "no-dpi": 4} {
		if got := overlayBars(ms[name])[0].H; got != want {
			t.Errorf("%s: top bar thickness = %d, want %d", name, got, want)
		}
	}
}

// 小さすぎるモニターでは枠を作らず、モニター全体の 1 本だけにする（4 本が重ならない）。
func TestOverlayBarsOnATinyMonitor(t *testing.T) {
	m := domain.Monitor{Size: domain.Size{W: 6, H: 100}, DPI: 96}
	bars := overlayBars(m)
	if bars[0].W != 6 || bars[0].H != 100 {
		t.Errorf("bars[0] = %+v, want the whole monitor", bars[0])
	}
	for i := 1; i < len(bars); i++ {
		if !bars[i].Empty() {
			t.Errorf("bars[%d] = %+v, want empty", i, bars[i])
		}
	}
}

// 札は上端の中央に置く。幅の上限（モニター幅の 60%）を超えない。
func TestOverlayLabelRect(t *testing.T) {
	m := layoutMonitors()["fhd"]
	r := overlayLabelRect(m, 300, 40)
	if r.W != 300+2*16 || r.H != 40+2*4 {
		t.Errorf("size = %dx%d, want %dx%d (text plus padding)", r.W, r.H, 300+2*16, 40+2*4)
	}
	if r.Y != m.Origin.Y {
		t.Errorf("Y = %d, want the top of the monitor %d", r.Y, m.Origin.Y)
	}
	if center := r.X + r.W/2; center != m.Origin.X+m.Size.W/2 {
		t.Errorf("center X = %d, want %d", center, m.Origin.X+m.Size.W/2)
	}

	wide := overlayLabelRect(m, 100000, 40)
	if wide.W > m.Size.W*60/100 {
		t.Errorf("width = %d, want <= 60%% of %d", wide.W, m.Size.W)
	}
	if wide.X < m.Origin.X || wide.X+wide.W > m.Origin.X+m.Size.W {
		t.Errorf("label %+v is outside the monitor", wide)
	}

	neg := layoutMonitors()["left-negative"]
	if r := overlayLabelRect(neg, 300, 40); r.X < neg.Origin.X || r.Y != neg.Origin.Y {
		t.Errorf("label %+v is not at the top of the monitor with a negative origin %+v", r, neg.Origin)
	}
}

// 札の文言: 改行で 2 行まで。各行は前後の空白を取り、80 文字（末尾の「…」を含む）に切り詰める。3 行目以降は捨てる。
// HTML の記号は文字のまま通す（ネイティブの描画は HTML として解釈しない）。
func TestSplitOverlayLabel(t *testing.T) {
	long := strings.Repeat("あ", 100)
	for _, tc := range []struct{ name, in, first, detail string }{
		{"one line", "操作中: alice（MCP）", "操作中: alice（MCP）", ""},
		{"two lines", "操作中: bot（エージェント）\nエージェント実行中: メモ帳", "操作中: bot（エージェント）", "エージェント実行中: メモ帳"},
		{"third line dropped", "a\nb\nc", "a", "b"},
		{"trimmed", "  a \n  b  ", "a", "b"},
		{"html is text", "<script>alert(1)</script>", "<script>alert(1)</script>", ""},
		{"long", long + "\n" + long, strings.Repeat("あ", 79) + "…", strings.Repeat("あ", 79) + "…"},
		{"exactly the limit", strings.Repeat("a", 80), strings.Repeat("a", 80), ""},
		{"control chars dropped", "a\x00b\tc\x1b\nd\x00", "abc", "d"},
		{"empty", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, detail := splitOverlayLabel(tc.in)
			if first != tc.first || detail != tc.detail {
				t.Errorf("splitOverlayLabel(%q) = (%q, %q), want (%q, %q)", tc.in, first, detail, tc.first, tc.detail)
			}
		})
	}
}

// 枠は外側の線（--ink）と内側の線（--bone）の 2 重線。外側の線はモニターの縁から overlayOuterDIP の輪で、4 本をまたいで途切れず
// （角で内側の線が外へ食い込まない）、各本の残りが内側の線になる。ウィンドウの左上が原点。
func TestOverlayOuterInkIsAContinuousRing(t *testing.T) {
	for name, m := range layoutMonitors() {
		t.Run(name, func(t *testing.T) {
			outer, border := overlayPx(m, overlayOuterDIP), overlayPx(m, overlayBorderDIP)
			if outer >= border {
				t.Fatalf("outer line %d px must leave an inner line within the %d px border", outer, border)
			}
			// 枠の各画素を、モニターの縁からの距離で外側（< outer）・内側（< border）に分けた結果と、overlayOuterInk の塗りが一致する。
			for i, bar := range overlayBars(m) {
				ink := overlayOuterInk(m, bar)
				for y := range bar.H {
					for x := range bar.W {
						gx, gy := bar.X+x-m.Origin.X, bar.Y+y-m.Origin.Y
						dist := min(gx, gy, m.Size.W-1-gx, m.Size.H-1-gy)
						if dist >= border {
							t.Fatalf("bar %d pixel (%d,%d) is %d px from the edge, beyond the border", i, x, y, dist)
						}
						painted := false
						for _, r := range ink {
							if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
								painted = true
							}
						}
						if painted != (dist < outer) {
							t.Fatalf("bar %d pixel (%d,%d): outer-ink = %v, want %v (distance %d, outer %d px)", i, x, y, painted, dist < outer, dist, outer)
						}
					}
				}
			}
		})
	}
}

// 小さすぎるモニターの 1 本（モニター全体）でも、外側の線の範囲は枠の中に収まる。
func TestOverlayOuterInkOnATinyMonitor(t *testing.T) {
	m := domain.Monitor{Size: domain.Size{W: 6, H: 100}, DPI: 96}
	bar := overlayBars(m)[0]
	for _, r := range overlayOuterInk(m, bar) {
		if r.X < 0 || r.Y < 0 || r.X+r.W > bar.W || r.Y+r.H > bar.H {
			t.Errorf("ink %+v is outside the bar %+v", r, bar)
		}
	}
}

// 色は app.css のトークンの値（ライト: --ink #050505・--bone #f5f5f4・--surface #ffffff、ダーク: --ink #ffffff・--bone #050505・--surface #141414）。
// 枠は外側が --ink・内側が --bone、札は --surface の地に --ink の文字。COLORREF は 0x00BBGGRR。
func TestOverlayColorsMatchTheTokens(t *testing.T) {
	if got := colorRef(0xf5f5f4); got != 0xf4f5f5 {
		t.Errorf("colorRef(0xf5f5f4) = %#x, want 0xf4f5f5", got)
	}
	if got := colorRef(0x123456); got != 0x563412 {
		t.Errorf("colorRef(0x123456) = %#x, want 0x563412", got)
	}
	want := map[bool]overlayColors{
		false: {Outer: 0x050505, Inner: 0xf4f5f5, LabelBG: 0xffffff, LabelText: 0x050505},
		true:  {Outer: 0xffffff, Inner: 0x050505, LabelBG: 0x141414, LabelText: 0xffffff},
	}
	for dark, w := range want {
		if got := overlayColorsFor(dark); got != w {
			t.Errorf("overlayColorsFor(%v) = %+v, want %+v", dark, got, w)
		}
	}
}

// overlayColorsFor の 16 進は、app.css の :root（ライト）と [data-theme="dark"]（ダーク）のトークンと同じ値（コピーのずれを拾う）。
func TestOverlayColorsMatchAppCSS(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "static", "src", "css", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	token := func(block, name string) uint32 {
		t.Helper()
		start := strings.Index(string(src), block)
		if start < 0 {
			t.Fatalf("app.css lacks %q", block)
		}
		end := strings.Index(string(src[start:]), "}")
		m := regexp.MustCompile(`\n\s*` + regexp.QuoteMeta(name) + `: #([0-9a-f]{6});`).FindStringSubmatch(string(src[start : start+end]))
		if m == nil {
			t.Fatalf("%s lacks %s", block, name)
		}
		v, err := strconv.ParseUint(m[1], 16, 32)
		if err != nil {
			t.Fatal(err)
		}
		return uint32(v)
	}
	for _, tt := range []struct {
		dark  bool
		block string
	}{{false, ":root {"}, {true, `[data-theme="dark"] {`}} {
		want := overlayColors{
			Outer:     colorRef(token(tt.block, "--ink")),
			Inner:     colorRef(token(tt.block, "--bone")),
			LabelBG:   colorRef(token(tt.block, "--surface")),
			LabelText: colorRef(token(tt.block, "--ink")),
		}
		if got := overlayColorsFor(tt.dark); got != want {
			t.Errorf("overlayColorsFor(%v) = %+v, app.css tokens give %+v", tt.dark, got, want)
		}
	}
}
