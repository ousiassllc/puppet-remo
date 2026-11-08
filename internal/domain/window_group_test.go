package domain_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

const (
	tPID   = 10
	tStart = 1000
	tClass = "Notepad"
)

func tWindow() domain.WindowInfo {
	return domain.WindowInfo{ID: "100", Title: "メモ帳", Process: "notepad.exe", PID: tPID, ProcessStart: tStart, Class: tClass,
		Rect: domain.Rect{X: 100, Y: 100, W: 400, H: 300}}
}

func tRef() domain.WindowRef { return domain.RefOf(tWindow()) }

func tLink() domain.WindowLink { return link("100", tPID, tStart, tClass) }

func link(id string, pid uint32, start uint64, class string) domain.WindowLink {
	return domain.WindowLink{ID: id, PID: pid, ProcessStart: start, Class: class}
}

// sameLink は T と同じプロセスのリンク。
func sameLink(id string) domain.WindowLink { return link(id, tPID, tStart, "Dlg") }

// win は T と同じプロセスのウィンドウ（owner 鎖つき）。
func win(id string, chain ...domain.WindowLink) domain.WindowInfo {
	return domain.WindowInfo{ID: id, Process: "notepad.exe", PID: tPID, ProcessStart: tStart, Class: "Dlg",
		Rect: domain.Rect{X: 120, Y: 120, W: 100, H: 80}, OwnerChain: chain}
}

func menu(id, ownerID string, chain ...domain.WindowLink) domain.WindowInfo {
	w := win(id)
	w.Class = domain.ClassMenu
	w.MenuOwnerID = ownerID
	w.MenuOwnerChain = chain
	return w
}

func with(w domain.WindowInfo, f func(*domain.WindowInfo)) domain.WindowInfo {
	f(&w)
	return w
}

// want の値: "member:<relation>" か、非メンバーの理由コード（任意で "/<cause>"）。
func describe(g domain.Group, id string) string {
	if m, ok := g.Member(id); ok {
		return "member:" + string(m.Relation)
	}
	if n, ok := g.NonMember(id); ok {
		if n.Cause != "" {
			return string(n.Reason) + "/" + string(n.Cause)
		}
		return string(n.Reason)
	}
	return "absent"
}

func TestResolveGroupMembership(t *testing.T) {
	cross := func(id string) domain.WindowInfo {
		return with(win(id, tLink()), func(w *domain.WindowInfo) { w.PID, w.ProcessStart = 99, 5000 })
	}
	tests := []struct {
		name    string
		windows []domain.WindowInfo
		want    map[string]string
	}{
		{"T 自身", []domain.WindowInfo{tWindow()}, map[string]string{"100": "member:target"}},
		{"owner 鎖 1 段のダイアログはメンバー", []domain.WindowInfo{win("d1", tLink()), tWindow()},
			map[string]string{"d1": "member:owned"}},
		{"owner 鎖 2 段のダイアログはメンバー", []domain.WindowInfo{win("d2", sameLink("d1"), tLink()), win("d1", tLink()), tWindow()},
			map[string]string{"d2": "member:owned", "d1": "member:owned"}},
		{"隠しオーナー（タイトルなし・非表示の同一プロセスのウィンドウ）経由のダイアログはメンバー",
			[]domain.WindowInfo{win("d", link("hidden", tPID, tStart, "Hidden"), tLink()), tWindow()},
			map[string]string{"d": "member:owned"}},
		{"同一プロセスの子ウィンドウはメンバー", []domain.WindowInfo{with(win("c", tLink()), func(w *domain.WindowInfo) { w.Style.Child = true }), tWindow()},
			map[string]string{"c": "member:child"}},
		{"メニュー: MenuOwnerChain が T に至り PID・開始時刻が一致ならメンバー",
			[]domain.WindowInfo{menu("m", "100", tLink()), tWindow()}, map[string]string{"m": "member:menu"}},
		{"メニュー: メニューの所有者がダイアログで、その owner 鎖が T に至ればメンバー",
			[]domain.WindowInfo{menu("m", "d1", sameLink("d1"), tLink()), win("d1", tLink()), tWindow()},
			map[string]string{"m": "member:menu", "d1": "member:owned"}},
		{"メニュー: GA_ROOTOWNER 相当が T でも鎖のリンクの開始時刻が取れなければメンバーにならない",
			[]domain.WindowInfo{menu("m", "d1", link("d1", tPID, 0, "Dlg"), tLink()), tWindow()},
			map[string]string{"m": "related_unverifiable/unknown_process"}},
		{"メニュー: 鎖のリンクが別 PID なら related_cross_process（T に至っていてもメンバーにならない）",
			[]domain.WindowInfo{menu("m", "x", link("x", 99, 5000, "Other"), tLink()), tWindow()},
			map[string]string{"m": "related_cross_process"}},
		{"メニュー: T のリンクの開始時刻が違えば（PID 再利用）related_cross_process",
			[]domain.WindowInfo{menu("m", "100", link("100", tPID, 777, tClass)), tWindow()},
			map[string]string{"m": "related_cross_process"}},
		{"メニュー: メニュー自身の開始時刻が T と違えば related_cross_process",
			[]domain.WindowInfo{with(menu("m", "100", tLink()), func(w *domain.WindowInfo) { w.ProcessStart = 2 }), tWindow()},
			map[string]string{"m": "related_cross_process"}},
		{"メニュー: メニュー自身の開始時刻が不明なら related_unverifiable",
			[]domain.WindowInfo{with(menu("m", "100", tLink()), func(w *domain.WindowInfo) { w.ProcessStart = 0 }), tWindow()},
			map[string]string{"m": "related_unverifiable/unknown_process"}},
		{"メニュー: hwndMenuOwner が 0（メニューモードでない）は related_unverifiable",
			[]domain.WindowInfo{menu("m", ""), tWindow()}, map[string]string{"m": "related_unverifiable/menu_owner_unknown"}},
		{"メニュー: 無所有（OwnerChain が空で MenuOwnerChain もない）は related_unverifiable",
			[]domain.WindowInfo{with(menu("m", "100"), func(w *domain.WindowInfo) { w.MenuOwnerChain = nil }), tWindow()},
			map[string]string{"m": "related_unverifiable/menu_owner_unknown"}},
		{"メニュー: OwnerChain が T に至っていても MenuOwnerChain がなければメンバーにならない（GW_OWNER だけで判定しない）",
			[]domain.WindowInfo{with(menu("m", ""), func(w *domain.WindowInfo) { w.OwnerChain = []domain.WindowLink{tLink()} }), tWindow()},
			map[string]string{"m": "related_unverifiable/menu_owner_unknown"}},
		{"メニュー: 所有者が別ウィンドウ（T に至らない）なら非メンバー",
			[]domain.WindowInfo{menu("m", "other", sameLink("other")), tWindow()},
			map[string]string{"m": "unrelated_same_process"}},
		{"メニュー: 鎖の先頭が MenuOwnerID と違うなら related_unverifiable",
			[]domain.WindowInfo{menu("m", "100", sameLink("zzz"), tLink()), tWindow()},
			map[string]string{"m": "related_unverifiable/menu_chain_inconsistent"}},
		{"PID一致だけで許可しない: 同一 PID・同一クラスで owner 鎖がなければ unrelated_same_process",
			[]domain.WindowInfo{with(win("s"), func(w *domain.WindowInfo) { w.Class = tClass }), tWindow()},
			map[string]string{"s": "unrelated_same_process"}},
		{"PID一致だけで許可しない: ApplicationFrameHost 型（1 プロセスの無関係な複数ウィンドウ）は T 以外すべて非メンバー",
			[]domain.WindowInfo{
				with(win("f1"), func(w *domain.WindowInfo) { w.Class = "ApplicationFrameWindow" }),
				with(win("f2"), func(w *domain.WindowInfo) { w.Class = "ApplicationFrameWindow" }),
				with(win("f3"), func(w *domain.WindowInfo) { w.Class = "ApplicationFrameWindow" }),
				tWindow(),
			},
			map[string]string{"f1": "unrelated_same_process", "f2": "unrelated_same_process", "f3": "unrelated_same_process"}},
		{"同一 PID の 2 フレーム（UWP 型）で T でない方は非メンバー",
			[]domain.WindowInfo{with(tWindow(), func(w *domain.WindowInfo) { w.ID = "101" }), tWindow()},
			map[string]string{"101": "unrelated_same_process"}},
		{"別プロセスで owner 鎖が T に至るウィンドウは related_cross_process", []domain.WindowInfo{cross("x"), tWindow()},
			map[string]string{"x": "related_cross_process"}},
		{"owner 鎖の途中に別 PID のリンクがあれば（同一プロセスのダイアログでも）related_cross_process",
			[]domain.WindowInfo{win("d", link("mid", 99, 5000, "Other"), tLink()), tWindow()},
			map[string]string{"d": "related_cross_process"}},
		{"別プロセスで T と所有関係がなければ unrelated_other_process",
			[]domain.WindowInfo{with(win("o"), func(w *domain.WindowInfo) { w.PID, w.ProcessStart = 99, 5000 }), tWindow()},
			map[string]string{"o": "unrelated_other_process"}},
		{"owner 鎖の循環は related_unverifiable",
			[]domain.WindowInfo{win("d", sameLink("a"), sameLink("b"), sameLink("a"), tLink()), tWindow()},
			map[string]string{"d": "related_unverifiable/chain_cycle"}},
		{"owner 鎖に自身が現れる循環は related_unverifiable",
			[]domain.WindowInfo{win("d", sameLink("d"), tLink()), tWindow()},
			map[string]string{"d": "related_unverifiable/chain_cycle"}},
		{"owner 鎖が 8 段で T に至るのはメンバー",
			[]domain.WindowInfo{win("d", chainTo(8)...), tWindow()}, map[string]string{"d": "member:owned"}},
		{"owner 鎖が 9 段（T に至る前に上限超過）は related_unverifiable",
			[]domain.WindowInfo{win("d", chainTo(9)...), tWindow()}, map[string]string{"d": "related_unverifiable/chain_too_long"}},
		{"owner 鎖の検証不能の印は related_unverifiable",
			[]domain.WindowInfo{win("d", sameLink("a"), domain.UnverifiableLink()), tWindow()},
			map[string]string{"d": "related_unverifiable/chain_unverifiable"}},
		{"owner 鎖のリンクの PID が不明（0）は related_unverifiable",
			[]domain.WindowInfo{win("d", link("a", 0, 0, "Dlg"), tLink()), tWindow()},
			map[string]string{"d": "related_unverifiable/unknown_process"}},
		{"owner 鎖のリンクの開始時刻が不明は related_unverifiable",
			[]domain.WindowInfo{win("d", link("a", tPID, 0, "Dlg"), tLink()), tWindow()},
			map[string]string{"d": "related_unverifiable/unknown_process"}},
		{"ウィンドウ自身の開始時刻が不明なら owner 鎖が T に至っても related_unverifiable",
			[]domain.WindowInfo{with(win("d", tLink()), func(w *domain.WindowInfo) { w.ProcessStart = 0 }), tWindow()},
			map[string]string{"d": "related_unverifiable/unknown_process"}},
		{"owner 鎖がなく開始時刻が不明な同一 PID のウィンドウは related_unverifiable（無関係と断定しない）",
			[]domain.WindowInfo{with(win("d"), func(w *domain.WindowInfo) { w.ProcessStart = 0 }), tWindow()},
			map[string]string{"d": "related_unverifiable/unknown_process"}},
		{"ダイアログの HWND が別 PID・別開始時刻で再利用されたものは非メンバー（鎖の T のリンクは古い T）",
			[]domain.WindowInfo{with(win("d", tLink()), func(w *domain.WindowInfo) { w.PID, w.ProcessStart = 55, 9000 }), tWindow()},
			map[string]string{"d": "related_cross_process"}},
		{"鎖の中の T のリンクのクラスが違えば related_unverifiable",
			[]domain.WindowInfo{win("d", link("100", tPID, tStart, "Other")), tWindow()},
			map[string]string{"d": "related_unverifiable/link_class_mismatch"}},
		{"自ウィンドウ（Own）は owner 鎖が T に至っても誰にも数えない",
			[]domain.WindowInfo{with(win("own", tLink()), func(w *domain.WindowInfo) { w.Own = true }), tWindow()},
			map[string]string{"own": "own_window"}},
		{"オーバーレイ（Overlay）はメンバーにも非メンバーにも出ない",
			[]domain.WindowInfo{with(win("ov", tLink()), func(w *domain.WindowInfo) { w.Own, w.Overlay = true, true }), tWindow()},
			map[string]string{"ov": "absent"}},
		{"昇格したメンバー候補は elevated_window",
			[]domain.WindowInfo{with(win("e", tLink()), func(w *domain.WindowInfo) { w.Elevated = true }), tWindow()},
			map[string]string{"e": "elevated_window"}},
		{"一覧にない所有者のウィンドウでも鎖が T に至っていればメンバー（所有者の列挙は不要）",
			[]domain.WindowInfo{win("d", sameLink("not-listed"), tLink()), tWindow()}, map[string]string{"d": "member:owned"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := domain.ResolveGroup(tRef(), tt.windows)
			if !g.Resolved() {
				t.Fatalf("TargetReason = %q, want resolved", g.TargetReason)
			}
			for id, want := range tt.want {
				if got := describe(g, id); got != want {
					t.Errorf("window %s: %s, want %s", id, got, want)
				}
			}
		})
	}
}

// chainTo は T に至るまでに n 段（T を含む）の同一プロセスのリンクを作る。T が n 段目。
func chainTo(n int) []domain.WindowLink {
	var out []domain.WindowLink
	for i := 1; i < n; i++ {
		out = append(out, sameLink(fmt.Sprintf("c%d", i)))
	}
	return append(out, tLink())
}

func TestResolveGroupTargetIdentity(t *testing.T) {
	uwp := domain.WindowInfo{ID: "100", PID: 1, ProcessStart: 5, Class: domain.ClassApplicationFrameWindow}
	tests := []struct {
		name    string
		ref     domain.WindowRef
		windows []domain.WindowInfo
		want    domain.GroupReason
	}{
		{"一覧にある", tRef(), []domain.WindowInfo{tWindow()}, ""},
		{"タイトルが変わっても同じ T", tRef(), []domain.WindowInfo{with(tWindow(), func(w *domain.WindowInfo) { w.Title = "変わった" })}, ""},
		{"T が一覧にない: target_closed", tRef(), []domain.WindowInfo{win("d", tLink())}, domain.ReasonTargetClosed},
		{"一覧が空: target_closed", tRef(), nil, domain.ReasonTargetClosed},
		{"HWND が別 PID で再利用: target_replaced", tRef(), []domain.WindowInfo{with(tWindow(), func(w *domain.WindowInfo) { w.PID = 77 })}, domain.ReasonTargetReplaced},
		{"同じ PID で開始時刻が違う: target_replaced", tRef(), []domain.WindowInfo{with(tWindow(), func(w *domain.WindowInfo) { w.ProcessStart = 1 })}, domain.ReasonTargetReplaced},
		{"クラスが違う: target_replaced", tRef(), []domain.WindowInfo{with(tWindow(), func(w *domain.WindowInfo) { w.Class = "Other" })}, domain.ReasonTargetReplaced},
		{"UWP の ApplicationFrameWindow は選べない: target_unsupported",
			domain.RefOf(uwp), []domain.WindowInfo{uwp}, domain.ReasonTargetUnsupported},
		{"開始時刻が不明の T は選べない: target_unsupported",
			domain.WindowRef{ID: "100", PID: tPID, Class: tClass}, []domain.WindowInfo{with(tWindow(), func(w *domain.WindowInfo) { w.ProcessStart = 0 })}, domain.ReasonTargetUnsupported},
		{"クラスが不明の T は選べない: target_unsupported",
			domain.WindowRef{ID: "100", PID: tPID, ProcessStart: tStart}, []domain.WindowInfo{with(tWindow(), func(w *domain.WindowInfo) { w.Class = "" })}, domain.ReasonTargetUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := domain.ResolveGroup(tt.ref, tt.windows)
			if g.TargetReason != tt.want {
				t.Fatalf("TargetReason = %q, want %q", g.TargetReason, tt.want)
			}
			if tt.want != "" && (len(g.Members) != 0 || len(g.NonMembers) != 0) {
				t.Errorf("unresolved group has Members %d, NonMembers %d, want none", len(g.Members), len(g.NonMembers))
			}
		})
	}
}

func TestWindowRefMatchesIgnoresTitle(t *testing.T) {
	ref := tRef()
	w := tWindow()
	w.Title = "別のタイトル"
	if !ref.Matches(w) {
		t.Error("Matches = false for a window whose only difference is the title")
	}
	for name, f := range map[string]func(*domain.WindowInfo){
		"ID":           func(w *domain.WindowInfo) { w.ID = "999" },
		"PID":          func(w *domain.WindowInfo) { w.PID = 99 },
		"ProcessStart": func(w *domain.WindowInfo) { w.ProcessStart = 99 },
		"Class":        func(w *domain.WindowInfo) { w.Class = "Other" },
	} {
		if ref.Matches(with(tWindow(), f)) {
			t.Errorf("Matches = true although %s differs", name)
		}
	}
	if (domain.WindowRef{}).Matches(domain.WindowInfo{}) {
		t.Error("an unknown WindowRef must not match an unknown window")
	}
}

func TestResolveGroupMemberLimit(t *testing.T) {
	windows := []domain.WindowInfo{tWindow()}
	for i := 0; i < domain.MaxGroupMembers; i++ { // T と合わせて 17 個
		windows = append(windows, win(fmt.Sprintf("d%02d", i), tLink()))
	}
	g := domain.ResolveGroup(tRef(), windows)
	if len(g.Members) != domain.MaxGroupMembers {
		t.Fatalf("members = %d, want %d", len(g.Members), domain.MaxGroupMembers)
	}
	last := fmt.Sprintf("d%02d", domain.MaxGroupMembers-1)
	if got, want := describe(g, last), "related_unverifiable/too_many_members"; got != want {
		t.Errorf("window %s: %s, want %s", last, got, want)
	}
	if got := g.Unverifiable(); len(got) != 1 || got[0].Window.ID != last {
		t.Errorf("Unverifiable() = %v, want only %s", got, last)
	}
}

func TestResolveGroupOwnAndOverlayCountForNobody(t *testing.T) {
	windows := []domain.WindowInfo{
		with(win("ov"), func(w *domain.WindowInfo) { w.Own, w.Overlay = true, true }),
		with(win("own", tLink()), func(w *domain.WindowInfo) { w.Own = true }),
		tWindow(),
	}
	g := domain.ResolveGroup(tRef(), windows)
	if len(g.Members) != 1 {
		t.Errorf("members = %d, want only T", len(g.Members))
	}
	if _, ok := g.NonMember("ov"); ok {
		t.Error("overlay appears in NonMembers")
	}
	if n, ok := g.NonMember("own"); !ok || n.Reason != domain.ReasonOwnWindow {
		t.Errorf("own window = %+v, %v; want non-member own_window", n, ok)
	}
}

func TestGroupBoundsAndMemberAt(t *testing.T) {
	dlg := with(win("d", tLink()), func(w *domain.WindowInfo) { w.Rect = domain.Rect{X: 50, Y: 400, W: 100, H: 80} }) // T の外へはみ出す
	men := with(menu("m", "100", tLink()), func(w *domain.WindowInfo) { w.Rect = domain.Rect{X: 450, Y: 120, W: 100, H: 100} })
	gone := with(win("min", tLink()), func(w *domain.WindowInfo) { w.Minimized, w.Rect = true, domain.Rect{X: -500, Y: -500, W: 10, H: 10} })
	other := with(win("o"), func(w *domain.WindowInfo) { w.PID, w.Rect = 99, domain.Rect{X: 0, Y: 0, W: 2000, H: 2000} })
	g := domain.ResolveGroup(tRef(), []domain.WindowInfo{men, other, dlg, gone, tWindow()})

	r, ok := g.Bounds()
	if want := (domain.Rect{X: 50, Y: 100, W: 500, H: 380}); !ok || r != want {
		t.Errorf("Bounds() = %+v, %v; want %+v (members' visible parts only: no minimized, no non-member)", r, ok, want)
	}
	if m, ok := g.MemberAt(domain.Point{X: 460, Y: 130}); !ok || m.Window.ID != "m" {
		t.Errorf("MemberAt(menu∩T) = %v, %v; want the topmost member (menu)", m.Window.ID, ok)
	}
	if m, ok := g.MemberAt(domain.Point{X: 300, Y: 300}); !ok || m.Window.ID != "100" {
		t.Errorf("MemberAt(T) = %v, %v; want T", m.Window.ID, ok)
	}
	if _, ok := g.MemberAt(domain.Point{X: 1000, Y: 1000}); ok {
		t.Error("MemberAt(non-member area) found a member")
	}
	if _, ok := domain.ResolveGroup(tRef(), nil).Bounds(); ok {
		t.Error("Bounds() of an unresolved group has ok=true")
	}
}

func leaf(id string, pid uint32, start uint64, class string) domain.LeafRef {
	return domain.LeafRef{WindowRef: domain.WindowRef{ID: id, PID: pid, ProcessStart: start, Class: class}, Process: "p.exe"}
}

func TestGroupCheckFocus(t *testing.T) {
	dlg := win("d", tLink())
	sameOther := win("s")
	crossDlg := with(win("x", tLink()), func(w *domain.WindowInfo) { w.PID, w.ProcessStart, w.Process = 99, 5000, "common.exe" })
	stranger := with(win("o"), func(w *domain.WindowInfo) { w.PID, w.ProcessStart, w.Process = 98, 6000, "calc.exe" })
	unver := menu("m", "")
	g := domain.ResolveGroup(tRef(), []domain.WindowInfo{dlg, sameOther, crossDlg, stranger, unver, tWindow()})

	selfLeaf := leaf("100", tPID, tStart, tClass)
	tests := []struct {
		name string
		f    domain.FocusInfo
		want domain.GroupReason
		pid  uint32
	}{
		{"T が前面で葉も T のプロセス", domain.FocusInfo{Foreground: tWindow(), FocusLeaf: selfLeaf}, "", 0},
		{"ダイアログ（メンバー）が前面", domain.FocusInfo{Foreground: dlg, FocusLeaf: leaf("e", tPID, tStart, "Edit")}, "", 0},
		{"前面が同一プロセスの別ウィンドウ", domain.FocusInfo{Foreground: sameOther, FocusLeaf: selfLeaf}, domain.ReasonUnrelatedSameProcess, tPID},
		{"前面が別プロセスの所有ダイアログ", domain.FocusInfo{Foreground: crossDlg, FocusLeaf: selfLeaf}, domain.ReasonRelatedCrossProcess, 99},
		{"前面が無関係な別プロセス", domain.FocusInfo{Foreground: stranger, FocusLeaf: selfLeaf}, domain.ReasonTargetNotForeground, 98},
		{"前面が検証不能のメニュー", domain.FocusInfo{Foreground: unver, FocusLeaf: selfLeaf}, domain.ReasonRelatedUnverifiable, tPID},
		{"前面がメンバーでも葉（引き上げ前）が別プロセスの子: related_cross_process", domain.FocusInfo{Foreground: tWindow(), FocusLeaf: leaf("c", 99, 5000, "Chrome_Child")}, domain.ReasonRelatedCrossProcess, 99},
		{"葉の開始時刻が違う（PID 再利用）: related_cross_process", domain.FocusInfo{Foreground: tWindow(), FocusLeaf: leaf("c", tPID, 1, "Edit")}, domain.ReasonRelatedCrossProcess, tPID},
		{"葉が不明（零値）: related_unverifiable", domain.FocusInfo{Foreground: tWindow()}, domain.ReasonRelatedUnverifiable, 0},
		{"葉の開始時刻が不明: related_unverifiable", domain.FocusInfo{Foreground: tWindow(), FocusLeaf: leaf("c", tPID, 0, "Edit")}, domain.ReasonRelatedUnverifiable, tPID},
		{"前面なし: target_not_foreground", domain.FocusInfo{}, domain.ReasonTargetNotForeground, 0},
		{"前面が T と同じ HWND の別ウィンドウ: target_replaced", domain.FocusInfo{Foreground: with(tWindow(), func(w *domain.WindowInfo) { w.PID, w.ProcessStart = 77, 7 }), FocusLeaf: selfLeaf}, domain.ReasonTargetReplaced, 77},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := g.CheckFocus(tt.f)
			if b.Reason != tt.want {
				t.Fatalf("Reason = %q, want %q (by %+v)", b.Reason, tt.want, b.By)
			}
			if tt.want != "" && tt.pid != 0 && b.By.PID != tt.pid {
				t.Errorf("By.PID = %d, want %d", b.By.PID, tt.pid)
			}
		})
	}
	if b := domain.ResolveGroup(tRef(), nil).CheckFocus(domain.FocusInfo{Foreground: tWindow(), FocusLeaf: selfLeaf}); b.Reason != domain.ReasonTargetClosed {
		t.Errorf("unresolved group: Reason = %q, want target_closed", b.Reason)
	}
}

func TestGroupCheckHit(t *testing.T) {
	// 配置: T(100,100,400x300)。その左上 (120..220, 120..200) をメンバーのダイアログ d が、右下 (400..500, 300..400) を別プロセスの w が覆う。
	dlg := win("d", tLink())
	covering := with(win("w"), func(w *domain.WindowInfo) {
		w.PID, w.ProcessStart, w.Process, w.Rect = 98, 6000, "calc.exe", domain.Rect{X: 400, Y: 300, W: 100, H: 100}
	})
	crossOwned := with(win("x", tLink()), func(w *domain.WindowInfo) {
		w.PID, w.ProcessStart, w.Process, w.Rect = 99, 5000, "common.exe", domain.Rect{X: 300, Y: 150, W: 100, H: 100}
	})
	sameOther := with(win("s"), func(w *domain.WindowInfo) { w.Rect = domain.Rect{X: 100, Y: 380, W: 50, H: 50} })
	g := domain.ResolveGroup(tRef(), []domain.WindowInfo{dlg, crossOwned, covering, sameOther, tWindow()})

	hit := func(top domain.WindowInfo) domain.HitInfo { return domain.HitInfo{Top: top, Leaf: domain.LeafOf(top)} }
	tests := []struct {
		name string
		p    domain.Point
		h    domain.HitInfo
		want domain.GroupReason
		by   uint32
	}{
		{"T の重なっていない座標は通る（非メンバーが別の場所で重なっていても）", domain.Point{X: 150, Y: 350}, hit(tWindow()), "", 0},
		{"ダイアログ上の座標は通る", domain.Point{X: 150, Y: 150}, hit(dlg), "", 0},
		{"非メンバーが覆っている座標（T の範囲内）は target_obscured", domain.Point{X: 450, Y: 350}, hit(covering), domain.ReasonTargetObscured, 98},
		{"T に重なった同一プロセスの無関係なウィンドウ上の座標は target_obscured", domain.Point{X: 120, Y: 390}, hit(sameOther), domain.ReasonTargetObscured, tPID},
		{"別プロセスの所有ウィンドウが覆う座標は related_cross_process", domain.Point{X: 350, Y: 200}, hit(crossOwned), domain.ReasonRelatedCrossProcess, 99},
		{"座標が G のどのメンバーにも入らない（他ウィンドウ上）は outside_target_group", domain.Point{X: 900, Y: 900}, hit(with(covering, func(w *domain.WindowInfo) { w.Rect = domain.Rect{X: 800, Y: 800, W: 200, H: 200} })), domain.ReasonOutsideTargetGroup, 98},
		{"座標がデスクトップ（ウィンドウなし）で G の外は outside_target_group", domain.Point{X: 900, Y: 900}, domain.HitInfo{}, domain.ReasonOutsideTargetGroup, 0},
		{"座標がデスクトップだが G の範囲内（最上位がない）は target_obscured", domain.Point{X: 150, Y: 350}, domain.HitInfo{}, domain.ReasonTargetObscured, 0},
		{"最上位がメンバーでも座標がその Rect の外は outside_target_group", domain.Point{X: 900, Y: 900}, hit(tWindow()), domain.ReasonOutsideTargetGroup, tPID},
		{"最上位がメンバー・葉が別プロセスの子（SetParent）は related_cross_process", domain.Point{X: 150, Y: 350},
			domain.HitInfo{Top: tWindow(), Leaf: leaf("c", 99, 5000, "Chrome_RenderWidgetHostHWND")}, domain.ReasonRelatedCrossProcess, 99},
		{"葉の開始時刻が違えば related_cross_process", domain.Point{X: 150, Y: 350},
			domain.HitInfo{Top: tWindow(), Leaf: leaf("c", tPID, 1, "Edit")}, domain.ReasonRelatedCrossProcess, tPID},
		{"葉を取得できなければ related_unverifiable", domain.Point{X: 150, Y: 350}, domain.HitInfo{Top: tWindow()}, domain.ReasonRelatedUnverifiable, 0},
		{"最上位が T と同じ HWND の別ウィンドウは target_replaced", domain.Point{X: 150, Y: 350},
			hit(with(tWindow(), func(w *domain.WindowInfo) { w.PID, w.ProcessStart = 77, 7 })), domain.ReasonTargetReplaced, 77},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := g.CheckHit(tt.p, tt.h)
			if b.Reason != tt.want {
				t.Fatalf("Reason = %q, want %q (by %+v)", b.Reason, tt.want, b.By)
			}
			if tt.want != "" && tt.by != 0 && b.By.PID != tt.by {
				t.Errorf("By.PID = %d, want %d", b.By.PID, tt.by)
			}
		})
	}
}

func TestGroupBlockedByCarriesProcessAndPID(t *testing.T) {
	cross := with(win("x", tLink()), func(w *domain.WindowInfo) { w.PID, w.ProcessStart, w.Process = 99, 5000, "common.exe" })
	g := domain.ResolveGroup(tRef(), []domain.WindowInfo{cross, tWindow()})
	n, ok := g.NonMember("x")
	if !ok {
		t.Fatal("cross-process window is not reported")
	}
	if got, want := n.BlockedBy(), (domain.BlockedBy{WindowID: "x", Process: "common.exe", PID: 99, Reason: domain.ReasonRelatedCrossProcess}); got != want {
		t.Errorf("BlockedBy() = %+v, want %+v", got, want)
	}
	if got := g.CrossProcess(); len(got) != 1 {
		t.Errorf("CrossProcess() = %d entries, want 1", len(got))
	}
}

func TestGroupDiff(t *testing.T) {
	d1, d2 := win("d1", tLink()), win("d2", tLink())
	prev := domain.ResolveGroup(tRef(), []domain.WindowInfo{d1, tWindow()})

	if got := domain.ResolveGroup(tRef(), []domain.WindowInfo{d1, tWindow()}).Diff(prev); !got.Empty() {
		t.Errorf("same windows: Diff = %+v, want empty", got)
	}
	added := domain.ResolveGroup(tRef(), []domain.WindowInfo{d2, d1, tWindow()}).Diff(prev)
	if !slices.Equal(added.Added, []string{"d2"}) || len(added.Removed) != 0 {
		t.Errorf("opened a dialog: Diff = %+v, want Added [d2]", added)
	}
	removed := domain.ResolveGroup(tRef(), []domain.WindowInfo{tWindow()}).Diff(prev)
	if !slices.Equal(removed.Removed, []string{"d1"}) || len(removed.Added) != 0 {
		t.Errorf("closed a dialog: Diff = %+v, want Removed [d1]", removed)
	}
	// d1 の HWND が別プロセスで再利用されたときは、メンバーでなくなる（Removed）。同一性だけ違うメンバーは Replaced。
	reused := with(d1, func(w *domain.WindowInfo) { w.PID, w.ProcessStart = 55, 9000 })
	if got := domain.ResolveGroup(tRef(), []domain.WindowInfo{reused, tWindow()}).Diff(prev); !slices.Equal(got.Removed, []string{"d1"}) {
		t.Errorf("reused HWND: Diff = %+v, want Removed [d1]", got)
	}
	renamed := with(d1, func(w *domain.WindowInfo) { w.Class = "Dlg2" })
	if got := domain.ResolveGroup(tRef(), []domain.WindowInfo{renamed, tWindow()}).Diff(prev); !slices.Equal(got.Replaced, []string{"d1"}) {
		t.Errorf("reused HWND with another class: Diff = %+v, want Replaced [d1]", got)
	}
}

func TestGroupIsMemberRejectsReusedHWND(t *testing.T) {
	d := win("d", tLink())
	g := domain.ResolveGroup(tRef(), []domain.WindowInfo{d, tWindow()})
	if !g.IsMember(d) {
		t.Fatal("dialog is not a member")
	}
	for name, f := range map[string]func(*domain.WindowInfo){
		"PID":          func(w *domain.WindowInfo) { w.PID = 55 },
		"ProcessStart": func(w *domain.WindowInfo) { w.ProcessStart = 55 },
		"Class":        func(w *domain.WindowInfo) { w.Class = "Reused" },
	} {
		if g.IsMember(with(d, f)) {
			t.Errorf("IsMember = true although %s differs (HWND reuse)", name)
		}
	}
}

func TestGroupClassifyNewWindowAfterResolve(t *testing.T) {
	g := domain.ResolveGroup(tRef(), []domain.WindowInfo{tWindow()})
	if m := g.Classify(win("late", tLink())); m.Relation != domain.RelationOwned {
		t.Errorf("Classify(new owned dialog) = %+v, want owned member", m)
	}
	if m := g.Classify(win("late")); m.Reason != domain.ReasonUnrelatedSameProcess {
		t.Errorf("Classify(new unrelated window) = %+v, want unrelated_same_process", m)
	}
}

func TestMatchesClassOfWindowInfoFields(t *testing.T) {
	// WindowInfo のゼロ値の ProcessStart・Class は「不明」で、一致として扱わない。
	if domain.RefOf(domain.WindowInfo{ID: "1", PID: 1}).Known() {
		t.Error("a window without ProcessStart and Class must not be a Known ref")
	}
}
