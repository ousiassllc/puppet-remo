package windows

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"unicode/utf16"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestSendError(t *testing.T) {
	tests := []struct {
		name    string
		errno   uintptr
		desktop bool
		want    domain.Code
	}{
		{"desktop locked", errnoAccessDenied, false, domain.CodeDesktopUnavailable},
		{"desktop locked, other error", 6, false, domain.CodeDesktopUnavailable},
		{"UIPI", errnoAccessDenied, true, domain.CodeInputBlocked},
		{"other", 87, true, domain.CodeInternal},
		{"no error code", 0, true, domain.CodeInternal},
	}
	for _, tt := range tests {
		err := sendError(0, 3, tt.errno, tt.desktop)
		if got := domain.CodeOf(err); got != tt.want {
			t.Errorf("%s: code = %q, want %q (err %v)", tt.name, got, tt.want, err)
		}
	}
}

func TestBGRAToRGBA(t *testing.T) {
	src := []byte{1, 2, 3, 0, 10, 20, 30, 0}
	dst := make([]byte, len(src))
	bgraToRGBA(dst, src)
	if want := []byte{3, 2, 1, 255, 30, 20, 10, 255}; !bytes.Equal(dst, want) {
		t.Errorf("dst = %v, want %v", dst, want)
	}
	bgraToRGBA(dst[:0], src) // 長さ 0 でも安全
	bgraToRGBA(dst, src[:5]) // 4 の倍数に満たない端は無視
}

func TestClipboardTextRoundTrip(t *testing.T) {
	for _, s := range []string{"", "hello", "日本語 😀 mixed", "line1\r\nline2\ttab", " leading and trailing ", "a\nb"} {
		units := encodeClipboardText(s)
		if units[len(units)-1] != 0 {
			t.Errorf("%q: not NUL terminated", s)
		}
		if got := decodeClipboardText(units); got != s {
			t.Errorf("round trip %q = %q", s, got)
		}
	}
}

func TestDecodeClipboardTextStopsAtNUL(t *testing.T) {
	units := append(utf16.Encode([]rune("abc")), 0, 'x', 'y')
	if got := decodeClipboardText(units); got != "abc" {
		t.Errorf("got %q, want abc", got)
	}
	if got := decodeClipboardText(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
	if got := decodeClipboardText([]uint16{0xD83D}); got != "\uFFFD" {
		t.Errorf("lone surrogate = %q", got)
	}
}

func TestThreadRunnerSerializesOnOneGoroutine(t *testing.T) {
	r := newThreadRunner()
	defer r.close()

	var running, overlaps atomic.Int32
	done := make(chan struct{})
	const callers = 16
	for range callers {
		go func() {
			defer func() { done <- struct{}{} }()
			_ = r.do(context.Background(), func(context.Context) error {
				if running.Add(1) != 1 {
					overlaps.Add(1)
				}
				running.Add(-1)
				return nil
			})
		}()
	}
	for range callers {
		<-done
	}
	if overlaps.Load() != 0 {
		t.Errorf("%d overlapping calls", overlaps.Load())
	}
}

func TestThreadRunnerReturnsResultAndCanceled(t *testing.T) {
	r := newThreadRunner()
	defer r.close()

	want := errors.New("boom")
	if err := r.do(context.Background(), func(context.Context) error { return want }); err != want {
		t.Errorf("error = %v, want %v", err, want)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if err := r.do(ctx, func(context.Context) error { called = true; return nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if called {
		t.Error("fn ran with a canceled context")
	}
}

func TestThreadRunnerAfterClose(t *testing.T) {
	r := newThreadRunner()
	r.close()
	r.close() // 複数回呼んでも安全
	if err := r.do(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, errClosed) {
		t.Errorf("error = %v, want errClosed", err)
	}
}

func TestDragPathStartsAfterFrom(t *testing.T) {
	path := dragPath(domain.Point{X: 0, Y: 0}, domain.Point{X: 8, Y: 0})
	want := []domain.Point{{X: 1}, {X: 2}, {X: 3}, {X: 4}, {X: 5}, {X: 6}, {X: 7}, {X: 8}}
	if !reflect.DeepEqual(path, want) {
		t.Errorf("path = %v, want %v", path, want)
	}
}
