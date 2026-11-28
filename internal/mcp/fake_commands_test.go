package mcp

import (
	"context"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// fakeCommands は service.CommandExec の偽物。検査（許可リスト・引数・ON/OFF・監査）は本物の CommandExec のテスト（service）が持ち、
// ここは mcp 層の整形・登録・リースの扱いを確かめるためのもの。リースの確認は本物の LeaseManager で、CommandExec と同じ順序（KillSwitch → リース）で行う。
type fakeCommands struct {
	leases service.LeaseManager
	kill   service.KillSwitchState

	mu   sync.Mutex
	runs []service.CommandRunRequest
	// result が Run の結果。err が非 nil なら、検査の後に Run・List がこれを返す。
	result service.CommandRunResult
	infos  []service.CommandInfo
	err    error
}

func newFakeCommands(leases service.LeaseManager, kill service.KillSwitchState) *fakeCommands {
	return &fakeCommands{leases: leases, kill: kill, result: service.CommandRunResult{Name: "echo-version", Stdout: []byte("1.2.3\n"), Duration: 12 * time.Millisecond}}
}

func (f *fakeCommands) Run(ctx context.Context, who domain.Actor, req service.CommandRunRequest) (service.CommandRunResult, error) {
	if f.kill.Active() {
		return service.CommandRunResult{}, domain.ErrKillSwitchActive
	}
	if err := f.err; err != nil {
		return service.CommandRunResult{}, err
	}
	if err := f.leases.Check(who); err != nil {
		if who.Kind != domain.ActorMCP {
			return service.CommandRunResult{}, err
		}
		if _, err := f.leases.Acquire(ctx, who); err != nil {
			return service.CommandRunResult{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, req)
	return f.result, nil
}

func (f *fakeCommands) List(context.Context, domain.Actor) ([]service.CommandInfo, error) {
	if f.kill.Active() {
		return nil, domain.ErrKillSwitchActive
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.infos, nil
}

func (f *fakeCommands) CancelAll(context.Context) error { return nil }
