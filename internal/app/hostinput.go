package app

import (
	"fmt"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// hostInputMonitor は、ホストの実入力の検知（platform.HostInputWatcher）を、設定（hostinput.pause_enabled）に合わせて
// 動かす・止める。無効の間はフック自体を置かない（キーボード・マウスのフックを必要のないときに持たない）。
type hostInputMonitor struct {
	watcher platform.HostInputWatcher
	notify  func(kind platform.HostInputKind)

	mu   sync.Mutex
	stop func() // 監視中だけ非 nil
}

func newHostInputMonitor(w platform.HostInputWatcher, notify func(kind platform.HostInputKind)) *hostInputMonitor {
	return &hostInputMonitor{watcher: w, notify: notify}
}

// Apply は enabled に合わせて監視を始める・止める。すでにその状態なら何もしない。始められなければエラー（監視しない状態のまま。
// service.HostInputPauser は実入力が来ないので停止しない）。
func (m *hostInputMonitor) Apply(enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case enabled && m.stop == nil:
		stop, err := m.watcher.Watch(m.notify)
		if err != nil {
			return fmt.Errorf("watch host input: %w", err)
		}
		m.stop = stop
	case !enabled && m.stop != nil:
		m.stop()
		m.stop = nil
	}
	return nil
}

// Close は監視を止める。複数回呼んでよい。
func (m *hostInputMonitor) Close() { _ = m.Apply(false) }
