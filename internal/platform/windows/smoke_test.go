//go:build windows && smoke

package windows_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	pwin "github.com/ousiassllc/puppet-remo/internal/platform/windows"
)

// 実機のスモークテスト（docs/operations/smoke-windows.md）。対話デスクトップのある Windows で、
//
//	go test -tags smoke -v -count=1 ./internal/platform/windows/
//
// で実行する。同じ契約テスト（platformtest）を、CI では偽の実装に、ここでは Windows 実装に流す。
//
// 入力注入は実際にカーソルを動かし、クリック・ホイール・キー・文字入力を前面のウィンドウへ送る。
// 使い捨ての環境で、メモ帳などを前面にして実行すること。誤送信を避けるため、
// 環境変数 PUPPET_REMO_SMOKE_INPUT=1 のときだけ入力のテストを走らせる。
// クリックする座標は PUPPET_REMO_SMOKE_X / PUPPET_REMO_SMOKE_Y（既定: プライマリモニターの中央）。
// クリップボードのテストは、テキスト以外の内容を失わせるので、PUPPET_REMO_SMOKE_CLIPBOARD=1 のときだけ走らせる。

func TestSmokeCapturer(t *testing.T) {
	platformtest.RunCapturerContract(t, func(*testing.T) platform.Capturer { return pwin.NewCapturer() })
}

// TestSmokeCapturerWatchAllMonitors は全モニターの Watch を同時に開き、どれも最初のコマが来て、取り消しで止まることを確かめる
// （マルチモニターで DXGI の出力を同時に複製できること。契約テストは 1 台ずつ）。画面は読むだけ。
// 最初のコマより前にエラー（ErrDesktopUnavailable なら DXGI を失って GDI に切り替わった）が流れたら、-v のログに出す。
func TestSmokeCapturerWatchAllMonitors(t *testing.T) {
	c := pwin.NewCapturer()
	monitors, err := c.Monitors(context.Background())
	if err != nil {
		t.Fatalf("Monitors: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type watch struct {
		frames <-chan platform.Frame
		errs   <-chan error
	}
	watches := make([]watch, len(monitors))
	for i, m := range monitors {
		frames, errs, err := c.Watch(ctx, m.Index, 10)
		if err != nil {
			t.Fatalf("Watch(%d): %v", m.Index, err)
		}
		watches[i] = watch{frames, errs}
	}
	deadline := time.After(10 * time.Second)
	for i, w := range watches {
		select {
		case fr, ok := <-w.frames:
			if !ok || fr.Image == nil {
				t.Fatalf("monitor %d: no first frame", monitors[i].Index)
			}
			t.Logf("monitor %d: first frame %v", monitors[i].Index, fr.Image.Bounds())
		case err := <-w.errs:
			t.Logf("monitor %d: error before the first frame: %v", monitors[i].Index, err)
		case <-deadline:
			t.Fatalf("monitor %d: no frame within 10s", monitors[i].Index)
		}
	}
	cancel()
	for i, w := range watches {
		for w.frames != nil || w.errs != nil {
			select {
			case _, ok := <-w.frames:
				if !ok {
					w.frames = nil
				}
			case _, ok := <-w.errs:
				if !ok {
					w.errs = nil
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("monitor %d: the channels were not closed within 10s of the cancel", monitors[i].Index)
			}
		}
	}
}

func TestSmokeWindowManager(t *testing.T) {
	platformtest.RunWindowManagerContract(t, func(t *testing.T) platform.WindowManager {
		wm, err := pwin.NewWindowManager()
		if err != nil {
			t.Fatalf("NewWindowManager: %v", err)
		}
		return wm
	})
}

func TestSmokeClipboard(t *testing.T) {
	if os.Getenv("PUPPET_REMO_SMOKE_CLIPBOARD") != "1" {
		t.Skip("set PUPPET_REMO_SMOKE_CLIPBOARD=1 to run (the clipboard's non-text content is lost)")
	}
	platformtest.RunClipboardContract(t, func(*testing.T) platform.Clipboard { return pwin.NewClipboard() })
}

func TestSmokeInput(t *testing.T) {
	if os.Getenv("PUPPET_REMO_SMOKE_INPUT") != "1" {
		t.Skip("set PUPPET_REMO_SMOKE_INPUT=1 to run (moves the cursor and sends clicks and keys to the foreground window)")
	}
	platformtest.RunInputContract(t, func(t *testing.T) platform.InputInjector {
		in := pwin.NewInput()
		t.Cleanup(func() { _ = in.Close() })
		return in
	}, smokeSafePoint(t))
}

// smokeSafePoint はクリックしてよい座標。
func smokeSafePoint(t *testing.T) domain.Point {
	t.Helper()
	x, y := os.Getenv("PUPPET_REMO_SMOKE_X"), os.Getenv("PUPPET_REMO_SMOKE_Y")
	if x != "" || y != "" {
		px, errX := strconv.Atoi(x)
		py, errY := strconv.Atoi(y)
		if errX != nil || errY != nil {
			t.Fatalf("PUPPET_REMO_SMOKE_X / PUPPET_REMO_SMOKE_Y must be integers: %q, %q", x, y)
		}
		return domain.Point{X: px, Y: py}
	}
	monitors, err := pwin.NewCapturer().Monitors(context.Background())
	if err != nil {
		t.Fatalf("Monitors: %v", err)
	}
	for _, m := range monitors {
		if m.Primary {
			return domain.Point{X: m.Origin.X + m.Size.W/2, Y: m.Origin.Y + m.Size.H/2}
		}
	}
	t.Fatal("no primary monitor")
	return domain.Point{}
}
