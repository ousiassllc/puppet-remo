//go:build windows && smoke

package windows_test

import (
	"context"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	pwin "github.com/ousiassllc/puppet-remo/internal/platform/windows"
)

// TestSmokeGrabGroupFailsClosed は、同一性の合わないウィンドウを撮らず、画面のコピーで補わないこと（#156 A-2。fail closed）を実機で確かめる。
// 他のウィンドウの画素は一切読まない（存在しない HWND と、同一性の値が揃わない参照だけを渡す）。入力・画面撮影・ファイルへの書き込みはしない。
//
// 実際のウィンドウを PrintWindow で撮り、メニュー・ダイアログが写ること（S1〜S3 の撮影の部分）は、専用の fixture を持つ `cmd/a2probe`
// が測る（docs/operations/smoke-windows.md §3.10.1）。ここには、他アプリのウィンドウを撮るテストを置かない。
func TestSmokeGrabGroupFailsClosed(t *testing.T) {
	c := pwin.NewCapturer()
	var gc platform.GroupCapturer = c // Windows の Capturer が GroupCapturer を実装している
	refs := []domain.WindowRef{
		{ID: "1", PID: 1, ProcessStart: 1, Class: "NoSuchClass"}, // 存在しない HWND
		{ID: "not-a-number", PID: 1, ProcessStart: 1, Class: "X"},
		{ID: "1"}, // 同一性の値が揃わない
	}
	got, err := gc.GrabGroup(context.Background(), refs)
	if err != nil {
		t.Fatalf("GrabGroup: %v", err)
	}
	if got.Image != nil {
		t.Errorf("Image = %v, want nil (nothing capturable; the desktop must never be the fallback)", got.Image.Bounds())
	}
	if len(got.Members) != len(refs) {
		t.Fatalf("Members = %d, want %d", len(got.Members), len(refs))
	}
	for i, m := range got.Members {
		if m.Status != platform.CaptureGone {
			t.Errorf("member %d status = %q, want %q", i, m.Status, platform.CaptureGone)
		}
	}
	if empty, err := gc.GrabGroup(context.Background(), nil); err != nil || empty.Image != nil {
		t.Errorf("GrabGroup(nil) = %v, %v, want a nil image", empty.Image, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gc.GrabGroup(ctx, refs); err == nil {
		t.Error("GrabGroup with a cancelled context returned no error")
	}
}
