package service

import (
	"cmp"
	"context"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// ViewerIDTTL は viewer_id が WebSocket に接続していないときの有効期間（API 仕様 §4.1）。
// 発行から、そして WebSocket が切れてからの両方に適用する（再接続の猶予。docs/architecture/frontend.md §6.4）。
const ViewerIDTTL = 5 * time.Minute

// ViewerCloseReason は ViewerSession が終わった理由。WebSocket の閉じコード（API 仕様 §5.2）への対応は ws が持つ。
type ViewerCloseReason string

const (
	// ViewerClosedDisconnect は接続側から終えた（Close、または Open に渡した ctx の取り消し = WebSocket の切断）。
	ViewerClosedDisconnect ViewerCloseReason = "disconnect"
	// ViewerClosedReplaced は同じ viewer_id の新しい接続に置き換えられた（同時接続は 1 本だけ）。
	ViewerClosedReplaced ViewerCloseReason = "replaced"
	// ViewerClosedClientRemoved はクライアントの削除・無効化で閉じられた（閉じコード 4510）。
	ViewerClosedClientRemoved ViewerCloseReason = "client_removed"
	// ViewerClosedKillSwitch はキルスイッチで閉じられた（閉じコード 4503）。
	ViewerClosedKillSwitch ViewerCloseReason = "kill_switch"
)

// ViewerSessions はブラウザ上のビューアのセッション（viewer_id。API 仕様 §4.1）の表。
// リース保持者（種別 web）は viewer_id で識別し（Actor.SessionID）、WebSocket の切断でリースを解放する（FR-L05）。
//
// viewer_id（ULID）は接続元クライアント（登録済みクライアント。tailnet のノードと 1 対 1）に束縛する。
// 有効期間は、WebSocket の接続中、または発行・切断から ViewerIDTTL。束縛と異なるクライアントからの利用、未知・期限切れの
// viewer_id は domain.ErrInvalidArgument（HTTP は 400 invalid_argument、WebSocket は閉じコード 4401）。
// 同時接続は 1 つの viewer_id につき 1 本で、新しい接続が古い接続を閉じて置き換える。
//
// SessionCloser として HostAdmin に渡すと、クライアントの削除・無効化（種別 web）でセッションを閉じる。
// キルスイッチの発動時は、構築時に登録したクローザーが全セッションを閉じる。
type ViewerSessions interface {
	// Issue は viewer_id を発行する（GET /viewer のレンダリング時）。
	Issue(client domain.Client) string
	// Actor は viewer_id を検証し、そのビューアのリース保持者（種別 web）を返す（HTTP の X-Viewer-Id の検証・リース操作用）。
	// 接続の有無は問わない。
	Actor(viewerID string, client domain.Client) (domain.Actor, error)
	// Open は WebSocket の接続時に viewer_id を検証し、閲覧者を Streamer に登録してセッションを返す。
	// 同じ viewer_id に接続中のセッションがあれば閉じ（ViewerClosedReplaced。リースを解放し、閲覧者の登録を外す）、
	// 外れるのを待ってから登録する。キルスイッチの発動中は domain.ErrKillSwitchActive、同時閲覧者の上限は ErrTooManyViewers、
	// 範囲外の品質・FPS・モニターは domain.ErrInvalidArgument（c の 0 は Streamer.Subscribe と同じく既定値）。
	// ctx の取り消しはセッションの終了（ViewerClosedDisconnect）。
	Open(ctx context.Context, viewerID string, client domain.Client, c ViewerConfig) (ViewerSession, error)
	// Configure は viewer_id の接続中のセッションの設定を変える（POST /viewer/settings。WebSocket の config と同じ経路）。
	// 接続していなければ domain.ErrInvalidArgument。
	Configure(viewerID string, client domain.Client, c ViewerConfig) error
	SessionCloser
}

// ViewerSession は WebSocket 1 本に対応するセッション。
type ViewerSession interface {
	// Actor はこのビューアのリース保持者（種別 web。Control.Do・LeaseManager に渡す）。
	Actor() domain.Actor
	// Viewer は Streamer の閲覧者（Frames・Done）。
	Viewer() *Viewer
	// Config は今の設定（0 を既定値で埋めた値）。
	Config() ViewerConfig
	// ConfigChanged は設定が変わるたびに、反映後の設定が届くチャネル（WebSocket の config メッセージ用）。
	// WebSocket の config と POST /viewer/settings のどちらから変えても届く。容量 1 で、読まれていなければ古い値を捨てて最新を残す。
	ConfigChanged() <-chan ViewerConfig
	// Configure は閲覧者のモニター・品質・FPS を変える。Quality・FPS が 0 なら今の値のまま、Monitor は常に反映する。
	// 範囲外は domain.ErrInvalidArgument（何も変えない）。終わったセッションも ErrInvalidArgument。
	Configure(c ViewerConfig) error
	// Done はセッションが終わったときに閉じる。Reason は Done が閉じた後に読む。
	Done() <-chan struct{}
	Reason() ViewerCloseReason
	// OnEnd は、セッションが終わるとき（どの理由でも）、web のリースを解放する前に fn を 1 度だけ呼ぶよう登録する
	// （WebSocket が押下中のボタン・キーを、まだ保持しているリースのうちに離すため。Control.Do は未保持のリースを拒む）。
	// 登録は 1 つだけで、後の登録が前を置き換える。fn は Control を呼んでよいが、ViewerSessions を呼ばない。
	// すでに終わったセッションには、その場で fn を呼ぶ（リースは解放済み）。
	OnEnd(fn func())
	// Close はセッションを終える（閲覧者の登録を外し、web のリースを解放する）。何度呼んでもよい。
	// viewer_id はこの後 ViewerIDTTL の間、再接続に使える。
	Close()
}

// ViewerSessionsConfig は NewViewerSessions の設定。すべて必須。
type ViewerSessionsConfig struct {
	Streamer Streamer
	Leases   LeaseManager
	// Kill は発動中の Open の拒否と、発動時に全セッションを閉じるクローザーの登録に使う。
	Kill  ControlKillSwitch
	Clock domain.Clock
	// IDs は viewer_id の供給元（ULID）。
	IDs domain.IDGen
}

// NewViewerSessions は ViewerSessions を返し、キルスイッチの発動時に全セッションを閉じるクローザーを登録する。
func NewViewerSessions(cfg ViewerSessionsConfig) (ViewerSessions, error) {
	if cfg.Streamer == nil || cfg.Leases == nil || cfg.Kill == nil || cfg.Clock == nil || cfg.IDs == nil {
		return nil, domain.ErrInvalidArgument.WithMsg("viewer sessions require streamer, leases, kill switch, clock and ids")
	}
	s := &viewerSessions{cfg: cfg, entries: make(map[string]*viewerEntry)}
	cfg.Kill.RegisterCloser("web-sessions", func(context.Context) error {
		s.closeWhere(func(*viewerSession) bool { return true }, ViewerClosedKillSwitch)
		return nil
	})
	return s, nil
}

type viewerSessions struct {
	cfg ViewerSessionsConfig

	// openMu は Open を直列化する（置き換えと登録の間に、同じ viewer_id の別の Open を割り込ませない）。mu より先に取る。
	openMu sync.Mutex
	// mu は entries と各 entry を守る。ロックを持ったまま LeaseManager・Streamer を呼ばない。
	mu      sync.Mutex
	entries map[string]*viewerEntry
}

// viewerEntry は発行済みの viewer_id 1 件。
type viewerEntry struct {
	clientID  string
	expiresAt time.Time      // conn が nil のあいだの期限
	conn      *viewerSession // 接続中のセッション。無ければ nil
}

var _ ViewerSessions = (*viewerSessions)(nil)

func (s *viewerSessions) Issue(client domain.Client) string {
	now := s.cfg.Clock.Now()
	id := s.cfg.IDs.NewID()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(now)
	s.entries[id] = &viewerEntry{clientID: client.ID, expiresAt: now.Add(ViewerIDTTL)}
	return id
}

func (s *viewerSessions) Actor(viewerID string, client domain.Client) (domain.Actor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.lookupLocked(viewerID, client.ID); err != nil {
		return domain.Actor{}, err
	}
	return viewerActor(viewerID, client), nil
}

func (s *viewerSessions) Open(ctx context.Context, viewerID string, client domain.Client, c ViewerConfig) (ViewerSession, error) {
	s.openMu.Lock()
	defer s.openMu.Unlock()

	s.mu.Lock()
	e, err := s.lookupLocked(viewerID, client.ID)
	var old *viewerSession
	if err == nil {
		old = e.conn
		if s.cfg.Kill.Active() {
			err = domain.ErrKillSwitchActive
		}
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if old != nil {
		old.finish(ViewerClosedReplaced)
		// 閲覧者の登録が外れるのを待つ（上限の MaxViewers に、置き換える自分の古い接続を数えさせない）。
		select {
		case <-old.viewer.Done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	streamCtx, cancel := context.WithCancel(ctx)
	v, err := s.cfg.Streamer.Subscribe(streamCtx, c)
	if err != nil {
		cancel()
		return nil, err
	}
	sess := &viewerSession{
		owner:   s,
		actor:   viewerActor(viewerID, client),
		viewer:  v,
		cancel:  cancel,
		done:    make(chan struct{}),
		cfg:     v.Config,
		changed: make(chan ViewerConfig, 1),
	}
	context.AfterFunc(ctx, func() { sess.finish(ViewerClosedDisconnect) })
	// Streamer が閲覧者を終えたとき（キャプチャの失敗）も、セッションを終える。
	go func() {
		select {
		case <-v.Done:
			sess.finish(ViewerClosedDisconnect)
		case <-sess.done:
		}
	}()

	s.mu.Lock()
	cur, err := s.lookupLocked(viewerID, client.ID)
	if err == nil && s.cfg.Kill.Active() {
		err = domain.ErrKillSwitchActive
	}
	if err == nil {
		select {
		case <-sess.done: // 登録の前に ctx が取り消された
			err = ctx.Err()
		default:
			cur.conn = sess
		}
	}
	s.mu.Unlock()
	if err != nil {
		sess.finish(ViewerClosedDisconnect) // ロックの外で閉じる
		return nil, err
	}
	return sess, nil
}

func (s *viewerSessions) Configure(viewerID string, client domain.Client, c ViewerConfig) error {
	s.mu.Lock()
	e, err := s.lookupLocked(viewerID, client.ID)
	var conn *viewerSession
	if err == nil {
		conn = e.conn
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if conn == nil {
		return domain.ErrInvalidArgument.WithMsg("viewer is not connected")
	}
	return conn.Configure(c)
}

// CloseClient は clientID の web のセッションをすべて閉じ（ViewerClosedClientRemoved）、その viewer_id を無効にする。
func (s *viewerSessions) CloseClient(clientID string, kind domain.ActorKind) {
	if kind != domain.ActorWeb {
		return
	}
	s.closeWhere(func(c *viewerSession) bool { return c.actor.ClientID == clientID }, ViewerClosedClientRemoved)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.entries {
		if e.clientID == clientID {
			delete(s.entries, id)
		}
	}
}

// CloseToken は何もしない（Web のセッションは Bearer トークンを持たない）。
func (s *viewerSessions) CloseToken(string) {}

// closeWhere は match するセッションを、ロックの外で reason で閉じる。
func (s *viewerSessions) closeWhere(match func(*viewerSession) bool, reason ViewerCloseReason) {
	s.mu.Lock()
	var targets []*viewerSession
	for _, e := range s.entries {
		if e.conn != nil && match(e.conn) {
			targets = append(targets, e.conn)
		}
	}
	s.mu.Unlock()
	for _, c := range targets {
		c.finish(reason)
	}
}

// lookupLocked は viewer_id の entry を返す。未知・期限切れ・別のクライアントに束縛された viewer_id は ErrInvalidArgument
// （どれかを区別して知らせない）。
func (s *viewerSessions) lookupLocked(viewerID, clientID string) (*viewerEntry, error) {
	s.purgeLocked(s.cfg.Clock.Now())
	e, ok := s.entries[viewerID]
	if !ok || e.clientID != clientID {
		return nil, domain.ErrInvalidArgument.WithMsg("viewer_id is invalid")
	}
	return e, nil
}

// purgeLocked は接続しておらず期限を過ぎた entry を捨てる。
func (s *viewerSessions) purgeLocked(now time.Time) {
	for id, e := range s.entries {
		if e.conn == nil && !now.Before(e.expiresAt) {
			delete(s.entries, id)
		}
	}
}

func viewerActor(viewerID string, client domain.Client) domain.Actor {
	return domain.Actor{
		Kind:      domain.ActorWeb,
		ClientID:  client.ID,
		Label:     cmp.Or(client.DisplayName, client.LoginName),
		SessionID: viewerID,
	}
}

type viewerSession struct {
	owner  *viewerSessions
	actor  domain.Actor
	viewer *Viewer
	cancel context.CancelFunc // Streamer への登録を外す

	done   chan struct{}
	once   sync.Once
	reason ViewerCloseReason // once の中で done を閉じる前に書く

	// hookMu は onEnd と ended を守る。
	hookMu sync.Mutex
	onEnd  func()
	ended  bool // finish が onEnd を取り出した

	// cfgMu は Configure を直列化し、cfg を守る。
	cfgMu   sync.Mutex
	cfg     ViewerConfig
	changed chan ViewerConfig
}

var _ ViewerSession = (*viewerSession)(nil)

func (c *viewerSession) Actor() domain.Actor                { return c.actor }
func (c *viewerSession) Viewer() *Viewer                    { return c.viewer }
func (c *viewerSession) ConfigChanged() <-chan ViewerConfig { return c.changed }
func (c *viewerSession) Done() <-chan struct{}              { return c.done }

// Reason は終わった理由。終わっていなければ空。
func (c *viewerSession) Reason() ViewerCloseReason {
	select {
	case <-c.done:
		return c.reason
	default:
		return ""
	}
}
func (c *viewerSession) Close() { c.finish(ViewerClosedDisconnect) }

func (c *viewerSession) OnEnd(fn func()) {
	c.hookMu.Lock()
	if c.ended {
		c.hookMu.Unlock()
		fn()
		return
	}
	c.onEnd = fn
	c.hookMu.Unlock()
}

func (c *viewerSession) Config() ViewerConfig {
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	return c.cfg
}

func (c *viewerSession) Configure(req ViewerConfig) error {
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	select {
	case <-c.done:
		return domain.ErrInvalidArgument.WithMsg("viewer session is closed")
	default:
	}
	if err := c.owner.cfg.Streamer.Configure(c.viewer.ID, req); err != nil {
		return err
	}
	// Streamer.Configure と同じ規則（0 の品質・FPS は今の値のまま、モニターは常に反映）で今の設定を更新する。
	next := c.cfg
	next.Monitor = req.Monitor
	if req.Quality != 0 {
		next.Quality = req.Quality
	}
	if req.FPS != 0 {
		next.FPS = req.FPS
	}
	c.cfg = next
	select {
	case <-c.changed: // 読まれていない古い値を捨てて、最新を残す
	default:
	}
	c.changed <- next
	return nil
}

// finish はセッションを 1 度だけ終える: 閲覧者の登録を外し、viewer_id を ViewerIDTTL の再接続の猶予に戻し、
// OnEnd の fn を呼んでから web のリースを解放する（FR-L05）。
func (c *viewerSession) finish(reason ViewerCloseReason) {
	c.once.Do(func() {
		c.reason = reason
		c.cancel()
		close(c.done)

		c.hookMu.Lock()
		fn := c.onEnd
		c.onEnd, c.ended = nil, true
		c.hookMu.Unlock()
		if fn != nil {
			fn() // 押下中の入力を、リースを保持しているうちに離す
		}

		s := c.owner
		s.mu.Lock()
		if e, ok := s.entries[c.actor.SessionID]; ok && e.conn == c {
			e.conn = nil
			e.expiresAt = s.cfg.Clock.Now().Add(ViewerIDTTL)
		}
		s.mu.Unlock()
		s.cfg.Leases.ReleaseSession(c.actor)
	})
}
