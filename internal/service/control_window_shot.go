package service

import (
	"context"
	"fmt"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// このファイルは screenshot の window_id（ウィンドウ単位の撮影。#269。FR-M26）。
//
// 撮影は #156 の GroupCapturer.GrabGroup（PrintWindow）を使う。前面にしない・画面の DC を使わない・ほかのウィンドウの画素が混ざらない
// （対象の上に別のウィンドウが重なっていても、対象自身の内容だけが写る）。モニター撮影（Capturer.Grab）やデスクトップの切り抜きへは切り替えない:
// 撮れないときは理由つきのエラーにする。

// WindowShotRequest は WindowScreenshot の指定。
type WindowShotRequest struct {
	// WindowID は list_windows の id（HWND の 10 進文字列）。必須。
	WindowID string
	// Format は "png"（既定）か "jpeg"。
	Format string
	// IncludeOwned は、対象と同一プロセスで所有関係を検証できる関連ウィンドウ（所有されたダイアログ・メニュー・子ウィンドウ。
	// domain.ResolveGroup の対象グループ G のメンバー）も、Z オーダーで 1 枚の画像に重ねる（#156 の GrabGroup の合成）。
	// 別プロセスのウィンドウ・所有関係を確かめられないウィンドウは入れず、WindowShot.Omitted に理由を出す。
	IncludeOwned bool
}

// WindowShotMember は WindowShot の画像に写っているウィンドウ 1 つ（撮影時点の事実）。
type WindowShotMember struct {
	WindowID string
	Relation domain.MemberRelation
	// Rect は画像の中でこのウィンドウが占める範囲を、モニターの縮小座標で表したもの（WindowShot.Rect と同じ座標系）。
	Rect domain.Rect
}

// WindowShotOmitted は IncludeOwned のときに、画像に入れなかった関連ウィンドウ。
type WindowShotOmitted struct {
	WindowID string
	// Reason は domain.ReasonRelatedCrossProcess（別プロセス。自動では含めない）・domain.ReasonRelatedUnverifiable（所有関係を確かめられない）・
	// domain.ReasonCaptureMemberUnavailable（メンバーだが撮れなかった）。
	Reason domain.GroupReason
}

// WindowShot はウィンドウ単位のスクリーンショット（縮小済み）。
type WindowShot struct {
	Data     []byte
	MIME     string
	WindowID string
	// Monitor は座標系の基準にしたモニター（対象のウィンドウが最も重なるモニター。縮小率もこのモニターのもの）。
	Monitor int
	// Rect は画像の左上と大きさを、Monitor の縮小スクリーンショットの座標で表したもの（W・H は Scaled と同じ）。画像の点 (px, py) は、
	// 縮小座標 (Rect.X + px, Rect.Y + py) に当たり、そのまま left_click などの x・y に使える（モニターの縮小座標と同じ縮小率。±1 px の丸め）。
	// ウィンドウがモニターからはみ出しているときは、はみ出した部分が負の座標か縮小スクリーンショットの外の座標になる（その座標へのクリックは invalid_coordinates）。
	Rect domain.Rect
	// Physical は撮れた画像の元の大きさ（物理ピクセル）。
	Physical domain.Size
	// Scaled は Data の画像の大きさ（Monitor の縮小率で縮小。長辺が max_dimension に収まるとは限らない: ウィンドウがモニターより大きいとき）。
	Scaled domain.Size
	Scale  float64
	// Members は画像に写っているウィンドウ（対象を含む。前面が先）。IncludeOwned でなければ対象 1 つだけ。
	Members []WindowShotMember
	// Omitted は IncludeOwned のとき、画像に入れなかった関連ウィンドウ。
	Omitted []WindowShotOmitted
	// OwnWindows は撮影時点で Monitor 上にある Nefos Remote のウィンドウの範囲（縮小座標。Screenshot と同じ。取れなければ nil）。
	// 画像には写らない（PrintWindow は対象自身の内容だけを返す）が、画面上でその範囲への入力は拒否される。
	OwnWindows []domain.OwnRegion
}

// WindowScreenshot は req.WindowID のウィンドウだけのスクリーンショットを返す（FR-M26。リース不要）。検査の順序は固定:
//
//  1. キルスイッチ → ErrKillSwitchActive。画像形式・window_id の検査 → ErrInvalidArgument
//  2. ウィンドウの特定: ListAll（可視・非 cloaked のトップレベルウィンドウ）にない → ErrWindowNotFound（閉じた・cloaked・形式が不正）
//  3. 撮らないウィンドウ → ErrInputBlocked（detail.reason）: 自ウィンドウ own_window（NFR-S06。puppet-remo の画面は決して撮らない。detail.own_windows 付き）、
//     昇格ウィンドウ elevated_window（PrintWindow は UIPI で届かない）、最小化 target_minimized、同一性を確かめる値が不明・検証前の UWP
//     （IncludeOwned のとき）target_unsupported
//  4. GroupCapturer.GrabGroup で撮る。GroupCapturer が無い・対象が撮れない・撮影の失敗は capture_unavailable、対象が撮影の直前に消えた・別のウィンドウに
//     なった（HWND の再利用）は ErrWindowNotFound。IncludeOwned のメンバー（対象以外）が撮れないときは停止せず Omitted に出す。ctx のキャンセルはそのエラー
//
// モニター撮影には切り替えない。画像は Monitor の縮小率で縮小し、座標は WindowShot.Rect に従って縮小座標へ対応づく。
func (c *control) WindowScreenshot(ctx context.Context, req WindowShotRequest) (WindowShot, error) {
	if c.kill.Active() {
		return WindowShot{}, domain.ErrKillSwitchActive
	}
	format, err := normalizeFormat(req.Format)
	if err != nil {
		return WindowShot{}, err
	}
	if req.WindowID == "" {
		return WindowShot{}, domain.ErrInvalidArgument.WithMsg("window_id must not be empty")
	}
	t, g, err := c.windowShotTarget(ctx, req)
	if err != nil {
		return WindowShot{}, err
	}
	members := windowShotMembers(t, g, req.IncludeOwned)
	img, omitted, err := c.grabWindowShot(ctx, t, members, g, req.IncludeOwned)
	if err != nil {
		return WindowShot{}, err
	}
	mons, err := c.capture.Monitors(ctx)
	if err != nil {
		return WindowShot{}, err
	}
	mon, err := pickMonitor(mons, t.Monitor)
	if err != nil {
		return WindowShot{}, err
	}
	maxDim, err := c.MaxDimension(ctx)
	if err != nil {
		return WindowShot{}, err
	}
	b := img.Image.Bounds()
	physical := domain.Size{W: b.Dx(), H: b.Dy()}
	rect := mon.ScaledExtent(domain.Rect{X: img.Origin.X, Y: img.Origin.Y, W: physical.W, H: physical.H}, maxDim)
	scaled := domain.Size{W: rect.W, H: rect.H}
	data, mime, err := encodeShot(resize(img.Image, scaled), format)
	if err != nil {
		return WindowShot{}, err
	}
	shot := WindowShot{
		Data: data, MIME: mime, WindowID: t.ID, Monitor: mon.Index, Rect: rect,
		Physical: physical, Scaled: scaled, Scale: mon.Scale(maxDim), Omitted: omitted,
	}
	for _, m := range members {
		if mc, ok := img.Status(m.Window.ID); ok && mc.Status == platform.CaptureOK {
			shot.Members = append(shot.Members, WindowShotMember{
				WindowID: m.Window.ID, Relation: m.Relation, Rect: mon.ScaledExtent(mc.Rect, maxDim),
			})
		}
	}
	// 自ウィンドウの範囲は手がかりなので、取れなくても撮影は返す（Screenshot と同じ）。
	own, _ := c.ownRegionsOn(ctx, mons, maxDim)
	shot.OwnWindows = regionsOnMonitor(own, mon.Index)
	return shot, nil
}

// windowShotTarget は window_id のウィンドウ T を特定し、撮ってよいかを確かめる。T と、その時点の対象グループ G（IncludeOwned のときの撮影・omitted に使う）を返す。
func (c *control) windowShotTarget(ctx context.Context, req WindowShotRequest) (domain.WindowInfo, domain.Group, error) {
	list, err := c.windows.ListAll(ctx)
	if err != nil {
		return domain.WindowInfo{}, domain.Group{}, err
	}
	var t domain.WindowInfo
	found := false
	for _, w := range list {
		if w.ID == req.WindowID {
			t, found = w, true
			break
		}
	}
	if !found {
		return domain.WindowInfo{}, domain.Group{}, domain.ErrWindowNotFound.
			WithMsg(fmt.Sprintf("window %q is not available (closed, cloaked, or not a top-level window)", req.WindowID)).
			WithDetail("window_id", req.WindowID)
	}
	if err := windowShotBlocked(t); err != nil {
		return domain.WindowInfo{}, domain.Group{}, c.withOwnWindows(ctx, err)
	}
	// 撮影は同一性（ID・PID・開始時刻・クラス）を確かめてから撮る（GrabGroup）。確かめられない窓・検証前の UWP は、include_owned でなくても撮らない。
	g := domain.ResolveGroup(domain.RefOf(t), list)
	if !g.Resolved() {
		return domain.WindowInfo{}, domain.Group{}, windowShotError(g.TargetReason, t.ID, "the window identity cannot be verified (unknown process or an unsupported UWP frame)")
	}
	return g.Target, g, nil
}

// windowShotBlocked は t を撮らない理由（自ウィンドウ・昇格・最小化）を ErrInputBlocked で返す。撮ってよければ nil。
func windowShotBlocked(t domain.WindowInfo) error {
	switch {
	case t.Own:
		return domain.CheckObserveTarget(t) // own_window。puppet-remo の画面（設定値・API キーの入力欄）は撮らない（NFR-S06）
	case t.Elevated:
		return windowShotError(domain.GroupReason(domain.ReasonElevatedWindow), t.ID, "the window is elevated, so its content cannot be captured per window")
	case t.Minimized:
		return windowShotError(domain.ReasonTargetMinimized, t.ID, "the window is minimized, so its content cannot be captured")
	}
	return nil
}

func windowShotError(reason domain.GroupReason, windowID, msg string) *domain.Error {
	return domain.ErrInputBlocked.WithMsg(msg).WithDetail("reason", string(reason)).WithDetail("window_id", windowID)
}

// windowShotMembers は撮るウィンドウ（前面が先）。対象は常に入れる。IncludeOwned のときは G のメンバーのうち、最小化されておらず矩形が空でないものを足す。
func windowShotMembers(t domain.WindowInfo, g domain.Group, includeOwned bool) []domain.GroupMember {
	if !includeOwned {
		return []domain.GroupMember{{Window: t, Relation: domain.RelationTarget}}
	}
	var out []domain.GroupMember
	for _, m := range g.Members {
		if m.Relation == domain.RelationTarget || !m.Window.Minimized && !m.Window.Rect.Empty() {
			out = append(out, m)
		}
	}
	return out
}

// grabWindowShot は members を GroupCapturer.GrabGroup で撮る。対象が撮れなければエラー、対象以外が撮れなかったものは omitted に出す。
// includeOwned のときは、G に入らなかった同一プロセス外の関連ウィンドウ（別プロセス・所有関係を確かめられない）も omitted に出す。
func (c *control) grabWindowShot(ctx context.Context, t domain.WindowInfo, members []domain.GroupMember, g domain.Group, includeOwned bool) (platform.GroupImage, []WindowShotOmitted, error) {
	gc, ok := c.capture.(platform.GroupCapturer)
	if !ok {
		return platform.GroupImage{}, nil, windowShotError(domain.ReasonCaptureUnavailable, t.ID, "per-window capture is not available on this host")
	}
	refs := make([]domain.WindowRef, len(members))
	for i, m := range members {
		refs[i] = domain.RefOf(m.Window)
	}
	img, err := gc.GrabGroup(ctx, refs)
	if err != nil {
		if ctx.Err() != nil {
			return platform.GroupImage{}, nil, ctx.Err()
		}
		return platform.GroupImage{}, nil, windowShotError(domain.ReasonCaptureUnavailable, t.ID, "the window could not be captured")
	}
	var omitted []WindowShotOmitted
	for _, m := range members {
		mc, found := img.Status(m.Window.ID)
		if found && mc.Status == platform.CaptureOK {
			continue
		}
		if m.Window.ID != t.ID {
			omitted = append(omitted, WindowShotOmitted{WindowID: m.Window.ID, Reason: domain.ReasonCaptureMemberUnavailable})
			continue
		}
		if found && mc.Status == platform.CaptureGone {
			return platform.GroupImage{}, nil, domain.ErrWindowNotFound.
				WithMsg("the window was closed or replaced before it could be captured").WithDetail("window_id", t.ID)
		}
		return platform.GroupImage{}, nil, windowShotError(domain.ReasonCaptureUnavailable, t.ID, "the window content could not be captured (PrintWindow failed)")
	}
	if img.Image == nil { // 対象が ok なのに画像がない（契約違反）。撮れなかったものとして扱う
		return platform.GroupImage{}, nil, windowShotError(domain.ReasonCaptureUnavailable, t.ID, "the window content could not be captured")
	}
	if includeOwned {
		for _, n := range append(g.CrossProcess(), g.Unverifiable()...) {
			omitted = append(omitted, WindowShotOmitted{WindowID: n.Window.ID, Reason: n.Reason})
		}
	}
	return img, omitted, nil
}
