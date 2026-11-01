package windows

// bgraToRGBA は GDI の 32 ビット DIB（B, G, R, 未使用）を RGBA（不透明）へ変換する。
// GDI はアルファを書かない（0 のまま）ので、A は 255 にする。dst と src は同じ長さ（4 の倍数）。
func bgraToRGBA(dst, src []byte) {
	n := min(len(dst), len(src)) &^ 3
	for i := 0; i < n; i += 4 {
		dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], 0xFF
	}
}

// makeOpaque は RGBA のアルファをすべて 255 にする。DXGI のコマはアルファが 0 や不定のことがあるので、GDI の Grab（bgraToRGBA）と同じく不透明にそろえる。
func makeOpaque(pix []byte) {
	for i := 3; i < len(pix); i += 4 {
		pix[i] = 0xFF
	}
}
