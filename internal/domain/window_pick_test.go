package domain

import "testing"

func TestPickCandidates(t *testing.T) {
	owner := WindowLink{ID: "1", PID: 10, ProcessStart: 100, Class: "Main"}
	win := func(id, title string, mod func(*WindowInfo)) WindowInfo {
		w := WindowInfo{ID: id, Title: title, Process: "app.exe", PID: 10, ProcessStart: 100, Class: "Main"}
		if mod != nil {
			mod(&w)
		}
		return w
	}
	windows := []WindowInfo{
		win("1", "普通のウィンドウ", nil),
		win("2", "所有されたダイアログ", func(w *WindowInfo) { w.OwnerChain = []WindowLink{owner}; w.Class = "#32770" }),
		win("3", "", nil), // タイトルなし
		win("4", "メニュー", func(w *WindowInfo) { w.Class = ClassMenu; w.MenuOwnerID = "1" }),
		win("5", "自ウィンドウ", func(w *WindowInfo) { w.Own = true }),
		win("6", "オーバーレイ", func(w *WindowInfo) { w.Own = true; w.Overlay = true }),
		win("7", "昇格", func(w *WindowInfo) { w.Elevated = true }),
		win("8", "最小化", func(w *WindowInfo) { w.Minimized = true }),
		win("9", "UWP", func(w *WindowInfo) { w.Class = ClassApplicationFrameWindow }),
		win("10", "同一性不明", func(w *WindowInfo) { w.ProcessStart = 0 }),
	}
	want := []struct {
		id     string
		reason GroupReason
	}{
		{"1", ""}, {"7", ReasonElevatedWindow}, {"8", ReasonTargetMinimized}, {"9", ReasonTargetUnsupported}, {"10", ReasonTargetUnsupported},
	}
	got := PickCandidates(windows)
	if len(got) != len(want) {
		t.Fatalf("candidates = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Window.ID != w.id || got[i].Reason != w.reason || got[i].Selectable() != (w.reason == "") {
			t.Errorf("candidate[%d] = %s %q, want %s %q", i, got[i].Window.ID, got[i].Reason, w.id, w.reason)
		}
	}
}
