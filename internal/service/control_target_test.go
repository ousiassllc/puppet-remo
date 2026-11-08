package service

import (
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// 入力スコープ（#156）の入力の検証。拒否はいずれも InputInjector への注入が 0 件、許可は期待の呼び出しが届くことを確かめる。
// 偽アプリ（FakeApp）による S1〜S3 の偽 platform 版は、実機の成立性ゲート（検証待ち）の代わりにならない。

// 検証に通るメンバーへの入力は注入される。メニュー・ダイアログの開閉で G が増減しても halted にならず、次の入力は新しい G で検証される（S1 の偽 platform 版）。
func TestScopedFileMenuToSaveDialogFlowIsInjectedAndNeverHalts(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)

	steps := []struct {
		name string
		do   func() error
		want func() bool
	}{
		{"「ファイル」をクリックするとメニューが出る", func() error { return te.click(ctlAlice, ptFileButton) }, func() bool { return te.app.MenuOpen() == "file" }},
		{"メニューの項目（メンバーの最上位）をクリックすると保存ダイアログが開く", func() error { return te.click(ctlAlice, ptFileItem) }, func() bool { return te.app.DialogOpen() && te.app.MenuOpen() == "" }},
		{"ダイアログが前面のとき、文字はダイアログに入る（8 文字ごとの検証をまたぐ長さ）", func() error { return te.do(ctlAlice, domain.TypeText{Text: "report-2026.txt"}) }, func() bool { return true }},
		{"Enter で保存して閉じる", func() error { return te.do(ctlAlice, keyOf("enter")) }, func() bool { return !te.app.DialogOpen() }},
		{"ダイアログが閉じたあと、T への入力は新しい G で検証されて入る", func() error { return te.do(ctlAlice, domain.TypeText{Text: "hello"}) }, func() bool { return te.app.Document() == "hello" }},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if !s.want() {
			t.Fatalf("%s: the app did not react", s.name)
		}
		if sc := te.scope(); sc.State != domain.ScopeActive {
			t.Fatalf("%s: scope = %+v, want active (members coming and going is not a halt)", s.name, sc)
		}
	}
	if got := te.app.Saved(); len(got) != 1 || got[0] != "report-2026.txt" {
		t.Errorf("Saved = %q, want [report-2026.txt]", got)
	}
	if te.capturer.GroupGrabCalls() == 0 {
		t.Error("the capture precheck did not run (GrabGroup was never called)")
	}
	te.capturer.AssertNoMonitorCapture(t)
	if n := len(te.entries("input.target_halted")); n != 0 {
		t.Errorf("input.target_halted entries = %d, want 0", n)
	}
}

// 右クリックメニューの項目（S2 の偽 platform 版）。ClipboardWrite は許可され、貼り付けに使える。
func TestScopedContextMenuSelectAllAndPasteAreInjected(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	if err := te.do(ctlAlice, domain.ClipboardWrite{Text: "pasted"}); err != nil {
		t.Fatalf("ClipboardWrite: %v", err)
	}
	for _, step := range []struct {
		b domain.Button
		p domain.Point
	}{{domain.ButtonRight, ptContext}, {domain.ButtonLeft, ptSelectAll}, {domain.ButtonRight, ptContext}, {domain.ButtonLeft, ptPaste}} {
		if err := te.do(ctlAlice, clickAt(step.b, step.p)); err != nil {
			t.Fatalf("click %v at %v: %v", step.b, step.p, err)
		}
	}
	if !te.app.Selected() || te.app.Document() != "pasted" {
		t.Errorf("Selected = %v, Document = %q; want select-all and the pasted text", te.app.Selected(), te.app.Document())
	}
	if te.scope().State != domain.ScopeActive {
		t.Error("the scope must stay active")
	}
	te.capturer.AssertNoMonitorCapture(t)
}

// 別プロセスのダイアログ（共通ダイアログ型）が前面になったら、入力は注入されず related_cross_process で停止する。
func TestScopedCrossProcessDialogStopsInput(t *testing.T) {
	te := newTgtEnv(t)
	te.app.DialogCrossProcess = true
	te.acquire(ctlAlice)
	for _, p := range []domain.Point{ptFileButton, ptFileItem} {
		if err := te.click(ctlAlice, p); err != nil {
			t.Fatal(err)
		}
	}
	if !te.app.DialogOpen() {
		t.Fatal("the dialog did not open")
	}
	before := len(injections(te.ctlEnv))
	err := te.do(ctlAlice, domain.TypeText{Text: "x"})
	d := te.requireHalted(err, domain.ReasonRelatedCrossProcess)
	by, _ := d["blocked_by"].(map[string]any)
	if by["pid"] != platformtest.FakeAppCommonDialogPID || by["process"] != "explorer.exe" || by["reason"] != string(domain.ReasonRelatedCrossProcess) {
		t.Errorf("blocked_by = %v, want the cross-process dialog's process and pid", by)
	}
	if n := len(injections(te.ctlEnv)); n != before {
		t.Errorf("injections grew from %d to %d, want 0 new", before, n)
	}
}

// 検証に通らない入力は、注入 0・ReleaseAll・halted・input.target_halted（理由つき）。
func TestScopedInputOutsideTheGroupIsNotInjectedAndHalts(t *testing.T) {
	someLeaf := domain.LeafRef{WindowRef: domain.WindowRef{ID: "9100", PID: crossPID, ProcessStart: crossStart, Class: "Child"}, Process: "embed.exe"}
	tests := []struct {
		name      string
		setup     func(*tgtEnv)
		op        domain.Op
		reason    domain.GroupReason
		wantPID   uint32 // 0 は確かめない
		wantProc  string
		wantByWin string
	}{
		{"key: 前面が別のウィンドウ", func(te *tgtEnv) { te.windows.Add(otherWindow("2001", rectOutside, true)) },
			keyOf("enter"), domain.ReasonTargetNotForeground, 7000, "other.exe", "2001"},
		{"type: 前面が同一プロセスの所有関係のないウィンドウ（PID 一致だけで許可しない）", func(te *tgtEnv) { te.windows.Add(sameProcessWindow("2002", rectOutside, true)) },
			domain.TypeText{Text: "x"}, domain.ReasonUnrelatedSameProcess, platformtest.FakeAppPID, "fakenotepad.exe", "2002"},
		{"KeyEvent: 前面が T に所有された別プロセスの共通ダイアログ", func(te *tgtEnv) {
			te.windows.Add(ownedWindow("2003", domain.Rect{X: 200, Y: 200, W: 100, H: 100}, crossPID, crossStart, true))
		}, domain.KeyEvent{Action: domain.KeyDown, Code: "KeyA", Key: "a"}, domain.ReasonRelatedCrossProcess, crossPID, "", "2003"},
		{"type: フォーカスの葉が別プロセスの子（最上位は T）", func(te *tgtEnv) { te.windows.SetFocusLeaf(platformtest.FakeAppMainID, someLeaf) },
			domain.TypeText{Text: "x"}, domain.ReasonRelatedCrossProcess, crossPID, "embed.exe", "9100"},
		{"key: フォーカスの葉を取れない", func(te *tgtEnv) { te.windows.SetFocusLeaf(platformtest.FakeAppMainID, domain.LeafRef{}) },
			keyOf("enter"), domain.ReasonRelatedUnverifiable, 0, "", ""},
		{"key: 前面が所有者を確かめられないメニュー", func(te *tgtEnv) {
			te.windows.Add(domain.WindowInfo{ID: "2004", Process: "fakenotepad.exe", PID: platformtest.FakeAppPID, ProcessStart: platformtest.FakeAppStart,
				Class: domain.ClassMenu, Rect: rectOutside, Foreground: true})
		}, keyOf("enter"), domain.ReasonRelatedUnverifiable, platformtest.FakeAppPID, "", "2004"},
		{"key: 前面が自ウィンドウ", func(te *tgtEnv) {
			te.windows.Add(domain.WindowInfo{ID: "2005", Own: true, Rect: rectOutside, Foreground: true})
		}, keyOf("enter"), domain.ReasonOwnWindow, 0, "", "2005"},
		{"click: 座標が非メンバーのウィンドウの上", func(te *tgtEnv) { te.windows.Add(otherWindow("2001", rectOutside, false)) },
			clickAt(domain.ButtonLeft, ptOutside), domain.ReasonOutsideTargetGroup, 7000, "other.exe", "2001"},
		{"click: 座標がどのウィンドウにもない", func(*tgtEnv) {}, clickAt(domain.ButtonLeft, ptDesktop), domain.ReasonOutsideTargetGroup, 0, "", ""},
		{"drag: 片端だけ外", func(te *tgtEnv) { te.windows.Add(otherWindow("2001", rectOutside, false)) },
			domain.Drag{From: scaled(ptInT), To: scaled(ptOutside), Monitor: domain.MonitorPrimary}, domain.ReasonOutsideTargetGroup, 7000, "other.exe", "2001"},
		{"scroll: 座標が非メンバーの上", func(te *tgtEnv) { te.windows.Add(otherWindow("2001", rectOutside, false)) },
			domain.Scroll{At: scaled(ptOutside), Dir: domain.DirDown, Amount: 3, Monitor: domain.MonitorPrimary}, domain.ReasonOutsideTargetGroup, 7000, "other.exe", "2001"},
		{"click: T に重なった非メンバーが座標の最上位（target_obscured）", func(te *tgtEnv) { te.windows.Add(otherWindow("2001", rectCoverT, false)) },
			clickAt(domain.ButtonLeft, domain.Point{X: 400, Y: 300}), domain.ReasonTargetObscured, 7000, "other.exe", "2001"},
		{"click: 同一プロセスの無関係なウィンドウが T の上", func(te *tgtEnv) { te.windows.Add(sameProcessWindow("2002", rectCoverT, false)) },
			clickAt(domain.ButtonLeft, domain.Point{X: 400, Y: 300}), domain.ReasonTargetObscured, platformtest.FakeAppPID, "fakenotepad.exe", "2002"},
		{"click: 葉が別プロセスの子ウィンドウ（SetParent で T の下）", func(te *tgtEnv) {
			te.windows.AddChild(platformtest.FakeAppMainID, platformtest.FakeChild{Rect: domain.Rect{X: 350, Y: 300, W: 80, H: 80}, Leaf: someLeaf})
		}, clickAt(domain.ButtonLeft, domain.Point{X: 380, Y: 330}), domain.ReasonRelatedCrossProcess, crossPID, "embed.exe", "9100"},
		{"click: T に所有された昇格ウィンドウの上", func(te *tgtEnv) {
			w := ownedWindow("2006", domain.Rect{X: 200, Y: 200, W: 100, H: 100}, platformtest.FakeAppPID, platformtest.FakeAppStart, false)
			w.Elevated = true
			te.windows.Add(w)
		}, clickAt(domain.ButtonLeft, domain.Point{X: 250, Y: 250}), domain.ReasonElevatedWindow, platformtest.FakeAppPID, "dialog.exe", "2006"},
		{"click: T に所有された自ウィンドウの上", func(te *tgtEnv) {
			w := ownedWindow("2007", domain.Rect{X: 200, Y: 200, W: 100, H: 100}, platformtest.FakeAppPID, platformtest.FakeAppStart, false)
			w.Own = true
			te.windows.Add(w)
		}, clickAt(domain.ButtonLeft, domain.Point{X: 250, Y: 250}), domain.ReasonOwnWindow, 0, "", "2007"},
		{"key: T が最小化された", func(te *tgtEnv) {
			te.windows.Update(platformtest.FakeAppMainID, func(w *domain.WindowInfo) { w.Minimized = true })
		}, keyOf("enter"), domain.ReasonTargetMinimized, 0, "", ""},
		{"key: T が昇格した", func(te *tgtEnv) {
			te.windows.Update(platformtest.FakeAppMainID, func(w *domain.WindowInfo) { w.Elevated = true })
		}, keyOf("enter"), domain.ReasonElevatedWindow, 0, "", ""},
		{"click: T が閉じられた", func(te *tgtEnv) { te.windows.Remove(platformtest.FakeAppMainID) },
			clickAt(domain.ButtonLeft, ptInT), domain.ReasonTargetClosed, 0, "", ""},
		{"type: 同じ HWND を別プロセスが再利用した（T）", func(te *tgtEnv) {
			te.windows.Replace(platformtest.FakeAppMainID, otherWindow("", platformtest.FakeAppLayout.Main, true))
		}, domain.TypeText{Text: "x"}, domain.ReasonTargetReplaced, 0, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			te := newTgtEnv(t)
			te.acquire(ctlAlice)
			tt.setup(te)
			err := te.do(ctlAlice, tt.op)
			d := te.requireHalted(err, tt.reason)
			if n := len(injections(te.ctlEnv)); n != 0 {
				t.Errorf("injections = %+v, want none", injections(te.ctlEnv))
			}
			by, _ := d["blocked_by"].(map[string]any)
			if tt.wantPID != 0 && by["pid"] != tt.wantPID {
				t.Errorf("blocked_by.pid = %v, want %d (detail %v)", by["pid"], tt.wantPID, d)
			}
			if tt.wantProc != "" && by["process"] != tt.wantProc {
				t.Errorf("blocked_by.process = %v, want %q", by["process"], tt.wantProc)
			}
			if tt.wantByWin != "" && d["window_id"] != tt.wantByWin {
				t.Errorf("window_id = %v, want %q", d["window_id"], tt.wantByWin)
			}
			te.capturer.AssertNoMonitorCapture(t)
		})
	}
}
