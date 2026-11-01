package ws

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// releaseTimeout は切断時の入力の解放（up）1 回あたりの上限。
const releaseTimeout = 3 * time.Second

// errInputEnded は、押下中の入力を離した後（セッションの終わり）に届いた入力を断る。
var errInputEnded = errors.New("ws: viewer session has ended")

// pressed は WebSocket 1 本が押している（down を注入した）ボタン・キーと、最後のポインタの位置を覚え、
// セッションの終わり（切断・置き換え・クライアントの削除・キルスイッチ）でそれらを離す。
// 入力の注入と状態の更新を 1 つのロックで直列化するので、離した後に入力が割り込むことは無い。
type pressed struct {
	mu      sync.Mutex
	ended   bool
	buttons []domain.Button   // 押した順
	keys    []domain.KeyEvent // 押した順（code ごとに最後の down）
	at      domain.Point
	monitor int
	hasAt   bool
}

// record は注入に成功した op を反映する。mu を持って呼ぶ。
func (p *pressed) record(op domain.Op) {
	switch o := op.(type) {
	case domain.PointerMove:
		p.at, p.monitor, p.hasAt = o.At, o.Monitor, true
	case domain.Wheel:
		p.at, p.monitor, p.hasAt = o.At, o.Monitor, true
	case domain.MouseButton:
		p.at, p.monitor, p.hasAt = o.At, o.Monitor, true
		switch o.Action {
		case domain.ButtonActionDown:
			if !slices.Contains(p.buttons, o.Button) {
				p.buttons = append(p.buttons, o.Button)
			}
		case domain.ButtonActionUp:
			p.buttons = slices.DeleteFunc(p.buttons, func(b domain.Button) bool { return b == o.Button })
		}
	case domain.KeyEvent:
		p.keys = slices.DeleteFunc(p.keys, func(k domain.KeyEvent) bool { return k.Code == o.Code })
		if o.Action == domain.KeyDown {
			p.keys = append(p.keys, o)
		}
	}
}

// injectInput は op を Control に渡し、成功したら押下の状態に反映する。離した後は errInputEnded。
func (c *conn) injectInput(op domain.Op) error {
	p := &c.pressed
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended {
		return errInputEnded
	}
	if _, err := c.h.cfg.Control.Do(c.ctx, c.sess.Actor(), op); err != nil {
		return err
	}
	p.record(op)
	return nil
}

// releasePressed は押下中のキー・ボタンを、押した順の逆に離す（ベストエフォート。失敗はログに残して続ける）。
// ViewerSession.OnEnd から、リースの解放の前に呼ばれる。キルスイッチの発動中は Control が拒むが、
// そのときは ReleaseAll が離す。
func (c *conn) releasePressed() {
	p := &c.pressed
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ended = true
	if len(p.keys) == 0 && len(p.buttons) == 0 {
		return
	}
	ops := make([]domain.Op, 0, len(p.keys)+len(p.buttons))
	for _, k := range slices.Backward(p.keys) {
		k.Action = domain.KeyUp
		ops = append(ops, k)
	}
	for _, b := range slices.Backward(p.buttons) {
		ops = append(ops, domain.MouseButton{At: p.at, Button: b, Action: domain.ButtonActionUp, Monitor: p.monitor})
	}
	p.keys, p.buttons = nil, nil

	// 切断で c.ctx は取り消されているので、取り消しを切り離して上限を付ける。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), releaseTimeout)
	defer cancel()
	for _, op := range ops {
		if _, err := c.h.cfg.Control.Do(ctx, c.sess.Actor(), op); err != nil && !errors.Is(err, domain.ErrKillSwitchActive) {
			c.h.log.Warn("websocket: releasing a pressed input failed", "op", op, "error", err)
		}
	}
}
