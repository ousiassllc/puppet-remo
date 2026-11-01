package remote

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// 以下は Web UI が必要とする操作だけを並べた、利用者側（本パッケージ）のインターフェース。
// 実体は service（ViewerSessions・LeaseManager・Control・HostSettingsService）が満たす。

// Sessions はビューアのセッション（viewer_id。API 仕様 §4.1）。実体は service.ViewerSessions。
type Sessions interface {
	Issue(client domain.Client) string
	Actor(viewerID string, client domain.Client) (domain.Actor, error)
	Configure(viewerID string, client domain.Client, c service.ViewerConfig) error
}

// Leases は操作権（リース）の取得・解放・強制剥奪と、保持者の読み取り。実体は service.LeaseManager。
type Leases interface {
	// RequestAcquire は承認を待たない取得（service.LeaseManager.RequestAcquire）。承認が要れば pending = true ですぐ返る。
	RequestAcquire(ctx context.Context, who domain.Actor) (l service.Lease, pending bool, err error)
	Release(who domain.Actor)
	Revoke(by domain.Actor) error
	Holder() (service.Holder, bool)
	// Pending は承認待ち（pending_approval。FR-L07）の要求者。承認待ちでなければ false。
	Pending() (service.PendingLease, bool)
}

// Control は入力の唯一の入口（クリップボード・ショートカット）とモニター一覧。実体は service.Control。
type Control interface {
	Monitors(ctx context.Context) ([]domain.Monitor, error)
	Do(ctx context.Context, who domain.Actor, op domain.Op) (service.Result, error)
}

// Settings は既定の画質・FPS（viewer.default_*）の読み出し。実体は service.HostSettingsService。
type Settings interface {
	Load(ctx context.Context) (service.HostSettings, error)
}

// Deps は Handler の依存先。Logger 以外はすべて必須。
type Deps struct {
	Sessions Sessions
	Leases   Leases
	Control  Control
	Settings Settings
	Logger   *slog.Logger // nil なら slog.Default()
}

// Handler はリモートの Web UI のハンドラ。
type Handler struct {
	d   Deps
	log *slog.Logger
}

// New は Handler を作る。
func New(d Deps) (*Handler, error) {
	if d.Sessions == nil || d.Leases == nil || d.Control == nil || d.Settings == nil {
		return nil, errors.New("remote: sessions, leases, control and settings are required")
	}
	h := &Handler{d: d, log: d.Logger}
	if h.log == nil {
		h.log = slog.Default()
	}
	return h, nil
}
