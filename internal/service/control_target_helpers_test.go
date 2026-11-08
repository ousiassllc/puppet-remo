package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは入力スコープ（#156）のテストの共通部品。偽アプリ（FakeApp）を入力の宛先にした Control を使う。

// tgtEnv は ctlEnv に偽アプリを置き、その Control（ctl）で操作する。T は FakeApp のメインウィンドウ（前面）。
type tgtEnv struct {
	*ctlEnv
	app *platformtest.FakeApp
	ctl Control
}

func newTgtEnv(t *testing.T) *tgtEnv {
	t.Helper()
	e := newCtlEnv(t)
	app := platformtest.NewFakeApp(e.windows, e.input, e.clipboard)
	e.capturer.AttachWindows(e.windows)
	te := &tgtEnv{ctlEnv: e, app: app}
	te.useControl(app, e.capturer)
	return te
}

// useControl は in・capturer を使う Control に te.ctl を差し替える。
func (te *tgtEnv) useControl(in platform.InputInjector, capturer platform.Capturer) {
	te.t.Helper()
	ctl, err := NewControl(ControlConfig{
		Capturer: capturer, Input: in, Windows: te.windows, Clipboard: te.clipboard, UI: te.ui,
		Launcher: te.launcher, Apps: te.apps, Leases: te.leases, Kill: te.kill, Settings: te.settings, Audit: te.audit, Clock: te.clock,
	})
	if err != nil {
		te.t.Fatal(err)
	}
	te.ctl = ctl
}

// acquire は who に、偽アプリの T を束縛したリースを取らせる。
func (te *tgtEnv) acquire(who domain.Actor) Lease {
	te.t.Helper()
	l, err := te.leases.AcquireScoped(context.Background(), who, te.app.Ref())
	if err != nil {
		te.t.Fatal(err)
	}
	return l
}

func (te *tgtEnv) scope() domain.LeaseScope {
	te.t.Helper()
	h, ok := te.leases.Holder()
	if !ok || h.Scope == nil {
		te.t.Fatalf("Holder = %+v, %v; want a scoped lease", h, ok)
	}
	return *h.Scope
}

func (te *tgtEnv) do(who domain.Actor, op domain.Op) error {
	_, err := te.ctl.Do(context.Background(), who, op)
	return err
}

// click は物理座標 p の左クリック（MCP の縮小座標へ直して送る）。
func (te *tgtEnv) click(who domain.Actor, p domain.Point) error {
	return te.do(who, clickAt(domain.ButtonLeft, p))
}

func clickAt(b domain.Button, p domain.Point) domain.Op {
	at := scaled(p)
	return domain.Click{At: &at, Button: b, Count: 1, Monitor: domain.MonitorPrimary}
}

// scaled は物理座標 p を、ctlMonitor（3200x1800、最大辺 1568）の縮小座標にする。往復で ±1 ピクセルずれうるので、境界から離れた点を使う。
func scaled(p domain.Point) domain.Point {
	const scale = 1568.0 / 3200.0
	return domain.Point{X: int(math.Round(float64(p.X) * scale)), Y: int(math.Round(float64(p.Y) * scale))}
}

func keyOf(spec string) domain.Op {
	k, err := domain.ParseKeyCombo(spec)
	if err != nil {
		panic(err)
	}
	return domain.KeyPress{Keys: k}
}

// 偽アプリのレイアウト上の、境界から離れた点。
var (
	ptInT        = domain.Point{X: 300, Y: 300}                // T の編集領域
	ptFileButton = domain.Point{X: 125, Y: 115}                // メニューバーの「ファイル」
	ptFileItem   = domain.Point{X: 180, Y: 137}                // 「ファイル」のメニューの項目
	ptContext    = domain.Point{X: 300, Y: 250}                // 右クリックの位置（メニューの左上になる）
	ptSelectAll  = domain.Point{X: 340, Y: 260}                // 右クリックメニューの上半分
	ptPaste      = domain.Point{X: 340, Y: 290}                // 右クリックメニューの下半分
	ptCancel     = domain.Point{X: 545, Y: 172}                // 保存ダイアログのキャンセル（T の外）
	ptOutside    = domain.Point{X: 900, Y: 200}                // 非メンバーの窓（otherWindow の既定の位置）の上
	ptDesktop    = domain.Point{X: 1500, Y: 1000}              // どの窓もない
	rectOutside  = domain.Rect{X: 800, Y: 100, W: 300, H: 300} // otherWindow の既定の位置
	rectCoverT   = domain.Rect{X: 300, Y: 100, W: 400, H: 300} // T の右半分を覆う
	crossStart   = platformtest.FakeAppCommonDialogStart       // 別プロセスの開始時刻
	crossPID     = platformtest.FakeAppCommonDialogPID         // 別プロセスの PID
	tLink        = domain.WindowLink{ID: platformtest.FakeAppMainID, PID: platformtest.FakeAppPID, ProcessStart: platformtest.FakeAppStart, Class: platformtest.FakeAppClass}
)

// otherWindow は T と所有関係のない別プロセスのウィンドウ（タイトルは監査に出ないことの確認に使う）。
func otherWindow(id string, r domain.Rect, foreground bool) domain.WindowInfo {
	return domain.WindowInfo{
		ID: id, Title: "other-title-xyz", Process: "other.exe", PID: 7000, ProcessStart: 7_000, Class: "OtherClass", Rect: r, Foreground: foreground,
	}
}

// sameProcessWindow は T と同じプロセスの、所有関係のないウィンドウ（同一 PID だけでは入れない）。
func sameProcessWindow(id string, r domain.Rect, foreground bool) domain.WindowInfo {
	return domain.WindowInfo{
		ID: id, Title: "same-process-title", Process: "fakenotepad.exe", PID: platformtest.FakeAppPID, ProcessStart: platformtest.FakeAppStart,
		Class: platformtest.FakeAppClass, Rect: r, Foreground: foreground,
	}
}

// ownedWindow は T に所有された窓（pid・start でプロセスを選ぶ）。
func ownedWindow(id string, r domain.Rect, pid uint32, start uint64, foreground bool) domain.WindowInfo {
	return domain.WindowInfo{
		ID: id, Title: "owned-title", Process: "dialog.exe", PID: pid, ProcessStart: start, Class: "#32770", Rect: r,
		OwnerChain: []domain.WindowLink{tLink}, Foreground: foreground,
	}
}

var injectionMethods = []string{"MoveTo", "Click", "Button", "Drag", "Scroll", "Wheel", "Key", "KeyEvent", "Text"}

// injections は InputInjector へ届いた注入の呼び出し（ReleaseAll・CursorPos は含まない）。
func injections(e *ctlEnv) []platformtest.Call {
	var out []platformtest.Call
	for _, c := range e.input.Calls() {
		if slices.Contains(injectionMethods, c.Method) {
			out = append(out, c)
		}
	}
	return out
}

func releaseAllCalls(e *ctlEnv) int { return len(e.input.CallsTo("ReleaseAll")) }

// blockedDetail は err が input_blocked なら、その detail を返す。そうでなければテストを失敗させる。
func blockedDetail(t *testing.T, err error) map[string]any {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeInputBlocked {
		t.Fatalf("err = %v, want input_blocked", err)
	}
	return de.Detail
}

func requireBlockedReason(t *testing.T, err error, reason domain.GroupReason) map[string]any {
	t.Helper()
	d := blockedDetail(t, err)
	if d["reason"] != string(reason) {
		t.Fatalf("reason = %v (err %v), want %q", d["reason"], err, reason)
	}
	return d
}

// requireHalted は、err が reason の input_blocked で、リースが halted（reason つき）になり、ReleaseAll が呼ばれ、input.target_halted が 1 件記録されたことを確かめる。
func (te *tgtEnv) requireHalted(err error, reason domain.GroupReason) map[string]any {
	te.t.Helper()
	d := requireBlockedReason(te.t, err, reason)
	s := te.scope()
	if !s.Halted() || s.HaltReason != reason {
		te.t.Errorf("scope = %+v, want halted with %q", s, reason)
	}
	if releaseAllCalls(te.ctlEnv) == 0 {
		te.t.Error("ReleaseAll was not called")
	}
	if n := len(te.entries("input.target_halted")); n != 1 {
		te.t.Errorf("input.target_halted entries = %d, want 1", n)
	}
	return d
}

// requireRefused は err が reason の input_blocked で、リースが active のまま（停止にしない）、ReleaseAll も呼ばれていないことを確かめる。
func (te *tgtEnv) requireRefused(err error, reason domain.GroupReason) {
	te.t.Helper()
	requireBlockedReason(te.t, err, reason)
	if s := te.scope(); s.State != domain.ScopeActive {
		te.t.Errorf("scope = %+v, want it to stay active (the operation alone is refused)", s)
	}
	if n := len(te.entries("input.target_halted")); n != 0 {
		te.t.Errorf("input.target_halted entries = %d, want 0", n)
	}
}

func (te *tgtEnv) entries(event string) []repository.AuditEntry {
	var out []repository.AuditEntry
	for _, e := range te.audit.Entries() {
		if e.Event == event {
			out = append(out, e)
		}
	}
	return out
}

func detailOf(t *testing.T, e repository.AuditEntry) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(e.DetailJSON, &d); err != nil {
		t.Fatalf("detail_json %q: %v", e.DetailJSON, err)
	}
	return d
}

// hookInput は MoveTo・Text の直後に hook を呼ぶ InputInjector（注入の間に画面が変わる状況を作る）。
type hookInput struct {
	platform.InputInjector
	afterMoveTo func()
	afterText   func()
}

func (h *hookInput) MoveTo(ctx context.Context, p domain.Point) error {
	err := h.InputInjector.MoveTo(ctx, p)
	if err == nil && h.afterMoveTo != nil {
		h.afterMoveTo()
	}
	return err
}

func (h *hookInput) Text(ctx context.Context, s string) error {
	err := h.InputInjector.Text(ctx, s)
	if err == nil && h.afterText != nil {
		h.afterText()
	}
	return err
}

// bareCapturer は platform.GroupCapturer を持たない Capturer（Windows の現状）。
type bareCapturer struct{ platform.Capturer }
