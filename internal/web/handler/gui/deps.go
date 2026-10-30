package gui

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// 以下は GUI が必要とする操作だけを並べた、利用者側（本パッケージ）のインターフェース。
// 実体は service（HostAdmin・HostSettingsService・KillSwitchManager・LeaseManager・StatusTracker・Bus など）が満たす。

// StatusReader はサーバー状態（tailnet IP・ポート・tailscaled 状態）を返す。
type StatusReader interface {
	ServerStatus(ctx context.Context) service.ServerStatus
}

// SessionLister は接続中のセッションを返す。
type SessionLister interface {
	Sessions() []service.SessionInfo
}

// LeaseReader は現在のリース保持者を返す（M1 は表示のみ）。
type LeaseReader interface {
	Holder() (service.Holder, bool)
}

// KillSwitch はキルスイッチの状態と、ホスト所有者による発動・解除。
type KillSwitch interface {
	Active() bool
	State() service.KillSwitchSnapshot
	Trigger(source string) error
	Release() error
}

// Admin は接続要求・クライアント・トークンの管理（FR-H03、FR-H04）。
type Admin interface {
	PendingRequests(ctx context.Context) ([]service.ConnRequest, error)
	ApproveRequest(ctx context.Context, requestID string, in service.ApproveInput) (domain.Client, error)
	DenyRequest(ctx context.Context, requestID string) error

	Clients(ctx context.Context) ([]domain.Client, error)
	Client(ctx context.Context, id string) (domain.Client, error)
	UpdateClient(ctx context.Context, id string, u service.ClientUpdate) (domain.Client, error)
	DeleteClient(ctx context.Context, id string) error

	Tokens(ctx context.Context, clientID string) ([]service.TokenRecord, error)
	IssueToken(ctx context.Context, clientID, label string, ttl *time.Duration) (plaintext string, err error)
	RevokeToken(ctx context.Context, clientID, tokenID string) error
}

// Settings は設定画面の読み書き（FR-S01〜S04、FR-S08）。
type Settings interface {
	Load(ctx context.Context) (service.HostSettings, error)
	Save(ctx context.Context, s service.HostSettings) (service.HostSettings, error)
	AutostartAvailable() bool
}

// Events は状態の変化の購読（SSE の元）。
type Events interface {
	Subscribe(topics ...string) (<-chan service.Event, func())
}

// Deps は New の依存先。Location・Logger・Heartbeat 以外は必須。
type Deps struct {
	Status     StatusReader
	Sessions   SessionLister
	Leases     LeaseReader
	KillSwitch KillSwitch
	Admin      Admin
	Settings   Settings
	Events     Events
	Clock      domain.Clock

	// Location は表示する時刻のタイムゾーン。nil なら time.Local。
	Location *time.Location
	Logger   *slog.Logger // nil なら slog.Default()
	// Heartbeat は SSE のコメント行（接続維持）の間隔。0 以下なら 20 秒。
	Heartbeat time.Duration
}

const defaultHeartbeat = 20 * time.Second

// killSwitchSource は GUI から発動したときの監査上の発動元。
const killSwitchSource = "gui"

// Handler はホスト GUI のハンドラ。
type Handler struct {
	d   Deps
	log *slog.Logger
	loc *time.Location
	hb  time.Duration
}

// New は Handler を作る。
func New(d Deps) (*Handler, error) {
	switch {
	case d.Status == nil, d.Sessions == nil, d.Leases == nil, d.KillSwitch == nil,
		d.Admin == nil, d.Settings == nil, d.Events == nil, d.Clock == nil:
		return nil, errors.New("gui: all dependencies except Location, Logger and Heartbeat are required")
	}
	h := &Handler{d: d, log: d.Logger, loc: d.Location, hb: d.Heartbeat}
	if h.log == nil {
		h.log = slog.Default()
	}
	if h.loc == nil {
		h.loc = time.Local
	}
	if h.hb <= 0 {
		h.hb = defaultHeartbeat
	}
	return h, nil
}
