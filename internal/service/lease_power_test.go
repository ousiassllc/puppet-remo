package service

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// keepAwakeCalls は FakePower が受けた KeepAwake の引数を、呼ばれた順に返す。
func keepAwakeCalls(e *ctlEnv) []bool {
	var got []bool
	for _, c := range e.power.CallsTo("KeepAwake") {
		got = append(got, c.Args[0].(bool))
	}
	return got
}

func wantKeepAwakeCalls(t *testing.T, e *ctlEnv, want ...bool) {
	t.Helper()
	if got := keepAwakeCalls(e); !slices.Equal(got, want) {
		t.Fatalf("KeepAwake calls = %v, want %v", got, want)
	}
	if awake := len(want) > 0 && want[len(want)-1]; e.power.Awake() != awake {
		t.Fatalf("Awake() = %v, want %v", e.power.Awake(), awake)
	}
}

// NFR-A05: リースを保持している間だけスリープと画面オフを抑止する。
func TestLeasePowerFollowsTheLease(t *testing.T) {
	tests := []struct {
		name        string
		end         func(e *ctlEnv)
		noReacquire bool // キルスイッチは発動中、取得できない
	}{
		{name: "明示解放", end: func(e *ctlEnv) { e.leases.Release(ctlAlice) }},
		{name: "セッション切断", end: func(e *ctlEnv) { e.leases.ReleaseSession(ctlAlice) }},
		{name: "ReleaseAll", end: func(e *ctlEnv) { e.leases.ReleaseAll(string(domain.ReleaseKillSwitch)) }},
		{name: "キルスイッチ", noReacquire: true, end: func(e *ctlEnv) {
			if err := e.kill.Trigger("test"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "アイドル満了（タイマー）", end: func(e *ctlEnv) {
			e.clock.Advance(domain.DefaultLeaseIdleTimeout)
			e.timers.FireAll()
		}},
		{name: "アイドル満了（Check）", end: func(e *ctlEnv) {
			e.clock.Advance(domain.DefaultLeaseIdleTimeout)
			_ = e.leases.Check(ctlAlice)
		}},
		{name: "アイドル満了（Holder）", end: func(e *ctlEnv) {
			e.clock.Advance(domain.DefaultLeaseIdleTimeout)
			e.leases.Holder()
		}},
		{name: "アイドルタイムアウトの短縮", end: func(e *ctlEnv) {
			e.clock.Advance(2 * time.Minute)
			if err := e.leases.SetIdleTimeout(time.Minute); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCtlEnv(t)
			wantKeepAwakeCalls(t, e) // 取得前は何も呼ばない

			mustAcquire(t, e, ctlAlice)
			wantKeepAwakeCalls(t, e, true)

			tt.end(e)
			wantKeepAwakeCalls(t, e, true, false)

			if tt.noReacquire {
				return
			}
			// 解放後の再取得で、もう一度だけ入る。
			mustAcquire(t, e, ctlBob)
			wantKeepAwakeCalls(t, e, true, false, true)
		})
	}
}

func TestLeasePowerIsIdempotent(t *testing.T) {
	e := newCtlEnv(t)

	mustAcquire(t, e, ctlAlice)
	mustAcquire(t, e, ctlAlice)                           // 同じ保持者の再取得（活動として数える）
	_ = e.leases.Check(ctlAlice)                          // 活動
	_, _ = e.leases.Acquire(context.Background(), ctlBob) // 他者の取得は拒否される
	e.leases.Holder()
	wantKeepAwakeCalls(t, e, true)

	e.leases.Release(ctlBob) // 保持者でなければ何もしない
	wantKeepAwakeCalls(t, e, true)

	e.leases.Release(ctlAlice)
	e.leases.Release(ctlAlice)
	e.leases.ReleaseAll("shutdown")
	wantKeepAwakeCalls(t, e, true, false)
}

func TestLeasePowerStaysOnWhenTheIdleTimerIsExtended(t *testing.T) {
	e := newCtlEnv(t)
	mustAcquire(t, e, ctlAlice)
	e.clock.Advance(4 * time.Minute)
	if err := e.leases.Check(ctlAlice); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	e.timers.FireAll() // 最初のタイマー満了。まだ期限前なので延長されるだけ
	wantKeepAwakeCalls(t, e, true)
}

func TestLeaseWithoutPowerWorks(t *testing.T) {
	l, err := NewLeaseManager(LeaseConfig{Clock: newCtlClock(), IDs: &ctlIDs{}, IdleTimeout: domain.DefaultLeaseIdleTimeout})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(context.Background(), ctlAlice); err != nil {
		t.Fatal(err)
	}
	l.Release(ctlAlice)
}
