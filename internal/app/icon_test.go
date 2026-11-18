package app

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

// トレイの ICO が、素材の約束（16・20・24・32px。透過・単色）のとおりで、明るいタスクバー用が黒、暗いタスクバー用が白であること。
// 取り違えると、明るいタスクバーで白いアイコン（見えない）が出る。
func TestTrayIconsByTaskbarTheme(t *testing.T) {
	tests := []struct {
		name string
		ico  []byte
		ink  [3]uint8 // 不透明な画素の色（RGB）
	}{
		{"明るいタスクバー", TrayIconLightTaskbar, [3]uint8{0x05, 0x05, 0x05}},
		{"暗いタスクバー", TrayIconDarkTaskbar, [3]uint8{0xff, 0xff, 0xff}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := tt.ico
			if len(d) < 6 || binary.LittleEndian.Uint16(d[0:2]) != 0 || binary.LittleEndian.Uint16(d[2:4]) != 1 {
				t.Fatalf("ICO のヘッダーではない")
			}
			n := int(binary.LittleEndian.Uint16(d[4:6]))
			var sizes []int
			for i := range n {
				e := d[6+16*i:]
				size, off, length := int(e[0]), int(binary.LittleEndian.Uint32(e[12:16])), int(binary.LittleEndian.Uint32(e[8:12]))
				img, err := png.Decode(bytes.NewReader(d[off : off+length]))
				if err != nil {
					t.Fatalf("エントリ %d: PNG を読めない: %v", i, err)
				}
				if b := img.Bounds(); b.Dx() != size || b.Dy() != size {
					t.Errorf("エントリ %d: 見出しの大きさ %d と画像の大きさ %v が違う", i, size, b)
				}
				sizes = append(sizes, size)
				opaque := 0
				for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
					for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
						r, g, b, a := img.At(x, y).RGBA()
						if a != 0xffff {
							continue
						}
						opaque++
						if got := [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}; got != tt.ink {
							t.Fatalf("%dpx (%d,%d): 不透明な画素の色 %v、want %v", size, x, y, got, tt.ink)
						}
					}
				}
				if opaque == 0 {
					t.Errorf("%dpx: 不透明な画素が無い", size)
				}
			}
			want := []int{16, 20, 24, 32}
			if len(sizes) != len(want) {
				t.Fatalf("大きさ = %v, want %v", sizes, want)
			}
			for i := range want {
				if sizes[i] != want[i] {
					t.Fatalf("大きさ = %v, want %v", sizes, want)
				}
			}
		})
	}
}
