package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/ousiassllc/puppet-remo/internal/service"
)

// このファイルは Wails のシェル（ウィンドウ・トレイ）の振る舞いを、Wails に依存しない形で持つ（FR-H01、FR-H06）。
// Wails の型への結線は wails_windows.go が行う。

// MainWindow はホスト GUI のメインウィンドウ。
type MainWindow interface {
	// Show はウィンドウを表示して前面に出す（最小化・非表示からの復帰を含む）。
	// path が空でなければ、そのページ（GUI ルーターのパス。例 "/requests"）を開く。
	Show(path string)
	// Hide はウィンドウを隠す。アプリは終了しない。
	Hide()
}

// Tray の表示。
type TrayState struct {
	// Tooltip はトレイアイコンのツールチップ。
	Tooltip string
	// KillSwitchLabel はキルスイッチのメニュー項目の文言。
	KillSwitchLabel string
	// KillSwitchEnabled は、キルスイッチのメニュー項目を押せるか（発動中は押せない。解除はダッシュボードで行う）。
	KillSwitchEnabled bool
	// RequestsLabel は接続要求のメニュー項目の文言。
	RequestsLabel string
	// RequestsVisible は、保留中の接続要求があるか（無いときは項目を隠す）。
	RequestsVisible bool
}

// TrayView はトレイの表示の更新先。
type TrayView interface {
	Apply(TrayState)
}

// ShellApp は Shell が使う App の窓口（*App が満たす）。
type ShellApp interface {
	KillSwitch() KillSwitchControl
	PendingRequests(ctx context.Context) (int, error)
	Bus() *service.Bus
}

// 発動元（監査の source）。
const (
	sourceTray = "tray"
)

// Shell は、ウィンドウを閉じたらトレイへ隠す・トレイメニューの操作・二重起動時の前面化・終了を決める（FR-H01）。
type Shell struct {
	app  ShellApp
	win  MainWindow
	view TrayView
	quit func()
	log  *slog.Logger

	quitting atomic.Bool
}

// NewShell は Shell を作る。quit は Wails のアプリを終える関数。
func NewShell(app ShellApp, win MainWindow, view TrayView, quit func(), log *slog.Logger) *Shell {
	if log == nil {
		log = slog.Default()
	}
	return &Shell{app: app, win: win, view: view, quit: quit, log: log}
}

// OpenDashboard はメニュー・トレイのクリックで、ダッシュボードを開く。
func (s *Shell) OpenDashboard() { s.win.Show("/") }

// OpenRequests は接続要求の画面を開く。
func (s *Shell) OpenRequests() { s.win.Show("/requests") }

// SecondInstance は 2 つ目のインスタンスが起動されたときに呼ぶ。1 つ目のウィンドウを前面に出す
// （バックグラウンド起動で隠れていても表示する）。
func (s *Shell) SecondInstance() { s.win.Show("") }

// WindowClosing はウィンドウの「閉じる」で呼ぶ。終了中でなければ、ウィンドウを隠し、閉じる処理を取り消す
// （cancel を呼ぶ）。トレイに常駐したままになる。
func (s *Shell) WindowClosing(cancel func()) {
	if s.quitting.Load() {
		return
	}
	s.win.Hide()
	cancel()
}

// TriggerKillSwitch はトレイメニューのキルスイッチ。
func (s *Shell) TriggerKillSwitch() {
	if err := s.app.KillSwitch().Trigger(sourceTray); err != nil {
		s.log.Error("kill switch trigger reported errors", "source", sourceTray, "err", err)
	}
	s.Refresh(context.Background())
}

// Quit はメニューの「終了」。以後の「閉じる」は取り消さない。
func (s *Shell) Quit() {
	s.quitting.Store(true)
	s.quit()
}

// Quitting は Quit が呼ばれたか。
func (s *Shell) Quitting() bool { return s.quitting.Load() }

// State は今のトレイの表示を返す。
func (s *Shell) State(ctx context.Context) TrayState {
	st := TrayState{Tooltip: "puppet-remo", KillSwitchLabel: "キルスイッチを発動", KillSwitchEnabled: true}
	if s.app.KillSwitch().Active() {
		st.Tooltip += "（キルスイッチ発動中）"
		st.KillSwitchLabel = "キルスイッチ発動中（解除はダッシュボード）"
		st.KillSwitchEnabled = false
	}
	if n, err := s.app.PendingRequests(ctx); err != nil {
		s.log.Warn("count pending connection requests", "err", err)
	} else if n > 0 {
		st.Tooltip += fmt.Sprintf("（接続要求 %d 件）", n)
		st.RequestsLabel = fmt.Sprintf("接続要求 %d 件を確認", n)
		st.RequestsVisible = true
	}
	return st
}

// Refresh はトレイの表示を今の状態に合わせる。
func (s *Shell) Refresh(ctx context.Context) { s.view.Apply(s.State(ctx)) }

// Watch はトレイの表示を、キルスイッチと接続要求の変化に合わせて更新し続ける。ctx の終了で戻る。
// 最初に 1 度、今の状態を反映する（購読を張ってから反映するので、取りこぼしがない）。
func (s *Shell) Watch(ctx context.Context) {
	events, cancel := s.app.Bus().Subscribe(service.TopicKillSwitch, service.TopicConnectionRequests)
	defer cancel()
	s.Refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-events:
			s.Refresh(ctx)
		}
	}
}
