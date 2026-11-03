package app

import (
	"math"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 操作中オーバーレイ（#69・#86）の配置。Windows ではモニターごとに「細い枠の 4 本」と「上端中央の札」を、それぞれ別の
// ネイティブウィンドウとして出す（wails_overlay_windows.go）。真ん中のウィンドウは作らないので、画面の中身は最初から覆わない。
// このファイルは配置の計算だけ（OS に依存しない）。ウィンドウの描画と出し入れは Windows 側が行う。

const (
	// overlayBorderDIP は枠の太さ（DIP。100% の DPI で 4 px）。
	overlayBorderDIP = 4
	// overlayLabelPadXDIP・overlayLabelPadYDIP は札の中の余白（DIP）。
	overlayLabelPadXDIP = 16
	overlayLabelPadYDIP = 4
	// overlayLabelMaxWidthPercent は札の幅の上限（モニター幅に対する割合）。超えた分は末尾を「…」にする。
	overlayLabelMaxWidthPercent = 60
	// overlayLabelMaxRunes は札の各行の長さの上限（文字数）。クライアント名は利用者が付けた任意の文字列なので切り詰める（O11）。
	overlayLabelMaxRunes = 80
)

// overlayScale は DPI の倍率（1.0 = 100%）。
func overlayScale(m domain.Monitor) float64 { return m.DPIScale() }

// overlayPx は DIP を m の物理ピクセルにする（最低 1 px）。
func overlayPx(m domain.Monitor, dip int) int {
	return max(1, int(math.Round(float64(dip)*overlayScale(m))))
}

// overlayBars はモニター m の縁に沿う枠の 4 本（上・下・左・右の順）を物理座標で返す。重ならない:
// 上下はモニターの幅いっぱい、左右は上下の間だけ。真ん中（モニターから枠を引いた内側）は含まない。
// モニターが枠の 2 本分より小さいときは、モニター全体を 1 本目（上）にして残りを空にする（作らない）。
func overlayBars(m domain.Monitor) [4]domain.Rect {
	b := overlayPx(m, overlayBorderDIP)
	x, y, w, h := m.Origin.X, m.Origin.Y, m.Size.W, m.Size.H
	if w <= 2*b || h <= 2*b {
		return [4]domain.Rect{{X: x, Y: y, W: max(w, 0), H: max(h, 0)}}
	}
	return [4]domain.Rect{
		{X: x, Y: y, W: w, H: b},
		{X: x, Y: y + h - b, W: w, H: b},
		{X: x, Y: y + b, W: b, H: h - 2*b},
		{X: x + w - b, Y: y + b, W: b, H: h - 2*b},
	}
}

// overlayLabelMaxTextWidth は札の文字が使える幅の上限（物理 px。余白を除く）。
func overlayLabelMaxTextWidth(m domain.Monitor) int {
	return max(1, m.Size.W*overlayLabelMaxWidthPercent/100-2*overlayPx(m, overlayLabelPadXDIP))
}

// overlayLabelRect は、文字の大きさ textW × textH（物理 px）の札を、モニター m の上端の中央に置く矩形を返す。
// 幅は overlayLabelMaxTextWidth を超えない（文字が長いときは描画側が末尾を「…」にする）。
func overlayLabelRect(m domain.Monitor, textW, textH int) domain.Rect {
	textW = min(max(textW, 0), overlayLabelMaxTextWidth(m))
	w := textW + 2*overlayPx(m, overlayLabelPadXDIP)
	h := max(textH, 0) + 2*overlayPx(m, overlayLabelPadYDIP)
	return domain.Rect{X: m.Origin.X + (m.Size.W-w)/2, Y: m.Origin.Y, W: w, H: h}
}

// splitOverlayLabel は ShowOverlay の label（改行で 2 行まで: 1 行目が札の文言、2 行目（任意）が補足。FR-H07）を分け、
// 各行の前後の空白を取って overlayLabelMaxRunes 文字までに切り詰める。3 行目以降は捨てる。
func splitOverlayLabel(label string) (first, detail string) {
	f, rest, _ := strings.Cut(label, "\n")
	d, _, _ := strings.Cut(rest, "\n")
	return truncateOverlayRunes(strings.TrimSpace(f)), truncateOverlayRunes(strings.TrimSpace(d))
}

// truncateOverlayRunes は s が overlayLabelMaxRunes 文字を超えるとき、末尾の「…」を含めてその文字数に切り詰める。
func truncateOverlayRunes(s string) string {
	r := []rune(s)
	if len(r) <= overlayLabelMaxRunes {
		return s
	}
	return string(r[:overlayLabelMaxRunes-1]) + "…"
}
