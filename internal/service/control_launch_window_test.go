package service

import (
	"context"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 起動後のウィンドウの観測（FR-M19、Issue #46）。待ちは偽の Sleep（e.slept に回数が残る）で行い、実時間は待たない。

const userNotepadPID = 500

// existingNotepad はユーザーが書きかけの文書を開いている、既存の前面ウィンドウ（起動したプロセスとは別の pid）。
func existingNotepad() domain.WindowInfo {
	return domain.WindowInfo{ID: "77", Title: "無題 - メモ帳", Process: "notepad.exe", PID: userNotepadPID, Foreground: true}
}

func launchNotepad(t *testing.T, e *ctlEnv) LaunchResult {
	t.Helper()
	app := registerNotepad(t, e)
	res, err := e.control.Launch(context.Background(), ctlAlice, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// 返した pid の新しいウィンドウが前面に出たなら、再利用の印は付かず、そのウィンドウを返す。待たずに返る。
func TestControlLaunchReportsTheNewForegroundWindow(t *testing.T) {
	e := newAppEnv(t)
	old := existingNotepad()
	e.windows.SetWindows(old)
	e.launcher.OnLaunch(func(pid uint32) {
		old.Foreground = false
		e.windows.SetWindows(domain.WindowInfo{ID: "88", Title: "無題 - メモ帳", Process: "notepad.exe", PID: pid, Foreground: true}, old)
	})

	res := launchNotepad(t, e)
	if res.ReusedExistingWindow {
		t.Error("ReusedExistingWindow = true, want false: a window of the launched pid is in the foreground")
	}
	if res.Window == nil || res.Window.ID != "88" || res.Window.Title != "無題 - メモ帳" || res.Window.PID != res.PID {
		t.Errorf("Window = %+v, want window 88 of pid %d", res.Window, res.PID)
	}
	if len(e.slept) != 0 {
		t.Errorf("slept %v, want no wait once the window is in the foreground", e.slept)
	}
	if d := auditDetail(t, lastLaunchEntry(t, e)); d["reused_existing_window"] != false {
		t.Errorf("detail = %v, want reused_existing_window=false", d)
	}
}

// 返した pid のウィンドウが無く、既存のウィンドウ（別の pid）が前面にあるだけなら、印を付け、その別のウィンドウは返さない（#85）。
// 返してしまうと、続けて送るキー・入力が既存のウィンドウ（書きかけの文書など）や別のアプリに入る。観測は上限の回数で打ち切り、前面は触らない。
func TestControlLaunchFlagsAnExistingWindowBroughtForward(t *testing.T) {
	e := newAppEnv(t)
	e.windows.SetWindows(existingNotepad())

	res := launchNotepad(t, e)
	if !res.ReusedExistingWindow {
		t.Error("ReusedExistingWindow = false, want true: only a window of another pid is in the foreground")
	}
	if res.Window != nil {
		t.Errorf("Window = %+v, want none: the foreground window is not the launched app's window", res.Window)
	}
	if fg, err := e.windows.Foreground(); err != nil || fg.ID != "77" {
		t.Errorf("foreground = %+v (%v), want the existing window 77 untouched", fg, err)
	}
	if len(e.slept) != launchWindowPollAttempts-1 {
		t.Errorf("waited %d times, want %d", len(e.slept), launchWindowPollAttempts-1)
	}
	for _, d := range e.slept {
		if d != launchWindowPollInterval {
			t.Errorf("waited %v, want %v", d, launchWindowPollInterval)
		}
	}
	if d := auditDetail(t, lastLaunchEntry(t, e)); d["reused_existing_window"] != true {
		t.Errorf("detail = %v, want reused_existing_window=true", d)
	}
}

// ウィンドウが少し遅れて出るアプリ: 出るまで待ち、出たら（前面なら）そこで観測を終える。
func TestControlLaunchWaitsForTheWindowToAppear(t *testing.T) {
	e := newAppEnv(t)
	old := existingNotepad()
	e.windows.SetWindows(old)
	var launched uint32
	e.launcher.OnLaunch(func(pid uint32) { launched = pid })
	e.onSleep = func() {
		if len(e.slept) == 3 {
			old.Foreground = false
			e.windows.SetWindows(domain.WindowInfo{ID: "88", Title: "新規", PID: launched, Foreground: true}, old)
		}
	}

	res := launchNotepad(t, e)
	if res.ReusedExistingWindow || res.Window == nil || res.Window.ID != "88" {
		t.Errorf("result = %+v (window %+v), want the new window 88 and no reuse flag", res, res.Window)
	}
	if len(e.slept) != 3 {
		t.Errorf("waited %d times, want 3 (stop as soon as the window is in the foreground)", len(e.slept))
	}
}

// 起動した pid のウィンドウはあるが前面は別のアプリ（Windows の前面ロックなど）: 前面の別アプリのウィンドウは返さず、
// pid のウィンドウを返して前面に出す。再利用の印は付けない（#85）。
func TestControlLaunchReturnsTheLaunchedWindowNotTheForegroundOne(t *testing.T) {
	e := newAppEnv(t)
	front := domain.WindowInfo{ID: "5", Title: "Grok Bot", Process: "grok.exe", PID: 900, Foreground: true}
	e.launcher.OnLaunch(func(pid uint32) {
		e.windows.SetWindows(front, domain.WindowInfo{ID: "88", Title: "無題", PID: pid})
	})

	res := launchNotepad(t, e)
	if res.ReusedExistingWindow {
		t.Error("ReusedExistingWindow = true, want false: a window of the launched pid exists")
	}
	if res.Window == nil || res.Window.ID != "88" || res.Window.PID != res.PID || !res.Window.Foreground {
		t.Errorf("Window = %+v, want the launched window 88, brought to the front", res.Window)
	}
	if fg, err := e.windows.Foreground(); err != nil || fg.ID != "88" {
		t.Errorf("foreground = %+v (%v), want window 88 focused", fg, err)
	}
}

// 前面のウィンドウを確かめられないとき: window を付けず、再利用の印も付けない（起動は成功のまま）。
func TestControlLaunchWithoutAForegroundWindow(t *testing.T) {
	e := newAppEnv(t)
	e.windows.SetWindows(domain.WindowInfo{ID: "1", Title: "background", PID: userNotepadPID})

	res := launchNotepad(t, e)
	if res.Window != nil || res.ReusedExistingWindow {
		t.Errorf("result = %+v (window %+v), want no window and no reuse flag", res, res.Window)
	}
	if res.PID == 0 {
		t.Error("PID = 0, want the launch to still succeed")
	}
	if len(e.slept) != launchWindowPollAttempts-1 {
		t.Errorf("waited %d times, want %d", len(e.slept), launchWindowPollAttempts-1)
	}
}

// 待ちが中断（ctx の取り消し）されても、起動は成功のまま、そこまでの観測を返す。
func TestControlLaunchKeepsTheResultWhenTheWaitIsInterrupted(t *testing.T) {
	e := newAppEnv(t)
	e.windows.SetWindows(existingNotepad())
	e.sleepErr = context.Canceled

	res := launchNotepad(t, e)
	if len(e.slept) != 1 {
		t.Errorf("waited %d times, want 1", len(e.slept))
	}
	if !res.ReusedExistingWindow || res.Window != nil {
		t.Errorf("result = %+v (window %+v), want the observation so far (reused, no window)", res, res.Window)
	}
}

// 失敗した起動には観測も待ちも無い。
func TestControlLaunchFailureDoesNotObserveWindows(t *testing.T) {
	e := newAppEnv(t)
	app := registerNotepad(t, e)
	e.launcher.DenyAccess(appNotepad)
	res, err := e.control.Launch(context.Background(), ctlAlice, app.ID)
	if err == nil || res.Window != nil || res.ReusedExistingWindow || len(e.slept) != 0 {
		t.Errorf("res = %+v, err = %v, slept = %v", res, err, e.slept)
	}
}

// 起動したプロセスがすぐ終了し、別の pid の新しいウィンドウが開いたときは、既存のウィンドウが前に出ただけではないので、
// 新しいウィンドウが背面にあっても印は付かない（Windows 11 のメモ帳。#55）。起動したアプリ（プロセス名が同じ）の新しいウィンドウを返し、前面に出す（#85）。
func TestControlLaunchDoesNotFlagWhenANewWindowOfAnotherPIDOpens(t *testing.T) {
	e := newAppEnv(t)
	old := existingNotepad()
	e.windows.SetWindows(old)
	e.launcher.OnLaunch(func(uint32) {
		fresh := domain.WindowInfo{ID: "88", Title: "無題 - メモ帳", Process: "Notepad.exe", PID: userNotepadPID + 1}
		e.windows.SetWindows(old, fresh)
	})

	res := launchNotepad(t, e)
	if res.ReusedExistingWindow {
		t.Error("ReusedExistingWindow = true, want false: a window that did not exist before the launch is open")
	}
	if res.Window == nil || res.Window.ID != "88" || !res.Window.Foreground {
		t.Errorf("Window = %+v, want the new window 88 (handed-over process), brought to the front", res.Window)
	}
	if fg, err := e.windows.Foreground(); err != nil || fg.ID != "88" {
		t.Errorf("foreground = %+v (%v), want the new window 88 focused", fg, err)
	}
	if d := auditDetail(t, lastLaunchEntry(t, e)); d["reused_existing_window"] != false {
		t.Errorf("detail = %v, want reused_existing_window=false", d)
	}
}

// 別のアプリの新しいウィンドウ（プロセス名が違う）が開いただけなら、それは起動したアプリのウィンドウと確かめられないので返さず、
// 前面にも出さない。再利用の印は #55 のとおり付けない（新しいウィンドウがあるため）。#85: 返したウィンドウへ入力すると別アプリに入る。
func TestControlLaunchDoesNotReturnANewWindowOfAnotherApp(t *testing.T) {
	e := newAppEnv(t)
	old := existingNotepad()
	e.windows.SetWindows(old)
	e.launcher.OnLaunch(func(uint32) {
		other := domain.WindowInfo{ID: "88", Title: "Grok Bot", Process: "grok.exe", PID: 900}
		e.windows.SetWindows(old, other)
	})

	res := launchNotepad(t, e)
	if res.Window != nil {
		t.Errorf("Window = %+v, want none: no window of the launched app was found", res.Window)
	}
	if res.ReusedExistingWindow {
		t.Error("ReusedExistingWindow = true, want false: a new window exists")
	}
	if fg, err := e.windows.Foreground(); err != nil || fg.ID != "77" {
		t.Errorf("foreground = %+v (%v), want the existing window 77 untouched", fg, err)
	}
}

// 新しいウィンドウが、起動から少し遅れて開いたときも、観測の途中で拾って false にする。
func TestControlLaunchDoesNotFlagWhenTheNewWindowOpensLater(t *testing.T) {
	e := newAppEnv(t)
	old := existingNotepad()
	e.windows.SetWindows(old)
	e.onSleep = func() {
		if len(e.slept) == 3 {
			e.windows.SetWindows(old, domain.WindowInfo{ID: "88", Title: "無題 - メモ帳", Process: "notepad.exe", PID: userNotepadPID + 1})
		}
	}

	res := launchNotepad(t, e)
	if res.ReusedExistingWindow {
		t.Error("ReusedExistingWindow = true, want false: a new window opened during the observation")
	}
}

// 起動の前からあったウィンドウの ID が同じまま（タイトルや前面が変わっただけ）なら、既存のウィンドウが前に出ただけのまま true。
// puppet-remo 自身のウィンドウ（承認ダイアログ・オーバーレイ）が新しく開いても、新しいウィンドウには数えない。
func TestControlLaunchIgnoresRetitledAndOwnWindowsWhenFlagging(t *testing.T) {
	e := newAppEnv(t)
	old := existingNotepad()
	e.windows.SetWindows(old)
	e.launcher.OnLaunch(func(uint32) {
		retitled := old
		retitled.Title = "*無題 - メモ帳"
		own := domain.WindowInfo{ID: "99", Title: "puppet-remo", Process: "puppet-remo.exe", PID: 4242, Own: true}
		e.windows.SetWindows(retitled, own)
	})

	res := launchNotepad(t, e)
	if !res.ReusedExistingWindow {
		t.Error("ReusedExistingWindow = false, want true: no new window other than puppet-remo's own appeared")
	}
}

// 起動の前の一覧が読めなかったときは、新しいウィンドウの有無を使わず、従来どおり（前面が別の pid のウィンドウだけなら true）に判断する。
func TestControlLaunchFallsBackWhenTheWindowListBeforeIsUnreadable(t *testing.T) {
	e := newAppEnv(t)
	e.windows.SetWindows(existingNotepad())
	e.windows.SetError("List", domain.ErrDesktopUnavailable)
	e.launcher.OnLaunch(func(uint32) { e.windows.SetError("List", nil) })

	res := launchNotepad(t, e)
	if !res.ReusedExistingWindow {
		t.Error("ReusedExistingWindow = false, want true: without the list before the launch, a foreground window of another pid is the only evidence")
	}
}

// 起動の前の一覧が読めて 0 件（空）のときは「前の状態が分からない」ではない: 起動後に別の pid のウィンドウが出たら新しいウィンドウと見なす（#55）。
func TestControlLaunchEmptyListBeforeCountsLaterWindowsAsNew(t *testing.T) {
	e := newAppEnv(t)
	e.windows.SetWindows() // デスクトップにウィンドウが 1 つも無い
	e.launcher.OnLaunch(func(uint32) {
		e.windows.SetWindows(domain.WindowInfo{ID: "88", Title: "無題 - メモ帳", Process: "notepad.exe", PID: userNotepadPID + 1})
	})

	res := launchNotepad(t, e)
	if res.ReusedExistingWindow {
		t.Error("ReusedExistingWindow = true, want false: the list before the launch was empty, so a window of another pid is new")
	}
}
