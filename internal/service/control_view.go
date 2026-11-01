package service

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

const (
	formatPNG  = "png"
	formatJPEG = "jpeg"

	jpegQuality = 80
)

// Screenshot は monitor のスクリーンショットを、長辺が screenshot.max_dimension 以下になるよう縮小して返す（FR-M02、FR-S03）。
// リースは不要。monitor が domain.MonitorPrimary ならプライマリ。format が空なら png。
func (c *control) Screenshot(ctx context.Context, monitor int, format string) (Shot, error) {
	if c.kill.Active() {
		return Shot{}, domain.ErrKillSwitchActive
	}
	switch format {
	case "":
		format = formatPNG
	case formatPNG, formatJPEG:
	default:
		return Shot{}, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("unknown image format %q", format))
	}
	mons, err := c.capture.Monitors(ctx)
	if err != nil {
		return Shot{}, err
	}
	mon, err := pickMonitor(mons, monitor)
	if err != nil {
		return Shot{}, err
	}
	maxDim, err := c.MaxDimension(ctx)
	if err != nil {
		return Shot{}, err
	}
	img, err := c.capture.Grab(ctx, mon.Index)
	if err != nil {
		return Shot{}, err
	}
	scaled := mon.ScaledSize(maxDim)
	data, mime, err := encodeShot(resize(img, scaled), format)
	if err != nil {
		return Shot{}, err
	}
	// 自ウィンドウの範囲は手がかりなので、取れなくてもスクリーンショットは返す。
	own, _ := c.ownRegionsOn(ctx, mons, maxDim)
	return Shot{
		Data:       data,
		MIME:       mime,
		Monitor:    mon.Index,
		Physical:   mon.Size,
		Scaled:     scaled,
		Scale:      mon.Scale(maxDim),
		OwnWindows: regionsOnMonitor(own, mon.Index),
	}, nil
}

// regionsOnMonitor は regions のうち monitor 上のものだけを返す。
func regionsOnMonitor(regions []domain.OwnRegion, monitor int) []domain.OwnRegion {
	var out []domain.OwnRegion
	for _, r := range regions {
		if r.Monitor == monitor {
			out = append(out, r)
		}
	}
	return out
}

// CursorPosition は現在のカーソル位置を、そのモニターの縮小座標で返す（FR-M03）。
func (c *control) CursorPosition(ctx context.Context) (Cursor, error) {
	if c.kill.Active() {
		return Cursor{}, domain.ErrKillSwitchActive
	}
	pos, err := c.input.CursorPos()
	if err != nil {
		return Cursor{}, err
	}
	mons, err := c.capture.Monitors(ctx)
	if err != nil {
		return Cursor{}, err
	}
	mon, ok := domain.MonitorAt(mons, pos)
	if !ok {
		return Cursor{}, domain.ErrDesktopUnavailable.WithMsg("the cursor is outside every monitor")
	}
	maxDim, err := c.MaxDimension(ctx)
	if err != nil {
		return Cursor{}, err
	}
	return Cursor{Point: mon.PhysicalToScaled(pos, maxDim), Monitor: mon.Index}, nil
}

// resize は img を size へ縮小する。同じ大きさならそのまま返す。
func resize(img *image.RGBA, size domain.Size) image.Image {
	if img.Bounds().Dx() == size.W && img.Bounds().Dy() == size.H {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, size.W, size.H))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Src, nil)
	return dst
}

func encodeShot(img image.Image, format string) (data []byte, mime string, err error) {
	var buf bytes.Buffer
	if format == formatJPEG {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality})
		mime = "image/jpeg"
	} else {
		err = png.Encode(&buf, img)
		mime = "image/png"
	}
	if err != nil {
		return nil, "", fmt.Errorf("encode screenshot: %w", err)
	}
	return buf.Bytes(), mime, nil
}
