package windows

import (
	"image"
	"image/color"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func solidRGBA(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

// 合成は空（透明）のキャンバスに置く。メンバーの外は透明のまま、前面（先頭）が後ろの上に来る。座標は origin + p。
// T の影（Window が Frame より大きい部分）は切り落とす。
func TestComposeGroupPlacesMembersOnTransparentCanvas(t *testing.T) {
	green := color.RGBA{G: 200, A: 255}
	red := color.RGBA{R: 200, A: 255}
	tl := GroupLayer{Frame: image.Rect(100, 100, 200, 200), Window: image.Rect(95, 95, 205, 205), Img: solidRGBA(110, 110, green)}
	menu := GroupLayer{Frame: image.Rect(180, 120, 260, 180), Window: image.Rect(180, 120, 260, 180), Img: solidRGBA(80, 60, red)}
	img, origin, rects := ComposeGroup([]GroupLayer{menu, tl})
	if img == nil {
		t.Fatal("canvas is nil")
	}
	if origin != (domain.Point{X: 100, Y: 100}) || img.Bounds().Dx() != 160 || img.Bounds().Dy() != 100 {
		t.Fatalf("origin = %v size = %v, want (100,100) 160x100", origin, img.Bounds().Size())
	}
	at := func(px, py int) color.RGBA { return img.RGBAAt(px-origin.X, py-origin.Y) }
	if got := at(150, 150); got != green {
		t.Errorf("T の内側 = %v", got)
	}
	if got := at(190, 150); got != red {
		t.Errorf("重なる点は前面のメニュー = %v", got)
	}
	if got := at(250, 150); got != red {
		t.Errorf("T の外のメニュー = %v", got)
	}
	if got := at(250, 110); got.A != 0 {
		t.Errorf("メンバーのない角は透明のまま = %v（デスクトップを下地にしない）", got)
	}
	if rects[1] != (domain.Rect{X: 100, Y: 100, W: 100, H: 100}) {
		t.Errorf("T の置いた矩形 = %v, want 見た目の枠 (100,100 100x100)", rects[1])
	}
}

func TestComposeGroupIgnoresMembersWithoutImage(t *testing.T) {
	if img, _, _ := ComposeGroup(nil); img != nil {
		t.Error("メンバーなしは nil")
	}
	ok := GroupLayer{Frame: image.Rect(0, 0, 10, 10), Window: image.Rect(0, 0, 10, 10), Img: solidRGBA(10, 10, color.RGBA{R: 1, A: 255})}
	missing := GroupLayer{Frame: image.Rect(500, 500, 600, 600), Window: image.Rect(500, 500, 600, 600)}
	img, origin, rects := ComposeGroup([]GroupLayer{missing, ok})
	if img == nil {
		t.Fatal("canvas is nil")
	}
	if img.Bounds().Dx() != 10 || origin != (domain.Point{}) {
		t.Fatalf("撮れなかったメンバーを外接矩形に含めた: %v %v", img.Bounds(), origin)
	}
	if !rects[0].Empty() {
		t.Errorf("撮れなかったメンバーの矩形 = %v, want 空", rects[0])
	}
}

// 画像が Window より小さいとき（切り詰め）、画像の外は置かない（範囲外を読まない）。
func TestComposeGroupClipsToTheImage(t *testing.T) {
	l := GroupLayer{Frame: image.Rect(0, 0, 20, 20), Window: image.Rect(0, 0, 20, 20), Img: solidRGBA(10, 10, color.RGBA{R: 9, A: 255})}
	img, _, rects := ComposeGroup([]GroupLayer{l})
	if img == nil {
		t.Fatal("canvas is nil")
	}
	if img.Bounds().Dx() != 10 || rects[0].W != 10 {
		t.Errorf("size = %v rect = %v, want 10x10", img.Bounds().Size(), rects[0])
	}
}

// 座標を画素に埋め込んだ画像で、T の影（Window が Frame より大きい）の切り落としと、負の原点・原点の変換を確かめる。
// 画素 (x, y)（Window 左上からの相対）は R=x, G=y。置かれた画素が元の位置から取られていることを見る。
func TestComposeGroupCopiesTheRightSourcePixelsAtNegativeOrigin(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 60, 50))
	for y := range 50 {
		for x := range 60 {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 1, A: 255})
		}
	}
	// Window は (-300,-200) から 60x50。見た目の枠はその内側 (-295,-197)-(-255,-157)（左 5・上 3 が影）。
	l := GroupLayer{Frame: image.Rect(-295, -197, -255, -157), Window: image.Rect(-300, -200, -240, -150), Img: img}
	out, origin, rects := ComposeGroup([]GroupLayer{l})
	if out == nil {
		t.Fatal("canvas is nil")
	}
	if origin != (domain.Point{X: -295, Y: -197}) || out.Bounds().Dx() != 40 || out.Bounds().Dy() != 40 {
		t.Fatalf("origin = %v size = %v, want (-295,-197) 40x40", origin, out.Bounds().Size())
	}
	if rects[0] != (domain.Rect{X: -295, Y: -197, W: 40, H: 40}) {
		t.Errorf("rect = %v", rects[0])
	}
	// キャンバスの (0,0) は物理 (-295,-197) = Window 相対 (5,3) の画素。
	for _, pt := range []image.Point{{0, 0}, {39, 0}, {0, 39}, {39, 39}, {17, 22}} {
		want := color.RGBA{R: uint8(pt.X + 5), G: uint8(pt.Y + 3), B: 1, A: 255}
		if got := out.RGBAAt(pt.X, pt.Y); got != want {
			t.Errorf("canvas%v = %v, want %v", pt, got, want)
		}
	}
}

// アルファ 0 の画素（PrintWindow が描かなかった。角丸の外など）は、下のメンバーの画素を消さず、透明のまま残る。
func TestComposeGroupSkipsUndrawnPixels(t *testing.T) {
	red := color.RGBA{R: 200, A: 255}
	under := GroupLayer{Frame: image.Rect(0, 0, 10, 10), Window: image.Rect(0, 0, 10, 10), Img: solidRGBA(10, 10, red)}
	top := solidRGBA(10, 10, color.RGBA{G: 200, A: 255})
	top.SetRGBA(0, 0, color.RGBA{}) // 角: 描かれていない
	over := GroupLayer{Frame: image.Rect(0, 0, 10, 10), Window: image.Rect(0, 0, 10, 10), Img: top}
	out, _, _ := ComposeGroup([]GroupLayer{over, under})
	if got := out.RGBAAt(0, 0); got != red {
		t.Errorf("上のメンバーの描かれていない角は下のメンバーが見える = %v", got)
	}
	if got := out.RGBAAt(5, 5); got.G != 200 {
		t.Errorf("上のメンバーの描かれた画素 = %v", got)
	}
	only, _, _ := ComposeGroup([]GroupLayer{over})
	if got := only.RGBAAt(0, 0); got.A != 0 {
		t.Errorf("下に何もないとき、描かれていない角は透明のまま = %v", got)
	}
}
