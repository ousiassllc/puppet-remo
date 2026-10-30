package platformtest

import (
	"context"
	"sync"
)

// FakeAutostart はログオン時の自動起動の登録（service.Autostart と同じメソッド集合）の偽の実装。
// 実装は platform/windows の Autostart（HKCU の Run キー）。
type FakeAutostart struct {
	Recorder

	mu      sync.Mutex
	enabled bool
}

func NewFakeAutostart() *FakeAutostart { return &FakeAutostart{} }

func (f *FakeAutostart) Enabled(ctx context.Context) (bool, error) {
	if err := f.enter(ctx, "Enabled"); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled, nil
}

func (f *FakeAutostart) Set(ctx context.Context, enabled bool) error {
	if err := f.enter(ctx, "Set", enabled); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled = enabled
	return nil
}
