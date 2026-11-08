package service

import (
	"context"
	"image"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルはスコープ付きリース（#156）の対象グループ G の撮影。ビューア（#157）の配信とエージェント（#158）の観測が使う。
// デスクトップ・画面領域・モニターは撮らない（GroupCapturer.GrabGroup だけ）。撮れないときは画面全体に切り替えず、理由つきのエラーにする。

// GroupMemberFrame は GroupFrame に写っているメンバー 1 つ（撮影時点の同一性と矩形）。
type GroupMemberFrame struct {
	Ref      domain.WindowRef
	Rect     domain.Rect // 仮想デスクトップ上の物理座標（合成に置いた矩形）
	Relation domain.MemberRelation
}

// GroupFrame は G の合成画像と、撮影時点のメンバーの事実。
type GroupFrame struct {
	// Image は撮れたメンバーだけを置いた透明のキャンバス（物理ピクセル。呼び出し側のもの）。合成画像の座標 p → 物理座標は Origin + p。
	Image  *image.RGBA
	Origin domain.Point
	// Group は撮影のもとにした G（ListAll から求めた。T の同一性・メンバー・非メンバーの分類）。
	Group domain.Group
	// Members は合成に入ったメンバー（T を含む。前面が先）。撮影時点の集合で、エージェントの基準 S になる。
	Members []GroupMemberFrame
	// Excluded は所有関係を確かめられず、撮らず合成に入れなかったウィンドウ（capture_partial の通知。停止ではない）。
	Excluded []domain.BlockedBy
}

// Partial は表示から除外したウィンドウがあるかを返す。
func (f GroupFrame) Partial() bool { return len(f.Excluded) > 0 }

// GrabGroupFrame は ref の T と、その時点の G を撮って合成した画像を返す（リース・保持者は問わない。呼び出し側がスコープ付きリースの T を渡す）。
//
//   - キルスイッチ中は ErrKillSwitchActive
//   - T を特定できない（target_closed / target_replaced / target_unsupported）、T が自ウィンドウ・昇格・最小化 → ErrInputBlocked（reason が入る）
//   - T が撮れない・GroupCapturer が無い・撮影の失敗は capture_unavailable、T 以外の検証できたメンバーが撮れないのは capture_member_unavailable
//     （いずれも ErrInputBlocked。**モニター撮影・画面領域の切り出しには切り替えない**。停止（halted）にするかは呼び出し側が決める）
//   - ctx のキャンセル・platform の失敗（ウィンドウ一覧）は、そのエラー
func (c *control) GrabGroupFrame(ctx context.Context, ref domain.WindowRef) (GroupFrame, error) {
	if c.kill.Active() {
		return GroupFrame{}, domain.ErrKillSwitchActive
	}
	list, err := c.windows.ListAll(ctx)
	if err != nil {
		return GroupFrame{}, err
	}
	g := domain.ResolveGroup(ref, list)
	if !g.Resolved() {
		return GroupFrame{Group: g}, scopeBlocked(g.TargetReason, domain.BlockedBy{WindowID: ref.ID, PID: ref.PID})
	}
	if s, blocked := targetStateStop(g.Target); blocked {
		return GroupFrame{Group: g}, s.err()
	}
	img, refs, stop, err := c.grabGroup(ctx, g)
	if err != nil {
		return GroupFrame{Group: g}, err
	}
	if stop != nil {
		return GroupFrame{Group: g}, stop.err()
	}
	frame := GroupFrame{Image: img.Image, Origin: img.Origin, Group: g}
	for _, r := range refs {
		mc, _ := img.Status(r.ID)
		m, _ := g.Member(r.ID)
		frame.Members = append(frame.Members, GroupMemberFrame{Ref: r, Rect: mc.Rect, Relation: m.Relation})
	}
	for _, n := range g.Unverifiable() {
		frame.Excluded = append(frame.Excluded, n.BlockedBy())
	}
	return frame, nil
}
