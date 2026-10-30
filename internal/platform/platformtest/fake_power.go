package platformtest

import (
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.Power = (*FakePower)(nil)

// FakePower は platform.Power の偽の実装。
type FakePower struct {
	Recorder

	mu    sync.Mutex
	awake bool
}

func NewFakePower() *FakePower { return &FakePower{} }

func (f *FakePower) KeepAwake(on bool) {
	_ = f.enterSync("KeepAwake", on) // 戻り値のない interface なので、注入されたエラーは使わない
	f.mu.Lock()
	defer f.mu.Unlock()
	f.awake = on
}

// Awake は最後の KeepAwake の値を返す。
func (f *FakePower) Awake() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.awake
}
