package windows

import (
	"image"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// GroupLayer は対象グループの合成に置くメンバー 1 つ（PrintWindow の結果）。OS に依存しないので Linux でもテストできる。
type GroupLayer struct {
	// Frame は見た目の枠（仮想デスクトップ上の物理座標。WindowManager の Rect＝DWM の拡張フレーム）。
	Frame image.Rectangle
	// Window は Img が覆う矩形（GetWindowRect。影・不可視の枠を含みうる）。
	Window image.Rectangle
	// Img は PrintWindow の結果（Window と同じ大きさ）。アルファが 0 の画素（PrintWindow が描かなかった。角丸の外など）は置かない。
	// GDI の描画はアルファを 0 のままにするので、呼び出し側が撮影のあとに描いた画素を 255 にそろえておく。
	Img *image.RGBA
}

// ComposeGroup は layers（前面が先）を、見た目の枠の外接矩形の**空（透明）のキャンバス**に、後ろから順に置いて返す（platform.GroupImage の合成）。
// デスクトップ・画面領域・モニターの画像は下地に使わない（キャンバスはメンバーの画素以外が透明のまま）。
// 置く範囲は Frame ∩ Window（画像が実際に覆っている見た目の枠）。画像のない（撮れなかった）メンバー・空の枠は外接矩形にも入らない。
// 置いた画素はアルファ 255 にする（Img のアルファが 0 の画素は置かない）。origin はキャンバスの左上の物理座標（合成画像の座標 p → 物理座標は origin + p）。置けるメンバーがなければ nil。
func ComposeGroup(layers []GroupLayer) (img *image.RGBA, origin domain.Point, rects []domain.Rect) {
	rects = make([]domain.Rect, len(layers))
	var bounds image.Rectangle
	for i, l := range layers {
		area := l.placeArea()
		if area.Empty() {
			continue
		}
		rects[i] = domain.Rect{X: area.Min.X, Y: area.Min.Y, W: area.Dx(), H: area.Dy()}
		bounds = bounds.Union(area)
	}
	if bounds.Empty() {
		return nil, domain.Point{}, rects
	}
	canvas := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for i := len(layers) - 1; i >= 0; i-- {
		l := layers[i]
		area := l.placeArea()
		if area.Empty() {
			continue
		}
		for y := area.Min.Y; y < area.Max.Y; y++ {
			src := l.Img.PixOffset(area.Min.X-l.Window.Min.X, y-l.Window.Min.Y)
			dst := canvas.PixOffset(area.Min.X-bounds.Min.X, y-bounds.Min.Y)
			for x := 0; x < area.Dx(); x++ {
				if l.Img.Pix[src+3] != 0 { // アルファ 0 は PrintWindow が描かなかった画素（角丸の外など）。透明のまま残す
					canvas.Pix[dst], canvas.Pix[dst+1], canvas.Pix[dst+2], canvas.Pix[dst+3] = l.Img.Pix[src], l.Img.Pix[src+1], l.Img.Pix[src+2], 255
				}
				src += 4
				dst += 4
			}
		}
	}
	return canvas, domain.Point{X: bounds.Min.X, Y: bounds.Min.Y}, rects
}

// placeArea は合成に置く範囲（Frame ∩ Window。画像の大きさに収まる範囲）。画像がなければ空。
func (l GroupLayer) placeArea() image.Rectangle {
	if l.Img == nil || l.Frame.Empty() || l.Window.Empty() {
		return image.Rectangle{}
	}
	covered := image.Rectangle{Min: l.Window.Min, Max: l.Window.Min.Add(l.Img.Bounds().Size())}
	return l.Frame.Intersect(l.Window).Intersect(covered)
}
