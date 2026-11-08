package main

import "image"

// グループ合成の幾何（A-2 の 3 の `GrabGroup` の設計材料。製品コードではなく、実機で座標と外接矩形を確かめるためのもの）。
// PrintWindow はウィンドウの矩形（GetWindowRect。影・不可視の枠を含みうる）全体を撮る。一覧（ListAll）の Rect は見た目の枠（DWM の拡張フレーム）。
// 合成は見た目の枠で切り出して、空（透明）のキャンバスの上に置く。

// placed は合成に置くメンバー 1 つ。
type placed struct {
	Frame  image.Rectangle // 見た目の枠（仮想デスクトップ上の物理座標。ListAll の Rect）
	Window image.Rectangle // PrintWindow の画像が覆う矩形（GetWindowRect）
	Img    *image.RGBA     // PrintWindow の結果（Window と同じ大きさ）
}

// composite は members を、見た目の枠の外接矩形の透明なキャンバスに置く。members は前面が先（後ろから順に描く）。
// 戻り値の origin はキャンバスの左上の物理座標（合成画像の座標 p → 物理座標は origin + p）。
// 画像が Window を覆っていない・Frame が Window の外にはみ出す部分は置かない（その部分は透明のまま）。メンバーがなければ nil。
func composite(members []placed) (img *image.RGBA, origin image.Point) {
	var bounds image.Rectangle
	for _, m := range members {
		if m.Img != nil && !m.Frame.Empty() {
			bounds = bounds.Union(m.Frame)
		}
	}
	if bounds.Empty() {
		return nil, image.Point{}
	}
	canvas := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for i := len(members) - 1; i >= 0; i-- {
		m := members[i]
		if m.Img == nil || m.Frame.Empty() {
			continue
		}
		// 置く範囲 = Frame ∩ Window（画像が実際に覆っている範囲）。
		area := m.Frame.Intersect(m.Window)
		for y := area.Min.Y; y < area.Max.Y; y++ {
			for x := area.Min.X; x < area.Max.X; x++ {
				sx, sy := x-m.Window.Min.X, y-m.Window.Min.Y
				if sx < 0 || sy < 0 || sx >= m.Img.Bounds().Dx() || sy >= m.Img.Bounds().Dy() {
					continue
				}
				s := m.Img.PixOffset(sx, sy)
				d := canvas.PixOffset(x-bounds.Min.X, y-bounds.Min.Y)
				canvas.Pix[d], canvas.Pix[d+1], canvas.Pix[d+2], canvas.Pix[d+3] = m.Img.Pix[s], m.Img.Pix[s+1], m.Img.Pix[s+2], 255
			}
		}
	}
	return canvas, bounds.Min
}
