package platformtest

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var (
	_ platform.FilePicker = (*FakeFilePicker)(nil)
	_ FilePickerDriver    = (*FakeFilePicker)(nil)
)

// FakeFilePicker は platform.FilePicker の偽の実装。PickExecutable は「ダイアログを開いた」状態で、
// Choose（選択）・Dismiss（キャンセル）・ctx の取り消しのどれかが来るまで待つ。操作する側（テスト）は FilePickerDriver の
// メソッドで、ダイアログを操作するユーザーの代わりをする。
type FakeFilePicker struct {
	Recorder

	opened  chan struct{}
	answers chan pickAnswer
}

type pickAnswer struct {
	path string
	ok   bool
}

func NewFakeFilePicker() *FakeFilePicker {
	return &FakeFilePicker{opened: make(chan struct{}, 16), answers: make(chan pickAnswer)}
}

func (f *FakeFilePicker) PickExecutable(ctx context.Context) (string, bool, error) {
	if err := f.enter(ctx, "PickExecutable"); err != nil {
		return "", false, err
	}
	select {
	case f.opened <- struct{}{}:
	default:
	}
	select {
	case a := <-f.answers:
		return a.path, a.ok, nil
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
}

// WaitOpen は PickExecutable がダイアログを開くまで待つ。
func (f *FakeFilePicker) WaitOpen(ctx context.Context) error {
	select {
	case <-f.opened:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Choose は開いているダイアログで path を選ぶ。ダイアログが開くまで待つ。
func (f *FakeFilePicker) Choose(ctx context.Context, path string) error {
	return f.answer(ctx, pickAnswer{path: path, ok: true})
}

// Dismiss は開いているダイアログをキャンセルする。ダイアログが開くまで待つ。
func (f *FakeFilePicker) Dismiss(ctx context.Context) error {
	return f.answer(ctx, pickAnswer{})
}

func (f *FakeFilePicker) answer(ctx context.Context, a pickAnswer) error {
	select {
	case f.answers <- a:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
