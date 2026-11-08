package domain

import "slices"

// MaxGroupMembers は対象グループ G のメンバー数（T を含む）の上限。超えた分は GroupCause=CauseTooManyMembers の検証不能として扱う。
const MaxGroupMembers = 16

// GroupReason は対象グループの判定・停止の理由コード（ErrInputBlocked の Detail["reason"]。blocked_by.reason にも使う）。
// 自ウィンドウ・昇格ウィンドウは既存の ReasonOwnWindow・ReasonElevatedWindow（GroupReason に変換して使う）。
type GroupReason string

const (
	// T の同一性（ResolveGroup が Group.TargetReason に入れる。選び直しのみ）。
	ReasonTargetClosed      GroupReason = "target_closed"      // T が一覧にない
	ReasonTargetReplaced    GroupReason = "target_replaced"    // 同じ ID で PID・開始時刻・クラスが違う（HWND の再利用）
	ReasonTargetUnsupported GroupReason = "target_unsupported" // 検証前の UWP、または同一性を確かめる値（PID・開始時刻・クラス）が不明

	// 非メンバーの分類。
	ReasonRelatedCrossProcess   GroupReason = "related_cross_process"   // T と所有関係があるが別プロセス（自動では含めず停止）
	ReasonRelatedUnverifiable   GroupReason = "related_unverifiable"    // 所有関係を確かめられない（自動では含めず停止。対象の表示からは除外）
	ReasonUnrelatedSameProcess  GroupReason = "unrelated_same_process"  // PID が同じだけで所有関係がない
	ReasonUnrelatedOtherProcess GroupReason = "unrelated_other_process" // 別プロセスで、T との所有関係もない（普通の他のウィンドウ）

	// 座標・前面の検証の失敗（GroupBlock.Reason）。
	ReasonTargetNotForeground GroupReason = "target_not_foreground" // 前面が G の外（所有関係のない別ウィンドウ）
	ReasonTargetObscured      GroupReason = "target_obscured"       // G のメンバーの範囲の座標だが、最上位は非メンバー
	ReasonOutsideTargetGroup  GroupReason = "outside_target_group"  // 座標が G のどのメンバーの範囲にも入らない
)

// GroupCause は GroupReason の細目（診断用。監査・ログに出してよい。ウィンドウの中身は含まない）。
type GroupCause string

const (
	CauseTooManyMembers        GroupCause = "too_many_members"        // メンバーが MaxGroupMembers を超えた
	CauseChainUnverifiable     GroupCause = "chain_unverifiable"      // 鎖に検証不能の印（循環・段数超過・消滅）があった
	CauseChainTooLong          GroupCause = "chain_too_long"          // 鎖が MaxChainHops 段を超えた
	CauseChainCycle            GroupCause = "chain_cycle"             // 鎖に同じウィンドウが 2 度現れた
	CauseUnknownLink           GroupCause = "unknown_link"            // 鎖のリンクの ID が空
	CauseUnknownProcess        GroupCause = "unknown_process"         // PID・開始時刻が不明で、T と同じプロセスか確かめられない
	CauseLinkClassMismatch     GroupCause = "link_class_mismatch"     // 鎖の中の T のクラスが T と違う
	CauseMenuOwnerUnknown      GroupCause = "menu_owner_unknown"      // hwndMenuOwner が無い（メニューモードでない・取得失敗）
	CauseMenuChainInconsistent GroupCause = "menu_chain_inconsistent" // MenuOwnerChain の先頭が MenuOwnerID と違う
)

// MemberRelation はメンバーが T とどう結びついているか。
type MemberRelation string

const (
	RelationTarget MemberRelation = "target" // T 自身
	RelationOwned  MemberRelation = "owned"  // owner 鎖で T に至る
	RelationChild  MemberRelation = "child"  // 子ウィンドウ（親の鎖で T に至る）
	RelationMenu   MemberRelation = "menu"   // メニュー（MenuOwnerChain で T に至る）
)

// Membership は 1 枚のウィンドウの分類。Relation が空でなければメンバー、空なら Reason が非メンバーの理由。
type Membership struct {
	Relation MemberRelation
	Reason   GroupReason
	Cause    GroupCause
	// PID は理由の元になったプロセス（別プロセスのリンクがあるときはそのリンクの PID。それ以外は窓自身の PID）。
	PID uint32
}

// Member はメンバーかを返す。
func (m Membership) Member() bool { return m.Relation != "" }

// GroupMember は G のメンバー。
type GroupMember struct {
	Window   WindowInfo
	Relation MemberRelation
}

// NonMember は G に入らなかったウィンドウと、その分類。Own・Elevated の窓は ReasonOwnWindow・ReasonElevatedWindow。
// クリックスルーのオーバーレイ（Overlay）は誰にも数えないので含めない。
type NonMember struct {
	Window WindowInfo
	Reason GroupReason
	Cause  GroupCause
	PID    uint32
}

// BlockedBy は停止の原因になったウィンドウ（input_blocked の blocked_by{process, pid, reason}。タイトルは含めない）。
type BlockedBy struct {
	WindowID string
	Process  string // 不明なら空
	PID      uint32
	Reason   GroupReason // 分類の理由（related_cross_process など）。分類がなければ空
}

// BlockedBy は n を停止の原因として表す。
func (n NonMember) BlockedBy() BlockedBy {
	return blockedByWindow(n.Window, n.Reason, n.PID)
}

func blockedByWindow(w WindowInfo, reason GroupReason, pid uint32) BlockedBy {
	b := BlockedBy{WindowID: w.ID, PID: pid, Reason: reason}
	if pid == w.PID {
		b.Process = w.Process
	}
	return b
}

// GroupBlock は座標・フォーカスの検証結果。Reason が空なら通る。
type GroupBlock struct {
	Reason GroupReason
	By     BlockedBy
}

// Blocked は検証に失敗した（入力を止める）かを返す。
func (b GroupBlock) Blocked() bool { return b.Reason != "" }

// Group は対象 T と、T との所有関係を検証できた同一プロセスの関連ウィンドウ（メンバー）、および非メンバーの分類。ResolveGroup が作る値で、更新しない。
type Group struct {
	Ref WindowRef
	// TargetReason が空でなければ T を特定できず（ReasonTargetClosed / Replaced / Unsupported）、Members・NonMembers は空。選び直しのみ。
	TargetReason GroupReason
	Target       WindowInfo // TargetReason が空のときの T
	// Members は T を含むメンバー（最大 MaxGroupMembers）。入力の windows の順（Z オーダー。前面が先）。
	Members []GroupMember
	// NonMembers はメンバーでない窓（オーバーレイを除く）。windows の順。
	NonMembers []NonMember
}

// ResolveGroup は一覧 windows（platform.WindowManager.ListAll の結果。Z オーダーで前面が先）から、ref の T と対象グループ G を求める純粋関数。
//
// メンバーは T と、T と同じプロセス（PID と ProcessStart の両方が一致）のウィンドウのうち、所有関係を検証できたもの:
//   - owner 鎖（OwnerChain。子ウィンドウは親の鎖）が T に至り、鎖の T までの各リンクの PID・ProcessStart が T と一致する
//   - メニュー（Class == ClassMenu）は MenuOwnerChain が T に至り、同じく T までの各リンクとメニュー自身が T と一致する
//
// PID が同じだけで所有関係がないウィンドウは入れない（ReasonUnrelatedSameProcess）。別プロセスのウィンドウ・所有関係を確かめられないウィンドウは
// 自動では入れず、NonMembers に理由（ReasonRelatedCrossProcess / ReasonRelatedUnverifiable）つきで残す。GA_ROOTOWNER 相当の値や PID の一致だけでは判定しない。
// 自プロセスのウィンドウ（Own）は誰にも数えない（Overlay は NonMembers にも出さない）。
func ResolveGroup(ref WindowRef, windows []WindowInfo) Group {
	g := Group{Ref: ref}
	if ref.Class == ClassApplicationFrameWindow || !ref.Known() {
		g.TargetReason = ReasonTargetUnsupported
		return g
	}
	ti := slices.IndexFunc(windows, func(w WindowInfo) bool { return w.ID == ref.ID })
	if ti < 0 {
		g.TargetReason = ReasonTargetClosed
		return g
	}
	t := windows[ti]
	switch {
	case !ref.Matches(t):
		g.TargetReason = ReasonTargetReplaced
		return g
	case t.Overlay:
		g.TargetReason = ReasonTargetUnsupported
		return g
	}
	g.Target = t

	budget := MaxGroupMembers - 1 // T の分を除く
	for i, w := range windows {
		if i == ti {
			g.Members = append(g.Members, GroupMember{Window: w, Relation: RelationTarget})
			continue
		}
		if w.Overlay {
			continue
		}
		m := classify(t, w)
		switch {
		case m.Member() && budget > 0:
			budget--
			g.Members = append(g.Members, GroupMember{Window: w, Relation: m.Relation})
		case m.Member():
			g.NonMembers = append(g.NonMembers, NonMember{Window: w, Reason: ReasonRelatedUnverifiable, Cause: CauseTooManyMembers, PID: w.PID})
		default:
			g.NonMembers = append(g.NonMembers, NonMember{Window: w, Reason: m.Reason, Cause: m.Cause, PID: m.PID})
		}
	}
	return g
}

// Resolved は T を特定できたか（TargetReason が空か）を返す。
func (g Group) Resolved() bool { return g.TargetReason == "" }

// Member は id のメンバーを返す。
func (g Group) Member(id string) (GroupMember, bool) {
	for _, m := range g.Members {
		if m.Window.ID == id {
			return m, true
		}
	}
	return GroupMember{}, false
}

// IsMember は w が G のメンバーかを返す。ID だけでなく同一性（ID・PID・開始時刻・クラス）が G を求めたときと一致しなければ false
// （HWND が別のウィンドウに再利用されたものをメンバーとして通さない）。
func (g Group) IsMember(w WindowInfo) bool {
	m, ok := g.Member(w.ID)
	if !ok {
		return false
	}
	return RefOf(m.Window) == RefOf(w)
}

// Classify は w（G を求めたあとに現れたウィンドウを含む）を T に対して分類する。メンバーなら Relation が入る。
// ID が T と同じで同一性が違うウィンドウは ReasonTargetReplaced。G が T を特定できていない（Resolved が false）ときは、非メンバー（理由は G.TargetReason）を返す。
func (g Group) Classify(w WindowInfo) Membership {
	if !g.Resolved() {
		return Membership{Reason: g.TargetReason, PID: w.PID}
	}
	return classify(g.Target, w)
}

// visibleMembers は最小化されておらず、矩形が空でないメンバー（Z オーダー順）。
func (g Group) visibleMembers() []GroupMember {
	var out []GroupMember
	for _, m := range g.Members {
		if !m.Window.Minimized && !m.Window.Rect.Empty() {
			out = append(out, m)
		}
	}
	return out
}

// Bounds はメンバー（最小化されていないもの）の矩形の外接矩形を返す。該当するメンバーがなければ ok=false。
// メニューやダイアログが T の矩形の外にあれば、その分だけ広がる。メンバー個別の矩形は Members[i].Window.Rect。
func (g Group) Bounds() (r Rect, ok bool) {
	for _, m := range g.visibleMembers() {
		if !ok {
			r, ok = m.Window.Rect, true
			continue
		}
		r = unionRect(r, m.Window.Rect)
	}
	return r, ok
}

func unionRect(a, b Rect) Rect {
	x0, y0 := min(a.X, b.X), min(a.Y, b.Y)
	x1, y1 := max(a.X+a.W, b.X+b.W), max(a.Y+a.H, b.Y+b.H)
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// MemberAt は p を矩形に含む最上位（Z オーダーで最も前）のメンバーを返す。最小化されたメンバーは対象外。
func (g Group) MemberAt(p Point) (GroupMember, bool) {
	for _, m := range g.visibleMembers() {
		if m.Window.Rect.Contains(p) {
			return m, true
		}
	}
	return GroupMember{}, false
}

// NonMember は id の非メンバーの分類を返す。
func (g Group) NonMember(id string) (NonMember, bool) {
	for _, n := range g.NonMembers {
		if n.Window.ID == id {
			return n, true
		}
	}
	return NonMember{}, false
}

// Unverifiable は所有関係を確かめられない非メンバー（ReasonRelatedUnverifiable）を返す。対象の表示からは除外し、撮らない。
func (g Group) Unverifiable() []NonMember { return g.withReason(ReasonRelatedUnverifiable) }

// CrossProcess は T と所有関係のある別プロセスの非メンバー（ReasonRelatedCrossProcess）を返す。
func (g Group) CrossProcess() []NonMember { return g.withReason(ReasonRelatedCrossProcess) }

func (g Group) withReason(r GroupReason) []NonMember {
	var out []NonMember
	for _, n := range g.NonMembers {
		if n.Reason == r {
			out = append(out, n)
		}
	}
	return out
}

// CheckFocus はキー・文字の宛先（前面と hwndFocus の葉）が G の中かを検証する。通れば Reason が空。
// 前面が非メンバーなら、その分類が ReasonRelatedCrossProcess / ReasonRelatedUnverifiable / ReasonUnrelatedSameProcess / ReasonTargetReplaced のときはその理由、
// それ以外（所有関係のない別プロセスのウィンドウ・自ウィンドウ・前面なし）は ReasonTargetNotForeground。
// 前面がメンバーでも、葉（GA_ROOT に引き上げる前）の PID・ProcessStart がメンバーと違えば ReasonRelatedCrossProcess、取れなければ ReasonRelatedUnverifiable。
func (g Group) CheckFocus(f FocusInfo) GroupBlock {
	if !g.Resolved() {
		return GroupBlock{Reason: g.TargetReason}
	}
	fg := f.Foreground
	if fg.ID == "" {
		return GroupBlock{Reason: ReasonTargetNotForeground}
	}
	if !g.IsMember(fg) {
		m := g.Classify(fg)
		reason := m.Reason
		switch reason {
		case ReasonRelatedCrossProcess, ReasonRelatedUnverifiable, ReasonUnrelatedSameProcess, ReasonTargetReplaced:
		default:
			reason = ReasonTargetNotForeground
		}
		return GroupBlock{Reason: reason, By: blockedByWindow(fg, m.Reason, m.PID)}
	}
	return checkLeaf(fg, f.FocusLeaf)
}

// CheckHit は座標 p の宛先（h。platform.WindowManager.At の結果。何もなければ零値）が G の中かを検証する。通れば Reason が空。
//   - 最上位が非メンバー: ReasonRelatedCrossProcess / ReasonRelatedUnverifiable / ReasonTargetReplaced ならその理由。それ以外は p がメンバーの範囲内なら
//     ReasonTargetObscured（By.Reason に非メンバーの分類）、範囲外なら ReasonOutsideTargetGroup。
//   - 最上位がメンバー: p がそのメンバーの範囲外なら ReasonOutsideTargetGroup。葉の PID・ProcessStart がメンバーと違えば
//     ReasonRelatedCrossProcess、取れなければ ReasonRelatedUnverifiable。
//   - 何もない（デスクトップ）: p がメンバーの範囲内なら ReasonTargetObscured、外なら ReasonOutsideTargetGroup。
//
// 非メンバーが別の場所で T に重なっていても、p の最上位がメンバーなら通る。
func (g Group) CheckHit(p Point, h HitInfo) GroupBlock {
	if !g.Resolved() {
		return GroupBlock{Reason: g.TargetReason}
	}
	top := h.Top
	_, inside := g.MemberAt(p)
	if top.ID == "" {
		return GroupBlock{Reason: obscuredOrOutside(inside)}
	}
	if !g.IsMember(top) {
		m := g.Classify(top)
		by := blockedByWindow(top, m.Reason, m.PID)
		switch m.Reason {
		case ReasonRelatedCrossProcess, ReasonRelatedUnverifiable, ReasonTargetReplaced:
			return GroupBlock{Reason: m.Reason, By: by}
		}
		return GroupBlock{Reason: obscuredOrOutside(inside), By: by}
	}
	member, _ := g.Member(top.ID)
	if !member.Window.Rect.Contains(p) || !top.Rect.Contains(p) {
		return GroupBlock{Reason: ReasonOutsideTargetGroup, By: BlockedBy{WindowID: top.ID, Process: top.Process, PID: top.PID}}
	}
	return checkLeaf(top, h.Leaf)
}

func obscuredOrOutside(insideMember bool) GroupReason {
	if insideMember {
		return ReasonTargetObscured
	}
	return ReasonOutsideTargetGroup
}

// checkLeaf は葉 leaf が、メンバー top と同じプロセスかを確かめる。
func checkLeaf(top WindowInfo, leaf LeafRef) GroupBlock {
	by := BlockedBy{WindowID: leaf.ID, Process: leaf.Process, PID: leaf.PID}
	if leaf.ID == "" || leaf.PID == 0 || leaf.ProcessStart == 0 {
		by.Reason = ReasonRelatedUnverifiable
		return GroupBlock{Reason: ReasonRelatedUnverifiable, By: by}
	}
	if leaf.PID != top.PID || leaf.ProcessStart != top.ProcessStart {
		by.Reason = ReasonRelatedCrossProcess
		return GroupBlock{Reason: ReasonRelatedCrossProcess, By: by}
	}
	return GroupBlock{}
}

// GroupDiff は 2 つの G のメンバーの差（ID。#158 のエージェントの観測に使う比較用のデータ）。
type GroupDiff struct {
	Added    []string // 新しい G にだけいる
	Removed  []string // 前の G にだけいる
	Replaced []string // ID は同じだが同一性（PID・開始時刻・クラス）が違う（HWND の再利用）
}

// Empty は差がないかを返す。
func (d GroupDiff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Replaced) == 0
}

// Diff は prev（前の G）から g（今の G）へのメンバーの増減を返す。メンバーの出入りそれ自体は停止の理由ではない。
func (g Group) Diff(prev Group) GroupDiff {
	var d GroupDiff
	for _, m := range g.Members {
		old, ok := prev.Member(m.Window.ID)
		switch {
		case !ok:
			d.Added = append(d.Added, m.Window.ID)
		case RefOf(old.Window) != RefOf(m.Window):
			d.Replaced = append(d.Replaced, m.Window.ID)
		}
	}
	for _, m := range prev.Members {
		if _, ok := g.Member(m.Window.ID); !ok {
			d.Removed = append(d.Removed, m.Window.ID)
		}
	}
	return d
}

// processRel は ウィンドウ・リンクが T と同じプロセスかの三値。
type processRel int

const (
	procSame processRel = iota
	procOther
	procUnknown
)

// processOf は (pid, start) のプロセスが T と同じかを返す。
// PID が違えば（同時に生きている別プロセスなので）開始時刻が不明でも別プロセス。PID が同じときは両方の開始時刻が分かって一致したときだけ同じ。
func processOf(t WindowInfo, pid uint32, start uint64) processRel {
	switch {
	case pid == 0:
		return procUnknown
	case pid != t.PID:
		return procOther
	case start == 0 || t.ProcessStart == 0:
		return procUnknown
	case start != t.ProcessStart:
		return procOther // 同じ PID でも開始時刻が違えば、PID が再利用された別のプロセス
	}
	return procSame
}

func unverifiable(cause GroupCause, pid uint32) Membership {
	return Membership{Reason: ReasonRelatedUnverifiable, Cause: cause, PID: pid}
}

func crossProcess(pid uint32) Membership {
	return Membership{Reason: ReasonRelatedCrossProcess, PID: pid}
}

// classify は w が t の G のメンバーかを分類する（t は同一性を確かめ済みの T）。
func classify(t, w WindowInfo) Membership {
	switch {
	case w.ID == t.ID:
		if RefOf(w) == RefOf(t) {
			return Membership{Relation: RelationTarget, PID: w.PID}
		}
		return Membership{Reason: ReasonTargetReplaced, PID: w.PID}
	case w.Own:
		return Membership{Reason: ReasonOwnWindow, PID: w.PID}
	}

	chain, relation := w.OwnerChain, RelationOwned
	if w.Style.Child {
		relation = RelationChild
	}
	if w.Class == ClassMenu {
		relation = RelationMenu
		chain = w.MenuOwnerChain
		switch {
		case w.MenuOwnerID == "" || len(chain) == 0:
			return unverifiable(CauseMenuOwnerUnknown, w.PID)
		case !chain[0].Unverifiable && chain[0].ID != w.MenuOwnerID:
			return unverifiable(CauseMenuChainInconsistent, w.PID)
		}
	}

	reached, problem := walkChain(t, w.ID, chain)
	if problem.Reason != "" {
		if problem.PID == 0 {
			problem.PID = w.PID
		}
		return problem
	}
	self := processOf(t, w.PID, w.ProcessStart)
	if reached {
		switch self {
		case procOther:
			return crossProcess(w.PID)
		case procUnknown:
			return unverifiable(CauseUnknownProcess, w.PID)
		}
		if reason := InputBlockReason(w); reason != "" {
			return Membership{Reason: GroupReason(reason), PID: w.PID}
		}
		return Membership{Relation: relation, PID: w.PID}
	}
	switch self {
	case procSame:
		return Membership{Reason: ReasonUnrelatedSameProcess, PID: w.PID}
	case procOther:
		return Membership{Reason: ReasonUnrelatedOtherProcess, PID: w.PID}
	}
	return unverifiable(CauseUnknownProcess, w.PID)
}

// walkChain は links（近い順）を T まで辿る。reached は T に至ったか。
// problem が空でなければ判定できない・通せない理由（reached が true のときは T までの途中のリンクの問題、false のときは鎖が辿れなかった）。
// 循環（同じ ID が 2 度・自身を含む）・MaxChainHops 段の超過・検証不能の印は、T に至る前なら検証不能。T に至ったあとのリンクは見ない。
func walkChain(t WindowInfo, selfID string, links []WindowLink) (reached bool, problem Membership) {
	seen := map[string]bool{selfID: true}
	var pending Membership // T に至るまでに見つけた問題（最初の 1 つ）
	for i, l := range links {
		switch {
		case i >= MaxChainHops:
			return false, unverifiable(CauseChainTooLong, 0)
		case l.Unverifiable:
			return false, unverifiable(CauseChainUnverifiable, 0)
		case l.ID == "":
			return false, unverifiable(CauseUnknownLink, 0)
		case seen[l.ID]:
			return false, unverifiable(CauseChainCycle, 0)
		}
		seen[l.ID] = true
		if l.ID == t.ID {
			if pending.Reason != "" {
				return true, pending
			}
			return true, checkTargetLink(t, l)
		}
		if pending.Reason != "" {
			continue
		}
		switch processOf(t, l.PID, l.ProcessStart) {
		case procOther:
			pending = crossProcess(l.PID)
		case procUnknown:
			pending = unverifiable(CauseUnknownProcess, l.PID)
		}
	}
	return false, Membership{}
}

// checkTargetLink は鎖の中の T のリンクが、T 自身と同じ（同じプロセス・同じクラス）かを確かめる。問題がなければ零値。
func checkTargetLink(t WindowInfo, l WindowLink) Membership {
	switch processOf(t, l.PID, l.ProcessStart) {
	case procOther:
		return crossProcess(l.PID)
	case procUnknown:
		return unverifiable(CauseUnknownProcess, l.PID)
	}
	if l.Class == "" || l.Class != t.Class {
		return unverifiable(CauseLinkClassMismatch, l.PID)
	}
	return Membership{}
}
