//go:build e2e

package app

import (
	"errors"
	"net/http"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// このファイルは E2E のテスト専用サーバーの偽のデスクトップ（#157）: 偽アプリ（platformtest.FakeApp。T・メニュー・保存ダイアログ）と、
// その後ろ・周りの偽のウィンドウ、シナリオを作る制御エンドポイント。**偽 platform** で、Windows 実機の成立性ゲート（S1〜S3。実機未確認）の
// 代わりにならない。

// e2eDesktop は偽のデスクトップ。
type e2eDesktop struct {
	windows  *platformtest.FakeWindows
	capturer *platformtest.FakeCapturer
	app      *platformtest.FakeApp
}

// 偽のウィンドウの ID（偽アプリの 1001〜1003 と重ならない）。
const (
	e2eNotesID    = "2001" // 背景のメモ帳（既存の E2E のクリックの宛先。T の後ろの全面）
	e2eElevatedID = "2002" // 昇格（管理者権限）のウィンドウ。一覧では灰色で選べない
	e2eForeignID  = "2003" // 別プロセスのウィンドウ（シナリオで T の前面・重なりに出す）
	e2eOtherID    = "2004" // 同一プロセス（偽アプリ）の、所有関係のない別ウィンドウ
)

func newE2EDesktop(input *platformtest.FakeInput, clip *platformtest.FakeClipboard) *e2eDesktop {
	w := platformtest.NewFakeWindows()
	c := platformtest.NewFakeCapturer() // 640x480 のプライマリ 1 台
	c.AttachWindows(w)
	d := &e2eDesktop{windows: w, capturer: c, app: platformtest.NewFakeApp(w, input, clip)}
	d.reset()
	return d
}

// backdrop は T の後ろに置く偽のウィンドウ（前面が先）。
func (d *e2eDesktop) backdrop() []domain.WindowInfo {
	return []domain.WindowInfo{
		{
			ID: e2eOtherID, Title: "Fake Notepad - other window (e2e)", Process: "fakenotepad.exe", PID: platformtest.FakeAppPID,
			ProcessStart: platformtest.FakeAppStart, Class: platformtest.FakeAppClass, Rect: domain.Rect{X: 20, Y: 330, W: 120, H: 100},
		},
		{
			ID: e2eElevatedID, Title: "Task Manager (e2e elevated)", Process: "taskmgr.exe", PID: 6100, ProcessStart: 6_100, Class: "TaskManagerWindow",
			Rect: domain.Rect{X: 500, Y: 20, W: 120, H: 60}, Elevated: true,
		},
		{
			ID: e2eNotesID, Title: "Notes - e2e-notes.txt", Process: "notes.exe", PID: 4242, ProcessStart: 4_242, Class: "NotesWindow",
			Rect: domain.Rect{X: 0, Y: 0, W: 640, H: 480},
		},
	}
}

// reset はテスト間の初期化: 偽アプリを初期状態にし、背景のウィンドウを置き直し、撮影不可の設定を解除する。
func (d *e2eDesktop) reset() {
	d.app.Reset()
	d.windows.SetWindows(append([]domain.WindowInfo{d.app.MainWindow()}, d.backdrop()...)...)
	for _, id := range []string{platformtest.FakeAppMainID, platformtest.FakeAppMenuID, platformtest.FakeAppDialogID} {
		d.capturer.SetNotPrintable(id, false)
	}
}

// foreignWindow は T の右半分に重なる、別プロセスの前面のウィンドウ（unrelated_other_process。T の外の座標は覆わない）。
func foreignWindow() domain.WindowInfo {
	return domain.WindowInfo{
		ID: e2eForeignID, Title: "Calculator (e2e)", Process: "calc.exe", PID: 7000, ProcessStart: 7_000, Class: "CalcWindow",
		Rect: domain.Rect{X: 300, Y: 120, W: 200, H: 150}, Foreground: true,
	}
}

// desktopHandlers は偽のデスクトップのシナリオを作る制御エンドポイント（loopback のみ。e2eServer.handler が登録する）。
func (s *e2eServer) desktopHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /desktop/app", s.desktopApp)
	mux.HandleFunc("POST /desktop/foreign-window", s.desktopForeign)
	mux.HandleFunc("POST /desktop/foreign-window/remove", s.desktopForeignRemove)
	mux.HandleFunc("POST /desktop/cross-process-dialog", s.desktopCrossProcessDialog)
	mux.HandleFunc("POST /desktop/ownerless-menu", s.desktopOwnerlessMenu)
	mux.HandleFunc("POST /desktop/unprintable", s.desktopUnprintable)
	mux.HandleFunc("POST /desktop/close-target", s.desktopCloseTarget)
}

// desktopApp は偽アプリの状態（保存されたファイル名・文書・メニュー・ダイアログ）。
func (s *e2eServer) desktopApp(w http.ResponseWriter, _ *http.Request) {
	a := s.desktop.app
	saved := a.Saved()
	if saved == nil {
		saved = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": saved, "document": a.Document(), "menu": a.MenuOpen(), "dialog": a.DialogOpen(), "selected": a.Selected()})
}

// desktopForeign は別プロセスのウィンドウを T の前面に出す（以後、キー・文字の宛先と T の右半分の座標の最上位がこのウィンドウになる）。
func (s *e2eServer) desktopForeign(w http.ResponseWriter, _ *http.Request) {
	s.desktop.windows.Add(foreignWindow())
	writeJSON(w, http.StatusOK, map[string]bool{"added": true})
}

func (s *e2eServer) desktopForeignRemove(w http.ResponseWriter, _ *http.Request) {
	s.desktop.windows.Remove(e2eForeignID)
	s.desktop.windows.Update(platformtest.FakeAppMainID, func(i *domain.WindowInfo) { i.Foreground = true })
	writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
}

// desktopCrossProcessDialog は、以後に開く保存ダイアログを別プロセス（エクスプローラーの共通ダイアログのように T に所有される）にする。on=1 / 0。
func (s *e2eServer) desktopCrossProcessDialog(w http.ResponseWriter, r *http.Request) {
	s.desktop.app.DialogCrossProcess = r.URL.Query().Get("on") != "0"
	writeJSON(w, http.StatusOK, map[string]bool{"cross_process_dialog": s.desktop.app.DialogCrossProcess})
}

// desktopOwnerlessMenu は、以後に開くメニューの所有関係を確かめられなくする（トレイのメニューのような hwndMenuOwner の無いメニュー）。on=1 / 0。
func (s *e2eServer) desktopOwnerlessMenu(w http.ResponseWriter, r *http.Request) {
	s.desktop.app.OwnerlessMenu = r.URL.Query().Get("on") != "0"
	writeJSON(w, http.StatusOK, map[string]bool{"ownerless_menu": s.desktop.app.OwnerlessMenu})
}

// desktopUnprintable は window（main | menu | dialog）を PrintWindow が撮れないものにする（SetNotPrintable）。on=1 / 0。
func (s *e2eServer) desktopUnprintable(w http.ResponseWriter, r *http.Request) {
	ids := map[string]string{"main": platformtest.FakeAppMainID, "menu": platformtest.FakeAppMenuID, "dialog": platformtest.FakeAppDialogID}
	id, ok := ids[r.URL.Query().Get("window")]
	if !ok {
		writeErr(w, http.StatusBadRequest, errBadWindow)
		return
	}
	s.desktop.capturer.SetNotPrintable(id, r.URL.Query().Get("on") != "0")
	writeJSON(w, http.StatusOK, map[string]bool{"unprintable": r.URL.Query().Get("on") != "0"})
}

// desktopCloseTarget は T を閉じる（target_closed。選び直しのみ）。
func (s *e2eServer) desktopCloseTarget(w http.ResponseWriter, _ *http.Request) {
	s.desktop.windows.Remove(platformtest.FakeAppMainID)
	writeJSON(w, http.StatusOK, map[string]bool{"closed": true})
}

var errBadWindow = errors.New("window must be main, menu or dialog")
