package main

import (
	"image"
	"image/color"
)

// 撮影画像の検査（画素の判定）。OS に依存しないので Linux でテストする。

// Rect は画像内のピクセル矩形（左上原点）。
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// overlay の判定値。
const (
	overlayVisible       = "visible"       // 札（洋紅）が写った
	overlayHidden        = "hidden"        // 対照（緑の窓）は写り、札だけ写っていない
	overlayPartial       = "partial"       // 一部だけ写った
	overlayIndeterminate = "indeterminate" // 対照が写っておらず、札の有無を言えない
)

const (
	// 札・対照を「写った」「写っていない」とみなす矩形内の画素の割合。
	shareVisible = 0.9
	shareAbsent  = 0.01
	// 色の許容差（色空間変換・拡縮の揺れ。洋紅 (255,0,255)・緑 (0,255,0) は十分に離れている）。
	colorTolerance = 48
	// 不透明とみなすアルファ（部分合成の画像では、窓の外が透明になる）。
	opaqueAlpha = 200
)

func near(a, b uint8) bool {
	d := int(a) - int(b)
	if d < 0 {
		d = -d
	}
	return d <= colorTolerance
}

func isMagenta(c color.NRGBA) bool {
	return c.A >= opaqueAlpha && near(c.R, 255) && near(c.G, 0) && near(c.B, 255)
}

func isGreen(c color.NRGBA) bool {
	return c.A >= opaqueAlpha && near(c.R, 0) && near(c.G, 255) && near(c.B, 0)
}

// share は矩形（画像の範囲に切り詰める）の中で match を満たす画素の割合。矩形が画像の外なら 0。
func share(img image.Image, r Rect, match func(color.NRGBA) bool) float64 {
	b := img.Bounds()
	x0, y0 := max(b.Min.X, int(r.X+0.5)), max(b.Min.Y, int(r.Y+0.5))
	x1, y1 := min(b.Max.X, int(r.X+r.W+0.5)), min(b.Max.Y, int(r.Y+r.H+0.5))
	if x1 <= x0 || y1 <= y0 {
		return 0
	}
	hit := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if match(color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)) {
				hit++
			}
		}
	}
	return float64(hit) / float64((x1-x0)*(y1-y0))
}

// blackShare は画像全体のうち、RGB がほぼ黒（または透明）の画素の割合。ロック中などで撮影が真っ黒かの目安。
func blackShare(img image.Image) float64 {
	b := img.Bounds()
	total, black := 0, 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			total++
			if c.A < opaqueAlpha || (c.R < 8 && c.G < 8 && c.B < 8) {
				black++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(black) / float64(total)
}

// classifyOverlay は札と対照の割合から判定を決める。対照が写っていなければ、札が無いことを根拠にしない。
func classifyOverlay(marker, control float64) string {
	if control < shareVisible {
		if marker >= shareVisible {
			return overlayVisible // 対照が見えなくても札が見えたなら、札は写る（映り込みの証拠として十分）
		}
		return overlayIndeterminate
	}
	switch {
	case marker >= shareVisible:
		return overlayVisible
	case marker <= shareAbsent:
		return overlayHidden
	default:
		return overlayPartial
	}
}
