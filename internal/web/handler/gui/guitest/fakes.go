package guitest

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui"
)

// FixedClock は進めない時計。
type FixedClock struct {
	mu sync.Mutex
	T  time.Time
}

func (c *FixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.T
}

// Fakes は GUI の依存先の偽実装一式。各フィールドは設定してから Deps で使う（並行に書き換えない）。
type Fakes struct {
	Clock      *FixedClock
	Bus        *service.Bus
	Status     *Status
	Sessions   *Sessions
	Leases     *Leases
	KillSwitch *KillSwitch
	Admin      *Admin
	Settings   *Settings
	Apps       *Apps
	Logs       *Logs
}

// New は空の状態の Fakes を返す。時計は 2026-10-30 12:00 UTC。
func New() *Fakes {
	clock := &FixedClock{T: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	bus := service.NewBus()
	return &Fakes{
		Clock:      clock,
		Bus:        bus,
		Status:     &Status{},
		Sessions:   &Sessions{},
		Leases:     &Leases{},
		KillSwitch: &KillSwitch{bus: bus},
		Admin:      &Admin{clock: clock, bus: bus},
		Apps:       &Apps{},
		Logs:       &Logs{FilePath: `C:\Users\demo\AppData\Local\puppet-remo\logs\puppet-remo.log`},
		Settings:   &Settings{Values: service.HostSettings{Port: 8765, KillSwitchHotkey: "Ctrl+Alt+Shift+K", ScreenshotMaxDimension: 1568, LeaseIdleTimeoutSec: 300, ViewerDefaultQuality: 70, ViewerDefaultFPS: 10}, CanAutostart: true},
	}
}

// Deps は gui.New に渡す依存先。表示の時刻は UTC。
func (f *Fakes) Deps() gui.Deps {
	return gui.Deps{
		Status: f.Status, Sessions: f.Sessions, Leases: f.Leases, KillSwitch: f.KillSwitch, Admin: f.Admin,
		Settings: f.Settings, Apps: f.Apps, Logs: f.Logs, Events: f.Bus, Clock: f.Clock, Location: time.UTC,
	}
}

// Status は gui.StatusReader の偽実装。
type Status struct{ V service.ServerStatus }

func (s *Status) ServerStatus(context.Context) service.ServerStatus { return s.V }

// Sessions は gui.SessionLister の偽実装。
type Sessions struct{ Items []service.SessionInfo }

func (s *Sessions) Sessions() []service.SessionInfo { return s.Items }

// Leases は gui.LeaseControl の偽実装。Revoke は呼び出しを記録し、保持者を外す（RevokeErr があればそのエラーを返して外さない）。
type Leases struct {
	H    service.Holder
	Held bool

	RevokeErr   error
	RevokeCalls int
	LastBy      domain.Actor
}

func (l *Leases) Holder() (service.Holder, bool) { return l.H, l.Held }

func (l *Leases) Revoke(by domain.Actor) error {
	l.RevokeCalls++
	l.LastBy = by
	if l.RevokeErr != nil {
		return l.RevokeErr
	}
	l.H, l.Held = service.Holder{}, false
	return nil
}

// KillSwitch は gui.KillSwitch の偽実装。発動・解除の呼び出しを数える。
type KillSwitch struct {
	mu                         sync.Mutex
	bus                        *service.Bus
	Snapshot                   service.KillSwitchSnapshot
	TriggerErr, ReleaseErr     error
	TriggerCalls, ReleaseCalls int
	LastSource                 string
	Now                        func() time.Time
}

func (k *KillSwitch) Active() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.Snapshot.Active
}

func (k *KillSwitch) State() service.KillSwitchSnapshot {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.Snapshot
}

// Trigger は発動する。TriggerErr が設定されていれば、発動状態にしたうえでそのエラーを返す（後始末の失敗を模す）。
func (k *KillSwitch) Trigger(source string) error {
	k.mu.Lock()
	k.TriggerCalls++
	k.LastSource = source
	if k.TriggerErr == nil || !errors.Is(k.TriggerErr, errNotTriggered) {
		k.Snapshot = service.KillSwitchSnapshot{Active: true, TriggeredAt: k.now(), Source: source}
	}
	err := k.TriggerErr
	k.mu.Unlock()
	k.publish()
	return err
}

func (k *KillSwitch) Release() error {
	k.mu.Lock()
	k.ReleaseCalls++
	if k.ReleaseErr != nil {
		err := k.ReleaseErr
		k.mu.Unlock()
		return err
	}
	k.Snapshot = service.KillSwitchSnapshot{}
	k.mu.Unlock()
	k.publish()
	return nil
}

func (k *KillSwitch) now() time.Time {
	if k.Now != nil {
		return k.Now()
	}
	return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
}

func (k *KillSwitch) publish() { k.bus.Publish(service.Event{Topic: service.TopicKillSwitch}) }

// ErrNotTriggered を TriggerErr に wrap すると、Trigger が発動状態にならずに失敗する（永続化の失敗を模す）。
var ErrNotTriggered = fmt.Errorf("kill switch could not be persisted: %w", errNotTriggered)

var errNotTriggered = errors.New("not triggered")

// Settings は gui.Settings の偽実装。
type Settings struct {
	mu           sync.Mutex
	Values       service.HostSettings
	CanAutostart bool
	LoadErr      error
	SaveErr      error
	Saves        []service.HostSettings
}

func (s *Settings) Load(context.Context) (service.HostSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Values, s.LoadErr
}

// Save は本物と同じ検証をしてから保存する。
func (s *Settings) Save(_ context.Context, v service.HostSettings) (service.HostSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := v.Validate(); err != nil {
		return service.HostSettings{}, err
	}
	if s.SaveErr != nil {
		return service.HostSettings{}, s.SaveErr
	}
	if !s.CanAutostart {
		v.Autostart = false
	}
	s.Values = v
	s.Saves = append(s.Saves, v)
	return v, nil
}

func (s *Settings) AutostartAvailable() bool { return s.CanAutostart }

// Admin は gui.Admin のインメモリ実装。本物（service.HostAdmin）と同じ規則（pending のみ許可、ラベル重複は conflict など）を持つ。
type Admin struct {
	mu       sync.Mutex
	clock    *FixedClock
	bus      *service.Bus
	next     int
	Requests []service.ConnRequest
	Cs       []domain.Client
	Toks     map[string][]service.TokenRecord
	// 呼び出しの記録（テストが検査する）。
	Approved []service.ApproveInput
	Issued   []IssueCall
	Updates  []service.ClientUpdate
	// 注入するエラー。nil でなければ、該当の操作がこれを返す。
	PendingErr error
	HistoryErr error
	IssueErr   error
}

// IssueCall は IssueToken の呼び出し 1 回分。
type IssueCall struct {
	ClientID, Label string
	TTL             *time.Duration
}

func (a *Admin) id(prefix string) string {
	a.next++
	return fmt.Sprintf("%s-%03d", prefix, a.next)
}

func (a *Admin) notify(topics ...string) {
	for _, t := range topics {
		a.bus.Publish(service.Event{Topic: t})
	}
}

func (a *Admin) PendingRequests(context.Context) ([]service.ConnRequest, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.PendingErr != nil {
		return nil, a.PendingErr
	}
	var out []service.ConnRequest
	for _, r := range a.Requests {
		if r.Status == service.ConnPending {
			out = append(out, r)
		}
	}
	return out, nil
}

// RequestHistory は Requests を、届いた日時（FirstSeenAt）の新しい順（同時刻は ID の新しい順）に 1 ページ返す（本物と同じ規則）。
func (a *Admin) RequestHistory(_ context.Context, page int) (service.RequestHistoryPage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.HistoryErr != nil {
		return service.RequestHistoryPage{}, a.HistoryErr
	}
	page = max(page, 1)
	all := slices.Clone(a.Requests)
	slices.SortStableFunc(all, func(x, y service.ConnRequest) int {
		if c := y.FirstSeenAt.Compare(x.FirstSeenAt); c != 0 {
			return c
		}
		return strings.Compare(y.ID, x.ID)
	})
	size := service.RequestHistoryPageSize
	start := min((page-1)*size, len(all))
	end := min(start+size, len(all))
	return service.RequestHistoryPage{Items: all[start:end], Page: page, HasPrev: page > 1, HasNext: end < len(all)}, nil
}

func (a *Admin) decide(id, status string, clientID *string) error {
	for i := range a.Requests {
		if a.Requests[i].ID != id {
			continue
		}
		if a.Requests[i].Status != service.ConnPending {
			return domain.NewError(domain.CodeConflict, "connection request is not pending", nil)
		}
		a.Requests[i].Status = status
		a.Requests[i].ClientID = clientID
		decided := a.clock.Now()
		a.Requests[i].DecidedAt = &decided
		return nil
	}
	return domain.NewError(domain.CodeConflict, "connection request is not pending", nil)
}

func (a *Admin) ApproveRequest(_ context.Context, id string, in service.ApproveInput) (domain.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var req *service.ConnRequest
	for i := range a.Requests {
		if a.Requests[i].ID == id && a.Requests[i].Status == service.ConnPending {
			req = &a.Requests[i]
		}
	}
	if req == nil {
		return domain.Client{}, domain.NewError(domain.CodeConflict, "connection request is not pending", nil)
	}
	name := in.DisplayName
	if name == "" {
		name = req.DisplayName
	}
	c := domain.Client{
		ID: a.id("client"), StableNodeID: req.StableNodeID, LoginName: req.LoginName, DisplayName: name,
		WebEnabled: in.WebEnabled, MCPEnabled: in.MCPEnabled, AgentEnabled: in.AgentEnabled, ApprovalMode: domain.ApprovalAlways,
	}
	a.Cs = append(a.Cs, c)
	a.Approved = append(a.Approved, in)
	if err := a.decide(id, service.ConnApproved, &c.ID); err != nil {
		return domain.Client{}, err
	}
	a.notify(service.TopicConnectionRequests)
	return c, nil
}

func (a *Admin) DenyRequest(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.decide(id, service.ConnDenied, nil); err != nil {
		return err
	}
	a.notify(service.TopicConnectionRequests)
	return nil
}

func (a *Admin) Clients(context.Context) ([]domain.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]domain.Client(nil), a.Cs...), nil
}

func (a *Admin) find(id string) int {
	for i := range a.Cs {
		if a.Cs[i].ID == id {
			return i
		}
	}
	return -1
}

func (a *Admin) Client(_ context.Context, id string) (domain.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i := a.find(id); i >= 0 {
		return a.Cs[i], nil
	}
	return domain.Client{}, fmt.Errorf("client: %w", service.ErrNotFound)
}

func (a *Admin) UpdateClient(_ context.Context, id string, u service.ClientUpdate) (domain.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := a.find(id)
	if i < 0 {
		return domain.Client{}, fmt.Errorf("client: %w", service.ErrNotFound)
	}
	if u.WebEnabled != nil {
		a.Cs[i].WebEnabled = *u.WebEnabled
	}
	if u.MCPEnabled != nil {
		a.Cs[i].MCPEnabled = *u.MCPEnabled
	}
	if u.AgentEnabled != nil {
		a.Cs[i].AgentEnabled = *u.AgentEnabled
	}
	a.Updates = append(a.Updates, u)
	return a.Cs[i], nil
}

func (a *Admin) DeleteClient(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := a.find(id)
	if i < 0 {
		return fmt.Errorf("client: %w", service.ErrNotFound)
	}
	a.Cs = append(a.Cs[:i], a.Cs[i+1:]...)
	delete(a.Toks, id)
	return nil
}

func (a *Admin) Tokens(_ context.Context, clientID string) ([]service.TokenRecord, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]service.TokenRecord(nil), a.Toks[clientID]...), nil
}

// IssueToken は prm_ + 実行時の乱数のトークンを返す。ラベルの重複は conflict。
func (a *Admin) IssueToken(_ context.Context, clientID, label string, ttl *time.Duration) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Issued = append(a.Issued, IssueCall{ClientID: clientID, Label: label, TTL: ttl})
	if a.IssueErr != nil {
		return "", a.IssueErr
	}
	if a.find(clientID) < 0 {
		return "", fmt.Errorf("client: %w", service.ErrNotFound)
	}
	for _, t := range a.Toks[clientID] {
		if t.Label == label {
			return "", domain.NewError(domain.CodeConflict, "a token with this label already exists", nil)
		}
	}
	var raw [32]byte
	if _, err := crand.Read(raw[:]); err != nil {
		return "", err
	}
	plaintext := "prm_" + base64.RawURLEncoding.EncodeToString(raw[:])
	now := a.clock.Now()
	var expires *time.Time
	switch {
	case ttl == nil:
		t := now.Add(service.DefaultTokenTTL)
		expires = &t
	case *ttl > 0:
		t := now.Add(*ttl)
		expires = &t
	}
	if a.Toks == nil {
		a.Toks = map[string][]service.TokenRecord{}
	}
	a.Toks[clientID] = append(a.Toks[clientID], service.TokenRecord{
		ID: a.id("token"), ClientID: clientID, Label: label, Prefix: plaintext[:8], ExpiresAt: expires, CreatedAt: now,
	})
	return plaintext, nil
}

func (a *Admin) RevokeToken(_ context.Context, clientID, tokenID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	toks := a.Toks[clientID]
	for i := range toks {
		if toks[i].ID == tokenID {
			if toks[i].RevokedAt == nil {
				now := a.clock.Now()
				toks[i].RevokedAt = &now
			}
			return nil
		}
	}
	return fmt.Errorf("token: %w", service.ErrNotFound)
}

// Logs は gui.Logs の偽実装。Items は新しい順に並べて入れる（本物のファイルと同じ順序。絞り込みとページ分けは本物と同じ規則）。
type Logs struct {
	Items    []service.AppLogEntry
	FilePath string
	Err      error
}

// Path は表示するログファイルのパス。
func (l *Logs) Path() string { return l.FilePath }

// Entries は Items を filter で絞って 1 ページ返す。
func (l *Logs) Entries(_ context.Context, filter service.AppLogFilter, page int) (service.AppLogPage, error) {
	if l.Err != nil {
		return service.AppLogPage{}, l.Err
	}
	page = service.ClampAppLogPage(page)
	var all []service.AppLogEntry
	for _, e := range l.Items {
		if e.Severity >= filter.MinLevel() {
			all = append(all, e)
		}
	}
	size := service.AppLogPageSize
	start := min((page-1)*size, len(all))
	end := min(start+size, len(all))
	return service.AppLogPage{Items: all[start:end], Page: page, HasPrev: page > 1, HasNext: end < len(all)}, nil
}
