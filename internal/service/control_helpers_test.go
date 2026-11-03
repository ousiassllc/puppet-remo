package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは lease / control / killswitch / audit のテストの共通部品。名前は ctl 接頭辞で他のテストと衝突させない。

// ctlClock は手動で進める Clock（並行利用可）。
type ctlClock struct {
	mu  sync.Mutex
	now time.Time
}

func newCtlClock() *ctlClock {
	return &ctlClock{now: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}
}

func (c *ctlClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ctlClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ctlIDs は "lease-1", "lease-2", ... を返す IDGen（並行利用可）。
type ctlIDs struct {
	mu sync.Mutex
	n  int
}

func (g *ctlIDs) NewID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return fmt.Sprintf("lease-%d", g.n)
}

// ctlTimers は AfterFunc の偽物。起動したタイマーを溜め、テストが Fire で満了させる。
type ctlTimers struct {
	mu     sync.Mutex
	timers []*ctlTimer
}

type ctlTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (ts *ctlTimers) AfterFunc(d time.Duration, f func()) func() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := &ctlTimer{d: d, f: f}
	ts.timers = append(ts.timers, t)
	return func() bool {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

// Active は止められていないタイマーの数。
func (ts *ctlTimers) Active() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	n := 0
	for _, t := range ts.timers {
		if !t.stopped {
			n++
		}
	}
	return n
}

// Last は最後に起動したタイマーの待ち時間。
func (ts *ctlTimers) Last() time.Duration {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.timers[len(ts.timers)-1].d
}

// FireAll は止められていない全タイマーを満了させる（コールバックは呼び出し元の goroutine で同期的に実行する）。
func (ts *ctlTimers) FireAll() {
	ts.mu.Lock()
	var fire []func()
	for _, t := range ts.timers {
		if !t.stopped {
			t.stopped = true
			fire = append(fire, t.f)
		}
	}
	ts.mu.Unlock()
	for _, f := range fire {
		f()
	}
}

// ctlAudit は SyncAuditRecorder の偽物（記録を溜める）。RecordSync の行（launch_app の試行）は entries ではなく sync に溜める
// （Record の結果の行と混ぜない）。syncErr が非 nil なら RecordSync は何も記録せずそのエラーを返す。
type ctlAudit struct {
	mu      sync.Mutex
	entries []repository.AuditEntry
	sync    []repository.AuditEntry
	syncErr error
	onSync  func() // RecordSync のたびに呼ばれる（同期書き込みの最中の出来事を再現する）
}

func (a *ctlAudit) RecordSync(_ context.Context, e repository.AuditEntry) error {
	a.mu.Lock()
	if a.syncErr != nil {
		err := a.syncErr
		a.mu.Unlock()
		return err
	}
	a.sync = append(a.sync, e)
	hook := a.onSync
	a.mu.Unlock()
	if hook != nil {
		hook() // ロックの外で呼ぶ（キルスイッチの発動は Record を通って同じロックを取る）
	}
	return nil
}

// SyncEntries は RecordSync で書かれた行を返す。
func (a *ctlAudit) SyncEntries() []repository.AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]repository.AuditEntry(nil), a.sync...)
}

// SetSyncErr は以後の RecordSync を err で失敗させる（nil で戻す）。
func (a *ctlAudit) SetSyncErr(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.syncErr = err
}

func (a *ctlAudit) Record(_ context.Context, e repository.AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
}

func (a *ctlAudit) Entries() []repository.AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]repository.AuditEntry(nil), a.entries...)
}

// Events は記録された event 名を順に返す。
func (a *ctlAudit) Events() []string {
	var out []string
	for _, e := range a.Entries() {
		out = append(out, e.Event)
	}
	return out
}

// ctlSettings は repository.Settings のメモリ実装。
type ctlSettings struct {
	mu     sync.Mutex
	values map[string]json.RawMessage
	setErr error
}

func newCtlSettings() *ctlSettings { return &ctlSettings{values: map[string]json.RawMessage{}} }

func (s *ctlSettings) Get(_ context.Context, key string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	return v, ok, nil
}

func (s *ctlSettings) Set(_ context.Context, key string, value json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return s.setErr
	}
	s.values[key] = value
	return nil
}

func (s *ctlSettings) All(context.Context) (map[string]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]json.RawMessage, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out, nil
}

func (s *ctlSettings) put(key, raw string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = json.RawMessage(raw)
}

var (
	ctlAlice = domain.Actor{Kind: domain.ActorMCP, ClientID: "client-a", Label: "alice", SessionID: "sess-a"}
	ctlBob   = domain.Actor{Kind: domain.ActorMCP, ClientID: "client-b", Label: "bob", SessionID: "sess-b"}
)

// ctlEnv は Control とその依存一式。モニターは 3200x1800（縮小率 0.49。最大辺 1568）1 台。
type ctlEnv struct {
	t         *testing.T
	clock     *ctlClock
	timers    *ctlTimers
	audit     *ctlAudit
	settings  *ctlSettings
	capturer  *platformtest.FakeCapturer
	input     *platformtest.FakeInput
	windows   *platformtest.FakeWindows
	clipboard *platformtest.FakeClipboard
	ui        *platformtest.FakeUIAutomation
	launcher  *platformtest.FakeAppLauncher
	picker    *platformtest.FakeFilePicker
	appStore  repository.LaunchableApps
	denyStore repository.LaunchDenylist
	appDB     string // appStore の SQLite ファイル（テストが行を直接書き換えるのに使う）
	apps      AppRegistry
	kill      *KillSwitchManager
	power     *platformtest.FakePower
	leases    LeaseManager
	control   Control
	slept     []time.Duration
	// onSleep は Sleep のたびに（slept に足したあとで）呼ばれる。待ちの間のウィンドウの出現などを再現する。
	// sleepErr が非 nil なら、Sleep はそれを返す（待ちの中断）。
	onSleep  func()
	sleepErr error
}

var ctlMonitor = domain.Monitor{Index: 0, Name: "m0", Primary: true, Size: domain.Size{W: 3200, H: 1800}, DPI: 96}

// 既定のウィンドウ: 通常（1）・自プロセス（2）・昇格（3）が左右に並び、4 は前面の通常ウィンドウ。
func ctlDefaultWindows() []domain.WindowInfo {
	return []domain.WindowInfo{
		{ID: "4", Title: "front", Rect: domain.Rect{X: 0, Y: 0, W: 800, H: 600}, Foreground: true},
		{ID: "1", Title: "normal", Rect: domain.Rect{X: 0, Y: 0, W: 1600, H: 900}},
		{ID: "2", Title: "puppet-remo", Own: true, Rect: domain.Rect{X: 1600, Y: 0, W: 800, H: 900}},
		{ID: "3", Title: "admin", Elevated: true, Rect: domain.Rect{X: 2400, Y: 0, W: 800, H: 900}},
	}
}

func newCtlEnv(t *testing.T) *ctlEnv {
	t.Helper()
	e := &ctlEnv{
		t:         t,
		clock:     newCtlClock(),
		timers:    &ctlTimers{},
		audit:     &ctlAudit{},
		settings:  newCtlSettings(),
		capturer:  platformtest.NewFakeCapturer(ctlMonitor),
		input:     platformtest.NewFakeInput(),
		windows:   platformtest.NewFakeWindows(ctlDefaultWindows()...),
		clipboard: platformtest.NewFakeClipboard(),
		power:     platformtest.NewFakePower(),
		launcher:  platformtest.NewFakeAppLauncher(),
		picker:    platformtest.NewFakeFilePicker(),
	}
	e.ui = platformtest.NewFakeUIAutomation(e.windows)
	e.appStore, e.denyStore, e.appDB = openAppStore(t)
	apps, err := NewAppRegistry(AppRegistryConfig{
		Apps: e.appStore, Denied: e.denyStore, Settings: e.settings, Launcher: e.launcher, Picker: e.picker, Audit: e.audit, Clock: e.clock, IDs: &ctlIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.apps = apps
	kill, err := NewKillSwitch(context.Background(), KillSwitchConfig{Settings: e.settings, Clock: e.clock, Audit: e.audit})
	if err != nil {
		t.Fatal(err)
	}
	e.kill = kill
	e.leases, err = NewLeaseManager(LeaseConfig{
		Clock: e.clock, IDs: &ctlIDs{}, IdleTimeout: domain.DefaultLeaseIdleTimeout,
		Audit: e.audit, Kill: kill, AfterFunc: e.timers.AfterFunc, Power: e.power,
	})
	if err != nil {
		t.Fatal(err)
	}
	kill.BindLeases(e.leases)
	e.control, err = NewControl(ControlConfig{
		Capturer: e.capturer, Input: e.input, Windows: e.windows, Clipboard: e.clipboard, UI: e.ui,
		Launcher: e.launcher, Apps: e.apps,
		Leases: e.leases, Kill: kill, Settings: e.settings, Audit: e.audit, Clock: e.clock,
		Sleep: func(_ context.Context, d time.Duration) error {
			e.slept = append(e.slept, d)
			if e.onSleep != nil {
				e.onSleep()
			}
			return e.sleepErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// openAppStore は一時ファイルの SQLite の起動許可リストとそのファイルのパスを返す（label の一意性・50 件の上限は本物の repository で確かめる）。
func openAppStore(t *testing.T) (repository.LaunchableApps, repository.LaunchDenylist, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "puppet-remo.db")
	db, err := repository.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db.LaunchableApps(), db.LaunchDenylist(), path
}
