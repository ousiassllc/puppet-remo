package ws

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// このファイルはビューアの対象ウィンドウ（スコープ付きリース。#157）の WebSocket 側: グループ源の座標・大きさ・原点、リースに連動した
// 配信の源の切り替えと前面化、target メッセージ（active / halted とバナーの材料）、再開（Control.ResumeTarget）。
//
// 配信の源はリースに従う（syncTarget）: このビューアがスコープ付きのリースを新しく持ったら T のグループ源に切り替えて T を前面にし、
// リースを失ったらモニター源へ戻す。同じリースのあいだ、利用者がモニター表示へ切り替えた（config の source / monitor）選択は覆さない
// （D1: モニター表示は権限として残る）。撮れない・検証できないときはモニター源に切り替えず、Control.VerifyTarget で停止にして表示不可を伝える。

// groupGeom は最後に書いたグループ源のフレームの大きさと原点（入力の座標変換と config の通知に使う）。
type groupGeom struct {
	known         bool
	width, height int
	origin        domain.Point
}

// targetState は conn の対象ウィンドウの状態（mu が守る）。
type targetState struct {
	mu         sync.Mutex
	geom       groupGeom
	boundLease string // 源を切り替えた（前面にした）スコープ付きリースの ID
	group      service.GroupStatus
	blocked    blockedInfo // 最後の入力・検証・再開が止められた理由と原因

	// sendMu は target メッセージの組み立てと送信を直列化する（lastTarget の重複排除と、送信順を保つ）。
	sendMu     sync.Mutex
	lastTarget []byte
	verifying  atomic.Bool
}

// blockedInfo は input_blocked の reason と blocked_by（ウィンドウのタイトルは含まない）。
type blockedInfo struct {
	reason string
	by     *blockedByBody
}

// geometry は cfg の源・大きさ・原点（ready・config 用）。モニター源はモニターの物理ピクセル、グループ源は最後のコマの大きさと原点
// （最初のコマまでは 0・省略）。
func (c *conn) geometry(cfg service.ViewerConfig, mons []domain.Monitor) (source string, w, h int, origin *pointBody) {
	if cfg.Target == (domain.WindowRef{}) {
		w, h = streamSize(mons, cfg.Monitor)
		return sourceMonitor, w, h, nil
	}
	c.tgt.mu.Lock()
	defer c.tgt.mu.Unlock()
	g := c.tgt.geom
	if !g.known {
		return sourceGroup, 0, 0, nil
	}
	return sourceGroup, g.width, g.height, &pointBody{X: g.origin.X, Y: g.origin.Y}
}

func (c *conn) writeConfig(cfg service.ViewerConfig, mons []domain.Monitor) error {
	src, w, h, origin := c.geometry(cfg, mons)
	return c.writeJSON(configMessage{
		Type: typeConfig, Monitor: cfg.Monitor, Source: src, Width: w, Height: h, Origin: origin, Quality: cfg.Quality, FPS: cfg.FPS,
	})
}

// writeFrame は Streamer のフレームを書く。グループ源のフレームの大きさ・原点が前回と違うときは、先に config（新しい大きさと原点）を送る
// （メニュー・ダイアログの開閉で画像の大きさが変わる。クライアントは config でキャンバスを作り直し、続くキーフレームを待つ）。
func (c *conn) writeFrame(b []byte) error {
	if info, ok := service.ParseGroupFrameInfo(b); ok && c.noteGroupFrame(info) {
		cfg := c.sess.Config()
		if cfg.Target != (domain.WindowRef{}) {
			mons, err := c.h.cfg.Control.Monitors(c.ctx)
			if err != nil {
				return err
			}
			if err := c.writeConfig(cfg, mons); err != nil {
				return err
			}
		}
	}
	return c.write(websocket.MessageBinary, b)
}

// noteGroupFrame は書こうとしているグループ源のフレームの大きさ・原点を覚え、前回から変わったか（最初を含む）を返す。
func (c *conn) noteGroupFrame(info service.GroupFrameInfo) bool {
	next := groupGeom{known: true, width: info.Width, height: info.Height, origin: info.Origin}
	c.tgt.mu.Lock()
	defer c.tgt.mu.Unlock()
	changed := c.tgt.geom != next
	c.tgt.geom = next
	return changed
}

// applySource は config の source を cfg.Target に反映する。group はこのビューアのスコープ付きリースの T だけ（無ければ invalid_argument）。
func (c *conn) applySource(msg clientMessage, cfg service.ViewerConfig) (service.ViewerConfig, error) {
	if msg.Source == nil {
		return cfg, nil
	}
	switch *msg.Source {
	case sourceMonitor:
		cfg.Target = domain.WindowRef{}
	case sourceGroup:
		h, ok := c.scopedHolder()
		if !ok {
			return cfg, domain.ErrInvalidArgument.WithMsg("no target window is bound to the lease")
		}
		cfg.Target = h.Scope.Target
	}
	return cfg, nil
}

// scopedHolder はこのビューアが保持しているスコープ付きのリース。
func (c *conn) scopedHolder() (service.Holder, bool) {
	h, ok := c.h.cfg.Leases.Holder()
	if !ok || h.Scope == nil || !h.Actor.SameSession(c.sess.Actor()) {
		return service.Holder{}, false
	}
	return h, true
}

// groupOp は座標を持つ op の At（合成画像の座標）を、物理座標（原点 + p）を含むモニターの座標へ直す。Control は従来どおり
// モニターのストリーム座標として変換・検証するので、座標の変換のための新しい経路は要らない。どのモニターにも入らない座標・原点が
// 未確定（最初のコマの前）は invalid_coordinates。
func (c *conn) groupOp(op domain.Op) (domain.Op, error) {
	switch o := op.(type) {
	case domain.PointerMove:
		var err error
		o.At, o.Monitor, err = c.mapGroupPoint(o.At)
		return o, err
	case domain.MouseButton:
		var err error
		o.At, o.Monitor, err = c.mapGroupPoint(o.At)
		return o, err
	case domain.Wheel:
		var err error
		o.At, o.Monitor, err = c.mapGroupPoint(o.At)
		return o, err
	}
	return op, nil
}

func (c *conn) mapGroupPoint(at domain.Point) (domain.Point, int, error) {
	c.tgt.mu.Lock()
	g := c.tgt.geom
	c.tgt.mu.Unlock()
	if !g.known {
		return at, 0, domain.ErrInvalidCoordinates.WithMsg("the target image has not arrived yet")
	}
	if at.X < 0 || at.Y < 0 || at.X >= g.width || at.Y >= g.height {
		return at, 0, domain.ErrInvalidCoordinates.WithMsg(fmt.Sprintf("(%d, %d) is outside the %dx%d target image", at.X, at.Y, g.width, g.height))
	}
	p := domain.Point{X: at.X + g.origin.X, Y: at.Y + g.origin.Y}
	mons, err := c.h.cfg.Control.Monitors(c.ctx)
	if err != nil {
		return at, 0, err
	}
	for _, m := range mons {
		if m.ContainsPhysical(p) {
			return domain.Point{X: p.X - m.Origin.X, Y: p.Y - m.Origin.Y}, m.Index, nil
		}
	}
	return at, 0, domain.ErrInvalidCoordinates.WithMsg("the point is outside every monitor")
}

// syncTarget はリースに合わせて配信の源を切り替える（冒頭のコメント）。切り替えの失敗はログに残すだけ（リースは変えない）。
func (c *conn) syncTarget() {
	cfg := c.sess.Config()
	h, mine := c.scopedHolder()
	if !mine {
		c.tgt.mu.Lock()
		c.tgt.boundLease = ""
		c.tgt.mu.Unlock()
		if cfg.Target != (domain.WindowRef{}) {
			cfg.Target = domain.WindowRef{}
			if err := c.sess.Configure(cfg); err != nil {
				c.h.log.Warn("websocket: returning to the monitor source failed", "error", err)
			}
		}
		return
	}
	c.tgt.mu.Lock()
	fresh := c.tgt.boundLease != h.LeaseID
	if fresh {
		c.tgt.boundLease = h.LeaseID
		c.tgt.geom = groupGeom{}
		c.tgt.group = service.GroupStatus{}
		c.tgt.blocked = blockedInfo{}
	}
	c.tgt.mu.Unlock()
	if !fresh {
		return
	}
	cfg.Target = h.Scope.Target
	if err := c.sess.Configure(cfg); err != nil {
		c.h.log.Warn("websocket: switching to the target window failed", "error", err)
		return
	}
	// 対象を前面にする（G のメンバーだけが宛先になれる Control.Do の経路。失敗しても表示・停止の判定はそのまま続ける）。
	if _, err := c.h.cfg.Control.Do(c.ctx, c.sess.Actor(), domain.FocusWindow{ID: h.Scope.Target.ID}); err != nil && c.ctx.Err() == nil {
		c.h.log.Debug("websocket: bringing the target window to the foreground failed", "error", err)
	}
}

// onGroupStatus はグループ源の撮影の状態を受ける。失敗は、このビューアのスコープ付きリースを Control.VerifyTarget で確かめて（撮れなければ
// 停止になる）、target を更新する。モニター源へは切り替えない。
func (c *conn) onGroupStatus(st service.GroupStatus) error {
	if _, ok := c.scopedHolder(); !ok {
		return nil
	}
	c.tgt.mu.Lock()
	c.tgt.group = st
	c.tgt.mu.Unlock()
	if st.Err == nil {
		return c.sendTarget()
	}
	if c.tgt.verifying.CompareAndSwap(false, true) {
		go func() {
			defer c.tgt.verifying.Store(false)
			_, err := c.h.cfg.Control.VerifyTarget(c.ctx, c.sess.Actor())
			c.noteBlocked(err)
			if err := c.sendTarget(); err != nil {
				c.finish(0, "")
			}
		}()
	}
	return nil
}

// resumeTarget は保持者の明示的な再開（target.resume）。全条件の再検証に通れば active、通らなければ halted のまま理由が更新される
// （どちらも target メッセージで返す）。リースの不備（保持者でない・スコープなし）は error メッセージ。
func (c *conn) resumeTarget() error {
	_, err := c.h.cfg.Control.ResumeTarget(c.ctx, c.sess.Actor())
	if err != nil && !errors.Is(err, domain.ErrInputBlocked) {
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		if werr := c.writeJSON(errorFor(err)); werr != nil {
			return werr
		}
	}
	c.noteBlocked(err)
	if err == nil {
		c.tgt.mu.Lock()
		c.tgt.blocked = blockedInfo{}
		c.tgt.mu.Unlock()
	}
	return c.sendTarget()
}

// noteBlocked は err が input_blocked なら、その reason と blocked_by を覚える（target メッセージの停止の理由の詳細）。
func (c *conn) noteBlocked(err error) {
	if err == nil || !errors.Is(err, domain.ErrInputBlocked) {
		return
	}
	info := blockedInfo{reason: detailString(err, "reason")}
	var de *domain.Error
	if errors.As(err, &de) {
		if m, ok := de.Detail["blocked_by"].(map[string]any); ok {
			by := &blockedByBody{}
			by.WindowID, _ = m["window_id"].(string)
			by.Process, _ = m["process"].(string)
			by.Reason, _ = m["reason"].(string)
			switch pid := m["pid"].(type) {
			case uint32:
				by.PID = pid
			case int:
				by.PID = uint32(pid)
			case float64:
				by.PID = uint32(pid)
			}
			info.by = by
		}
	}
	c.tgt.mu.Lock()
	c.tgt.blocked = info
	c.tgt.mu.Unlock()
}

// targetMessage は今の対象ウィンドウの状態。スコープ付きのリースを持たなければ state none。
func (c *conn) targetMessage() targetMessage {
	h, ok := c.scopedHolder()
	if !ok {
		return targetMessage{Type: typeTarget, State: "none"}
	}
	sc := h.Scope
	m := targetMessage{Type: typeTarget, State: string(sc.State), Process: sc.Process, Members: sc.Members}
	c.tgt.mu.Lock()
	defer c.tgt.mu.Unlock()
	if g := c.tgt.group; g.Err == nil && g.Members > 0 {
		m.Members = g.Members
		for _, e := range g.Excluded {
			m.Excluded = append(m.Excluded, blockedByBody{WindowID: e.WindowID, Process: e.Process, PID: e.PID, Reason: string(e.Reason)})
		}
		if len(m.Excluded) > 0 {
			m.Notices = []string{domain.NoticeCapturePartial}
		}
	}
	if sc.Halted() {
		m.Reason = string(sc.HaltReason)
		m.Reselect = sc.HaltReason.ReselectOnly()
		if c.tgt.blocked.reason == m.Reason {
			m.BlockedBy = c.tgt.blocked.by
		}
	}
	return m
}

// sendTarget は target メッセージを、前回送ったものと違うときだけ送る（none の初期状態は送らない）。
func (c *conn) sendTarget() error {
	c.tgt.sendMu.Lock()
	defer c.tgt.sendMu.Unlock()
	b, err := json.Marshal(c.targetMessage())
	if err != nil {
		return fmt.Errorf("ws: encode target: %w", err)
	}
	if c.tgt.lastTarget == nil {
		c.tgt.lastTarget = noTargetJSON
	}
	if bytes.Equal(b, c.tgt.lastTarget) {
		return nil
	}
	c.tgt.lastTarget = b
	return c.write(websocket.MessageText, b)
}

// noTargetJSON はスコープ付きのリースを持たない初期状態の target（重複排除の基準。送らない）。
var noTargetJSON = func() []byte {
	b, err := json.Marshal(targetMessage{Type: typeTarget, State: "none"})
	if err != nil {
		panic(err)
	}
	return b
}()
