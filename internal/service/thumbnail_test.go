package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// gradientImage は固定の（乱数を使わない）グラデーション画像。JPEG の大きさの検査に使う。
func gradientImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 255 / max(w, 1)), G: uint8(y * 255 / max(h, 1)), B: uint8((x + y) % 256), A: 255})
		}
	}
	return img
}

func TestThumbnailSize(t *testing.T) {
	tests := []struct {
		name         string
		w, h         int
		wantW, wantH int
	}{
		{"landscape 16:9", 1920, 1080, 320, 180},
		{"portrait", 1080, 1920, 180, 320},
		{"square", 1000, 1000, 320, 320},
		{"already small is not enlarged", 200, 100, 200, 100},
		{"exactly 320 is kept", 320, 240, 320, 240},
		{"just over", 321, 321, 320, 320},
		{"ultra wide keeps one pixel", 10000, 10, 320, 1},
		{"ultra tall keeps one pixel", 10, 10000, 1, 320},
		{"huge", 1 << 30, 1 << 29, 320, 160},
		{"empty", 0, 100, 0, 0},
		{"negative", -1, 100, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, h := ThumbnailSize(tt.w, tt.h)
			if w != tt.wantW || h != tt.wantH {
				t.Fatalf("ThumbnailSize(%d, %d) = %dx%d, want %dx%d", tt.w, tt.h, w, h, tt.wantW, tt.wantH)
			}
		})
	}
}

func TestNewThumbnailEncodesJPEG(t *testing.T) {
	tests := []struct {
		name         string
		w, h         int
		wantW, wantH int
	}{
		{"landscape", 1600, 900, 320, 180},
		{"portrait", 900, 1600, 180, 320},
		{"square", 640, 640, 320, 320},
		{"small stays", 300, 200, 300, 200},
		{"ultra wide", 3200, 8, 320, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := gradientImage(tt.w, tt.h)
			before := bytes.Clone(src.Pix)
			th, err := NewThumbnail(src)
			if err != nil {
				t.Fatal(err)
			}
			if th.Width != tt.wantW || th.Height != tt.wantH {
				t.Fatalf("size = %dx%d, want %dx%d", th.Width, th.Height, tt.wantW, tt.wantH)
			}
			if !bytes.Equal(before, src.Pix) {
				t.Fatal("the source image must not be modified")
			}
			img, err := jpeg.Decode(bytes.NewReader(th.JPEG))
			if err != nil {
				t.Fatalf("not a JPEG: %v", err)
			}
			if b := img.Bounds(); b.Dx() != tt.wantW || b.Dy() != tt.wantH || max(b.Dx(), b.Dy()) > 320 {
				t.Fatalf("decoded size = %v", b)
			}
		})
	}
}

// 品質 60 は既定（75）より小さく、品質 95 より小さい（固定の画像で比較する）。
func TestNewThumbnailUsesQuality60(t *testing.T) {
	src := gradientImage(640, 360)
	th, err := NewThumbnail(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := image.NewRGBA(image.Rect(0, 0, th.Width, th.Height))
	sized, _ := jpeg.Decode(bytes.NewReader(th.JPEG))
	for y := range th.Height {
		for x := range th.Width {
			dst.Set(x, y, sized.At(x, y))
		}
	}
	size := func(q int) int {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: q}); err != nil {
			t.Fatal(err)
		}
		return buf.Len()
	}
	if got, q95 := len(th.JPEG), size(95); got >= q95 {
		t.Fatalf("size %d should be below quality 95 (%d)", got, q95)
	}
	if got, q40 := len(th.JPEG), size(40); got <= q40 {
		t.Fatalf("size %d should be above quality 40 (%d)", got, q40)
	}
}

func TestNewThumbnailRejectsEmpty(t *testing.T) {
	for name, img := range map[string]*image.RGBA{"nil": nil, "empty": image.NewRGBA(image.Rect(0, 0, 0, 0))} {
		if _, err := NewThumbnail(img); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("%s: err = %v, want invalid argument", name, err)
		}
	}
}

func TestCaptureThumbnailUsesCapturer(t *testing.T) {
	c := platformtest.NewFakeCapturer(domain.Monitor{Index: 0, Primary: true, Size: domain.Size{W: 1280, H: 720}, DPI: 96})
	c.SetImage(0, gradientImage(1280, 720))
	th, err := CaptureThumbnail(context.Background(), c, 0)
	if err != nil || th.Width != 320 || th.Height != 180 {
		t.Fatalf("thumb = %dx%d, %v", th.Width, th.Height, err)
	}
	if calls := c.CallsTo("Grab"); len(calls) != 1 || calls[0].Args[0] != 0 {
		t.Fatalf("grab calls = %+v", calls)
	}
	if _, err := CaptureThumbnail(context.Background(), c, 9); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("unknown monitor: err = %v", err)
	}
}
