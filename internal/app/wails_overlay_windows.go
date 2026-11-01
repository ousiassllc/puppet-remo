//go:build windows

package app

import (
	"fmt"
	"net/url"
	"slices"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 操作中オーバーレイ（#69）の Wails 実装。実 Wails・WebView2 が要るため CI では動かず、実機スモーク
// （docs/operations/smoke-windows.md §4）で確認する。出し入れの判断は overlayController（overlay.go）にあり、偽物でテストしている。

// overlayExStyle はオーバーレイの拡張ウィンドウスタイル。Wails が既定で組み立てる値（WS_EX_CONTROLPARENT・TOPMOST・TOOLWINDOW・
// 透過のための TRANSPARENT | LAYERED）に、フォーカスを奪わない WS_EX_NOACTIVATE を足したもの。
//   - WS_EX_TRANSPARENT | WS_EX_LAYERED: クリックスルー（マウスを下のウィンドウへ通す）。platform/windows の WindowManager はこの組で
//     自プロセスのウィンドウを Overlay と判定し、InputGuard の own_window から外す（domain.WindowInfo.Overlay）
//   - WS_EX_TOOLWINDOW: タスクバーと Alt+Tab に出さない
//   - WS_EX_NOACTIVATE: 表示してもアクティブにならず、操作中のウィンドウからフォーカスを奪わない
//
// ExStyle を指定すると Wails の組み立てを丸ごと置き換えるので、必要なものはここですべて挙げる。
const overlayExStyle = w32.WS_EX_CONTROLPARENT | w32.WS_EX_TOPMOST | w32.WS_EX_TOOLWINDOW |
	w32.WS_EX_TRANSPARENT | w32.WS_EX_LAYERED | w32.WS_EX_NOACTIVATE

// wailsOverlay は出しているオーバーレイのウィンドウ（1 モニターに 1 枚）。ShowOverlay・HideOverlay は overlayController の
// goroutine だけが呼ぶが、念のため mu で守る。
type wailsOverlay struct {
	mu       sync.Mutex
	seq      int                          // ウィンドウ名を一意にする連番（Wails は名前でウィンドウを引く）
	monitors []domain.Monitor             // wins を作ったときのモニター構成
	wins     []*application.WebviewWindow // monitors と同じ順
}

// ShowOverlay は Windows の実装。すでに出していてモニター構成が同じなら、ページの URL（label）だけを差し替える。
// 構成が違えば（モニターの抜き差し・解像度・配置・DPI の変更）作り直す。出している最中の構成の変化は追わず、次の ShowOverlay で反映する。
func (w wailsWindows) ShowOverlay(monitors []domain.Monitor, label string) {
	if len(monitors) == 0 {
		return
	}
	o := &w.h.overlay
	o.mu.Lock()
	defer o.mu.Unlock()

	pageURL := overlayURL(label)
	if len(o.wins) > 0 && slices.Equal(o.monitors, monitors) {
		for _, win := range o.wins {
			win.SetURL(pageURL)
		}
		return
	}
	o.closeLocked()
	o.seq++
	for _, m := range monitors {
		o.wins = append(o.wins, w.h.newOverlayWindow(o.seq, m, pageURL))
	}
	o.monitors = slices.Clone(monitors)
}

// HideOverlay は Windows の実装。出しているオーバーレイをすべて閉じる。
func (w wailsWindows) HideOverlay() {
	o := &w.h.overlay
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closeLocked()
}

func (o *wailsOverlay) closeLocked() {
	for _, win := range o.wins {
		win.Close()
	}
	o.wins, o.monitors = nil, nil
}

// overlayURL はオーバーレイのページ（GET /overlay）の URL。label の長さの制限と HTML のエスケープはページ側が行う。
func overlayURL(label string) string { return "/overlay?label=" + url.QueryEscape(label) }

// newOverlayWindow はモニター m を覆うオーバーレイのウィンドウを作って表示する。
//   - フレームなし・透過の背景・最前面・サイズ変更なし・タスクバーなし・フォーカスを奪わない・クリックスルー（overlayExStyle）
//   - ContentProtectionEnabled: true（WDA_EXCLUDEFROMCAPTURE）。スクリーンショットにもライブ画面にも写さない（NFR-S06）
//   - 位置と大きさは、モニターの物理座標（domain.Monitor は物理ピクセル）で SetPhysicalBounds に渡す。Wails の X・Y・Width・Height は
//     DIP（デバイス非依存）で、DPI の違う複数モニターでは物理座標とずれるため使わない。非表示で作り、置いてから表示する
func (h *WailsHost) newOverlayWindow(seq int, m domain.Monitor, pageURL string) *application.WebviewWindow {
	win := h.wails.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:                     fmt.Sprintf("overlay-%d-%d", seq, m.Index),
		Title:                    "",  // タイトルなし: list_windows（タイトルのないウィンドウは含めない）に出さない
		Width:                    200, // 仮の大きさ。直後に物理座標で置き直す
		Height:                   200,
		Hidden:                   true,
		Frameless:                true,
		DisableResize:            true,
		AlwaysOnTop:              true,
		URL:                      pageURL,
		BackgroundType:           application.BackgroundTypeTransparent,
		BackgroundColour:         application.NewRGBA(0, 0, 0, 0),
		IgnoreMouseEvents:        true,
		ContentProtectionEnabled: true,
		MinimiseButtonState:      application.ButtonHidden,
		MaximiseButtonState:      application.ButtonHidden,
		Windows: application.WindowsWindow{
			ExStyle:                           overlayExStyle,
			HiddenOnTaskbar:                   true,
			DisableIcon:                       true,
			DisableFramelessWindowDecorations: true, // 影・角丸を付けない
		},
	})
	want := application.Rect{X: m.Origin.X, Y: m.Origin.Y, Width: m.Size.W, Height: m.Size.H}
	win.SetPhysicalBounds(want)
	win.Show()
	// DPI の違うモニターへ移すと、最初の指定がその DPI の再計算でずれることがあるので、確かめて 1 度だけ置き直す。
	if got := win.PhysicalBounds(); got != want {
		win.SetPhysicalBounds(want)
		if got = win.PhysicalBounds(); got != want {
			h.log.Warn("operation overlay is not exactly on the monitor", "monitor", m.Index, "want", want, "got", got)
		}
	}
	return win
}
