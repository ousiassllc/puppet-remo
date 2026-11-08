package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// 入力スコープ（#156）の停止・再開・撮影・クリップボード・キーの規則・監査。

// 非メンバーが別の場所で T に重なっていても、座標の最上位が T なら注入される。重なっている座標だけ拒否される。
func TestScopedOverlapElsewhereIsAllowedButTheOverlappedPointIsNot(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	te.windows.Add(otherWindow("2001", rectCoverT, false)) // T の右半分（x >= 300）を覆う。前面は T のまま

	if err := te.click(ctlAlice, domain.Point{X: 200, Y: 300}); err != nil {
		t.Fatalf("click on the uncovered part of T: %v", err)
	}
	if err := te.do(ctlAlice, domain.TypeText{Text: "ok"}); err != nil {
		t.Fatalf("type while T is foreground: %v", err)
	}
	if len(te.input.CallsTo("Click")) != 1 || len(te.input.CallsTo("Text")) != 1 {
		t.Fatalf("injections = %+v, want one click and one text", injections(te.ctlEnv))
	}
	before := len(injections(te.ctlEnv))

	err := te.click(ctlAlice, domain.Point{X: 400, Y: 300})
	d := te.requireHalted(err, domain.ReasonTargetObscured)
	by, _ := d["blocked_by"].(map[string]any)
	if by["process"] != "other.exe" || by["pid"] != uint32(7000) {
		t.Errorf("blocked_by = %v, want other.exe / 7000", by)
	}
	if n := len(injections(te.ctlEnv)); n != before {
		t.Errorf("the overlapped click was injected (%d → %d injections)", before, n)
	}
}

// MoveTo の後・クリックの前に非メンバーが覆っても、クリックは注入されず、押下中のものは ReleaseAll で解放される。
func TestScopedClickIsNotInjectedWhenANonMemberCoversThePointAfterMoveTo(t *testing.T) {
	te := newTgtEnv(t)
	hook := &hookInput{InputInjector: te.app}
	te.useControl(hook, te.capturer)
	te.acquire(ctlAlice)
	hook.afterMoveTo = func() { te.windows.Add(otherWindow("2001", rectCoverT, true)) }

	err := te.click(ctlAlice, domain.Point{X: 400, Y: 300})
	te.requireHalted(err, domain.ReasonTargetObscured)
	if len(te.input.CallsTo("MoveTo")) != 1 || len(te.input.CallsTo("Click")) != 0 {
		t.Errorf("injections = %+v, want only the MoveTo", injections(te.ctlEnv))
	}
}

// MoveTo の後・クリックの前にメンバー（ダイアログ）が消えても、クリックは注入されない。
func TestScopedClickIsNotInjectedWhenTheMemberDisappearsAfterMoveTo(t *testing.T) {
	te := newTgtEnv(t)
	hook := &hookInput{InputInjector: te.app}
	te.useControl(hook, te.capturer)
	te.acquire(ctlAlice)
	for _, p := range []domain.Point{ptFileButton, ptFileItem} {
		if err := te.click(ctlAlice, p); err != nil {
			t.Fatal(err)
		}
	}
	clicks := len(te.input.CallsTo("Click"))
	hook.afterMoveTo = func() { te.windows.Remove(platformtest.FakeAppDialogID) }

	err := te.click(ctlAlice, ptCancel) // ダイアログのキャンセル（T の外）
	te.requireHalted(err, domain.ReasonOutsideTargetGroup)
	if n := len(te.input.CallsTo("Click")); n != clicks {
		t.Errorf("Click calls = %d, want %d (the click after MoveTo must not be injected)", n, clicks)
	}
}

// 入力の途中（文字列の複数回の注入の間）に前面が G の外へ移ったら、続きは注入しない。
func TestScopedTypeStopsBetweenChunksWhenForegroundLeavesTheGroup(t *testing.T) {
	te := newTgtEnv(t)
	hook := &hookInput{InputInjector: te.app}
	te.useControl(hook, te.capturer)
	te.acquire(ctlAlice)
	hook.afterText = func() { te.windows.Add(otherWindow("2001", rectOutside, true)) }

	err := te.do(ctlAlice, domain.TypeText{Text: strings.Repeat("a", 3*scopedTypeChunkRunes)})
	te.requireHalted(err, domain.ReasonTargetNotForeground)
	calls := te.input.CallsTo("Text")
	if len(calls) != 1 || utf8.RuneCountInString(calls[0].Args[0].(string)) != scopedTypeChunkRunes {
		t.Errorf("Text calls = %+v, want exactly the first %d-rune chunk", calls, scopedTypeChunkRunes)
	}
}

// メンバー（ダイアログ）の HWND が別プロセスに再利用されたら、そのウィンドウは G に入らず、入力は届かない。
func TestScopedReusedMemberHWNDIsNotAMember(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	for _, p := range []domain.Point{ptFileButton, ptFileItem} {
		if err := te.click(ctlAlice, p); err != nil {
			t.Fatal(err)
		}
	}
	te.windows.Replace(platformtest.FakeAppDialogID, otherWindow("", platformtest.FakeAppLayout.Dialog, true))
	before := len(injections(te.ctlEnv))

	err := te.do(ctlAlice, keyOf("enter"))
	d := te.requireHalted(err, domain.ReasonTargetNotForeground)
	if d["window_id"] != platformtest.FakeAppDialogID {
		t.Errorf("window_id = %v, want the reused HWND %s", d["window_id"], platformtest.FakeAppDialogID)
	}
	if n := len(injections(te.ctlEnv)); n != before {
		t.Errorf("injections grew %d → %d", before, n)
	}
}

// 所有者を確かめられないメニュー（トレイのメニュー型）は G に入らず、対象の表示から除外される（通知のみ）。入力先（座標の最上位）になったら停止する。
func TestScopedUnverifiableMenuIsExcludedFromTheDisplayAndStopsWhenItBecomesTheInputTarget(t *testing.T) {
	te := newTgtEnv(t)
	te.app.OwnerlessMenu = true
	te.acquire(ctlAlice)
	if err := te.click(ctlAlice, ptFileButton); err != nil {
		t.Fatal(err)
	}
	if te.app.MenuOpen() != "file" {
		t.Fatal("the menu did not open")
	}

	st, err := te.ctl.VerifyTarget(context.Background(), ctlAlice)
	if err != nil || st.State != domain.ScopeActive {
		t.Fatalf("VerifyTarget = %+v, %v; an unverifiable window alone is not a halt", st, err)
	}
	if len(st.Notices) != 1 || st.Notices[0] != domain.NoticeCapturePartial {
		t.Errorf("Notices = %v, want [capture_partial]", st.Notices)
	}
	if len(st.Excluded) != 1 || st.Excluded[0].WindowID != platformtest.FakeAppMenuID {
		t.Errorf("Excluded = %+v, want the menu", st.Excluded)
	}
	grabs := te.capturer.CallsTo("GrabGroup")
	last, _ := grabs[len(grabs)-1].Args[0].([]domain.WindowRef)
	if len(last) != 1 || last[0].ID != platformtest.FakeAppMainID {
		t.Errorf("GrabGroup members = %+v, want T only (the unverifiable menu is not captured)", last)
	}
	if err := te.do(ctlAlice, domain.TypeText{Text: "x"}); err != nil {
		t.Errorf("typing into T while an unverifiable menu is open: %v", err)
	}

	before := len(injections(te.ctlEnv))
	err = te.click(ctlAlice, ptFileItem) // メニューが座標の最上位
	te.requireHalted(err, domain.ReasonRelatedUnverifiable)
	if n := len(injections(te.ctlEnv)); n != before {
		t.Errorf("injections grew %d → %d", before, n)
	}
}

// 撮影の失敗が停止の 2 つ目の原因。T の失敗は capture_unavailable、検証できたメンバーの失敗は capture_member_unavailable。
// どちらも注入せず、モニター撮影（Grab・Watch）に切り替えない。
func TestScopedCaptureFailureHaltsWithoutInjectionOrMonitorCapture(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*testing.T, *tgtEnv)
		reason domain.GroupReason
		window string
	}{
		{"T が撮れない", func(_ *testing.T, te *tgtEnv) { te.capturer.SetNotPrintable(platformtest.FakeAppMainID, true) },
			domain.ReasonCaptureUnavailable, platformtest.FakeAppMainID},
		{"検証できたメンバー（メニュー）が撮れない", func(t *testing.T, te *tgtEnv) {
			if err := te.click(ctlAlice, ptFileButton); err != nil {
				t.Fatal(err)
			}
			te.capturer.SetNotPrintable(platformtest.FakeAppMenuID, true)
		}, domain.ReasonCaptureMemberUnavailable, platformtest.FakeAppMenuID},
		{"GrabGroup 自体が失敗する", func(_ *testing.T, te *tgtEnv) { te.capturer.SetError("GrabGroup", errors.New("desktop gone")) },
			domain.ReasonCaptureUnavailable, platformtest.FakeAppMainID},
		{"Capturer が GroupCapturer を持たない（Windows の現状）", func(_ *testing.T, te *tgtEnv) { te.useControl(te.app, bareCapturer{te.capturer}) },
			domain.ReasonCaptureUnavailable, platformtest.FakeAppMainID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			te := newTgtEnv(t)
			te.acquire(ctlAlice)
			tt.setup(t, te)
			before := len(injections(te.ctlEnv))
			err := te.do(ctlAlice, domain.TypeText{Text: "x"})
			d := te.requireHalted(err, tt.reason)
			if d["window_id"] != tt.window {
				t.Errorf("window_id = %v, want %q", d["window_id"], tt.window)
			}
			if n := len(injections(te.ctlEnv)); n != before {
				t.Errorf("injections grew %d → %d, want none", before, n)
			}
			te.capturer.AssertNoMonitorCapture(t)
		})
	}
}

// 入力なしの事前確認（VerifyTarget）。撮れなくなったら、入力のときと同じく halted になる。
func TestVerifyTargetHaltsWhenTheTargetCannotBeCaptured(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	st, err := te.ctl.VerifyTarget(context.Background(), ctlAlice)
	if err != nil || st.State != domain.ScopeActive || st.Members != 1 {
		t.Fatalf("VerifyTarget = %+v, %v; want active with 1 member", st, err)
	}
	te.capturer.SetNotPrintable(platformtest.FakeAppMainID, true)
	st, err = te.ctl.VerifyTarget(context.Background(), ctlAlice)
	te.requireHalted(err, domain.ReasonCaptureUnavailable)
	if st.State != domain.ScopeHalted || st.HaltReason != domain.ReasonCaptureUnavailable {
		t.Errorf("status = %+v, want halted / capture_unavailable", st)
	}
	te.capturer.AssertNoMonitorCapture(t)
	if st, err := te.ctl.VerifyTarget(context.Background(), ctlAlice); err != nil || st.State != domain.ScopeHalted {
		t.Errorf("VerifyTarget of a halted lease = %+v, %v; want the halted state without re-verifying or resuming", st, err)
	}
	if _, err := te.ctl.VerifyTarget(context.Background(), ctlBob); err == nil {
		t.Error("VerifyTarget by a non-holder must fail")
	}
}

// halted は Esc を含む一切の入力・クリップボード書き込みを拒否する。ClipboardRead は常に clipboard_out_of_scope。
func TestScopedHaltedRefusesEverythingIncludingEscAndClipboardWrite(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	te.windows.Add(otherWindow("2001", rectOutside, true))
	te.requireHalted(te.do(ctlAlice, keyOf("a")), domain.ReasonTargetNotForeground)
	te.windows.Remove("2001")
	te.windows.Update(platformtest.FakeAppMainID, func(w *domain.WindowInfo) { w.Foreground = true }) // 原因の解消（ホスト側）

	ops := []struct {
		name string
		op   domain.Op
	}{
		{"escape", keyOf("escape")},
		{"ctrl+c", keyOf("ctrl+c")},
		{"enter", keyOf("enter")},
		{"click", clickAt(domain.ButtonLeft, ptInT)},
		{"type", domain.TypeText{Text: "x"}},
		{"KeyEvent", domain.KeyEvent{Action: domain.KeyDown, Code: "Escape", Key: "Escape"}},
		{"PointerMove", domain.PointerMove{At: ptInT, Monitor: domain.MonitorPrimary}},
		{"MouseButton", domain.MouseButton{At: ptInT, Button: domain.ButtonLeft, Action: domain.ButtonActionDown, Monitor: domain.MonitorPrimary}},
		{"Wheel", domain.Wheel{At: ptInT, DY: 1, Monitor: domain.MonitorPrimary}},
		{"FocusWindow", domain.FocusWindow{ID: platformtest.FakeAppMainID}},
		{"ClipboardWrite", domain.ClipboardWrite{Text: "x"}},
	}
	for _, o := range ops {
		err := te.do(ctlAlice, o.op)
		d := requireBlockedReason(t, err, domain.ReasonTargetHalted)
		if d["halt_reason"] != string(domain.ReasonTargetNotForeground) {
			t.Errorf("%s: halt_reason = %v, want target_not_foreground", o.name, d["halt_reason"])
		}
	}
	if len(injections(te.ctlEnv)) != 0 {
		t.Errorf("injections = %+v, want none", injections(te.ctlEnv))
	}
	if n := len(te.clipboard.CallsTo("WriteText")); n != 0 {
		t.Errorf("WriteText calls = %d, want 0 while halted", n)
	}
	if n := len(te.windows.CallsTo("Focus")); n != 0 {
		t.Errorf("Focus calls = %d, want 0", n)
	}
	requireBlockedReason(t, te.do(ctlAlice, domain.ClipboardRead{}), domain.ReasonClipboardOutOfScope)
	if n := len(te.clipboard.CallsTo("ReadText")); n != 0 {
		t.Errorf("ReadText calls = %d, want 0", n)
	}
	if n := len(te.entries("input.target_halted")); n != 1 {
		t.Errorf("input.target_halted entries = %d, want 1 (refusals while halted are not new halts)", n)
	}
}

// 原因の解消だけでは再開しない。
func TestScopedHaltDoesNotResumeWhenTheCauseGoesAway(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	te.windows.Add(otherWindow("2001", rectOutside, true))
	te.requireHalted(te.do(ctlAlice, keyOf("a")), domain.ReasonTargetNotForeground)

	te.windows.Remove("2001") // 関連しないウィンドウをホスト側で閉じた
	te.windows.Update(platformtest.FakeAppMainID, func(w *domain.WindowInfo) { w.Foreground = true })
	requireBlockedReason(t, te.do(ctlAlice, keyOf("a")), domain.ReasonTargetHalted)
	if s := te.scope(); !s.Halted() {
		t.Fatalf("scope = %+v, want it to stay halted", s)
	}
	if n := len(injections(te.ctlEnv)); n != 0 {
		t.Errorf("injections = %d, want 0", n)
	}
	if n := len(te.entries("lease.target_resumed")); n != 0 {
		t.Errorf("lease.target_resumed entries = %d, want 0", n)
	}
}

// 明示的な再開は全条件を再検証し、通ったときだけ active に戻す。一つでも通らなければ halted のまま。自動では前面にしない。
func TestResumeTargetReverifiesAllConditions(t *testing.T) {
	halt := func(t *testing.T) *tgtEnv {
		te := newTgtEnv(t)
		te.acquire(ctlAlice)
		te.windows.Add(otherWindow("2001", rectOutside, true))
		te.requireHalted(te.do(ctlAlice, keyOf("a")), domain.ReasonTargetNotForeground)
		return te
	}
	restore := func(te *tgtEnv) {
		te.windows.Remove("2001")
		te.windows.Update(platformtest.FakeAppMainID, func(w *domain.WindowInfo) { w.Foreground = true })
	}

	t.Run("全条件が通れば active に戻り、入力できる", func(t *testing.T) {
		te := halt(t)
		restore(te)
		st, err := te.ctl.ResumeTarget(context.Background(), ctlAlice)
		if err != nil || st.State != domain.ScopeActive || st.HaltReason != "" {
			t.Fatalf("ResumeTarget = %+v, %v", st, err)
		}
		if s := te.scope(); s.State != domain.ScopeActive || s.HaltReason != "" {
			t.Errorf("scope = %+v, want active", s)
		}
		if err := te.do(ctlAlice, keyOf("a")); err != nil {
			t.Errorf("input after the resume: %v", err)
		}
		if n := len(te.windows.CallsTo("Focus")); n != 0 {
			t.Errorf("Focus calls = %d, want 0 (never auto-focus)", n)
		}
		if got := te.entries("lease.target_resumed"); len(got) != 1 || got[0].SessionID != ctlAlice.SessionID {
			t.Errorf("lease.target_resumed entries = %+v, want 1 by the holder", got)
		}
		te.capturer.AssertNoMonitorCapture(t)
	})

	t.Run("非メンバーが前面のままなら halted のまま", func(t *testing.T) {
		te := halt(t)
		st, err := te.ctl.ResumeTarget(context.Background(), ctlAlice)
		requireBlockedReason(t, err, domain.ReasonTargetNotForeground)
		if st.State != domain.ScopeHalted || !te.scope().Halted() {
			t.Errorf("status = %+v, scope = %+v; want halted", st, te.scope())
		}
		requireBlockedReason(t, te.do(ctlAlice, keyOf("a")), domain.ReasonTargetHalted)
	})

	t.Run("T が撮れなければ halted のまま。撮れるようになれば再開できる", func(t *testing.T) {
		te := halt(t)
		restore(te)
		te.capturer.SetNotPrintable(platformtest.FakeAppMainID, true)
		_, err := te.ctl.ResumeTarget(context.Background(), ctlAlice)
		requireBlockedReason(t, err, domain.ReasonCaptureUnavailable)
		if !te.scope().Halted() {
			t.Fatal("must stay halted")
		}
		te.capturer.SetNotPrintable(platformtest.FakeAppMainID, false)
		if st, err := te.ctl.ResumeTarget(context.Background(), ctlAlice); err != nil || st.State != domain.ScopeActive {
			t.Fatalf("ResumeTarget = %+v, %v", st, err)
		}
		te.capturer.AssertNoMonitorCapture(t)
	})

	t.Run("保持者でなければ再開できない", func(t *testing.T) {
		te := halt(t)
		restore(te)
		if _, err := te.ctl.ResumeTarget(context.Background(), ctlBob); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("ResumeTarget by another session = %v, want ErrLeaseHeldByOther", err)
		}
		if !te.scope().Halted() {
			t.Error("must stay halted")
		}
	})

	t.Run("active のリースの再開は何もしない", func(t *testing.T) {
		te := newTgtEnv(t)
		te.acquire(ctlAlice)
		if st, err := te.ctl.ResumeTarget(context.Background(), ctlAlice); err != nil || st.State != domain.ScopeActive {
			t.Errorf("ResumeTarget = %+v, %v", st, err)
		}
		if n := len(te.entries("lease.target_resumed")); n != 0 {
			t.Errorf("lease.target_resumed entries = %d, want 0", n)
		}
	})

	t.Run("スコープなしのリースは ErrInvalidArgument", func(t *testing.T) {
		te := newTgtEnv(t)
		mustAcquire(t, te.ctlEnv, ctlAlice)
		if _, err := te.ctl.ResumeTarget(context.Background(), ctlAlice); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("ResumeTarget = %v, want ErrInvalidArgument", err)
		}
	})
}

// target_closed / target_replaced は再開できず、選び直しのみ。同じウィンドウが戻っても再開しない。
func TestResumeTargetRefusesClosedAndReplacedTargets(t *testing.T) {
	tests := []struct {
		name   string
		break_ func(*tgtEnv)
		reason domain.GroupReason
	}{
		{"closed", func(te *tgtEnv) { te.windows.Remove(platformtest.FakeAppMainID) }, domain.ReasonTargetClosed},
		{"replaced", func(te *tgtEnv) {
			te.windows.Replace(platformtest.FakeAppMainID, otherWindow("", platformtest.FakeAppLayout.Main, true))
		}, domain.ReasonTargetReplaced},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			te := newTgtEnv(t)
			te.acquire(ctlAlice)
			tt.break_(te)
			te.requireHalted(te.do(ctlAlice, keyOf("a")), tt.reason)

			te.windows.Add(te.app.MainWindow()) // 同じ同一性の T が戻る
			st, err := te.ctl.ResumeTarget(context.Background(), ctlAlice)
			requireBlockedReason(t, err, tt.reason)
			if st.State != domain.ScopeHalted || st.HaltReason != tt.reason || !te.scope().Halted() {
				t.Errorf("status = %+v, scope = %+v; want halted with %q", st, te.scope(), tt.reason)
			}
			requireBlockedReason(t, te.do(ctlAlice, keyOf("a")), domain.ReasonTargetHalted)

			// 解放して選び直せば、新しいリースで入力できる。
			te.leases.Release(ctlAlice)
			te.acquire(ctlAlice)
			if err := te.do(ctlAlice, keyOf("a")); err != nil {
				t.Errorf("input after re-selecting: %v", err)
			}
		})
	}
}

// クリップボード（D7）はサーバー側の規則。スコープ付きリースの ClipboardRead は、どの入口の保持者種別でも拒否され、platform の読み取りは呼ばれない。
func TestScopedClipboardReadIsRefusedForEveryActorKindWithoutCallingThePlatform(t *testing.T) {
	agent := domain.Actor{Kind: domain.ActorAgent, Label: "agent", ClientID: "client-a", SessionID: "task-1"}
	for _, who := range []domain.Actor{ctlAlice, ctlViewer, agent} {
		t.Run(string(who.Kind), func(t *testing.T) {
			te := newTgtEnv(t)
			te.clipboard.SetText("secret")
			te.acquire(who)
			err := te.do(who, domain.ClipboardRead{})
			te.requireRefused(err, domain.ReasonClipboardOutOfScope)
			if n := len(te.clipboard.CallsTo("ReadText")); n != 0 {
				t.Errorf("ReadText calls = %d, want 0", n)
			}
		})
	}
}

func TestScopedClipboardWriteIsAllowed(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlViewer)
	if err := te.do(ctlViewer, domain.ClipboardWrite{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	calls := te.clipboard.CallsTo("WriteText")
	if len(calls) != 1 || calls[0].Args[0] != "hello" {
		t.Errorf("WriteText calls = %+v", calls)
	}
	if te.scope().State != domain.ScopeActive {
		t.Error("the scope must stay active")
	}
}

func TestUnscopedClipboardReadIsUnchanged(t *testing.T) {
	te := newTgtEnv(t)
	te.clipboard.SetText("plain")
	res, err := te.ctl.Do(context.Background(), ctlAlice, domain.ClipboardRead{})
	if err != nil || res.Text != "plain" {
		t.Fatalf("Do = %+v, %v; want the clipboard text", res, err)
	}
	if n := len(te.clipboard.CallsTo("ReadText")); n != 1 {
		t.Errorf("ReadText calls = %d, want 1", n)
	}
}

// スコープ時、win 修飾・Alt+Tab 等は key_out_of_scope で拒否される（その操作だけの拒否。停止にはしない）。
func TestScopedForbiddenKeysAreRefusedWithoutHalting(t *testing.T) {
	forbidden := []struct {
		name string
		op   domain.Op
	}{
		{"win+r", keyOf("win+r")},
		{"alt+tab", keyOf("alt+tab")},
		{"alt+escape", keyOf("alt+escape")},
		{"ctrl+escape", keyOf("ctrl+escape")},
		{"ctrl+shift+escape", keyOf("ctrl+shift+escape")},
		{"KeyEvent Alt+Tab", domain.KeyEvent{Action: domain.KeyDown, Code: "Tab", Key: "Tab", Mods: []domain.Modifier{domain.ModAlt}}},
		{"KeyEvent MetaLeft", domain.KeyEvent{Action: domain.KeyDown, Code: "MetaLeft", Key: "Meta"}},
		{"click with win modifier", domain.Click{At: &domain.Point{X: 147, Y: 147}, Button: domain.ButtonLeft, Count: 1, Mods: []domain.Modifier{domain.ModWin}, Monitor: domain.MonitorPrimary}},
	}
	for _, f := range forbidden {
		t.Run(f.name, func(t *testing.T) {
			te := newTgtEnv(t)
			te.acquire(ctlAlice)
			te.requireRefused(te.do(ctlAlice, f.op), domain.ReasonKeyOutOfScope)
			if n := len(injections(te.ctlEnv)); n != 0 {
				t.Errorf("injections = %+v, want none", injections(te.ctlEnv))
			}
			if err := te.do(ctlAlice, keyOf("ctrl+c")); err != nil { // 拒否のあとも、通常の入力は入る
				t.Errorf("ctrl+c after the refusal: %v", err)
			}
		})
	}

	t.Run("許される組み合わせ", func(t *testing.T) {
		te := newTgtEnv(t)
		te.acquire(ctlAlice)
		for _, spec := range []string{"ctrl+c", "escape", "alt+f4", "ctrl+tab", "shift+escape"} {
			if err := te.do(ctlAlice, keyOf(spec)); err != nil {
				t.Errorf("%s: %v", spec, err)
			}
		}
	})

	t.Run("スコープなしは従来どおり（Alt+Tab・Win+R が届き、グループの検証も撮影もしない）", func(t *testing.T) {
		te := newTgtEnv(t)
		te.windows.Add(otherWindow("2001", rectOutside, true)) // 前面が別のウィンドウでも、スコープなしは見ない
		for _, spec := range []string{"alt+tab", "win+r"} {
			if err := te.do(ctlAlice, keyOf(spec)); err != nil {
				t.Errorf("%s: %v", spec, err)
			}
		}
		if n := len(te.input.CallsTo("Key")); n != 2 {
			t.Errorf("Key calls = %d, want 2", n)
		}
		if n := len(te.windows.CallsTo("ListAll")); n != 0 {
			t.Errorf("ListAll calls = %d, want 0 (no group verification without a scope)", n)
		}
		if te.capturer.GroupGrabCalls() != 0 {
			t.Error("GrabGroup was called without a scope")
		}
		if h, _ := te.leases.Holder(); h.Scope != nil {
			t.Errorf("Scope = %+v, want nil", h.Scope)
		}
	})
}

// FocusWindow はメンバーだけ。メンバー以外はその操作だけの拒否で、停止にはしない。
func TestScopedFocusWindowIsOnlyForMembers(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	te.windows.Add(otherWindow("2001", rectOutside, false))
	for _, p := range []domain.Point{ptFileButton, ptFileItem} {
		if err := te.click(ctlAlice, p); err != nil {
			t.Fatal(err)
		}
	}

	te.requireRefused(te.do(ctlAlice, domain.FocusWindow{ID: "2001"}), domain.ReasonOutsideTargetGroup)
	if n := len(te.windows.CallsTo("Focus")); n != 0 {
		t.Fatalf("Focus calls = %d, want 0 for a non-member", n)
	}
	if err := te.do(ctlAlice, domain.FocusWindow{ID: platformtest.FakeAppMainID}); err != nil {
		t.Errorf("FocusWindow on T: %v", err)
	}
	if err := te.do(ctlAlice, domain.FocusWindow{ID: platformtest.FakeAppDialogID}); err != nil {
		t.Errorf("FocusWindow on a member dialog: %v", err)
	}
	if n := len(te.windows.CallsTo("Focus")); n != 2 {
		t.Errorf("Focus calls = %d, want 2", n)
	}
}

// スコープ付きリースの間、他の保持者は lease_held_by_other。
func TestScopedLeaseIsHeldAgainstOthers(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	if err := te.do(ctlBob, keyOf("a")); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Errorf("Do by another holder = %v, want ErrLeaseHeldByOther", err)
	}
	if _, err := te.leases.AcquireScoped(context.Background(), ctlBob, te.app.Ref()); !errors.Is(err, domain.ErrLeaseHeldByOther) {
		t.Errorf("AcquireScoped by another holder = %v, want ErrLeaseHeldByOther", err)
	}
	if n := len(injections(te.ctlEnv)); n != 0 {
		t.Errorf("injections = %d, want 0", n)
	}
}

// 監査: lease.target_set と input.target_halted は reason・window_id・process・pid・メンバー数を持ち、ウィンドウのタイトル・入力した文字は残らない。
func TestScopeAuditEventsCarryNoTitlesOrTypedText(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	for _, p := range []domain.Point{ptFileButton, ptFileItem} {
		if err := te.click(ctlAlice, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := te.do(ctlAlice, domain.TypeText{Text: "typed-secret-123"}); err != nil {
		t.Fatal(err)
	}
	te.windows.Add(otherWindow("2001", rectOutside, true))
	te.requireHalted(te.do(ctlAlice, domain.TypeText{Text: "another-secret-456"}), domain.ReasonTargetNotForeground)

	set := te.entries("lease.target_set")
	if len(set) != 1 || set[0].Kind != "lease" || set[0].LeaseID == "" {
		t.Fatalf("lease.target_set entries = %+v", set)
	}
	d := detailOf(t, set[0])
	if d["window_id"] != platformtest.FakeAppMainID || d["process"] != "fakenotepad.exe" || d["pid"] != float64(platformtest.FakeAppPID) || d["members"] != float64(1) {
		t.Errorf("lease.target_set detail = %v", d)
	}
	halted := te.entries("input.target_halted")[0]
	if halted.Kind != "input" || halted.Result != auditDenied || halted.ErrorCode != string(domain.CodeInputBlocked) {
		t.Errorf("input.target_halted entry = %+v", halted)
	}
	d = detailOf(t, halted)
	if d["reason"] != string(domain.ReasonTargetNotForeground) || d["window_id"] != "2001" || d["process"] != "other.exe" || d["pid"] != float64(7000) || d["members"] == nil {
		t.Errorf("input.target_halted detail = %v", d)
	}

	all, _ := json.Marshal(te.audit.Entries())
	for _, secret := range []string{"無題 - FakeNotepad", "名前を付けて保存", "other-title-xyz", "typed-secret-123", "another-secret-456"} {
		if strings.Contains(string(all), secret) {
			t.Errorf("the audit log contains %q", secret)
		}
	}
}
