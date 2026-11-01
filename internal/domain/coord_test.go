package domain_test

import (
	"errors"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

const defaultMaxDim = 1568

var (
	// 1920x1080 / 150%: 縮小される（0.8166...、縮小後 1568x882）。
	fhd = domain.Monitor{Index: 0, Primary: true, Origin: domain.Point{}, Size: domain.Size{W: 1920, H: 1080}, DPI: 144}
	// 1280x720: 長辺が上限以下なので縮小しない。
	hd = domain.Monitor{Index: 1, Origin: domain.Point{X: 1920}, Size: domain.Size{W: 1280, H: 720}, DPI: 96}
	// プライマリの左・上に置かれたモニター（原点が負）。
	left = domain.Monitor{Index: 2, Origin: domain.Point{X: -2560, Y: -200}, Size: domain.Size{W: 2560, H: 1440}, DPI: 192}
	// 縦置き 1080x1920（長辺は高さ）。
	portrait = domain.Monitor{Index: 3, Origin: domain.Point{X: 3200, Y: -420}, Size: domain.Size{W: 1080, H: 1920}, DPI: 120}
)

func TestMonitorScale(t *testing.T) {
	tests := []struct {
		name   string
		m      domain.Monitor
		maxDim int
		want   float64
	}{
		{"長辺が上限を超えると縮小", fhd, defaultMaxDim, 1568.0 / 1920.0},
		{"長辺が上限ちょうどなら縮小しない", domain.Monitor{Size: domain.Size{W: 1568, H: 882}}, defaultMaxDim, 1},
		{"長辺が上限より 1 大きいと縮小", domain.Monitor{Size: domain.Size{W: 1569, H: 882}}, defaultMaxDim, 1568.0 / 1569.0},
		{"上限以下は 1 を超えない", hd, defaultMaxDim, 1},
		{"縦置きは高さが長辺", portrait, defaultMaxDim, 1568.0 / 1920.0},
		{"4K", left, defaultMaxDim, 1568.0 / 2560.0},
		{"上限が正でなければ縮小しない", fhd, 0, 1},
		{"空のモニターは縮小しない", domain.Monitor{}, defaultMaxDim, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.Scale(tt.maxDim); got != tt.want {
				t.Errorf("Scale() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMonitorScaledSize(t *testing.T) {
	tests := []struct {
		name   string
		m      domain.Monitor
		maxDim int
		want   domain.Size
	}{
		{"16:9 は 1568x882", fhd, defaultMaxDim, domain.Size{W: 1568, H: 882}},
		{"縮小なし", hd, defaultMaxDim, domain.Size{W: 1280, H: 720}},
		{"縦置き", portrait, defaultMaxDim, domain.Size{W: 882, H: 1568}},
		{"4K 16:9", left, defaultMaxDim, domain.Size{W: 1568, H: 882}},
		// 短辺は四捨五入し、最小 1。
		{"極端に細長い短辺は 1", domain.Monitor{Size: domain.Size{W: 10000, H: 1}}, defaultMaxDim, domain.Size{W: 1568, H: 1}},
		{"0.5 は切り上げ", domain.Monitor{Size: domain.Size{W: 200, H: 5}}, 100, domain.Size{W: 100, H: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.ScaledSize(tt.maxDim); got != tt.want {
				t.Errorf("ScaledSize() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScaledToPhysical(t *testing.T) {
	tests := []struct {
		name    string
		m       domain.Monitor
		maxDim  int
		in      domain.Point
		want    domain.Point
		wantErr error
	}{
		// 端
		{"左上端", fhd, defaultMaxDim, pt(0, 0), pt(0, 0), nil},
		{"右下端は物理の最終ピクセルに収まる", fhd, defaultMaxDim, pt(1567, 881), pt(1919, 1079), nil},
		{"縮小なしの右下端", hd, defaultMaxDim, pt(1279, 719), pt(1920+1279, 719), nil},
		// 範囲外
		{"x が右端の外", fhd, defaultMaxDim, pt(1568, 0), domain.Point{}, domain.ErrInvalidCoordinates},
		{"y が下端の外", fhd, defaultMaxDim, pt(0, 882), domain.Point{}, domain.ErrInvalidCoordinates},
		{"x が負", fhd, defaultMaxDim, pt(-1, 0), domain.Point{}, domain.ErrInvalidCoordinates},
		{"y が負", fhd, defaultMaxDim, pt(0, -1), domain.Point{}, domain.ErrInvalidCoordinates},
		{"縮小なしの右端の外", hd, defaultMaxDim, pt(1280, 0), domain.Point{}, domain.ErrInvalidCoordinates},
		// 丸め: 縮小率 0.5 のとき x=1 → 2、縮小率 2/3 のとき x=1 → 1.5 → 2（.5 は切り上げ）
		{"丸め: ちょうど割り切れる", domain.Monitor{Size: domain.Size{W: 200, H: 100}}, 100, pt(1, 1), pt(2, 2), nil},
		{"丸め: .5 は切り上げ", domain.Monitor{Size: domain.Size{W: 300, H: 150}}, 200, pt(1, 1), pt(2, 2), nil},
		{"丸め: .5 未満は切り捨て", fhd, defaultMaxDim, pt(1, 1), pt(1, 1), nil},                // 1.2245
		{"丸め: .5 以上は切り上げ", fhd, defaultMaxDim, pt(2, 2), pt(2, 2), nil},                // 2.449
		{"丸め: 3 → 3.6735 は 4", fhd, defaultMaxDim, pt(3, 3), pt(4, 4), nil},            // 3.6735
		{"丸め: 400 → 489.8 は 490", fhd, defaultMaxDim, pt(400, 400), pt(490, 490), nil}, // 489.795...
		// 複数モニター: 原点を加算する（負の原点を含む）
		{"2 台目の左上", hd, defaultMaxDim, pt(0, 0), pt(1920, 0), nil},
		{"2 台目の中央", hd, defaultMaxDim, pt(640, 360), pt(2560, 360), nil},
		{"負の原点の左上", left, defaultMaxDim, pt(0, 0), pt(-2560, -200), nil},
		{"負の原点の右下端（物理の最終ピクセルは丸めで届かない）", left, defaultMaxDim, pt(1567, 881), pt(-2560+2558, -200+1438), nil},
		{"縦置きの右下端", portrait, defaultMaxDim, pt(881, 1567), pt(3200+1079, -420+1919), nil},
		{"縦置きは幅側の範囲が狭い", portrait, defaultMaxDim, pt(882, 0), domain.Point{}, domain.ErrInvalidCoordinates},
		// DPI は変換に影響しない（物理座標を使う。プロセスは Per-Monitor DPI Aware V2）
		{"DPI 100%", withDPI(fhd, 96), defaultMaxDim, pt(784, 441), pt(960, 540), nil},
		{"DPI 200%", withDPI(fhd, 192), defaultMaxDim, pt(784, 441), pt(960, 540), nil},
		{"DPI 未設定", withDPI(fhd, 0), defaultMaxDim, pt(784, 441), pt(960, 540), nil},
		// 不正な入力
		{"上限が 0", fhd, 0, pt(0, 0), domain.Point{}, domain.ErrInvalidArgument},
		{"空のモニター", domain.Monitor{}, defaultMaxDim, pt(0, 0), domain.Point{}, domain.ErrInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.m.ScaledToPhysical(tt.in, tt.maxDim)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("ScaledToPhysical(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func withDPI(m domain.Monitor, dpi float64) domain.Monitor {
	m.DPI = dpi
	return m
}

func TestScaledToPhysicalErrorDetail(t *testing.T) {
	_, err := fhd.ScaledToPhysical(domain.Point{X: 1568, Y: 0}, defaultMaxDim)
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("not a *domain.Error: %v", err)
	}
	if de.Code != domain.CodeInvalidCoordinates {
		t.Errorf("code = %q", de.Code)
	}
	if de.Detail["width"] != 1568 || de.Detail["height"] != 882 || de.Detail["monitor"] != 0 {
		t.Errorf("detail = %v", de.Detail)
	}
}

func TestPhysicalToScaled(t *testing.T) {
	tests := []struct {
		name string
		m    domain.Monitor
		in   domain.Point
		want domain.Point
	}{
		{"左上端", fhd, pt(0, 0), pt(0, 0)},
		{"右下端は縮小範囲の最終ピクセル", fhd, pt(1919, 1079), pt(1567, 881)},
		{"中央", fhd, pt(960, 540), pt(784, 441)},
		{"縮小なし", hd, pt(1920+100, 50), pt(100, 50)},
		{"負の原点", left, pt(-2560+1280, -200+720), pt(784, 441)},
		{"縦置きの右下端", portrait, pt(3200+1079, -420+1919), pt(881, 1567)},
		// モニターの外は範囲を検査せずそのまま計算する
		{"右の外", fhd, pt(1920, 0), pt(1568, 0)},
		{"左の外は負", fhd, pt(-100, 0), pt(-82, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.PhysicalToScaled(tt.in, defaultMaxDim); got != tt.want {
				t.Errorf("PhysicalToScaled(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// 縮小 → 物理 → 縮小の往復は、すべての縮小座標で 1 px を超えてずれず、物理座標はモニターの内側に収まる。
func TestRoundTripWithinOnePixel(t *testing.T) {
	monitors := []domain.Monitor{
		fhd, hd, left, portrait,
		{Size: domain.Size{W: 3840, H: 2160}},
		{Size: domain.Size{W: 1569, H: 883}},
		{Size: domain.Size{W: 1567, H: 1}},
		{Size: domain.Size{W: 7680, H: 4320}, Origin: domain.Point{X: -7680}},
		{Size: domain.Size{W: 2000, H: 3}},
	}
	for _, maxDim := range []int{defaultMaxDim, 1000, 1, 3, 4096} {
		for i, m := range monitors {
			scaled := m.ScaledSize(maxDim)
			for y := range scaled.H {
				for x := range scaled.W {
					p, err := m.ScaledToPhysical(domain.Point{X: x, Y: y}, maxDim)
					if err != nil {
						t.Fatalf("monitor#%d maxDim=%d (%d,%d): %v", i, maxDim, x, y, err)
					}
					if !m.ContainsPhysical(p) {
						t.Fatalf("monitor#%d maxDim=%d (%d,%d) -> %v is outside the monitor", i, maxDim, x, y, p)
					}
					back := m.PhysicalToScaled(p, maxDim)
					if abs(back.X-x) > 1 || abs(back.Y-y) > 1 {
						t.Fatalf("monitor#%d maxDim=%d (%d,%d) -> %v -> %v: error exceeds 1px", i, maxDim, x, y, p, back)
					}
				}
			}
		}
	}
}

// モニター内の物理座標は、縮小座標に直した結果が必ず縮小スクリーンショットの範囲内に収まる。
func TestPhysicalToScaledStaysInRange(t *testing.T) {
	for _, m := range []domain.Monitor{fhd, left, portrait, {Size: domain.Size{W: 1999, H: 1001}}} {
		scaled := m.ScaledSize(defaultMaxDim)
		for y := range m.Size.H {
			for x := range m.Size.W {
				s := m.PhysicalToScaled(domain.Point{X: m.Origin.X + x, Y: m.Origin.Y + y}, defaultMaxDim)
				if s.X < 0 || s.X >= scaled.W || s.Y < 0 || s.Y >= scaled.H {
					t.Fatalf("monitor %v physical (%d,%d) -> %v is outside %v", m.Size, x, y, s, scaled)
				}
			}
		}
	}
}

func TestMonitorAt(t *testing.T) {
	monitors := []domain.Monitor{fhd, hd, left}
	tests := []struct {
		name   string
		p      domain.Point
		want   int
		wantOK bool
	}{
		{"プライマリの左上端", pt(0, 0), 0, true},
		{"プライマリの右下端", pt(1919, 1079), 0, true},
		{"プライマリの右端の外は 2 台目", pt(1920, 0), 1, true},
		{"2 台目の下端の外", pt(1920, 720), 0, false},
		{"負の座標のモニター", pt(-1, 0), 2, true},
		{"負の原点の左上端", pt(-2560, -200), 2, true},
		{"負の原点の外", pt(-2561, 0), 0, false},
		{"どれにも含まれない", pt(9999, 9999), 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ok := domain.MonitorAt(monitors, tt.p)
			if ok != tt.wantOK || (ok && m.Index != tt.want) {
				t.Errorf("MonitorAt(%v) = (%d, %v), want (%d, %v)", tt.p, m.Index, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestDPIScale(t *testing.T) {
	tests := []struct {
		dpi  float64
		want float64
	}{{0, 1}, {-1, 1}, {96, 1}, {120, 1.25}, {144, 1.5}, {192, 2}}
	for _, tt := range tests {
		if got := (domain.Monitor{DPI: tt.dpi}).DPIScale(); got != tt.want {
			t.Errorf("DPIScale(%v) = %v, want %v", tt.dpi, got, tt.want)
		}
	}
}

func pt(x, y int) domain.Point { return domain.Point{X: x, Y: y} }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func TestScaledRect(t *testing.T) {
	tests := []struct {
		name   string
		m      domain.Monitor
		r      domain.Rect
		want   domain.Rect
		wantOK bool
	}{
		{"縮小あり", fhd, domain.Rect{X: 960, Y: 540, W: 960, H: 540}, domain.Rect{X: 784, Y: 441, W: 784, H: 441}, true},
		{"縮小なしの 2 台目はモニターの原点を引く", hd, domain.Rect{X: 1920 + 100, Y: 50, W: 200, H: 100}, domain.Rect{X: 100, Y: 50, W: 200, H: 100}, true},
		{"負の原点", left, domain.Rect{X: -2560, Y: -200, W: 1280, H: 720}, domain.Rect{X: 0, Y: 0, W: 784, H: 441}, true},
		{"モニターからはみ出す部分は切り詰める", fhd, domain.Rect{X: 1800, Y: 1000, W: 500, H: 500}, domain.Rect{X: 1470, Y: 817, W: 98, H: 65}, true},
		{"重ならない", fhd, domain.Rect{X: 1920, Y: 0, W: 100, H: 100}, domain.Rect{}, false},
		{"別のモニターの左", hd, domain.Rect{X: 0, Y: 0, W: 1920, H: 1080}, domain.Rect{}, false},
		{"最小化の位置", fhd, domain.Rect{X: -32000, Y: -32000, W: 160, H: 28}, domain.Rect{}, false},
		{"空の矩形", fhd, domain.Rect{X: 10, Y: 10}, domain.Rect{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.m.ScaledRect(tt.r, defaultMaxDim)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("ScaledRect(%v) = %v, %v; want %v, %v", tt.r, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestStreamToPhysical(t *testing.T) {
	m := domain.Monitor{Index: 1, Origin: domain.Point{X: -1920, Y: 100}, Size: domain.Size{W: 1920, H: 1080}}
	tests := []struct {
		name    string
		in      domain.Point
		want    domain.Point
		wantErr bool
	}{
		{name: "左上", in: domain.Point{X: 0, Y: 0}, want: domain.Point{X: -1920, Y: 100}},
		{name: "右下の最後の画素", in: domain.Point{X: 1919, Y: 1079}, want: domain.Point{X: -1, Y: 1179}},
		{name: "右端の外", in: domain.Point{X: 1920, Y: 0}, wantErr: true},
		{name: "下端の外", in: domain.Point{X: 0, Y: 1080}, wantErr: true},
		{name: "負の x", in: domain.Point{X: -1, Y: 0}, wantErr: true},
		{name: "負の y", in: domain.Point{X: 0, Y: -1}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := m.StreamToPhysical(tt.in)
			if tt.wantErr {
				if !errors.Is(err, domain.ErrInvalidCoordinates) {
					t.Fatalf("want ErrInvalidCoordinates, got %v", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("StreamToPhysical(%v) = %v, %v; want %v", tt.in, got, err, tt.want)
			}
		})
	}
}
