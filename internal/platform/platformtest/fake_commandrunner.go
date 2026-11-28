package platformtest

import (
	"context"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.CommandRunner = (*FakeCommandRunner)(nil)

// FakeCommandRunner は platform.CommandRunner の偽の実装。プロセスは起こさず、受けた指定を記録し（Specs）、
// SetResult で決めた結果（既定は終了コード 0・出力なし）を返す。SetHandler を設定すると、結果をそちらで決められる（ctx を見て待つなど）。
type FakeCommandRunner struct {
	Recorder

	hmu     sync.Mutex
	result  platform.CommandResult
	handler func(ctx context.Context, spec platform.CommandSpec) (platform.CommandResult, error)
}

func NewFakeCommandRunner() *FakeCommandRunner { return &FakeCommandRunner{} }

// SetResult は以後の Run が返す結果を決める。
func (f *FakeCommandRunner) SetResult(res platform.CommandResult) {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	f.result = res
}

// SetHandler は Run の結果を fn で決める（nil で解除）。SetResult より優先する。
func (f *FakeCommandRunner) SetHandler(fn func(ctx context.Context, spec platform.CommandSpec) (platform.CommandResult, error)) {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	f.handler = fn
}

// Specs は受けた指定を、受けた順に返す（Reset で消える）。
func (f *FakeCommandRunner) Specs() []platform.CommandSpec {
	var out []platform.CommandSpec
	for _, c := range f.CallsTo("Run") {
		out = append(out, c.Args[0].(platform.CommandSpec))
	}
	return out
}

func (f *FakeCommandRunner) Run(ctx context.Context, spec platform.CommandSpec) (platform.CommandResult, error) {
	if err := f.enter(ctx, "Run", spec); err != nil {
		return platform.CommandResult{}, err
	}
	f.hmu.Lock()
	res, handler := f.result, f.handler
	f.hmu.Unlock()
	if handler != nil {
		return handler(ctx, spec)
	}
	return res, nil
}
