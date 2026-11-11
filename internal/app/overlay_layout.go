package app

import (
	"math"
	"strings"
	"unicode"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 操作中オーバーレイ（#69・#86）の配置。Windows ではモニターごとに「細い枠の 4 本」と「上端中央の札」を、それぞれ別の
// ネイティブウィンドウとして出す（wails_overlay_windows.go）。真ん中のウィンドウは作らないので、画面の中身は最初から覆わない。
// このファイルは配置の計算だけ（OS に依存しない）。ウィンドウの描画と出し入れは Windows 側が行う。

const (
	// overlayBorderDIP は枠の太さ（DIP。100% の DPI で 4 px）。外側の線（overlayOuterDIP）と内側の線の 2 重線で、内側の太さは差の分。
	overlayBorderDIP = 4
	// overlayOuterDIP は外側の線（--ink）の太さ（DIP）。内側の線（--bone）は overlayBorderDIP - overlayOuterDIP = 2 DIP。
	overlayOuterDIP = 2
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
	return overlayStrips(m, overlayPx(m, overlayBorderDIP))
}

// overlayStrips はモニター m の縁に沿う太さ b（物理 px）の 4 本（上・下・左・右の順）。overlayBars の形と同じ。
func overlayStrips(m domain.Monitor, b int) [4]domain.Rect {
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

// overlayOuterInk は、枠の 1 本（物理座標の矩形 bar。overlayBars の要素）のうち、外側の線（--ink）で塗る範囲を bar の左上を原点とする座標で返す。
// bar の残りは内側の線（--bone）。外側の線はモニターの縁から overlayOuterDIP の太さの輪なので、角でも 4 本をまたいで途切れない。
func overlayOuterInk(m domain.Monitor, bar domain.Rect) []domain.Rect {
	var ink []domain.Rect
	for _, s := range overlayStrips(m, min(overlayPx(m, overlayOuterDIP), overlayPx(m, overlayBorderDIP))) {
		x0, y0 := max(s.X, bar.X), max(s.Y, bar.Y)
		x1, y1 := min(s.X+s.W, bar.X+bar.W), min(s.Y+s.H, bar.Y+bar.H)
		if x0 < x1 && y0 < y1 {
			ink = append(ink, domain.Rect{X: x0 - bar.X, Y: y0 - bar.Y, W: x1 - x0, H: y1 - y0})
		}
	}
	return ink
}

// overlayColors は操作中オーバーレイの色（COLORREF = 0x00BBGGRR）。値は app.css のトークンと同じ 16 進
// （frontend.md §8.5「案件ガイド」）。ネイティブウィンドウは CSS を読めないので、ここに写す。
type overlayColors struct {
	Outer     uint32 // 枠の外側の線（--ink）
	Inner     uint32 // 枠の内側の線（--bone）
	LabelBG   uint32 // 札の地（--surface）
	LabelText uint32 // 札の文字（--ink）
}

// colorRef は 0xRRGGBB を COLORREF（0x00BBGGRR）にする。
func colorRef(rgb uint32) uint32 {
	return (rgb&0xff)<<16 | rgb&0xff00 | rgb>>16&0xff
}

// overlayColorsFor は OS のアプリのテーマ（dark）に合わせた色。ライトは --ink #050505・--bone #f5f5f4・--surface #ffffff、
// ダークは --ink #ffffff・--bone #050505・--surface #141414（app.css の :root と [data-theme="dark"]）。
func overlayColorsFor(dark bool) overlayColors {
	if dark {
		return overlayColors{Outer: colorRef(0xffffff), Inner: colorRef(0x050505), LabelBG: colorRef(0x141414), LabelText: colorRef(0xffffff)}
	}
	return overlayColors{Outer: colorRef(0x050505), Inner: colorRef(0xf5f5f4), LabelBG: colorRef(0xffffff), LabelText: colorRef(0x050505)}
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
	return truncateOverlayRunes(cleanOverlayLine(f)), truncateOverlayRunes(cleanOverlayLine(d))
}

// cleanOverlayLine は制御文字（NUL・タブ・ESC など）を取り、前後の空白を取る。クライアント名は利用者が付けた任意の文字列で、
// NUL は Windows の文字列 API（UTF-16 への変換）が受け付けず panic の元になる。
func cleanOverlayLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
}

// truncateOverlayRunes は s が overlayLabelMaxRunes 文字を超えるとき、末尾の「…」を含めてその文字数に切り詰める。
func truncateOverlayRunes(s string) string {
	r := []rune(s)
	if len(r) <= overlayLabelMaxRunes {
		return s
	}
	return string(r[:overlayLabelMaxRunes-1]) + "…"
}
