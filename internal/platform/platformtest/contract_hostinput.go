package platformtest

import (
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// RunHostInputContract は platform.HostInputWatcher の契約を検査する。実入力は模擬できないので、開始と停止だけを検査する
// （実入力で fn が呼ばれ、自分の注入で呼ばれないことは、実機では手動で確かめる）。
func RunHostInputContract(t *testing.T, newWatcher func(t *testing.T) platform.HostInputWatcher) {
	t.Helper()
	noop := func(platform.HostInputKind) {}

	t.Run("stop is safe to call twice", func(t *testing.T) {
		w := newWatcher(t)
		stop, err := w.Watch(noop)
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if stop == nil {
			t.Fatal("Watch returned a nil stop")
		}
		stop()
		stop()
	})

	t.Run("only one watch at a time, and a new one can start after stop", func(t *testing.T) {
		w := newWatcher(t)
		stop, err := w.Watch(noop)
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if again, err := w.Watch(noop); err == nil {
			again()
			t.Error("watching twice at the same time succeeded")
		}
		stop()
		restarted, err := w.Watch(noop)
		if err != nil {
			t.Fatalf("Watch after stop: %v", err)
		}
		restarted()
	})

	t.Run("a nil callback is invalid", func(t *testing.T) {
		_, err := newWatcher(t).Watch(nil)
		requireCode(t, err, domain.ErrInvalidArgument, "Watch(nil)")
	})
}
