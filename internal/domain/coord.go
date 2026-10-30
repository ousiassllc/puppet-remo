package domain

import "fmt"

// Point は座標。使う座標系は文脈で決まる（MCP は縮小座標、WebSocket・platform は物理座標）。
type Point struct{ X, Y int }

// Size は幅と高さ。
type Size struct{ W, H int }

// Monitor はモニター 1 台。Origin は仮想デスクトップ上の物理座標で、左や上に置かれたモニターは負になりうる。
type Monitor struct {
	Index   int
	Name    string
	Primary bool
	Origin  Point   // 仮想デスクトップ上の物理座標
	Size    Size    // 物理ピクセル
	DPI     float64 // 実効 DPI（96 が 100%）。座標変換には影響しない
}

// DPIScale は DPI スケール（1.0 = 100%）。DPI が未設定なら 1.0。
func (m Monitor) DPIScale() float64 {
	if m.DPI <= 0 {
		return 1
	}
	return m.DPI / 96
}

// longSide はモニターの長辺（物理ピクセル）。
func (m Monitor) longSide() int { return max(m.Size.W, m.Size.H) }

// ratio は縮小率 min(1, maxDim ÷ 長辺) を整数の比 num/den で返す（FR-M16）。
// 丸めを浮動小数点の誤差に左右させないため、変換は整数演算で行う。
// maxDim が正でない、またはモニターが空のときは縮小しない（1/1）。
func (m Monitor) ratio(maxDim int) (num, den int) {
	long := m.longSide()
	if maxDim <= 0 || long <= 0 || long <= maxDim {
		return 1, 1
	}
	return maxDim, long
}

// Scale は縮小率 min(1, maxDim ÷ 長辺) を返す（FR-M16）。
// モニターごとに決定的に決まり、スクリーンショットの履歴に依存しない。
func (m Monitor) Scale(maxDim int) float64 {
	num, den := m.ratio(maxDim)
	return float64(num) / float64(den)
}

// ScaledSize は縮小スクリーンショットの大きさ（各辺を四捨五入。最小 1）。
func (m Monitor) ScaledSize(maxDim int) Size {
	num, den := m.ratio(maxDim)
	return Size{
		W: scaleLength(m.Size.W, num, den),
		H: scaleLength(m.Size.H, num, den),
	}
}

func scaleLength(n, num, den int) int {
	if n <= 0 {
		return 0
	}
	return max(1, divRound(n*num, den))
}

// ContainsPhysical は物理座標 p（仮想デスクトップ上）がこのモニターの内側かを返す。
func (m Monitor) ContainsPhysical(p Point) bool {
	return p.X >= m.Origin.X && p.X < m.Origin.X+m.Size.W &&
		p.Y >= m.Origin.Y && p.Y < m.Origin.Y+m.Size.H
}

// ScaledToPhysical は縮小座標 p（このモニターの縮小スクリーンショットの左上が原点）を、
// 仮想デスクトップ上の物理座標へ変換する: round(p ÷ 縮小率) + モニターの原点（FR-M16）。
// 縮小スクリーンショットの範囲外は ErrInvalidCoordinates（FR-M17）。
func (m Monitor) ScaledToPhysical(p Point, maxDim int) (Point, error) {
	if m.Size.W <= 0 || m.Size.H <= 0 {
		return Point{}, ErrInvalidArgument.WithMsg(fmt.Sprintf("monitor %d has an empty size", m.Index))
	}
	if maxDim <= 0 {
		return Point{}, ErrInvalidArgument.WithMsg("max dimension must be positive")
	}
	scaled := m.ScaledSize(maxDim)
	if p.X < 0 || p.X >= scaled.W || p.Y < 0 || p.Y >= scaled.H {
		return Point{}, ErrInvalidCoordinates.
			WithMsg(fmt.Sprintf("(%d, %d) is outside the %dx%d screenshot of monitor %d", p.X, p.Y, scaled.W, scaled.H, m.Index)).
			WithDetail("monitor", m.Index).
			WithDetail("width", scaled.W).
			WithDetail("height", scaled.H)
	}
	num, den := m.ratio(maxDim)
	return Point{
		X: m.Origin.X + divRound(p.X*den, num),
		Y: m.Origin.Y + divRound(p.Y*den, num),
	}, nil
}

// PhysicalToScaled は仮想デスクトップ上の物理座標 p を、このモニターの縮小座標へ変換する。
// モニターの内側の点は、縮小スクリーンショットの範囲内に収めて返す（右端・下端の丸めで 1 px はみ出さない）。
// モニターの外側の点は範囲を検査せず、計算した値をそのまま返す。
// ScaledToPhysical との往復の誤差は 1 px を超えない。
func (m Monitor) PhysicalToScaled(p Point, maxDim int) Point {
	num, den := m.ratio(maxDim)
	out := Point{
		X: divRound((p.X-m.Origin.X)*num, den),
		Y: divRound((p.Y-m.Origin.Y)*num, den),
	}
	if m.ContainsPhysical(p) {
		scaled := m.ScaledSize(maxDim)
		out.X = min(out.X, scaled.W-1)
		out.Y = min(out.Y, scaled.H-1)
	}
	return out
}

// MonitorAt は物理座標 p を含むモニターを返す。どれにも含まれなければ false。
func MonitorAt(monitors []Monitor, p Point) (Monitor, bool) {
	for _, m := range monitors {
		if m.ContainsPhysical(p) {
			return m, true
		}
	}
	return Monitor{}, false
}

// divRound は a ÷ b（b > 0）を四捨五入する（.5 は正の無限大方向）。負の a でも床除算で一貫する。
func divRound(a, b int) int {
	n := 2*a + b
	q := n / (2 * b)
	if n%(2*b) < 0 {
		q--
	}
	return q
}
