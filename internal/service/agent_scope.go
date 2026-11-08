package service

import (
	"context"
	"fmt"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは window_id つきの run_task（#158。docs/components/overview.md §5.6）の、見るもの（グループ合成画像）・基準 S・座標の変換・
// 同一性の判定。ループ（agent_run.go）が呼ぶ。
//
// 基準 S は、モデルが見た最後の撮影（最初の撮影と、バッチのあとの撮影）。S の原点・縮小率・各メンバー（T を含む）の同一性・矩形・集合を撮影時点で保存し、
// 座標 → 物理座標の変換は、バッチの最後まで常に S のもので行う（バッチの途中で画像は撮らない・再計算しない）。
// 入力の注入の直前ごと（1 つの操作の複数回の注入の間を含む）に、Control が求め直した G（G_k）を、直前の検証の G（G_ref。最初は S の G）と比べる（observe）:
//   - T または既存メンバーの矩形が違う → target_moved、同一性（ID・PID・開始時刻・クラス）が違う → target_replaced（停止。failed。どの k でも）
//   - メンバーの増減だけ（既存メンバーは同一）→ 中断（残りを実行せず、新しい画像から計画し直す。失敗ではない。k = 1 でも）
//   - 差がなければ、座標の最上位・前面とフォーカスが G（= S のメンバー）の中かは Control の #156 の検証（control_target.go）が見る

// agentScope は 1 つのタスクの対象 T と、基準 S。run の goroutine だけが触る。
type agentScope struct {
	ref domain.WindowRef

	// 基準 S（撮影ごとに差し替える）。
	origin domain.Point // 合成画像の座標 p → 物理座標 = origin + p ÷ 縮小率
	phys   domain.Size  // 合成画像（物理ピクセル）の大きさ
	scaled domain.Size  // モデルへ渡した画像の大きさ
	frame  GroupFrame   // 撮影時点の G とメンバー（ID・PID・開始時刻・クラス・矩形）。画像は持たない

	// prev は直前の検証の G（G_ref）。
	prev domain.Group
	// diff は直近の中断の原因（メンバーの増減）。
	diff domain.GroupDiff
}

func newAgentScope(ref domain.WindowRef) *agentScope { return &agentScope{ref: ref} }

// setBaseline は撮影 f（モデルへ渡した画像の大きさ scaled）を新しい基準 S にする。
func (s *agentScope) setBaseline(f GroupFrame, scaled domain.Size) {
	b := f.Image.Bounds()
	s.origin, s.phys, s.scaled = f.Origin, domain.Size{W: b.Dx(), H: b.Dy()}, scaled
	f.Image = nil
	s.frame = f
	s.prev = f.Group
}

// fitSize は w×h を、長辺が maxDim 以下になるよう縮小した大きさにする（各辺は四捨五入、最小 1。maxDim が正でない・収まるなら等倍）。
func fitSize(w, h, maxDim int) domain.Size {
	long := max(w, h)
	if maxDim <= 0 || long <= maxDim {
		return domain.Size{W: w, H: h}
	}
	return domain.Size{W: max(1, roundDiv(w*maxDim, long)), H: max(1, roundDiv(h*maxDim, long))}
}

// roundDiv は a ÷ b（b > 0）を四捨五入する（.5 は正の無限大方向）。
func roundDiv(a, b int) int {
	q, r := a/b, a%b
	if r < 0 {
		q, r = q-1, r+b
	}
	if 2*r >= b {
		q++
	}
	return q
}

// toPhysical はモデルの座標 p（S の画像上）を、S の原点・縮小率で仮想デスクトップの物理座標にする。
func (s *agentScope) toPhysical(p domain.Point) domain.Point {
	if s.scaled.W <= 0 || s.scaled.H <= 0 {
		return p
	}
	return domain.Point{X: s.origin.X + roundDiv(p.X*s.phys.W, s.scaled.W), Y: s.origin.Y + roundDiv(p.Y*s.phys.H, s.scaled.H)}
}

// convert は op の座標を S の原点・縮小率で物理座標へ変換する（座標を持たない op はそのまま）。monitor の指定はスコープでは使えない
// （グループ合成画像の座標にはモニターが無い。MonitorPrimary 以外は ErrInvalidArgument）。
func (s *agentScope) convert(op domain.Op) (domain.Op, error) {
	var monitor int
	switch o := op.(type) {
	case domain.Click:
		monitor = o.Monitor
	case domain.Drag:
		monitor = o.Monitor
	case domain.Scroll:
		monitor = o.Monitor
	case domain.MouseMove:
		monitor = o.Monitor
	default:
		return op, nil
	}
	if monitor != domain.MonitorPrimary {
		return nil, domain.ErrInvalidArgument.WithMsg("monitor cannot be specified for a window-scoped task")
	}
	switch o := op.(type) {
	case domain.Click:
		if o.At != nil {
			at := s.toPhysical(*o.At)
			o.At = &at
		}
		return o, nil
	case domain.Drag:
		o.From, o.To = s.toPhysical(o.From), s.toPhysical(o.To)
		return o, nil
	case domain.Scroll:
		o.At = s.toPhysical(o.At)
		return o, nil
	case domain.MouseMove:
		o.At = s.toPhysical(o.At)
		return o, nil
	}
	return op, nil
}

// observe は Control が入力の注入の直前に求め直した G（g）を G_ref と比べる（groupObserver）。差がなければ G_ref を g にして通す。
func (s *agentScope) observe(g domain.Group) (*scopeStop, bool) {
	for _, old := range s.prev.Members {
		// 同じ ID のウィンドウが別の同一性（HWND の再利用）になった。メンバーのままでも、メンバーから外れて非メンバーに分類されても置き換え。
		var cur domain.WindowInfo
		if m, ok := g.Member(old.Window.ID); ok {
			cur = m.Window
		} else if n, ok := g.NonMember(old.Window.ID); ok {
			cur = n.Window
		} else {
			continue
		}
		if domain.RefOf(old.Window) != domain.RefOf(cur) {
			return &scopeStop{reason: domain.ReasonTargetReplaced, by: byWindow(cur)}, false
		}
	}
	for _, old := range s.prev.Members {
		if cur, ok := g.Member(old.Window.ID); ok && old.Window.Rect != cur.Window.Rect {
			return &scopeStop{reason: domain.ReasonTargetMoved, by: byWindow(cur.Window)}, false
		}
	}
	if d := g.Diff(s.prev); !d.Empty() {
		s.diff = d
		return nil, true
	}
	s.prev = g
	return nil, false
}

// observeScreen は新しい画面を PNG で撮る。スコープなしは FR-G06 の規則（プライマリモニター・最大辺は screenshot.max_dimension）、
// スコープ付きは対象グループの合成画像（GrabGroupFrame。モニター撮影・画面領域の切り出しには切り替えない）を同じ最大辺へ縮小し、
// 縮小率・原点・メンバーを新しい基準 S に保存する。除外したウィンドウ（所有関係を確かめられない）があればタスクに capture_partial を残す。
func (r *run) observeScreen(ctx context.Context) ([]byte, error) {
	if r.scope == nil {
		return r.capture(ctx)
	}
	frame, err := r.a.cfg.Control.GrabGroupFrame(ctx, r.scope.ref)
	if err != nil {
		return nil, err
	}
	maxDim, err := r.a.cfg.Control.MaxDimension(ctx)
	if err != nil {
		return nil, err
	}
	b := frame.Image.Bounds()
	scaled := fitSize(b.Dx(), b.Dy(), maxDim)
	data, _, err := encodeShot(resize(frame.Image, scaled), formatPNG)
	if err != nil {
		return nil, fmt.Errorf("encode the group frame: %w", err)
	}
	r.scope.setBaseline(frame, scaled)
	if frame.Partial() && !r.task.CapturePartial {
		r.task.CapturePartial = true
		r.persist()
	}
	return data, nil
}
