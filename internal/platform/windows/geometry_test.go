package windows

import (
	"reflect"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// Windows は正規化した絶対座標 n を v' = n × size ÷ 65536 で画素へ戻す（切り捨てと四捨五入のどちらの実装でも）。
// どの画素も、往復で元の画素に戻る。
func TestNormalizeAbsRoundTrip(t *testing.T) {
	sizes := []struct{ origin, size int }{
		{0, 1920}, {0, 3840}, {-1920, 3840}, {-2560, 5120}, {0, 7680}, {-1080, 3000}, {0, 1},
	}
	for _, s := range sizes {
		for v := s.origin; v < s.origin+s.size; v++ {
			n := normalizeAbs(v, s.origin, s.size)
			if n < 0 || n >= absoluteRange {
				t.Fatalf("normalizeAbs(%d, %d, %d) = %d, out of range", v, s.origin, s.size, n)
			}
			floor := n*s.size/absoluteRange + s.origin
			round := (n*s.size+absoluteRange/2)/absoluteRange + s.origin
			if floor != v || round != v {
				t.Fatalf("size %d origin %d: pixel %d -> %d -> floor %d, round %d", s.size, s.origin, v, n, floor, round)
			}
		}
	}
}

func TestNormalizeAbsEdges(t *testing.T) {
	if got := normalizeAbs(0, 0, 0); got != 0 {
		t.Errorf("empty size = %d, want 0", got)
	}
	if got := normalizeAbs(-5, 0, 100); got != 0 {
		t.Errorf("below origin = %d, want clamped to 0", got)
	}
	if got := normalizeAbs(1000, 0, 100); got != absoluteRange-1 {
		t.Errorf("above range = %d, want clamped to %d", got, absoluteRange-1)
	}
}

func TestWheelCarry(t *testing.T) {
	var c wheelCarry
	// 縦は符号を反転（dy > 0 が下 = Win32 では負）、横はそのまま。
	if h, v := c.take(1, 1); h != 120 || v != -120 {
		t.Errorf("take(1, 1) = %d, %d; want 120, -120", h, v)
	}
	if h, v := c.take(-0.5, -1.5); h != -60 || v != 180 {
		t.Errorf("take(-0.5, -1.5) = %d, %d; want -60, 180", h, v)
	}

	// 1/120 ノッチ未満の端数は持ち越され、合計は失われない。
	c.reset()
	total := int32(0)
	for range 1200 {
		_, v := c.take(0, 0.001) // 1200 回で合計 1.2 ノッチ = 144
		total += v
	}
	if total != -144 {
		t.Errorf("accumulated vertical = %d, want -144", total)
	}

	c.reset()
	if h, v := c.take(0, 0); h != 0 || v != 0 {
		t.Errorf("take(0, 0) = %d, %d", h, v)
	}
}

func TestWheelCarryResetDropsRemainder(t *testing.T) {
	var c wheelCarry
	c.take(0.004, 0) // 0.48 → 0、端数 0.48
	c.reset()
	if h, _ := c.take(0.004, 0); h != 0 {
		t.Errorf("after reset the remainder must be gone, got %d", h)
	}
}

func TestWheelSteps(t *testing.T) {
	if got := wheelSteps(false, 0); len(got) != 0 {
		t.Errorf("delta 0: %+v", got)
	}
	got := wheelSteps(false, -300)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(got), got)
	}
	var sum int32
	for _, s := range got {
		d := int32(s.data)
		if d < -wheelDelta || d > wheelDelta {
			t.Errorf("chunk %d exceeds WHEEL_DELTA", d)
		}
		if s.flags != mouseeventfWheel {
			t.Errorf("flags = %#x, want vertical wheel", s.flags)
		}
		sum += d
	}
	if sum != -300 {
		t.Errorf("sum = %d, want -300", sum)
	}
	if got := wheelSteps(true, 60); len(got) != 1 || got[0].flags != mouseeventfHWheel || int32(got[0].data) != 60 {
		t.Errorf("horizontal 60: %+v", got)
	}
}

func TestScrollNotches(t *testing.T) {
	tests := []struct {
		dir    domain.Direction
		dx, dy float64
		ok     bool
	}{
		{domain.DirUp, 0, -3, true},
		{domain.DirDown, 0, 3, true},
		{domain.DirLeft, -3, 0, true},
		{domain.DirRight, 3, 0, true},
		{"sideways", 0, 0, false},
	}
	for _, tt := range tests {
		dx, dy, ok := scrollNotches(tt.dir, 3)
		if dx != tt.dx || dy != tt.dy || ok != tt.ok {
			t.Errorf("scrollNotches(%q, 3) = %v, %v, %v; want %v, %v, %v", tt.dir, dx, dy, ok, tt.dx, tt.dy, tt.ok)
		}
	}
}

func TestDragPath(t *testing.T) {
	if got := dragPath(domain.Point{X: 5, Y: 5}, domain.Point{X: 5, Y: 5}); got != nil {
		t.Errorf("same point: %v", got)
	}
	from, to := domain.Point{X: 100, Y: 100}, domain.Point{X: 20, Y: 330}
	path := dragPath(from, to)
	if len(path) != dragSteps {
		t.Fatalf("len = %d, want %d", len(path), dragSteps)
	}
	if path[len(path)-1] != to {
		t.Errorf("last = %v, want %v", path[len(path)-1], to)
	}
	prev := from
	for _, p := range path {
		if p.X > prev.X || p.Y < prev.Y {
			t.Errorf("path not monotonic: %v after %v", p, prev)
		}
		prev = p
	}
}

func TestOrderMonitors(t *testing.T) {
	in := []domain.Monitor{
		{Index: 7, Name: "right", Primary: true, Origin: domain.Point{X: 0}},
		{Index: 3, Name: "far-right", Origin: domain.Point{X: 1920}},
		{Index: 9, Name: "left", Origin: domain.Point{X: -1920}},
		{Index: 1, Name: "left-below", Origin: domain.Point{X: -1920, Y: 1080}},
	}
	got := orderMonitors(in)
	var names []string
	for i, m := range got {
		names = append(names, m.Name)
		if m.Index != i {
			t.Errorf("monitor %q has index %d, want %d", m.Name, m.Index, i)
		}
	}
	if want := []string{"left", "left-below", "right", "far-right"}; !reflect.DeepEqual(names, want) {
		t.Errorf("order = %v, want %v", names, want)
	}
	if in[0].Index != 7 {
		t.Error("orderMonitors modified its input")
	}
}

func TestMonitorIndexFor(t *testing.T) {
	monitors := []domain.Monitor{
		{Index: 0, Origin: domain.Point{X: -1920}, Size: domain.Size{W: 1920, H: 1080}},
		{Index: 1, Primary: true, Origin: domain.Point{X: 0}, Size: domain.Size{W: 1920, H: 1080}},
	}
	tests := []struct {
		name string
		r    domain.Rect
		want int
	}{
		{"inside left", domain.Rect{X: -1800, Y: 100, W: 500, H: 400}, 0},
		{"inside primary", domain.Rect{X: 100, Y: 100, W: 500, H: 400}, 1},
		{"straddling, more on left", domain.Rect{X: -1000, Y: 0, W: 1200, H: 500}, 0},
		{"straddling, more on primary", domain.Rect{X: -200, Y: 0, W: 1200, H: 500}, 1},
		{"offscreen falls back to primary", domain.Rect{X: -32000, Y: -32000, W: 160, H: 28}, 1},
	}
	for _, tt := range tests {
		if got := monitorIndexFor(monitors, tt.r); got != tt.want {
			t.Errorf("%s: monitorIndexFor = %d, want %d", tt.name, got, tt.want)
		}
	}
	if got := monitorIndexFor(nil, domain.Rect{W: 1, H: 1}); got != 0 {
		t.Errorf("no monitors = %d, want 0", got)
	}
	noPrimary := []domain.Monitor{{Index: 4, Origin: domain.Point{X: 0}, Size: domain.Size{W: 10, H: 10}}}
	if got := monitorIndexFor(noPrimary, domain.Rect{X: 500, Y: 500, W: 5, H: 5}); got != 4 {
		t.Errorf("no primary fallback = %d, want 4", got)
	}
}

func TestWindowID(t *testing.T) {
	if got := formatWindowID(0x1234); got != "4660" {
		t.Errorf("formatWindowID = %q", got)
	}
	for _, id := range []string{"4660", "1", "4294967295"} {
		h, ok := parseWindowID(id)
		if !ok || formatWindowID(h) != id {
			t.Errorf("parseWindowID(%q) = %d, %v", id, h, ok)
		}
	}
	for _, id := range []string{"", "0", "-1", "00012", "+5", " 5", "5 ", "0x10", "4294967296", "no-such-window", "1e3"} {
		if _, ok := parseWindowID(id); ok {
			t.Errorf("parseWindowID(%q) should be rejected", id)
		}
	}
}

func TestProcessBaseName(t *testing.T) {
	tests := map[string]string{
		`C:\Windows\System32\notepad.exe`: "notepad.exe",
		`C:/Program Files/app/App.EXE`:    "App.EXE",
		`notepad.exe`:                     "notepad.exe",
		``:                                "",
	}
	for in, want := range tests {
		if got := processBaseName(in); got != want {
			t.Errorf("processBaseName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRoundDiv(t *testing.T) {
	tests := []struct{ a, b, want int }{{7, 2, 4}, {-7, 2, -4}, {5, 4, 1}, {-5, 4, -1}, {0, 8, 0}, {3, 8, 0}, {4, 8, 1}}
	for _, tt := range tests {
		if got := roundDiv(tt.a, tt.b); got != tt.want {
			t.Errorf("roundDiv(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
