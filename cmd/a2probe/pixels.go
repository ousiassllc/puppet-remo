package main

import (
	"image"
	"image/color"
	"strconv"
)

// 撮影画像の検査（画素の判定。OS に依存しないので Linux でテストする）。

// sentinel は PrintWindow に渡す前に DIB を塗っておく色。PrintWindow が TRUE を返しても何も描かなかった（画素が触られていない）ことを、
// 本当に黒いウィンドウと見分けるために使う（fixture の模様・メニューの色には使わない）。
var sentinel = color.RGBA{R: 7, G: 251, B: 13, A: 0}

// magenta は別プロセスの最前面窓（interferer）の塗りつぶしの色。撮影画像に 1 画素でも出たら、他アプリの画素が混ざった証拠になる。
var magenta = color.RGBA{R: 255, G: 0, B: 255, A: 255}

// magentaTolerance はマゼンタとみなす各チャネルの許容差（拡縮・色変換の揺れ。模様の色は十分に離してある）。
const magentaTolerance = 16

// distinctCap は異なる色の数を数える上限（超えたら打ち切る。写り方の目安にだけ使う）。
const distinctCap = 256

// verdict の値。
const (
	verdictContent   = "content"   // 内容がある
	verdictUntouched = "untouched" // 画素が触られていない（PrintWindow が何も描かなかった）
	verdictAllBlack  = "all_black" // ほぼ全面が黒
	verdictUniform   = "uniform"   // ほぼ全面が 1 色
)

// blankShare はこの割合以上が同じ状態なら「空」とみなす。
const blankShare = 0.99

// pixelStats は画像 1 枚の統計。アルファは見ない（GDI の描画はアルファを 0 のままにするので、RGB だけで判定する）。
type pixelStats struct {
	W, H      int
	Total     int
	Untouched int // sentinel の画素
	Black     int // RGB が (0,0,0)
	Magenta   int // マゼンタ（許容差つき）
	AlphaZero int // 参考: アルファが 0 の画素
	Distinct  int // 異なる色の数（distinctCap で打ち切り）
	Dominant  float64
	Watched   map[string]int // 指定した色の画素数（"r,g,b" をキーにする）
}

// colorKey は watch の色を Watched のキーにする。
func colorKey(c color.RGBA) string {
	return strconv.Itoa(int(c.R)) + "," + strconv.Itoa(int(c.G)) + "," + strconv.Itoa(int(c.B))
}

func near(a, b uint8, tol int) bool {
	d := int(a) - int(b)
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// analyzePixels は img の統計を数える。watch の色は完全一致で数える。
func analyzePixels(img *image.RGBA, watch []color.RGBA) pixelStats {
	b := img.Bounds()
	s := pixelStats{W: b.Dx(), H: b.Dy(), Total: b.Dx() * b.Dy(), Watched: map[string]int{}}
	if s.Total == 0 {
		return s
	}
	counts := map[[3]uint8]int{}
	overflow := false
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := img.Pix[(y-b.Min.Y)*img.Stride:]
		for x := range b.Dx() {
			r, g, bl, a := row[x*4], row[x*4+1], row[x*4+2], row[x*4+3]
			if a == 0 {
				s.AlphaZero++
			}
			switch {
			case r == sentinel.R && g == sentinel.G && bl == sentinel.B:
				s.Untouched++
			case r == 0 && g == 0 && bl == 0:
				s.Black++
			}
			if near(r, magenta.R, magentaTolerance) && near(g, magenta.G, magentaTolerance) && near(bl, magenta.B, magentaTolerance) {
				s.Magenta++
			}
			k := [3]uint8{r, g, bl}
			if _, ok := counts[k]; ok || len(counts) < distinctCap {
				counts[k]++
			} else {
				overflow = true
			}
		}
	}
	for _, w := range watch {
		s.Watched[colorKey(w)] = counts[[3]uint8{w.R, w.G, w.B}]
	}
	s.Distinct = len(counts)
	if overflow {
		s.Distinct = distinctCap + 1 // 打ち切り。多色であることだけが分かればよい
	}
	best := 0
	for _, n := range counts {
		best = max(best, n)
	}
	s.Dominant = float64(best) / float64(s.Total)
	return s
}

// verdict は写り方の判定。内容があるかを見る（正しい内容かどうかは別の検査）。
func (s pixelStats) verdict() string {
	if s.Total == 0 {
		return verdictUntouched
	}
	share := func(n int) float64 { return float64(n) / float64(s.Total) }
	switch {
	case share(s.Untouched) >= blankShare:
		return verdictUntouched
	case share(s.Black) >= blankShare:
		return verdictAllBlack
	case s.Dominant >= blankShare:
		return verdictUniform
	}
	return verdictContent
}

// sameShare は a と b の同じ位置の画素（RGB）が等しい割合を返す。大きさが違えば 0。
func sameShare(a, b *image.RGBA) float64 {
	if a.Bounds().Size() != b.Bounds().Size() {
		return 0
	}
	n := a.Bounds().Dx() * a.Bounds().Dy()
	if n == 0 {
		return 0
	}
	same := 0
	for y := range a.Bounds().Dy() {
		ra := a.Pix[y*a.Stride:]
		rb := b.Pix[y*b.Stride:]
		for x := range a.Bounds().Dx() {
			if ra[x*4] == rb[x*4] && ra[x*4+1] == rb[x*4+1] && ra[x*4+2] == rb[x*4+2] {
				same++
			}
		}
	}
	return float64(same) / float64(n)
}
