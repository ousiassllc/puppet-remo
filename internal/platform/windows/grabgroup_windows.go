//go:build windows

package windows

import (
	"context"
	"image"
	"unsafe"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.GroupCapturer = (*Capturer)(nil)

var w32PrintWindow = w32User32.NewProc("PrintWindow")

const pwRenderFullContent = 0x00000002 // PW_RENDERFULLCONTENT

// printSentinel は PrintWindow に渡す前に DIB を塗っておく色（B, G, R, A の順）。PrintWindow が TRUE を返しても何も描かなかったとき
// （画素が触られていない）を、撮れたものと取り違えない。cmd/a2probe の sentinel と同じ値。
var printSentinel = [4]byte{13, 251, 7, 0}

// GrabGroup は members の各ウィンドウを PrintWindow(PW_RENDERFULLCONTENT) でメモリ DC へ撮り、見た目の枠の外接矩形の空（透明）のキャンバスに
// 前面が先の順で重ねて返す（ComposeGroup）。**画面の DC・BitBlt・モニター撮影・デスクトップの切り抜きは使わない**。メンバーの上に別のウィンドウが
// 重なっていても、PrintWindow はそのウィンドウ自身の内容だけを返すので、非メンバーの画素は混ざらない。
//
// 撮れないメンバーは Status で返す: 同一性（ID・PID・ProcessStart・クラス）が違う・消えた → CaptureGone、最小化・矩形が空・PrintWindow が FALSE・
// PrintWindow が何も描かなかった（DIB が触られていない）→ CaptureNotPrintable。**全面が黒い画像は撮れたものとして扱う**（GPU 描画のアプリが
// 黒く写る範囲と、それを検出できるかは cmd/a2probe の M6 と実アプリの観察で測る。実機の成立性ゲートが済むまで採用の判断はしない）。
//
// 実機での成立性（S1〜S3）は未確認（docs/operations/smoke-windows.md §3.10）。error は ctx のキャンセルだけ。
func (*Capturer) GrabGroup(ctx context.Context, members []domain.WindowRef) (platform.GroupImage, error) {
	if err := ctx.Err(); err != nil {
		return platform.GroupImage{}, err
	}
	out := platform.GroupImage{Members: make([]platform.MemberCapture, len(members))}
	layers := make([]GroupLayer, len(members))
	cache := newProcessCache()
	for i, ref := range members {
		if err := ctx.Err(); err != nil {
			return platform.GroupImage{}, err
		}
		mc := platform.MemberCapture{Ref: ref, Status: platform.CaptureGone}
		layer, status := grabMember(ref, cache)
		mc.Status = status
		layers[i] = layer
		out.Members[i] = mc
	}
	img, origin, rects := ComposeGroup(layers)
	out.Image, out.Origin = img, origin
	for i := range out.Members {
		if out.Members[i].Status != platform.CaptureOK {
			continue
		}
		if rects[i].Empty() { // 見た目の枠と画像が重ならない（置けない）ものは撮れなかったものとして扱う
			out.Members[i].Status = platform.CaptureNotPrintable
			continue
		}
		out.Members[i].Rect = rects[i]
	}
	return out, nil
}

// grabMember は ref のウィンドウ 1 枚を撮る。同一性の確認は撮影の直前（HWND の再利用を別のウィンドウの撮影にしない）。
func grabMember(ref domain.WindowRef, cache *processCache) (GroupLayer, platform.CaptureStatus) {
	hwnd, ok := parseWindowID(ref.ID)
	if !ok {
		return GroupLayer{}, platform.CaptureGone
	}
	if r, _, _ := w32IsWindow.Call(hwnd); r == 0 {
		return GroupLayer{}, platform.CaptureGone
	}
	var pid uint32
	_, _, _ = w32GetWindowThreadProcess.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != ref.PID || windowClass(hwnd) != ref.Class || cache.lookup(pid).start != ref.ProcessStart {
		return GroupLayer{}, platform.CaptureGone
	}
	if minimized, _, _ := w32IsIconic.Call(hwnd); minimized != 0 {
		return GroupLayer{}, platform.CaptureNotPrintable
	}
	frame := windowRect(hwnd)
	var wr w32Rect
	if ok, _, _ := w32GetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr))); ok == 0 || wr.width() <= 0 || wr.height() <= 0 || frame.Empty() {
		return GroupLayer{}, platform.CaptureNotPrintable
	}
	img, ok := printWindowRGBA(hwnd, wr.width(), wr.height())
	if !ok {
		return GroupLayer{}, platform.CaptureNotPrintable
	}
	return GroupLayer{
		Frame:  image.Rect(frame.X, frame.Y, frame.X+frame.W, frame.Y+frame.H),
		Window: image.Rect(int(wr.Left), int(wr.Top), int(wr.Right), int(wr.Bottom)),
		Img:    img,
	}, platform.CaptureOK
}

// printWindowRGBA は hwnd を w×h のメモリ DIB へ PrintWindow(PW_RENDERFULLCONTENT) で撮る。画面の DC は取らない。
// PrintWindow が FALSE、または DIB が触られていない（全面が sentinel）ときは ok=false。
func printWindowRGBA(hwnd uintptr, w, h int) (*image.RGBA, bool) {
	memDC, _, _ := w32CreateCompatibleDC.Call(0)
	if memDC == 0 {
		return nil, false
	}
	defer func() { _, _, _ = w32DeleteDC.Call(memDC) }()
	header := w32BitmapInfoHeader{Size: uint32(unsafe.Sizeof(w32BitmapInfoHeader{})), Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	var bits unsafe.Pointer
	bitmap, _, _ := w32CreateDIBSection.Call(0, uintptr(unsafe.Pointer(&header)), dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		return nil, false
	}
	defer func() { _, _, _ = w32DeleteObject.Call(bitmap) }()
	old, _, _ := w32SelectObject.Call(memDC, bitmap)
	defer func() { _, _, _ = w32SelectObject.Call(memDC, old) }()

	pix := unsafe.Slice((*byte)(bits), w*h*4)
	for i := 0; i < len(pix); i += 4 {
		copy(pix[i:i+4], printSentinel[:])
	}
	if r, _, _ := w32PrintWindow.Call(hwnd, memDC, pwRenderFullContent); r == 0 {
		return nil, false
	}
	if untouched(pix) {
		return nil, false
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bgraToRGBA(img.Pix, pix)
	return img, true
}

// untouched は pix のほぼ全面（99% 以上）が sentinel のままか（PrintWindow が描かなかったか）を返す。
func untouched(pix []byte) bool {
	total := len(pix) / 4
	if total == 0 {
		return true
	}
	n := 0
	for i := 0; i < len(pix); i += 4 {
		if pix[i] == printSentinel[0] && pix[i+1] == printSentinel[1] && pix[i+2] == printSentinel[2] {
			n++
		}
	}
	return n*100 >= total*99
}
