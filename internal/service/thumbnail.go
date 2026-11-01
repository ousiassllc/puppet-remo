package service

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"

	"golang.org/x/image/draw"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// 監査のサムネイルの形（FR-AU02）: JPEG・長辺 320 px・品質 60。拡大はしない。
const (
	thumbMaxEdge     = 320
	thumbJPEGQuality = 60
)

// Thumbnail は監査エントリに付ける縮小スクリーンショット。JPEG は符号化済みで、Width・Height は JPEG の画素数。
type Thumbnail struct {
	JPEG   []byte
	Width  int
	Height int
}

// ThumbnailSize は w×h の画像を、アスペクト比を保って長辺 320 px に縮めたときの大きさを返す。
// 長辺がすでに 320 以下なら拡大せず、そのまま返す。短辺は最小 1 px にする（極端なアスペクト比でも 0 にならない）。
func ThumbnailSize(w, h int) (int, int) {
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	long := max(w, h)
	if long <= thumbMaxEdge {
		return w, h
	}
	scale := func(v int) int {
		// 四捨五入。int64 にして、極端に大きい画像でも溢れさせない。
		n := (int64(v)*thumbMaxEdge + int64(long)/2) / int64(long)
		return max(int(n), 1)
	}
	if w >= h {
		return thumbMaxEdge, scale(h)
	}
	return scale(w), thumbMaxEdge
}

// NewThumbnail は img を長辺 320 px の JPEG（品質 60）にする純粋関数。img は変更しない。
// 画像が空（0 画素）なら ErrInvalidArgument。純 Go（image/jpeg と golang.org/x/image/draw。cgo なし）。
func NewThumbnail(img *image.RGBA) (Thumbnail, error) {
	if img == nil {
		return Thumbnail{}, domain.ErrInvalidArgument.WithMsg("thumbnail source image is nil")
	}
	b := img.Bounds()
	w, h := ThumbnailSize(b.Dx(), b.Dy())
	if w == 0 {
		return Thumbnail{}, domain.ErrInvalidArgument.WithMsg("thumbnail source image is empty")
	}
	src := image.Image(img)
	if w != b.Dx() || h != b.Dy() {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		src = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: thumbJPEGQuality}); err != nil {
		return Thumbnail{}, fmt.Errorf("encode thumbnail: %w", err)
	}
	return Thumbnail{JPEG: buf.Bytes(), Width: w, Height: h}, nil
}

// CaptureThumbnail は monitor を Capturer.Grab で撮り、サムネイルにする。puppet-remo 自身のウィンドウは、
// 実機のキャプチャ（WDA_EXCLUDEFROMCAPTURE）の時点で画像に写らない（NFR-S06）ので、ここでは何もしない。
func CaptureThumbnail(ctx context.Context, c platform.Capturer, monitor int) (Thumbnail, error) {
	img, err := c.Grab(ctx, monitor)
	if err != nil {
		return Thumbnail{}, fmt.Errorf("grab for thumbnail: %w", err)
	}
	return NewThumbnail(img)
}
