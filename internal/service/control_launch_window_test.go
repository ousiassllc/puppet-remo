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

// 返した pid のウィンドウが無く、既存のウィンドウ（別の pid）が前面にあるだけなら、印を付けてそのウィンドウを返す。
// 観測は上限の回数で打ち切る。
func TestControlLaunchFlagsAnExistingWindowBroughtForward(t *testing.T) {
	e := newAppEnv(t)
	e.windows.SetWindows(existingNotepad())

	res := launchNotepad(t, e)
	if !res.ReusedExistingWindow {
		t.Error("ReusedExistingWindow = false, want true: only a window of another pid is in the foreground")
	}
	if res.Window == nil || res.Window.ID != "77" || res.Window.Title != "無題 - メモ帳" || res.Window.PID != userNotepadPID {
		t.Errorf("Window = %+v, want the existing window 77 of pid %d", res.Window, userNotepadPID)
	}
	if res.Window != nil && res.Window.PID == res.PID {
		t.Errorf("Window.PID = launched pid %d, want a different pid", res.PID)
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

// 新しいウィンドウはあるが前面は別のアプリ: 前面のウィンドウをそのまま返し、再利用の印は付けない（pid が違うので見分けられる）。
func TestControlLaunchNewWindowBehindAnotherAppIsNotFlagged(t *testing.T) {
	e := newAppEnv(t)
	front := domain.WindowInfo{ID: "5", Title: "ブラウザ", PID: 900, Foreground: true}
	e.launcher.OnLaunch(func(pid uint32) {
		e.windows.SetWindows(front, domain.WindowInfo{ID: "88", Title: "無題", PID: pid})
	})

	res := launchNotepad(t, e)
	if res.ReusedExistingWindow {
		t.Error("ReusedExistingWindow = true, want false: a window of the launched pid exists")
	}
	if res.Window == nil || res.Window.ID != "5" || res.Window.PID == res.PID {
		t.Errorf("Window = %+v, want the foreground window 5 of another pid", res.Window)
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
	if !res.ReusedExistingWindow || res.Window == nil || res.Window.ID != "77" {
		t.Errorf("result = %+v (window %+v), want the observation so far", res, res.Window)
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
