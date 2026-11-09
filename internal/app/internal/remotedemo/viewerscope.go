package main

import (
	"net/http"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// このファイルは -viewer-scope の部品（#157。ビューアのウィンドウ選択・グループ表示・手動操作の手動確認とスクリーンショット）。
// 偽のメモ帳（platformtest.FakeApp。T は window_id "1001"）と、その後ろ・周りの偽のウィンドウを置く。
// **偽 platform の画面で、Windows 実機の成立性ゲート（S1〜S3）の代わりにならない**（実機未確認。docs/operations/smoke-windows.md §3.10）。
//
// ブラウザで /viewer を開き、一覧から「無題 - FakeNotepad」を選ぶ。メニューバーの「ファイル」→ 項目で保存ダイアログが開き（T の外へ出る）、
// T の右下の端での右クリックでコンテキストメニューが T の外へ出る。安全停止の場面は、ホスト GUI のポートの制御エンドポイントで作る（loopback のみ）:
//
//	/__demo/viewer-scope?do=foreign        別プロセスのウィンドウを T の前面に出す（以後のキー・文字は届かず halted）
//	/__demo/viewer-scope?do=foreign-off    そのウィンドウを退かす（halted からは自動では戻らない。「再開」で再検証）
//	/__demo/viewer-scope?do=cross-process  以後に開く保存ダイアログを別プロセス（共通ダイアログ）にする
//	/__demo/viewer-scope?do=unprintable    メニューを撮れないものにする（capture_member_unavailable で halted。画面全体には切り替わらない）
//	/__demo/viewer-scope?do=reset          すべて初期状態へ

// 偽のウィンドウの ID（偽のメモ帳の 1001〜1003 と重ならない）。
const (
	viewerScopeNotesID    = "2001"
	viewerScopeOtherID    = "2002"
	viewerScopeElevatedID = "2003"
	viewerScopeForeignID  = "2004"
)

// viewerScopeScene は -viewer-scope の偽の画面。
type viewerScopeScene struct{ *scopeDemoScene }

func newViewerScopeScene(clip *platformtest.FakeClipboard) *viewerScopeScene {
	s := &viewerScopeScene{newScopeDemoScene(clip)}
	s.reset()
	return s
}

// reset は偽のメモ帳を初期状態にし、背景のウィンドウ（前面が先）を置き直す。
func (s *viewerScopeScene) reset() {
	s.app.Reset()
	s.capture.SetNotPrintable(platformtest.FakeAppMenuID, false)
	s.windows.SetWindows(s.app.MainWindow(),
		domain.WindowInfo{
			ID: viewerScopeOtherID, Title: "無題 - FakeNotepad（別の文書）", Process: "fakenotepad.exe", PID: platformtest.FakeAppPID,
			ProcessStart: platformtest.FakeAppStart, Class: platformtest.FakeAppClass, Rect: domain.Rect{X: 560, Y: 460, W: 300, H: 200},
		},
		domain.WindowInfo{
			ID: viewerScopeElevatedID, Title: "タスク マネージャー（管理者）", Process: "taskmgr.exe", PID: 6100, ProcessStart: 6100, Class: "TaskManagerWindow",
			Rect: domain.Rect{X: 700, Y: 60, W: 280, H: 200}, Elevated: true,
		},
		domain.WindowInfo{
			ID: viewerScopeNotesID, Title: "Notes - demo-notes.txt", Process: "notes.exe", PID: 4242, ProcessStart: 4242, Class: "NotesWindow",
			Rect: domain.Rect{X: 40, Y: 30, W: 940, H: 700},
		},
	)
}

// ServeHTTP は安全停止の場面を作る制御エンドポイント（ホスト GUI のポート。loopback のみ）。
func (s *viewerScopeScene) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("do") {
	case "foreign":
		s.windows.Add(domain.WindowInfo{
			ID: viewerScopeForeignID, Title: "電卓（デモ）", Process: "calc.exe", PID: 7000, ProcessStart: 7000, Class: "CalcWindow",
			Rect: domain.Rect{X: 300, Y: 120, W: 220, H: 160}, Foreground: true,
		})
	case "foreign-off":
		s.windows.Remove(viewerScopeForeignID)
		s.windows.Update(platformtest.FakeAppMainID, func(i *domain.WindowInfo) { i.Foreground = true })
	case "cross-process":
		s.app.DialogCrossProcess = true
	case "unprintable":
		s.capture.SetNotPrintable(platformtest.FakeAppMenuID, true)
	case "reset":
		s.reset()
	default:
		http.Error(w, "do は foreign / foreign-off / cross-process / unprintable / reset のどれか", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
