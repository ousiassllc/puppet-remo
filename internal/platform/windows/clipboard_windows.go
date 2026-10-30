//go:build windows

package windows

import (
	"context"
	"runtime"
	"time"
	"unsafe"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.Clipboard = (*Clipboard)(nil)

const (
	// clipboardOpenAttempts と clipboardRetryInterval は、他のプロセスが開いているクリップボードを待つ上限（合計で約 0.5 秒）。
	clipboardOpenAttempts  = 25
	clipboardRetryInterval = 20 * time.Millisecond
)

// Clipboard は platform.Clipboard の Windows 実装（CF_UNICODETEXT）。
// 開閉は OS スレッドに結びつくので、呼び出しごとにスレッドを固定する。状態を持たず、どの goroutine からも呼べる。
type Clipboard struct{}

// NewClipboard はクリップボードの実装を作る。
func NewClipboard() *Clipboard { return &Clipboard{} }

// ReadText はクリップボードのテキストを返す。テキスト以外が入っているときは空文字列。
func (*Clipboard) ReadText(ctx context.Context) (string, error) {
	var text string
	err := withClipboard(ctx, func() error {
		if ok, _, _ := w32IsClipboardFormat.Call(cfUnicodeText); ok == 0 {
			return nil
		}
		h, _, err := w32GetClipboardData.Call(cfUnicodeText)
		if h == 0 {
			return clipboardFailed("GetClipboardData", err)
		}
		size, _, _ := w32GlobalSize.Call(h)
		p, _, err := w32GlobalLock.Call(h)
		if p == 0 {
			return clipboardFailed("GlobalLock", err)
		}
		defer w32GlobalUnlock.Call(h)
		text = decodeClipboardText(unsafe.Slice((*uint16)(fromUintptr(p)), size/2))
		return nil
	})
	return text, err
}

// WriteText はクリップボードの内容をテキストに置き換える。
func (*Clipboard) WriteText(ctx context.Context, s string) error {
	data := encodeClipboardText(s)
	return withClipboard(ctx, func() error {
		mem, _, err := w32GlobalAlloc.Call(gmemMoveable, uintptr(len(data))*2)
		if mem == 0 {
			return clipboardFailed("GlobalAlloc", err)
		}
		p, _, err := w32GlobalLock.Call(mem)
		if p == 0 {
			w32GlobalFree.Call(mem)
			return clipboardFailed("GlobalLock", err)
		}
		copy(unsafe.Slice((*uint16)(fromUintptr(p)), len(data)), data)
		w32GlobalUnlock.Call(mem)

		if r, _, err := w32EmptyClipboard.Call(); r == 0 {
			w32GlobalFree.Call(mem)
			return clipboardFailed("EmptyClipboard", err)
		}
		if r, _, err := w32SetClipboardData.Call(cfUnicodeText, mem); r == 0 {
			w32GlobalFree.Call(mem)
			return clipboardFailed("SetClipboardData", err)
		}
		return nil // 成功したらメモリの所有権はシステムに移る
	})
}

// withClipboard は fn の間だけクリップボードを開く。他のプロセスが開いている間は、上限つきで再試行する。
func withClipboard(ctx context.Context, fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var lastErr error
	for attempt := range clipboardOpenAttempts {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, _, err := w32OpenClipboard.Call(0)
		if r != 0 {
			defer w32CloseClipboard.Call()
			return fn()
		}
		lastErr = err
		if attempt == clipboardOpenAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(clipboardRetryInterval):
		}
	}
	return clipboardFailed("OpenClipboard", lastErr)
}

func clipboardFailed(call string, err error) error {
	return desktopOr(err, call)
}

// fromUintptr は GlobalLock が返した（Go のヒープの外の）アドレスをポインタにする。
func fromUintptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }
