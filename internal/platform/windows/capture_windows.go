//go:build windows

package windows

import (
	"context"
	"errors"
	"image"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.Capturer = (*Capturer)(nil)

// Capturer は platform.Capturer の Windows 実装。1 枚撮り（Grab）は GDI BitBlt、継続キャプチャ（Watch）は DXGI Desktop Duplication（失敗したら GDI）。
// 状態を持たないので、どの goroutine からも呼べる。
type Capturer struct{}

// NewCapturer はキャプチャを作る。座標を物理ピクセルで扱うため、プロセスを Per-Monitor DPI Aware にする（EnableDPIAwareness）。
func NewCapturer() *Capturer {
	EnableDPIAwareness()
	return &Capturer{}
}

// Monitors はモニター一覧（左から右、上から下の順に Index 0..）を返す。
func (*Capturer) Monitors(ctx context.Context) ([]domain.Monitor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return listMonitors()
}

// Grab は monitor の画面を 1 枚撮る。画像の大きさはモニターの物理ピクセルで、原点は (0, 0)。
func (*Capturer) Grab(ctx context.Context, monitor int) (*image.RGBA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	monitors, err := listMonitors()
	if err != nil {
		return nil, err
	}
	if monitor < 0 || monitor >= len(monitors) {
		return nil, domain.ErrInvalidArgument.WithMsg("unknown monitor")
	}
	return grabRegion(monitors[monitor])
}

// Watch は monitor の継続キャプチャを始める。DXGI Desktop Duplication を使い、開けない・復旧できないときは GDI に切り替える
// （制御は runWatch。契約は platform.Capturer.Watch）。モニターごとに 1 本の goroutine で、ctx の取り消しで止まる。
func (c *Capturer) Watch(ctx context.Context, monitor int, fps int) (<-chan platform.Frame, <-chan error, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if fps <= 0 {
		return nil, nil, domain.ErrInvalidArgument.WithMsg("fps must be positive")
	}
	monitors, err := listMonitors()
	if err != nil {
		return nil, nil, err
	}
	if monitor < 0 || monitor >= len(monitors) {
		return nil, nil, domain.ErrInvalidArgument.WithMsg("unknown monitor")
	}
	frames, errs := startWatch(ctx, watchConfig{
		interval: watchInterval(fps),
		// 再初期化では、解像度などが変わっていてもよいように、モニターの一覧を引き直す。
		openDXGI: func() (frameSource, error) {
			current, err := listMonitors()
			if err != nil {
				return nil, err
			}
			if monitor >= len(current) {
				return nil, errors.New("monitor is gone")
			}
			return openDXGISource(current[monitor])
		},
		openGDI: func() frameSource {
			return newGDISource(func(ctx context.Context) (*image.RGBA, error) { return c.Grab(ctx, monitor) })
		},
		recoverTries: recoverTries,
		recoverDelay: recoverDelay,
	})
	return frames, errs, nil
}

// grabRegion は仮想デスクトップ上の m の範囲を BitBlt で 32 ビットの DIB へ写し、RGBA にして返す。
func grabRegion(m domain.Monitor) (*image.RGBA, error) {
	w, h := m.Size.W, m.Size.H
	screenDC, _, err := w32GetDC.Call(0)
	if screenDC == 0 {
		return nil, captureFailed("GetDC", err)
	}
	defer func() { _, _, _ = w32ReleaseDC.Call(0, screenDC) }()

	memDC, _, err := w32CreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, captureFailed("CreateCompatibleDC", err)
	}
	defer func() { _, _, _ = w32DeleteDC.Call(memDC) }()

	header := w32BitmapInfoHeader{
		Size:     uint32(unsafe.Sizeof(w32BitmapInfoHeader{})),
		Width:    int32(w),
		Height:   -int32(h), // 負は上から下
		Planes:   1,
		BitCount: 32,
	}
	var bits unsafe.Pointer
	bitmap, _, err := w32CreateDIBSection.Call(screenDC, uintptr(unsafe.Pointer(&header)), dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		return nil, captureFailed("CreateDIBSection", err)
	}
	defer func() { _, _, _ = w32DeleteObject.Call(bitmap) }()

	old, _, _ := w32SelectObject.Call(memDC, bitmap)
	defer func() { _, _, _ = w32SelectObject.Call(memDC, old) }()

	ok, _, err := w32BitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h), screenDC, uintptr(int32(m.Origin.X)), uintptr(int32(m.Origin.Y)), srccopy|captureblt)
	if ok == 0 {
		return nil, captureFailed("BitBlt", err)
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bgraToRGBA(img.Pix, unsafe.Slice((*byte)(bits), w*h*4))
	return img, nil
}

// captureFailed はキャプチャの失敗を、デスクトップを使えないなら ErrDesktopUnavailable に、そうでなければ原因つきのエラーにする。
func captureFailed(call string, err error) error {
	return desktopOr(err, call)
}

var (
	enumMonitorsMu       sync.Mutex
	enumMonitorsResult   []monitorRecord
	enumMonitorsCallback = sync.OnceValue(func() uintptr { return windows.NewCallback(monitorEnumProc) })
)

type monitorRecord struct {
	handle  uintptr
	name    string
	primary bool
	rect    w32Rect
}

func monitorEnumProc(hMonitor, _, _, _ uintptr) uintptr {
	info := w32MonitorInfoEx{Size: uint32(unsafe.Sizeof(w32MonitorInfoEx{}))}
	if r, _, _ := w32GetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&info))); r != 0 {
		enumMonitorsResult = append(enumMonitorsResult, monitorRecord{
			handle:  hMonitor,
			name:    windows.UTF16ToString(info.Device[:]),
			primary: info.Flags&monitorInfoPrimary != 0,
			rect:    info.Monitor,
		})
	}
	return 1 // 列挙を続ける
}

// listMonitors は接続中のモニターを、位置の順に Index を振って返す。
func listMonitors() ([]domain.Monitor, error) {
	enumMonitorsMu.Lock()
	enumMonitorsResult = nil
	r, _, err := w32EnumDisplayMonitors.Call(0, 0, enumMonitorsCallback(), 0)
	records := enumMonitorsResult
	enumMonitorsResult = nil
	enumMonitorsMu.Unlock()
	if r == 0 {
		return nil, desktopOr(err, "EnumDisplayMonitors")
	}

	monitors := make([]domain.Monitor, 0, len(records))
	for _, rec := range records {
		monitors = append(monitors, domain.Monitor{
			Name:    rec.name,
			Primary: rec.primary,
			Origin:  domain.Point{X: int(rec.rect.Left), Y: int(rec.rect.Top)},
			Size:    domain.Size{W: rec.rect.width(), H: rec.rect.height()},
			DPI:     monitorDPI(rec.handle),
		})
	}
	if len(monitors) == 0 {
		return nil, domain.ErrDesktopUnavailable.WithMsg("no monitor is attached")
	}
	return orderMonitors(monitors), nil
}

// monitorDPI は実効 DPI（96 が 100%）。取得できなければ 0（DPIScale は 1.0 になる）。
func monitorDPI(handle uintptr) float64 {
	if w32GetDpiForMonitor.Find() != nil {
		return 0
	}
	var dpiX, dpiY uint32
	if hr, _, _ := w32GetDpiForMonitor.Call(handle, mdtEffectiveDPI, uintptr(unsafe.Pointer(&dpiX)), uintptr(unsafe.Pointer(&dpiY))); hr != 0 {
		return 0
	}
	return float64(dpiX)
}
