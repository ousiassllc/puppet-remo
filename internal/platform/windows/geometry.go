package windows

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

const (
	// wheelDelta は 1 ノッチのホイール移動量（WHEEL_DELTA）。
	wheelDelta = 120
	// maxWheelNotches は Scroll の amount と Wheel の dx / dy の絶対値の上限（API 仕様 §3 / §5.3）。
	maxWheelNotches = 50
	// absoluteRange は SendInput の絶対座標の範囲（0〜65535）。
	absoluteRange = 65536
	// dragSteps は Drag の途中の移動の数。アプリがドラッグを検出できるように、押下から解放までの間に移動を挟む。
	dragSteps = 8
)

// normalizeAbs は仮想デスクトップ上の物理ピクセル v（origin から size ピクセルの範囲）を、
// SendInput の絶対座標（0〜65535 が仮想デスクトップ全体）へ変換する。
// Windows は絶対座標を v' = n * size / 65536（切り捨てか四捨五入）で画素へ戻すので、切り上げた n は
// どちらの丸めでも元の画素 v に戻る（size < 65536 のとき）。
func normalizeAbs(v, origin, size int) int {
	if size <= 0 {
		return 0
	}
	n := ((v-origin)*absoluteRange + size - 1) / size
	return min(max(n, 0), absoluteRange-1)
}

// wheelCarry はホイールの丸めの端数（1/120 ノッチ未満）を、次の呼び出しへ持ち越す。専用スレッドが所有する。
type wheelCarry struct{ x, y float64 }

// take は dx / dy ノッチ（dy > 0 が下、dx > 0 が右）を Win32 の移動量にする。
// × 120 を四捨五入して整数にし、端数を持ち越す。縦は Win32 の正が上向きなので符号を反転する。
func (c *wheelCarry) take(dx, dy float64) (horizontal, vertical int32) {
	horizontal, c.x = roundWithCarry(c.x + dx*wheelDelta)
	vertical, c.y = roundWithCarry(c.y - dy*wheelDelta)
	return horizontal, vertical
}

func (c *wheelCarry) reset() { *c = wheelCarry{} }

func roundWithCarry(total float64) (int32, float64) {
	n := math.Round(total)
	return int32(n), total - n
}

// wheelSteps は移動量 delta を、1 イベントあたり 120（1 ノッチ）以下に分けたホイールのイベントにする。
// 0 は何も返さない。
func wheelSteps(horizontal bool, delta int32) []inputStep {
	var steps []inputStep
	for delta != 0 {
		chunk := max(min(delta, wheelDelta), -wheelDelta)
		steps = append(steps, wheelStep(horizontal, chunk))
		delta -= chunk
	}
	return steps
}

// scrollNotches は Scroll の方向と量を、Wheel と同じ符号（dy > 0 が下、dx > 0 が右）のノッチにする。
func scrollNotches(dir domain.Direction, amount int) (dx, dy float64, ok bool) {
	n := float64(amount)
	switch dir {
	case domain.DirUp:
		return 0, -n, true
	case domain.DirDown:
		return 0, n, true
	case domain.DirLeft:
		return -n, 0, true
	case domain.DirRight:
		return n, 0, true
	}
	return 0, 0, false
}

// dragPath は from から to までの途中の点（from を除き、to を含む）。from == to なら空。
func dragPath(from, to domain.Point) []domain.Point {
	if from == to {
		return nil
	}
	path := make([]domain.Point, 0, dragSteps)
	for i := 1; i <= dragSteps; i++ {
		path = append(path, domain.Point{
			X: from.X + roundDiv((to.X-from.X)*i, dragSteps),
			Y: from.Y + roundDiv((to.Y-from.Y)*i, dragSteps),
		})
	}
	return path
}

// roundDiv は a ÷ b（b > 0）を 0 から遠い方へ四捨五入する。
func roundDiv(a, b int) int {
	if a < 0 {
		return -((-a + b/2) / b)
	}
	return (a + b/2) / b
}

// orderMonitors はモニターを左から右、上から下の順に並べて、Index を 0 から振り直す。
// 並べ方は接続順や列挙順に依らず、配置が同じなら Index も同じになる。
func orderMonitors(monitors []domain.Monitor) []domain.Monitor {
	out := slices.Clone(monitors)
	slices.SortStableFunc(out, func(a, b domain.Monitor) int {
		if a.Origin.X != b.Origin.X {
			return a.Origin.X - b.Origin.X
		}
		return a.Origin.Y - b.Origin.Y
	})
	for i := range out {
		out[i].Index = i
	}
	return out
}

// monitorIndexFor は r と最も広く重なるモニターの Index を返す。どれとも重ならなければプライマリ（なければ先頭）。
func monitorIndexFor(monitors []domain.Monitor, r domain.Rect) int {
	best, bestArea := -1, 0
	fallback := 0
	for i, m := range monitors {
		if i == 0 || m.Primary {
			fallback = m.Index
		}
		if area := overlapArea(r, domain.Rect{X: m.Origin.X, Y: m.Origin.Y, W: m.Size.W, H: m.Size.H}); area > bestArea {
			best, bestArea = m.Index, area
		}
	}
	if best < 0 {
		return fallback
	}
	return best
}

func overlapArea(a, b domain.Rect) int {
	w := min(a.X+a.W, b.X+b.W) - max(a.X, b.X)
	h := min(a.Y+a.H, b.Y+b.H) - max(a.Y, b.Y)
	if w <= 0 || h <= 0 {
		return 0
	}
	return w * h
}

// formatWindowID は HWND を ID（10 進文字列）にする。
func formatWindowID(hwnd uintptr) string { return strconv.FormatUint(uint64(uint32(hwnd)), 10) }

// parseWindowID は ID を HWND に戻す。10 進の正準形（先頭の 0・符号なし）で、0 でも 32 ビットを超えてもいない場合だけ ok。
// HWND は 64 ビットのプロセスでも 32 ビットに収まる（符号拡張しても同じ値になる）。
func parseWindowID(id string) (uintptr, bool) {
	v, err := strconv.ParseUint(id, 10, 32)
	if err != nil || v == 0 || strconv.FormatUint(v, 10) != id {
		return 0, false
	}
	return uintptr(v), true
}

// processBaseName は実行ファイルのパス（`C:\Windows\notepad.exe`）からファイル名（notepad.exe）を取り出す。
func processBaseName(path string) string {
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		return path[i+1:]
	}
	return path
}
