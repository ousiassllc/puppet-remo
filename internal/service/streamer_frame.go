package service

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは Streamer の画面フレームの符号化（docs/api/endpoints.md §5.4）と、64×64 のタイル単位の差分の検出。

const (
	// FrameVersion はバイナリフレームのバージョン（先頭の 1 バイト）。
	FrameVersion = 1
	// FrameFlagKeyframe はフラグ（オフセット 1）の bit0。立っていればフレーム全体を覆うキーフレーム。
	FrameFlagKeyframe = 1
	// FrameFlagGroup はフラグの bit1。立っていればグループ源（#157）のフレームで、見出しの直後（オフセット 14〜）に i32 の原点 x・y が
	// 付き、タイルはその後ろに続く。モニター index は FrameMonitorGroup。
	FrameFlagGroup = 2
	// FrameMonitorGroup はグループ源のフレームのモニター index（実在のモニターの index とは重ならない）。
	FrameMonitorGroup = math.MaxUint16
	// TileSize は差分を比べる格子の 1 辺（px）。
	TileSize = 64

	frameHeaderSize = 14
	groupOriginSize = 8
	tileHeaderSize  = 12
)

// changedRects は prev から cur で変わった範囲を、64×64 の格子を単位に矩形へまとめて返す（変化がなければ nil）。
// 同じ行で隣り合う変化したタイルは 1 つの横長の矩形にし、次の行で同じ横の範囲が続けば縦にも伸ばす。
// prev と cur は同じ大きさ（原点 (0, 0)）であること。右端・下端の矩形は画像の範囲に切り詰める。
func changedRects(prev, cur *image.RGBA) []image.Rectangle {
	b := cur.Bounds()
	w, h := b.Dx(), b.Dy()
	cols := (w + TileSize - 1) / TileSize
	rows := (h + TileSize - 1) / TileSize

	type span struct{ c0, c1 int } // [c0, c1) の列
	var out []image.Rectangle
	open := map[span]int{} // 直前の行で伸ばせる矩形（out の添字）
	changed := make([]bool, cols)
	for r := range rows {
		y0, y1 := r*TileSize, min((r+1)*TileSize, h)
		for c := range cols {
			changed[c] = !tileEqual(prev, cur, c*TileSize, y0, min((c+1)*TileSize, w), y1)
		}
		next := map[span]int{}
		for c := 0; c < cols; c++ {
			if !changed[c] {
				continue
			}
			c0 := c
			for c+1 < cols && changed[c+1] {
				c++
			}
			s := span{c0, c + 1}
			if i, ok := open[s]; ok {
				out[i].Max.Y = y1
				next[s] = i
				continue
			}
			out = append(out, image.Rect(c0*TileSize, y0, min((c+1)*TileSize, w), y1))
			next[s] = len(out) - 1
		}
		open = next
	}
	return out
}

// tileEqual は [x0, x1) × [y0, y1) の画素が prev と cur で同じかを返す。
func tileEqual(prev, cur *image.RGBA, x0, y0, x1, y1 int) bool {
	n := (x1 - x0) * 4
	for y := y0; y < y1; y++ {
		po, co := prev.PixOffset(x0, y), cur.PixOffset(x0, y)
		if !bytes.Equal(prev.Pix[po:po+n], cur.Pix[co:co+n]) {
			return false
		}
	}
	return true
}

// encodeFrame は rects（img の範囲内）を JPEG にして、バイナリフレームの 1 メッセージにする（リトルエンディアン）。
// キーフレームのときは呼び出し側が img 全体の矩形 1 つを渡す。
func encodeFrame(img *image.RGBA, rects []image.Rectangle, monitor int, seq uint32, key bool, quality int) ([]byte, error) {
	return encodeTiles(img, rects, monitor, nil, seq, key, quality)
}

// encodeGroupFrame は encodeFrame のグループ源版。原点 origin（物理座標 = origin + 画像の座標）を見出しの後ろに付ける。
func encodeGroupFrame(img *image.RGBA, rects []image.Rectangle, origin domain.Point, seq uint32, key bool, quality int) ([]byte, error) {
	return encodeTiles(img, rects, FrameMonitorGroup, &origin, seq, key, quality)
}

// GroupFrameInfo はグループ源のフレームの見出しの内容（ws が config・座標変換に使う）。
type GroupFrameInfo struct {
	Width, Height int
	Origin        domain.Point
}

// ParseGroupFrameInfo は b がグループ源のフレーム（FrameFlagGroup つき）なら、その大きさと原点を返す。モニター源・不正な見出しは false。
func ParseGroupFrameInfo(b []byte) (GroupFrameInfo, bool) {
	if len(b) < frameHeaderSize+groupOriginSize || b[0] != FrameVersion || b[1]&FrameFlagGroup == 0 {
		return GroupFrameInfo{}, false
	}
	return GroupFrameInfo{
		Width: int(binary.LittleEndian.Uint16(b[8:])), Height: int(binary.LittleEndian.Uint16(b[10:])),
		Origin: domain.Point{
			X: int(int32(binary.LittleEndian.Uint32(b[frameHeaderSize:]))),
			Y: int(int32(binary.LittleEndian.Uint32(b[frameHeaderSize+4:]))),
		},
	}, true
}

func encodeTiles(img *image.RGBA, rects []image.Rectangle, monitor int, origin *domain.Point, seq uint32, key bool, quality int) ([]byte, error) {
	b := img.Bounds()
	if b.Dx() > math.MaxUint16 || b.Dy() > math.MaxUint16 {
		return nil, fmt.Errorf("frame %dx%d exceeds the u16 limit of the frame format", b.Dx(), b.Dy())
	}
	if len(rects) == 0 || len(rects) > math.MaxUint16 {
		return nil, fmt.Errorf("frame has %d tiles; want 1 to %d", len(rects), math.MaxUint16)
	}
	if monitor < 0 || monitor > math.MaxUint16 {
		return nil, errors.New("monitor index exceeds the u16 limit of the frame format")
	}

	var flags byte
	if key {
		flags = FrameFlagKeyframe
	}
	buf := bytes.NewBuffer(make([]byte, 0, 4096))
	var head [frameHeaderSize]byte
	head[0], head[1] = FrameVersion, flags
	binary.LittleEndian.PutUint16(head[2:], uint16(monitor))
	binary.LittleEndian.PutUint32(head[4:], seq)
	binary.LittleEndian.PutUint16(head[8:], uint16(b.Dx()))
	binary.LittleEndian.PutUint16(head[10:], uint16(b.Dy()))
	binary.LittleEndian.PutUint16(head[12:], uint16(len(rects)))
	if origin != nil {
		head[1] |= FrameFlagGroup
	}
	buf.Write(head[:])
	if origin != nil {
		var o [groupOriginSize]byte
		binary.LittleEndian.PutUint32(o[0:], uint32(int32(origin.X)))
		binary.LittleEndian.PutUint32(o[4:], uint32(int32(origin.Y)))
		buf.Write(o[:])
	}

	opts := &jpeg.Options{Quality: quality}
	for _, r := range rects {
		tileAt := buf.Len()
		var th [tileHeaderSize]byte
		binary.LittleEndian.PutUint16(th[0:], uint16(r.Min.X))
		binary.LittleEndian.PutUint16(th[2:], uint16(r.Min.Y))
		binary.LittleEndian.PutUint16(th[4:], uint16(r.Dx()))
		binary.LittleEndian.PutUint16(th[6:], uint16(r.Dy()))
		buf.Write(th[:])
		if err := jpeg.Encode(buf, img.SubImage(r), opts); err != nil {
			return nil, fmt.Errorf("encode jpeg tile: %w", err)
		}
		binary.LittleEndian.PutUint32(buf.Bytes()[tileAt+8:], uint32(buf.Len()-tileAt-tileHeaderSize))
	}
	return buf.Bytes(), nil
}
