package platformtest

import (
	"context"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.Clipboard = (*FakeClipboard)(nil)

// FakeClipboard は platform.Clipboard の偽の実装。
type FakeClipboard struct {
	Recorder

	mu   sync.Mutex
	text string
}

func NewFakeClipboard() *FakeClipboard { return &FakeClipboard{} }

// SetText は ReadText が返すテキストを設定する（記録されない）。
func (f *FakeClipboard) SetText(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.text = s
}

func (f *FakeClipboard) ReadText(ctx context.Context) (string, error) {
	if err := f.enter(ctx, "ReadText"); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.text, nil
}

func (f *FakeClipboard) WriteText(ctx context.Context, s string) error {
	if err := f.enter(ctx, "WriteText", s); err != nil {
		return err
	}
	f.SetText(s)
	return nil
}

// peek は記録せずに現在のテキストを返す（FakeApp の貼り付け用。ReadText の呼び出し回数に数えない）。
func (f *FakeClipboard) peek() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.text
}
