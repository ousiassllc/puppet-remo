package platformtest

import (
	"context"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// FilePickerDriver は開いているファイル選択ダイアログを操作する（ユーザーの代わり）。偽の実装は FakeFilePicker が兼ねる。
// 実機のダイアログは自動で操作できないので、実機のスモークでは使わない（手動で確かめる。smoke-windows.md §3.8 L9）。
type FilePickerDriver interface {
	// WaitOpen はダイアログが開くまで待つ。
	WaitOpen(ctx context.Context) error
	// Choose はダイアログで path を選ぶ（開くまで待つ）。
	Choose(ctx context.Context, path string) error
	// Dismiss はダイアログをキャンセルする（開くまで待つ）。
	Dismiss(ctx context.Context) error
}

// contractTimeout は、契約テストが非同期の完了を待つ上限（待ちきれなければテストを失敗にする）。
const contractTimeout = 10 * time.Second

type pickResult struct {
	path string
	ok   bool
	err  error
}

// RunFilePickerContract は platform.FilePicker の契約を検査する。
func RunFilePickerContract(t *testing.T, newPicker func(t *testing.T) (platform.FilePicker, FilePickerDriver)) {
	t.Helper()

	// pick は PickExecutable を別 goroutine で始め、結果を受け取る関数を返す。
	pick := func(ctx context.Context, p platform.FilePicker) func() pickResult {
		res := make(chan pickResult, 1)
		go func() {
			path, ok, err := p.PickExecutable(ctx)
			res <- pickResult{path, ok, err}
		}()
		return func() pickResult {
			select {
			case r := <-res:
				return r
			case <-time.After(contractTimeout):
				t.Fatal("PickExecutable did not return")
				return pickResult{}
			}
		}
	}
	driverCtx := func(t *testing.T) context.Context {
		ctx, cancel := context.WithTimeout(context.Background(), contractTimeout)
		t.Cleanup(cancel)
		return ctx
	}

	t.Run("chosen path is returned", func(t *testing.T) {
		p, d := newPicker(t)
		wait := pick(context.Background(), p)
		const want = `C:\Program Files\App\app.exe`
		if err := d.Choose(driverCtx(t), want); err != nil {
			t.Fatalf("Choose: %v", err)
		}
		r := wait()
		if r.err != nil || !r.ok || r.path != want {
			t.Errorf("PickExecutable = (%q, %v, %v), want (%q, true, nil)", r.path, r.ok, r.err, want)
		}
	})

	t.Run("dismissing the dialog is ok=false without an error", func(t *testing.T) {
		p, d := newPicker(t)
		wait := pick(context.Background(), p)
		if err := d.Dismiss(driverCtx(t)); err != nil {
			t.Fatalf("Dismiss: %v", err)
		}
		r := wait()
		if r.err != nil || r.ok || r.path != "" {
			t.Errorf("PickExecutable = (%q, %v, %v), want (\"\", false, nil)", r.path, r.ok, r.err)
		}
	})

	t.Run("canceling the context closes the open dialog", func(t *testing.T) {
		p, d := newPicker(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		wait := pick(ctx, p)
		if err := d.WaitOpen(driverCtx(t)); err != nil {
			t.Fatalf("WaitOpen: %v", err)
		}
		cancel()
		r := wait()
		if r.ok || r.path != "" {
			t.Errorf("PickExecutable = (%q, %v), want no selection", r.path, r.ok)
		}
		requireCanceled(t, r.err, "PickExecutable")
	})

	t.Run("canceled context", func(t *testing.T) {
		p, _ := newPicker(t)
		path, ok, err := p.PickExecutable(canceledCtx())
		requireCanceled(t, err, "PickExecutable")
		if ok || path != "" {
			t.Errorf("PickExecutable = (%q, %v), want no selection", path, ok)
		}
	})
}
