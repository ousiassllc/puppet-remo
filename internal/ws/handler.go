// Package ws は WebSocket /ws/screen（docs/api/endpoints.md §5）。JSON のテキストフレームとバイナリの画面フレームの
// 符号化・復号、接続の認可・keepalive・閉じコード、入力メッセージの domain.Op への変換、Streamer の配信の中継を持つ。
//
// service と domain だけに依存する（scripts/check-imports.sh の ws 規則）。Host / Origin・キルスイッチ・WhoIs・許可リストの検査は
// リモートルーターのミドルウェア連鎖（router）が済ませており、ここは連鎖が認可したクライアント（ClientFrom）を受け取る。
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// Subprotocol は /ws/screen のサブプロトコル（API 仕様 §1。破壊的変更のとき v2 に上げる）。
const Subprotocol = "puppet-remo.v1"

// 閉じコード（API 仕様 §5.2）。
const (
	CloseInvalidMessage websocket.StatusCode = 4400 // 不正なメッセージ（形式・サイズ超過・範囲外の config・wheel の絶対値 > 50）
	CloseUnauthorized   websocket.StatusCode = 4401 // 認可失敗（viewer_id 不正）
	CloseTooManyViewers websocket.StatusCode = 4429 // 同時閲覧者の上限
	CloseKillSwitch     websocket.StatusCode = 4503 // キルスイッチで受付停止
	CloseClientRemoved  websocket.StatusCode = 4510 // クライアントが削除・無効化された
)

const (
	// DefaultPingInterval はサーバーが ping を送る間隔、DefaultPongTimeout は pong が無くて切断するまでの時間（NFR-A04）。
	DefaultPingInterval = 15 * time.Second
	DefaultPongTimeout  = 45 * time.Second

	// MaxMessageBytes はクライアント → サーバーのメッセージの上限（NFR-S09）。超過は閉じコード 4400。
	MaxMessageBytes = 64 << 10

	// writeTimeout は 1 回の書き込みの上限。詰まったクライアントを長く待たない。
	writeTimeout = 10 * time.Second
	// oversizeDrainBytes は、上限を超えたメッセージの残りを読み捨てる量の上限（閉じのハンドシェイクのため読みの権利を返す）。
	oversizeDrainBytes = 1 << 20
)

// Config は New の設定。ClientFrom 以外のインターフェースは必須。
type Config struct {
	Sessions service.ViewerSessions
	Control  service.Control
	Leases   service.LeaseManager
	// Events はリースの変化（lease トピック）の購読元。lease メッセージの送信に使う。
	Events *service.Bus
	// ClientFrom はミドルウェア連鎖が認可したクライアントをリクエストから取り出す（web/middleware.ClientFrom）。
	// ws は web を import できないため、router / app が渡す。
	ClientFrom func(ctx context.Context) (domain.Client, bool)

	// PingInterval・PongTimeout が 0 以下なら 15 秒・45 秒（NFR-A04）。テストが短くする。
	PingInterval time.Duration
	PongTimeout  time.Duration
	Logger       *slog.Logger // nil なら slog.Default()
}

// Handler は /ws/screen の http.Handler。
type Handler struct {
	cfg Config
	log *slog.Logger

	mu     sync.Mutex
	closed bool
	conns  map[*conn]struct{}
	wg     sync.WaitGroup
}

var _ http.Handler = (*Handler)(nil)

// New は Handler を返す。
func New(cfg Config) (*Handler, error) {
	if cfg.Sessions == nil || cfg.Control == nil || cfg.Leases == nil || cfg.Events == nil || cfg.ClientFrom == nil {
		return nil, errors.New("ws: handler requires sessions, control, leases, events and ClientFrom")
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = DefaultPingInterval
	}
	if cfg.PongTimeout <= 0 {
		cfg.PongTimeout = DefaultPongTimeout
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Handler{cfg: cfg, log: log, conns: make(map[*conn]struct{})}, nil
}

// Close は接続中の WebSocket をすべて閉じ（1001）、終わるのを待つ。以後の接続は 503 で断る（アプリの終了時）。
func (h *Handler) Close() {
	h.mu.Lock()
	h.closed = true
	conns := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	// 閉じのハンドシェイクは相手の応答を待つので、並行に閉じる。
	var closing sync.WaitGroup
	for _, c := range conns {
		closing.Go(func() { c.finish(websocket.StatusGoingAway, "server shutting down") })
	}
	closing.Wait()
	h.wg.Wait()
}

// track は接続を登録する。Close 済みなら false。
func (h *Handler) track(c *conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	h.conns[c] = struct{}{}
	h.wg.Add(1)
	return true
}

func (h *Handler) untrack(c *conn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
	h.wg.Done()
}

// ServeHTTP は GET /ws/screen?viewer_id=<ULID> を WebSocket に切り替える。
// ハンドシェイク前の拒否は problem+json。viewer_id の不正などハンドシェイク後に分かるものは閉じコードで返す
// （ブラウザは、ハンドシェイクの失敗では理由を読めないため）。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	client, ok := h.cfg.ClientFrom(r.Context())
	if !ok {
		// 連鎖を通っていない（組み間違い）。素通りさせない。
		writeProblem(w, http.StatusInternalServerError, domain.CodeInternal, "Internal error")
		return
	}
	if !offersSubprotocol(r) {
		writeProblem(w, http.StatusBadRequest, domain.CodeInvalidArgument, "Unsupported WebSocket subprotocol")
		return
	}
	nc, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{Subprotocol},
		CompressionMode: websocket.CompressionDisabled, // 画面は JPEG で、圧縮しても縮まない
	})
	if err != nil {
		return // Accept が応答を書いている
	}
	nc.SetReadLimit(-1) // 上限は readLoop が 4400 で扱う（ライブラリの上限は 1009 で閉じるため使わない）
	// 切断以外の理由（置き換え・キルスイッチなど）で閉じるときは自分から閉じる。リクエストの ctx は使わない。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &conn{h: h, ws: nc, ctx: ctx, cancel: cancel, client: client, viewerID: r.URL.Query().Get("viewer_id")}
	if !h.track(c) {
		_ = nc.Close(websocket.StatusTryAgainLater, "server shutting down")
		return
	}
	defer h.untrack(c)
	c.serve()
}

// offersSubprotocol はクライアントが Sec-WebSocket-Protocol で Subprotocol を申し出ているかを返す。
func offersSubprotocol(r *http.Request) bool {
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			if strings.TrimSpace(p) == Subprotocol {
				return true
			}
		}
	}
	return false
}

// conn は WebSocket 1 本。
type conn struct {
	h        *Handler
	ws       *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	client   domain.Client
	viewerID string

	sess    service.ViewerSession
	pressed pressed // 押下中のボタン・キー。セッションの終わりで離す

	// グループ源・対象ウィンドウ（#157。target.go）。
	tgt targetState

	closeOnce sync.Once
}

// finish は WebSocket を 1 度だけ閉じ、すべてのループを止める。code が 0 なら閉じのハンドシェイクをしない。
func (c *conn) finish(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		if code == 0 {
			_ = c.ws.CloseNow()
		} else {
			_ = c.ws.Close(code, reason)
		}
		c.cancel()
	})
}

func (c *conn) serve() {
	defer c.finish(0, "")
	if !c.open() {
		return
	}
	// セッションの終わり（切断・置き換えなど）で、web のリースを解放する前に、押下中のボタン・キーを離す。
	c.sess.OnEnd(c.releasePressed)
	defer c.sess.Close() // 閲覧者の登録を外し、web のリースを解放する（FR-L05）

	// 先に購読して、ready の後にリースが変わっても取りこぼさない。lease-result は、このビューアの承認待ちの失敗（拒否・タイムアウト）。
	leaseEvents, unsubscribe := c.h.cfg.Events.Subscribe(service.TopicLease, service.TopicLeaseResult)
	defer unsubscribe()

	if err := c.sendReady(); err != nil {
		c.finish(websocket.StatusInternalError, "failed to send ready")
		return
	}

	var loops sync.WaitGroup
	loops.Add(3)
	go func() { defer loops.Done(); c.readLoop() }()
	go func() { defer loops.Done(); c.writeLoop(leaseEvents) }()
	go func() { defer loops.Done(); c.keepalive() }()
	loops.Wait()
}

// open は viewer_id を検証して閲覧者を登録する。失敗は閉じコードで閉じて false。
func (c *conn) open() bool {
	ctx := c.ctx
	if _, err := c.h.cfg.Sessions.Actor(c.viewerID, c.client); err != nil {
		c.finish(CloseUnauthorized, "invalid viewer_id")
		return false
	}
	mons, err := c.h.cfg.Control.Monitors(ctx)
	if err != nil {
		c.h.log.Warn("websocket: monitors are unavailable", "error", err)
		c.finish(websocket.StatusInternalError, "monitors are unavailable")
		return false
	}
	sess, err := c.h.cfg.Sessions.Open(ctx, c.viewerID, c.client, service.ViewerConfig{Monitor: defaultMonitor(mons)})
	switch {
	case err == nil:
		c.sess = sess
		return true
	case errors.Is(err, service.ErrTooManyViewers):
		c.finish(CloseTooManyViewers, "too many viewers")
	case errors.Is(err, domain.ErrKillSwitchActive):
		c.finish(CloseKillSwitch, "kill switch is active")
	case errors.Is(err, domain.ErrInvalidArgument):
		c.finish(CloseUnauthorized, "invalid viewer_id")
	case ctx.Err() != nil:
		c.finish(0, "")
	default:
		c.h.log.Warn("websocket: opening the viewer failed", "error", err)
		c.finish(websocket.StatusInternalError, "internal error")
	}
	return false
}

// defaultMonitor は接続時に選ぶモニター（プライマリ。無ければ先頭）。
func defaultMonitor(mons []domain.Monitor) int {
	for _, m := range mons {
		if m.Primary {
			return m.Index
		}
	}
	if len(mons) > 0 {
		return mons[0].Index
	}
	return 0
}

func (c *conn) sendReady() error {
	mons, err := c.h.cfg.Control.Monitors(c.ctx)
	if err != nil {
		return err
	}
	cfg := c.sess.Config()
	src, w, h, origin := c.geometry(cfg, mons)
	return c.writeJSON(readyMessage{
		Type: typeReady, Monitors: monitorBodies(mons), Monitor: cfg.Monitor, Source: src, Width: w, Height: h, Origin: origin,
		Quality: cfg.Quality, FPS: cfg.FPS, Lease: c.leaseBody(),
	})
}

// streamSize はモニターのストリームの座標空間（物理ピクセル）。見つからなければ 0。
func streamSize(mons []domain.Monitor, index int) (w, h int) {
	for _, m := range mons {
		if m.Index == index {
			return m.Size.W, m.Size.H
		}
	}
	return 0, 0
}

// leaseBody は現在のリースの状態（held → pending_approval → idle の順に見る。承認待ちと保持は同時にならない）。
// pending_approval の holder は承認待ちの要求者で、idle のときだけ holder を省く（API 仕様 §5.3）。
func (c *conn) leaseBody() leaseBody {
	me := c.sess.Actor()
	if holder, ok := c.h.cfg.Leases.Holder(); ok {
		return leaseBody{
			State:  string(domain.LeaseHeld),
			Holder: &holderBody{Kind: string(holder.Actor.Kind), Label: holder.Actor.Label},
			Mine:   holder.Actor.SameSession(me),
		}
	}
	if p, ok := c.h.cfg.Leases.Pending(); ok {
		return leaseBody{
			State:  string(domain.LeasePendingApproval),
			Holder: &holderBody{Kind: string(p.Actor.Kind), Label: p.Actor.Label},
			Mine:   p.Actor.SameSession(me),
		}
	}
	return leaseBody{State: string(domain.LeaseIdle)}
}

// writeLoop は Streamer のフレーム・設定の変化・リースの変化・セッションの終わりを書き出す。
func (c *conn) writeLoop(leaseEvents <-chan service.Event) {
	frames := c.sess.Viewer().Frames
	groupStatus := c.sess.Viewer().Group
	for {
		select {
		case <-c.ctx.Done():
			return
		case b, ok := <-frames:
			if !ok { // 閲覧者が外れた。理由は Done で扱う
				frames = nil
				continue
			}
			if err := c.writeFrame(b); err != nil {
				c.finish(0, "")
				return
			}
		case st, ok := <-groupStatus:
			if !ok {
				groupStatus = nil
				continue
			}
			if err := c.onGroupStatus(st); err != nil {
				c.finish(0, "")
				return
			}
		case cfg := <-c.sess.ConfigChanged():
			if err := c.sendConfig(cfg); err != nil {
				c.finish(0, "")
				return
			}
		case ev := <-leaseEvents:
			if err := c.writeLeaseEvent(ev); err != nil {
				c.finish(0, "")
				return
			}
		case <-c.sess.Done():
			c.closeForReason(c.sess.Reason())
			return
		}
	}
}

// writeLeaseEvent は lease トピックでは現在のリースの状態（lease）を、lease-result トピックではこのビューアの承認待ちの失敗
// （拒否・タイムアウト）を error メッセージで送る。他のビューアの lease-result は送らない（API 仕様 §5.3）。
func (c *conn) writeLeaseEvent(ev service.Event) error {
	if ev.Topic == service.TopicLeaseResult {
		res, ok := ev.Data.(service.LeaseResult)
		if !ok || !res.Who.SameSession(c.sess.Actor()) {
			return nil
		}
		return c.writeJSON(errorFor(res.Err))
	}
	if err := c.writeJSON(leaseMessage{Type: typeLease, leaseBody: c.leaseBody()}); err != nil {
		return err
	}
	c.syncTarget()
	return c.sendTarget()
}

func (c *conn) sendConfig(cfg service.ViewerConfig) error {
	mons, err := c.h.cfg.Control.Monitors(c.ctx)
	if err != nil {
		return c.writeJSON(errorFor(err))
	}
	return c.writeConfig(cfg, mons)
}

// closeForReason はセッションが終わった理由を閉じコードにして閉じる（API 仕様 §5.2）。
func (c *conn) closeForReason(reason service.ViewerCloseReason) {
	switch reason {
	case service.ViewerClosedKillSwitch:
		_ = c.writeJSON(killSwitchMessage{Type: typeKillSwitch, Active: true})
		c.finish(CloseKillSwitch, "kill switch is active")
	case service.ViewerClosedClientRemoved:
		c.finish(CloseClientRemoved, "client removed or disabled")
	case service.ViewerClosedReplaced:
		// 同じ viewer_id の新しい接続に置き換えられた。再接続すると置き換え合うので、再接続しない 4401 で閉じる。
		c.finish(CloseUnauthorized, "replaced by a newer connection")
	default:
		// 接続側の終わり（ここへは通常来ない）、またはキャプチャの失敗。クライアントは再接続する。
		c.finish(websocket.StatusInternalError, "stream ended")
	}
}

// keepalive は pingInterval ごとに ping を送り、pongTimeout の間 pong が無ければ切断する（NFR-A04）。
func (c *conn) keepalive() {
	t := time.NewTicker(c.h.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(c.ctx, c.h.cfg.PongTimeout)
		err := c.ws.Ping(ctx)
		cancel()
		if err != nil {
			if c.ctx.Err() == nil {
				c.h.log.Debug("websocket: no pong; disconnecting", "error", err)
			}
			c.finish(0, "")
			return
		}
	}
}

// readLoop はクライアントのメッセージを処理する。
func (c *conn) readLoop() {
	for {
		typ, r, err := c.ws.Reader(c.ctx)
		if err != nil {
			c.finish(0, "")
			return
		}
		if typ != websocket.MessageText {
			c.finish(CloseInvalidMessage, "text frames only")
			return
		}
		data, err := io.ReadAll(io.LimitReader(r, MaxMessageBytes+1))
		if err != nil {
			c.finish(0, "")
			return
		}
		if len(data) > MaxMessageBytes {
			// 読みの権利を返して、閉じのハンドシェイクを進められるようにする（残りは上限まで読み捨てる）。
			_, _ = io.Copy(io.Discard, io.LimitReader(r, oversizeDrainBytes))
			c.finish(CloseInvalidMessage, "message too large")
			return
		}
		if err := c.handle(data); err != nil {
			if errors.Is(err, errInvalidMessage) {
				c.finish(CloseInvalidMessage, "invalid message")
			} else {
				c.finish(0, "")
			}
			return
		}
	}
}

// handle は 1 メッセージを処理する。errInvalidMessage は閉じコード 4400、それ以外のエラーは接続の終わり。
func (c *conn) handle(data []byte) error {
	msg, err := decodeClientMessage(data)
	if err != nil {
		return err
	}
	switch msg.Type {
	case typeClientPing:
		return c.writeJSON(typeOnly{Type: typePong})
	case typeClientConfig:
		cfg, err := msg.viewerConfig(c.sess.Config())
		if err != nil {
			return err
		}
		if cfg, err = c.applySource(msg, cfg); err != nil {
			if errors.Is(err, errInvalidMessage) {
				return err
			}
			return c.writeJSON(errorFor(err))
		}
		if err := c.sess.Configure(cfg); err != nil {
			if errors.Is(err, domain.ErrInvalidArgument) {
				return errInvalidMessage // 未知のモニターなど
			}
			return err
		}
		return nil // 反映後の設定は ConfigChanged から config メッセージで返る
	case typeInputMouse, typeInputKey, typeInputText:
		cfg := c.sess.Config()
		op, err := msg.op(cfg.Monitor)
		if err != nil {
			return err
		}
		if cfg.Target != (domain.WindowRef{}) {
			seen, err := msg.frameRef()
			if err != nil {
				return err
			}
			if op, err = c.groupOp(op, seen); err != nil {
				return c.writeJSON(errorFor(err))
			}
		}
		return c.input(op)
	case typeClientTargetResume:
		return c.resumeTarget()
	case typeClientClipboardRead:
		return c.rejectClipboardRead()
	}
	return errInvalidMessage
}

// input は入力を Control に渡す。拒否（リース・InputGuard・座標など）は error メッセージで返し、入力は破棄する（切断しない）。
func (c *conn) input(op domain.Op) error {
	if err := c.injectInput(op); err != nil {
		if errors.Is(err, errInputEnded) {
			return err // セッションが終わった。接続を閉じる
		}
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		if domain.CodeOf(err) == domain.CodeInternal {
			c.h.log.Warn("websocket: input failed", "error", err)
		}
		if err := c.writeJSON(errorFor(err)); err != nil {
			return err
		}
		c.noteBlocked(err)
		return c.sendTarget()
	}
	return nil
}

func (c *conn) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("ws: encode message: %w", err)
	}
	return c.write(websocket.MessageText, b)
}

func (c *conn) write(typ websocket.MessageType, b []byte) error {
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	return c.ws.Write(ctx, typ, b)
}

// writeProblem はハンドシェイク前のエラーを problem+json で返す（API 仕様 §2）。
func writeProblem(w http.ResponseWriter, status int, code domain.Code, title string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(struct {
		Type   string      `json:"type"`
		Title  string      `json:"title"`
		Status int         `json:"status"`
		Code   domain.Code `json:"code"`
	}{Type: "urn:puppet-remo:error:" + string(code), Title: title, Status: status, Code: code}); err != nil {
		slog.Debug("write problem response failed", "error", err)
	}
}
