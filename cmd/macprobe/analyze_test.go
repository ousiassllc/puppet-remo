package main

import (
	"image"
	"image/color"
	"testing"
)

func paint(img *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

var (
	cMagenta = color.NRGBA{R: 255, G: 0, B: 255, A: 255}
	cGreen   = color.NRGBA{R: 0, G: 255, B: 0, A: 255}
)

// 札の矩形が洋紅で埋まっていれば visible、緑の対照だけが写っていれば hidden、対照も無ければ indeterminate と判定する。
func TestClassifyOverlayFromPixels(t *testing.T) {
	marker := Rect{X: 10, Y: 10, W: 20, H: 10}
	control := Rect{X: 50, Y: 30, W: 30, H: 20}
	mk := func(withMarker, withControl bool) image.Image {
		img := image.NewNRGBA(image.Rect(0, 0, 100, 60))
		paint(img, img.Bounds(), color.NRGBA{R: 40, G: 40, B: 90, A: 255})
		if withMarker {
			paint(img, image.Rect(10, 10, 30, 20), cMagenta)
		}
		if withControl {
			paint(img, image.Rect(50, 30, 80, 50), cGreen)
		}
		return img
	}
	cases := []struct {
		name           string
		marker, ctrl   bool
		wantMarkerFrac float64
		want           string
	}{
		{"両方写る", true, true, 1, overlayVisible},
		{"札だけ消える", false, true, 0, overlayHidden},
		{"対照が写らない", false, false, 0, overlayIndeterminate},
		{"札だけ写る", true, false, 1, overlayVisible},
	}
	for _, tc := range cases {
		img := mk(tc.marker, tc.ctrl)
		m := share(img, marker, isMagenta)
		c := share(img, control, isGreen)
		if m != tc.wantMarkerFrac {
			t.Errorf("%s: 札の割合 = %v、want %v", tc.name, m, tc.wantMarkerFrac)
		}
		if got := classifyOverlay(m, c); got != tc.want {
			t.Errorf("%s: 判定 = %s、want %s", tc.name, got, tc.want)
		}
	}
}

// 透明（アルファ 0）の画素は洋紅・緑・黒でなく数える。矩形が画像の外にはみ出しても落ちない。
func TestShareIgnoresTransparentAndClipsRect(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	paint(img, image.Rect(0, 0, 10, 10), color.NRGBA{R: 255, G: 0, B: 255, A: 0})
	if got := share(img, Rect{X: 0, Y: 0, W: 10, H: 10}, isMagenta); got != 0 {
		t.Errorf("透明な洋紅 = %v、want 0", got)
	}
	paint(img, image.Rect(5, 5, 10, 10), cMagenta)
	if got := share(img, Rect{X: 5, Y: 5, W: 50, H: 50}, isMagenta); got != 1 {
		t.Errorf("はみ出した矩形の割合 = %v、want 1（画像内の部分だけで数える）", got)
	}
	if got := share(img, Rect{X: 20, Y: 20, W: 5, H: 5}, isMagenta); got != 0 {
		t.Errorf("画像外の矩形 = %v、want 0", got)
	}
	if got := blackShare(img); got != 0.75 {
		t.Errorf("blackShare = %v、want 0.75（透明 75 画素）", got)
	}
}
