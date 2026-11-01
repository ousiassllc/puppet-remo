package remotetest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/remote"
)

// Fakes は Web UI の依存先の偽実装一式。設定してから Deps で使う。
type Fakes struct {
	Sessions *Sessions
	Leases   *Leases
	Control  *Control
	Settings *Settings
}

// New は空の状態の Fakes を返す。モニターはメイン 1 枚、既定の画質 70・10 fps。
func New() *Fakes {
	leases := &Leases{}
	return &Fakes{
		Sessions: &Sessions{},
		Leases:   leases,
		Control: &Control{leases: leases, Mons: []domain.Monitor{
			{Index: 0, Name: "DISPLAY1", Primary: true, Size: domain.Size{W: 1920, H: 1080}},
			{Index: 1, Name: "DISPLAY2", Size: domain.Size{W: 1280, H: 720}},
		}},
		Settings: &Settings{Values: service.HostSettings{ViewerDefaultQuality: 70, ViewerDefaultFPS: 10}},
	}
}

// Deps は Handler の依存先。
func (f *Fakes) Deps() remote.Deps {
	return remote.Deps{Sessions: f.Sessions, Leases: f.Leases, Control: f.Control, Settings: f.Settings}
}

// Sessions は viewer_id の表。
type Sessions struct {
	mu         sync.Mutex
	n          int
	owner      map[string]string // viewer_id → client ID
	Configured []service.ViewerConfig
	// ConfigureErr が nil でなければ Configure はこれを返す（接続していないビューアなど）。
	ConfigureErr error
}

func (s *Sessions) Issue(c domain.Client) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	id := fmt.Sprintf("VIEWER%02d", s.n)
	if s.owner == nil {
		s.owner = map[string]string{}
	}
	s.owner[id] = c.ID
	return id
}

// Rebind は viewer_id を別のクライアントに束縛し直す（別のクライアントの viewer_id を使う場合のテストの準備）。
func (s *Sessions) Rebind(viewerID, clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owner[viewerID] = clientID
}

// Actor は束縛されたクライアントの viewer_id だけを受け付ける（API 仕様 §4.1）。
func (s *Sessions) Actor(viewerID string, c domain.Client) (domain.Actor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner, ok := s.owner[viewerID]; !ok || owner != c.ID {
		return domain.Actor{}, domain.ErrInvalidArgument.WithMsg("unknown viewer_id")
	}
	return domain.Actor{Kind: domain.ActorWeb, ClientID: c.ID, Label: c.DisplayName, SessionID: viewerID}, nil
}

func (s *Sessions) Configure(_ string, _ domain.Client, c service.ViewerConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ConfigureErr != nil {
		return s.ConfigureErr
	}
	s.Configured = append(s.Configured, c)
	return nil
}

// Leases は 1 つのリースを持つ偽の LeaseManager。保持者がいるとき、他者の Acquire は ErrLeaseHeldByOther。
type Leases struct {
	mu       sync.Mutex
	holder   *domain.Actor
	revoked  *domain.Actor
	Revokers []domain.Actor // Revoke を呼んだ操作主体
	Acquires int
	Releases int
}

// Hold は actor が保持している状態にする（テストの準備）。
func (l *Leases) Hold(a domain.Actor) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holder, l.revoked = &a, nil
}

// Holding は今の保持者。
func (l *Leases) Holding() (domain.Actor, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == nil {
		return domain.Actor{}, false
	}
	return *l.holder, true
}

func (l *Leases) Acquire(_ context.Context, who domain.Actor) (service.Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Acquires++
	if l.holder != nil && !l.holder.SameSession(who) {
		return service.Lease{}, domain.ErrLeaseHeldByOther
	}
	l.holder, l.revoked = &who, nil
	return service.Lease{ID: "lease-1", Holder: who, Since: time.Unix(0, 0)}, nil
}

func (l *Leases) Release(who domain.Actor) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Releases++
	if l.holder != nil && l.holder.SameSession(who) {
		l.holder = nil
	}
}

func (l *Leases) Revoke(by domain.Actor) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Revokers = append(l.Revokers, by)
	if !by.Kind.CanRevokeLease() {
		return domain.ErrInvalidArgument
	}
	if l.holder != nil {
		h := *l.holder
		l.holder, l.revoked = nil, &h
	}
	return nil
}

func (l *Leases) Holder() (service.Holder, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == nil {
		return service.Holder{}, false
	}
	return service.Holder{LeaseID: "lease-1", Actor: *l.holder, Since: time.Unix(0, 0)}, true
}

// check は Control の入力の前提（保持者であること）。
func (l *Leases) check(who domain.Actor) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.holder == nil && l.revoked != nil && l.revoked.SameSession(who):
		return domain.ErrLeaseRevoked
	case l.holder == nil:
		return domain.ErrLeaseRequired
	case !l.holder.SameSession(who):
		return domain.ErrLeaseHeldByOther
	}
	return nil
}

// Control は入力を記録する偽の Control。リースの前提と ctrl+alt+delete の拒否は本物と同じ規則（domain.CheckKeyCombo）。
type Control struct {
	leases *Leases
	Mons   []domain.Monitor
	// MonitorsErr が nil でなければ Monitors はこれを返す。
	MonitorsErr error
	// DoErr が nil でなければ、リースの確認のあとの Do はこれを返す（desktop_unavailable など）。
	DoErr error

	mu        sync.Mutex
	Ops       []domain.Op
	Clipboard string
}

func (c *Control) Monitors(context.Context) ([]domain.Monitor, error) {
	if c.MonitorsErr != nil {
		return nil, c.MonitorsErr
	}
	return c.Mons, nil
}

func (c *Control) Do(_ context.Context, who domain.Actor, op domain.Op) (service.Result, error) {
	if err := c.leases.check(who); err != nil {
		return service.Result{}, err
	}
	if kp, ok := op.(domain.KeyPress); ok {
		if err := domain.CheckKeyCombo(kp.Keys); err != nil {
			return service.Result{}, err
		}
	}
	if c.DoErr != nil {
		return service.Result{}, c.DoErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Ops = append(c.Ops, op)
	switch o := op.(type) {
	case domain.ClipboardWrite:
		c.Clipboard = o.Text
	case domain.ClipboardRead:
		return service.Result{Text: c.Clipboard}, nil
	}
	return service.Result{}, nil
}

// Settings は設定の読み出し。
type Settings struct {
	Values service.HostSettings
	Err    error
}

func (s *Settings) Load(context.Context) (service.HostSettings, error) {
	if s.Err != nil {
		return service.HostSettings{}, s.Err
	}
	return s.Values, nil
}
