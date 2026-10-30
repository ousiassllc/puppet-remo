package tailnet

import (
	"context"
	"errors"
	"net/netip"
	"strings"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// Status は tailscaled の状態。LoggedIn は tailnet に参加済みか。
// IPs は tailnet に接続中（Running）のときだけ埋まる。Err は tailscaled に問い合わせられなかったとき。
type Status struct {
	LoggedIn bool
	IPs      []netip.Addr
	DNSName  string // MagicDNS 名（末尾のドットなし）。未取得は空
	Err      error
}

// Identity は WhoIs で解決した接続元。
type Identity struct {
	LoginName    string
	StableNodeID string
	NodeName     string
}

// ErrWhoIsFailed は WhoIs の失敗。接続は拒否する（fail closed。FR-A03、NFR-S05）。errors.Is で判定する。
var ErrWhoIsFailed = domain.NewError(domain.CodeWhoisFailed, "the source address could not be resolved by tailscaled", nil)

// LocalAPI は本パッケージが使う tailscaled LocalAPI の範囲。
type LocalAPI interface {
	// StatusWithoutPeers は自ノードの状態を返す（LocalAPI の status?peers=false）。
	StatusWithoutPeers(ctx context.Context) (Status, error)
	// WhoIs は remoteAddr（IP または IP:port）の持ち主を返す。
	WhoIs(ctx context.Context, remoteAddr string) (Identity, error)
	// WatchIPNBus は IPN バスの購読を開始する。ctx の終了で購読も終わる。
	WatchIPNBus(ctx context.Context) (BusWatcher, error)
}

// BusWatcher は IPN バスの購読。
type BusWatcher interface {
	// Next は状態に関わる通知（IPN 状態・自ノードの変化）が届くまで待つ。
	// 切断・Close・ctx の終了ではエラーを返す。
	Next() error
	// Close は購読を終え、ブロック中の Next を解除する。何度呼んでもよい。
	Close() error
}

// backendRunning は tailscaled が tailnet に接続している状態（ipn.Running の文字列）。
const backendRunning = "Running"

// localClient は *local.Client を LocalAPI に合わせる。
type localClient struct{ c *local.Client }

// NewLocalAPI は tailscaled の LocalAPI に接続する LocalAPI を返す。
// c が nil なら既定（Windows は名前付きパイプ、他は Unix ソケット）で接続する。
func NewLocalAPI(c *local.Client) LocalAPI {
	if c == nil {
		c = &local.Client{}
	}
	return localClient{c: c}
}

func (l localClient) StatusWithoutPeers(ctx context.Context) (Status, error) {
	st, err := l.c.StatusWithoutPeers(ctx)
	if err != nil {
		return Status{}, err
	}
	return statusFrom(st), nil
}

func (l localClient) WhoIs(ctx context.Context, remoteAddr string) (Identity, error) {
	resp, err := l.c.WhoIs(ctx, remoteAddr)
	if err != nil {
		return Identity{}, err
	}
	return identityFrom(resp)
}

func (l localClient) WatchIPNBus(ctx context.Context) (BusWatcher, error) {
	w, err := l.c.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err != nil {
		return nil, err
	}
	return busWatcher{w: w}, nil
}

type busWatcher struct{ w *local.IPNBusWatcher }

func (b busWatcher) Next() error {
	for {
		n, err := b.w.Next()
		if err != nil {
			return err
		}
		// ピアの増減など、自分に関係のない通知では再取得しない。
		if n.State != nil || n.SelfChange != nil || n.InitialStatus != nil {
			return nil
		}
	}
}

func (b busWatcher) Close() error { return b.w.Close() }

// statusFrom は LocalAPI の status を Status に写す。
// ログイン済みは Running・Starting・Stopped。tailnet IP は Running のときだけ返す
// （接続していない間は Listen しない）。
func statusFrom(st *ipnstate.Status) Status {
	out := Status{}
	switch st.BackendState {
	case backendRunning:
		out.LoggedIn = true
		out.IPs = append(out.IPs, st.TailscaleIPs...)
	case "Starting", "Stopped":
		out.LoggedIn = true
	}
	if st.Self != nil {
		out.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
	}
	return out
}

// identityFrom は WhoIs の応答から Identity を作る。ログイン名かノード ID が欠けていれば失敗とする。
func identityFrom(resp *apitype.WhoIsResponse) (Identity, error) {
	if resp == nil || resp.Node == nil || resp.UserProfile == nil {
		return Identity{}, errors.New("incomplete WhoIs response")
	}
	id := Identity{
		LoginName:    resp.UserProfile.LoginName,
		StableNodeID: string(resp.Node.StableID),
		NodeName:     resp.Node.ComputedName,
	}
	if id.LoginName == "" || id.StableNodeID == "" {
		return Identity{}, errors.New("WhoIs response lacks login name or node ID")
	}
	return id, nil
}
