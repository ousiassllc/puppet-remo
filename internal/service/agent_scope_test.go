package service

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは window_id つきの run_task（#158）の試験。偽 platform（FakeApp・FakeWindows・FakeCapturer）と台本つきの偽モデルで、
// 実機の成立性ゲート（S1〜S3）の代わりにはならない（実機未確認。docs/operations/smoke-windows.md §3.10）。

var (
	lay      = platformtest.FakeAppLayout
	tID      = platformtest.FakeAppMainID
	menuID   = platformtest.FakeAppMenuID
	dialogID = platformtest.FakeAppDialogID
	// 合成画像の外接矩形: T だけのとき・保存ダイアログ（T の右へはみ出す）があるとき。
	boundsT      = lay.Main
	boundsDialog = domain.Rect{X: 100, Y: 80, W: 550, H: 320}
)

// scopedMaxDim は newAgentEnv の screenshot.max_dimension。
const scopedMaxDim = 320

func newScopedEnv(t *testing.T, o agentOpts, script ...modelStepSpec) *agentEnv {
	t.Helper()
	o.app = true
	o.script = script
	return newAgentEnv(t, o)
}

func (ae *agentEnv) startScoped(t *testing.T) domain.Task {
	t.Helper()
	task, err := ae.agent.Start(context.Background(), ae.by, StartInput{Prompt: "save the file", WindowID: tID})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return task
}

// mp は物理座標 p を、外接矩形 b の合成画像（最大辺 scopedMaxDim へ縮小）上のモデルの座標にする。
func mp(p domain.Point, b domain.Rect) domain.Point {
	sz := fitSize(b.W, b.H, scopedMaxDim)
	return domain.Point{X: roundDiv((p.X-b.X)*sz.W, b.W), Y: roundDiv((p.Y-b.Y)*sz.H, b.H)}
}

func leftClick(p domain.Point) domain.Click {
	return domain.Click{At: &p, Button: domain.ButtonLeft, Count: 1, Monitor: domain.MonitorPrimary}
}

// 偽アプリのレイアウト上の物理座標（境界から離れた点）。
var (
	physFileButton = domain.Point{X: 125, Y: 115}
	physFileItem   = domain.Point{X: 180, Y: 137}
	physSaveButton = domain.Point{X: 485, Y: 172}
	physInT        = domain.Point{X: 300, Y: 300}
)

func (ae *agentEnv) clicks() int { return len(ae.input.CallsTo("Click")) }

func (ae *agentEnv) injectionCount() int { return len(injections(ae.ctlEnv)) }

// openMenuBeforeStart は偽アプリで「ファイル」メニューを開いておく（タスクの開始前。Recorder の件数もそのぶん増える）。
func (ae *agentEnv) openFileMenu(t *testing.T) {
	t.Helper()
	ae.input.SetCursor(physFileButton)
	if err := ae.app.Click(context.Background(), domain.ButtonLeft, 1, nil); err != nil {
		t.Fatal(err)
	}
	if ae.app.MenuOpen() != "file" {
		t.Fatalf("menu = %q", ae.app.MenuOpen())
	}
	ae.input.Reset()
}

// openSaveDialog は偽アプリで保存ダイアログ（T に所有される同一プロセスのメンバー）を開いておく。
func (ae *agentEnv) openSaveDialog(t *testing.T) {
	t.Helper()
	ae.openFileMenu(t)
	ae.input.SetCursor(physFileItem)
	if err := ae.app.Click(context.Background(), domain.ButtonLeft, 1, nil); err != nil {
		t.Fatal(err)
	}
	if !ae.app.DialogOpen() {
		t.Fatal("the save dialog is not open")
	}
	ae.input.Reset()
}

// requireNoScreenCapture はモニター撮影（Grab・Watch）が 1 回も呼ばれていないことを確かめる（画面領域の撮影の API は platform に無い）。
func (ae *agentEnv) requireNoMonitorCapture(t *testing.T) {
	t.Helper()
	ae.capturer.AssertNoMonitorCapture(t)
}

func withBefore(spec modelStepSpec, fn func()) modelStepSpec {
	spec.before = fn
	return spec
}

func requireTaskEnd(t *testing.T, ae *agentEnv, got domain.Task, status domain.TaskStatus, blockReason string) {
	t.Helper()
	if got.Status != status {
		t.Fatalf("task = %s / %s (%s), want %s", got.Status, got.ErrorCode, blockReasonFromAudit(t, ae, got.ID), status)
	}
	if status == domain.TaskFailed {
		if blockReason != "" && got.ErrorCode != domain.TaskErrorInputBlocked {
			t.Errorf("error_code = %q, want input_blocked", got.ErrorCode)
		}
		if blockReason != "" {
			if r := blockReasonFromAudit(t, ae, got.ID); r != blockReason {
				t.Errorf("block_reason = %q, want %q", r, blockReason)
			}
		}
	}
}

func blockReasonFromAudit(t *testing.T, ae *agentEnv, id string) string {
	t.Helper()
	d := detailOf(t, lastEntry(t, ae, id, agentTaskEndAuditEvent))
	r, _ := d["block_reason"].(string)
	return r
}

func batchYields(t *testing.T, ae *agentEnv, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range ae.auditOf(id) {
		if e.Event == agentBatchYieldedAuditEvent {
			out = append(out, detailOf(t, e))
		}
	}
	return out
}

// ---- 開始・見るもの ----

// 対象つきのタスクは、タスク行・監査・get に対象（プロセス名・pid・window_id）が出て、タイトルは出ない。最初の画像はグループ合成画像
// （T の外に出た保存ダイアログを含み、非メンバー・デスクトップの画素を含まない）で、高速パスは使われない。モニター撮影は 0。
func TestAgentScopedStartRecordsTargetAndSendsGroupComposite(t *testing.T) {
	ae := newScopedEnv(t, agentOpts{}, finalAnswer("done"))
	// 保存ダイアログを開き、T に重なる別プロセスのウィンドウと、画面の別の場所の窓を置く。
	ae.input.SetCursor(physFileButton)
	ctx := context.Background()
	_ = ae.app.Click(ctx, domain.ButtonLeft, 1, nil)
	ae.input.SetCursor(physFileItem)
	_ = ae.app.Click(ctx, domain.ButtonLeft, 1, nil)
	if !ae.app.DialogOpen() {
		t.Fatal("the save dialog is not open")
	}
	ae.windows.Add(otherWindow("9001", domain.Rect{X: 300, Y: 300, W: 100, H: 100}, false))
	ae.windows.Add(otherWindow("9002", domain.Rect{X: 700, Y: 300, W: 100, H: 100}, false))

	task := ae.startScoped(t)
	if task.TargetWindowID != tID || task.TargetProcess != "fakenotepad.exe" || task.TargetPID != platformtest.FakeAppPID {
		t.Errorf("accepted task target = %q %q %d", task.TargetWindowID, task.TargetProcess, task.TargetPID)
	}
	got := ae.waitTask(t, task.ID)
	requireTaskEnd(t, ae, got, domain.TaskCompleted, "")
	if got.FastSteps != 0 || got.HandoffReason != "" {
		t.Errorf("fast_steps = %d, handoff = %q", got.FastSteps, got.HandoffReason)
	}

	row, err := ae.db.Tasks().Get(ctx, task.ID)
	if err != nil || row.TargetWindowID != tID || row.TargetProcess != "fakenotepad.exe" || row.TargetPID != platformtest.FakeAppPID {
		t.Errorf("stored task = %+v, %v", row, err)
	}
	for _, event := range []string{agentTaskStartAuditEvent, agentTaskEndAuditEvent} {
		e := lastEntry(t, ae, task.ID, event)
		target, _ := detailOf(t, e)["target"].(map[string]any)
		if target["window_id"] != tID || target["process"] != "fakenotepad.exe" || target["pid"] != float64(platformtest.FakeAppPID) {
			t.Errorf("%s target = %v", event, target)
		}
		if bytes.Contains(e.DetailJSON, []byte("FakeNotepad")) || bytes.Contains(e.DetailJSON, []byte("名前を付けて保存")) {
			t.Errorf("%s has a window title: %s", event, e.DetailJSON)
		}
	}

	if len(ae.model.images) != 1 {
		t.Fatalf("model images = %d", len(ae.model.images))
	}
	img, err := png.Decode(bytes.NewReader(ae.model.images[0]))
	if err != nil {
		t.Fatal(err)
	}
	want := fitSize(boundsDialog.W, boundsDialog.H, scopedMaxDim)
	if b := img.Bounds(); b.Dx() != want.W || b.Dy() != want.H {
		t.Fatalf("image = %v, want %dx%d (the group bounds, longest side <= %d)", b, want.W, want.H, scopedMaxDim)
	}
	px := func(p domain.Point) (uint32, uint32, uint32, uint32) {
		q := mp(p, boundsDialog)
		return img.At(q.X, q.Y).RGBA()
	}
	// T の矩形の外のダイアログの画素が写る（画素の持ち主は偽 platform の ID ごとの単色）。
	dr, dg, db, da := px(domain.Point{X: 600, Y: 100})
	if c := platformtest.WindowColor(dialogID); da == 0 || uint8(dr>>8) != c.R || uint8(dg>>8) != c.G || uint8(db>>8) != c.B {
		t.Errorf("dialog pixel = %d,%d,%d,%d, want the dialog color %v", dr>>8, dg>>8, db>>8, da>>8, c)
	}
	// どのメンバーの矩形にも入らない場所（T の下のダイアログの横・T と別窓の重なる所）は透明か T の画素で、非メンバー・デスクトップの色ではない。
	if _, _, _, a := px(domain.Point{X: 600, Y: 350}); a != 0 {
		t.Errorf("a pixel outside every member is not transparent (alpha %d)", a>>8)
	}
	r2, g2, b2, _ := px(domain.Point{X: 350, Y: 350})
	for _, id := range []string{"9001", "9002"} {
		if c := platformtest.WindowColor(id); uint8(r2>>8) == c.R && uint8(g2>>8) == c.G && uint8(b2>>8) == c.B {
			t.Errorf("a non-member window (%s) is in the image", id)
		}
	}
	ae.requireNoMonitorCapture(t)
	if len(ae.ui.Calls()) != 0 {
		t.Errorf("UIA calls = %d", len(ae.ui.Calls()))
	}
}

// Start の選択検査: 自ウィンドウ・昇格・不在・agent.deny_processes に当たる対象は、タスクを開始しない（行も作らない・リースも取らない）。
func TestAgentScopedStartRefusals(t *testing.T) {
	cases := []struct {
		name     string
		windowID string
		setup    func(ae *agentEnv)
		reason   string
		notFound bool
	}{
		{"own window", "own", func(ae *agentEnv) {
			ae.windows.Add(domain.WindowInfo{ID: "own", Own: true, Process: "puppet-remo.exe", PID: 8, ProcessStart: 8, Class: "Own", Rect: domain.Rect{X: 700, Y: 0, W: 100, H: 100}})
		}, string(domain.ReasonOwnWindow), false},
		{"elevated window", "adm", func(ae *agentEnv) {
			ae.windows.Add(domain.WindowInfo{ID: "adm", Elevated: true, Process: "notepad.exe", PID: 9, ProcessStart: 9, Class: "Notepad", Rect: domain.Rect{X: 700, Y: 0, W: 100, H: 100}})
		}, string(domain.ReasonElevatedWindow), false},
		{"missing window", "nope", func(*agentEnv) {}, "target_closed", true},
		{"deny_processes", tID, func(ae *agentEnv) { ae.settings.put(SettingAgentDenyProcesses, `["FakeNotepad.exe"]`) }, string(domain.ReasonDeniedProcess), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ae := newScopedEnv(t, agentOpts{}, finalAnswer("never"))
			tc.setup(ae)
			_, err := ae.agent.Start(context.Background(), ae.by, StartInput{Prompt: "p", WindowID: tc.windowID})
			if err == nil {
				t.Fatal("Start succeeded")
			}
			wantCode := domain.CodeInputBlocked
			if tc.notFound {
				wantCode = domain.CodeWindowNotFound
			}
			if domain.CodeOf(err) != wantCode {
				t.Fatalf("Start err = %v, want %s", err, wantCode)
			}
			if de, ok := err.(*domain.Error); ok && de.Detail["reason"] != tc.reason {
				t.Errorf("reason = %v, want %s", de.Detail["reason"], tc.reason)
			}
			if page, _ := ae.db.Tasks().List(context.Background(), repository.TaskQuery{}); len(page.Tasks) != 0 {
				t.Errorf("tasks = %d, want none", len(page.Tasks))
			}
			if _, held := ae.leases.Holder(); held || ae.agent.RunningTasks() != 0 || ae.model.Calls() != 0 || ae.injectionCount() != 0 {
				t.Error("the task started or injected input")
			}
		})
	}
}

// スコープ付きの run_task は、画面全体の撮影・画面領域の切り出しに切り替わらず、撮れないと failed（capture_unavailable /
// capture_member_unavailable）。T が撮れない・メンバー（メニュー）が撮れない・開始後にメンバーが撮れなくなる場合。
func TestAgentScopedCaptureFailuresStop(t *testing.T) {
	t.Run("T is not printable at the start", func(t *testing.T) {
		ae := newScopedEnv(t, agentOpts{}, finalAnswer("never"))
		ae.capturer.SetNotPrintable(tID, true)
		got := ae.waitTask(t, ae.startScoped(t).ID)
		requireTaskEnd(t, ae, got, domain.TaskFailed, string(domain.ReasonCaptureUnavailable))
		if ae.model.Calls() != 0 {
			t.Error("the model was called without an image")
		}
		ae.requireNoMonitorCapture(t)
	})
	t.Run("a menu is not printable at the start", func(t *testing.T) {
		ae := newScopedEnv(t, agentOpts{}, finalAnswer("never"))
		ae.openFileMenu(t)
		ae.capturer.SetNotPrintable(menuID, true)
		got := ae.waitTask(t, ae.startScoped(t).ID)
		requireTaskEnd(t, ae, got, domain.TaskFailed, string(domain.ReasonCaptureMemberUnavailable))
		ae.requireNoMonitorCapture(t)
		if ae.injectionCount() != 0 {
			t.Error("input was injected")
		}
	})
	t.Run("the menu that the batch opened is not printable (after the batch)", func(t *testing.T) {
		ae := newScopedEnv(t, agentOpts{
			wrapInput: func(in platform.InputInjector) platform.InputInjector {
				return &afterClickInput{InputInjector: in, after: func(int) { /* 下で差し込む */ }}
			},
		}, step("call-1", leftClick(mp(physFileButton, boundsT)), leftClick(mp(physFileItem, boundsT))), finalAnswer("never"))
		// メニューが開いた直後に、そのメニューを撮れなくする（GrabGroup は撮影のたびにウィンドウを調べる）。
		ae.capturer.SetNotPrintable(menuID, true) // 開く前に印を付けても、メニューが出るまでは撮影に関係しない
		got := ae.waitTask(t, ae.startScoped(t).ID)
		requireTaskEnd(t, ae, got, domain.TaskFailed, string(domain.ReasonCaptureMemberUnavailable))
		if ae.clicks() != 1 {
			t.Errorf("clicks = %d, want 1 (the second click of the batch is never injected)", ae.clicks())
		}
		if ae.model.Calls() != 1 {
			t.Errorf("model calls = %d, want 1 (no second call after the failed capture)", ae.model.Calls())
		}
		// 中断のあとに撮影が失敗しても、実行・未実行の件数は監査に残る（中断した Op は agent.step の行を残さないので、この行だけが記録）。
		if y := batchYields(t, ae, got.ID); len(y) != 1 || y[0]["executed"] != float64(1) || y[0]["not_executed"] != float64(1) || y[0]["reason"] != "group_changed" {
			t.Errorf("batch_yielded = %v, want one row (executed 1, not_executed 1) even though the capture after it failed", y)
		}
		ae.requireNoMonitorCapture(t)
	})
}

// afterClickInput は Click の直後に after(n)（n は 1 始まりの Click の通し番号）を呼ぶ InputInjector（バッチの途中で画面が変わる状況を作る）。
// afterMove・afterText は MoveTo・Text の直後の同様の hook（1 つの操作の複数回の注入の途中で画面が変わる状況）。
type afterClickInput struct {
	platform.InputInjector
	after     func(n int)
	afterMove func(n int)
	afterText func(n int)
	n, m, tx  int
}

func (a *afterClickInput) Click(ctx context.Context, b domain.Button, count int, mods []domain.Modifier) error {
	err := a.InputInjector.Click(ctx, b, count, mods)
	a.n++
	if err == nil && a.after != nil {
		a.after(a.n)
	}
	return err
}

func (a *afterClickInput) MoveTo(ctx context.Context, p domain.Point) error {
	err := a.InputInjector.MoveTo(ctx, p)
	a.m++
	if err == nil && a.afterMove != nil {
		a.afterMove(a.m)
	}
	return err
}

func (a *afterClickInput) Text(ctx context.Context, s string) error {
	err := a.InputInjector.Text(ctx, s)
	a.tx++
	if err == nil && a.afterText != nil {
		a.afterText(a.tx)
	}
	return err
}

// ---- 座標・同一性・移動 ----

// 撮影後に T を動かす・リサイズする → 次のアクション（click・key・type）は注入されず target_moved（T 単独の移動を検出）。
func TestAgentScopedTargetMovedStopsEveryActionKind(t *testing.T) {
	actions := map[string]domain.Op{
		"click": leftClick(mp(physInT, boundsT)),
		"key":   agentKey,
		"type":  agentType,
	}
	changes := map[string]func(w *domain.WindowInfo){
		"moved":   func(w *domain.WindowInfo) { w.Rect.X += 40 },
		"resized": func(w *domain.WindowInfo) { w.Rect.W += 30 },
	}
	for an, op := range actions {
		for cn, change := range changes {
			t.Run(an+" after T "+cn, func(t *testing.T) {
				var ae *agentEnv
				ae = newScopedEnv(t, agentOpts{}, withBefore(step("call-1", op), func() { ae.windows.Update(tID, change) }), finalAnswer("never"))
				got := ae.waitTask(t, ae.startScoped(t).ID)
				requireTaskEnd(t, ae, got, domain.TaskFailed, string(domain.ReasonTargetMoved))
				if n := ae.injectionCount(); n != 0 {
					t.Errorf("injections = %d, want 0", n)
				}
				if ae.model.Calls() != 1 {
					t.Errorf("model calls = %d", ae.model.Calls())
				}
				ae.requireNoMonitorCapture(t)
			})
		}
	}
}

// 既存メンバー（メニュー）の矩形だけが変わると target_moved、同一性（ID は同じで PID・開始時刻・クラス）が変わると target_replaced。
// T の同一性が変わる（HWND の再利用）のも target_replaced。
func TestAgentScopedMemberRectAndIdentityChanges(t *testing.T) {
	cases := []struct {
		name   string
		change func(ae *agentEnv)
		reason domain.GroupReason
	}{
		{"member rect only", func(ae *agentEnv) {
			ae.windows.Update(menuID, func(w *domain.WindowInfo) { w.Rect.Y += 10 })
		}, domain.ReasonTargetMoved},
		{"member replaced (HWND reuse)", func(ae *agentEnv) {
			ae.windows.Update(menuID, func(w *domain.WindowInfo) { w.PID, w.ProcessStart = 9999, 99 })
		}, domain.ReasonTargetReplaced},
		{"T replaced (HWND reuse)", func(ae *agentEnv) {
			ae.windows.Update(tID, func(w *domain.WindowInfo) { w.PID, w.ProcessStart, w.Class = 9999, 99, "Other" })
		}, domain.ReasonTargetReplaced},
		{"T closed", func(ae *agentEnv) { ae.windows.Remove(tID) }, domain.ReasonTargetClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ae *agentEnv
			ae = newScopedEnv(t, agentOpts{}, withBefore(step("call-1", leftClick(mp(physInT, boundsT))), func() { tc.change(ae) }), finalAnswer("never"))
			ae.openFileMenu(t)
			got := ae.waitTask(t, ae.startScoped(t).ID)
			requireTaskEnd(t, ae, got, domain.TaskFailed, string(tc.reason))
			if n := ae.injectionCount(); n != 0 {
				t.Errorf("injections = %d, want 0", n)
			}
		})
	}
}

// バッチの 2 手目以降（k >= 2）で T が動く・メンバー（保存ダイアログ）の矩形が変わるのは、中断ではなく失敗（target_moved）。
func TestAgentScopedMovesInsideTheBatchFailNotYield(t *testing.T) {
	changes := map[string]func(ae *agentEnv){
		"T moved":             func(ae *agentEnv) { ae.windows.Update(tID, func(w *domain.WindowInfo) { w.Rect.Y += 12 }) },
		"member rect moved":   func(ae *agentEnv) { ae.windows.Update(dialogID, func(w *domain.WindowInfo) { w.Rect.X += 12 }) },
		"member rect resized": func(ae *agentEnv) { ae.windows.Update(dialogID, func(w *domain.WindowInfo) { w.Rect.H += 12 }) },
	}
	inDialog := domain.Point{X: 550, Y: 110} // タイトル部分（ボタンではない）
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			var ae *agentEnv
			ae = newScopedEnv(t, agentOpts{wrapInput: func(in platform.InputInjector) platform.InputInjector {
				return &afterClickInput{InputInjector: in, after: func(n int) {
					if n == 1 {
						change(ae)
					}
				}}
			}}, step("call-1", leftClick(mp(inDialog, boundsDialog)), leftClick(mp(inDialog, boundsDialog)), agentKey), finalAnswer("never"))
			ae.openSaveDialog(t)
			got := ae.waitTask(t, ae.startScoped(t).ID)
			requireTaskEnd(t, ae, got, domain.TaskFailed, string(domain.ReasonTargetMoved))
			if ae.clicks() != 1 || ae.injectionCount() != 2 { // 1 手目の MoveTo + Click だけ
				t.Errorf("clicks = %d, injections = %d, want 1 click only", ae.clicks(), ae.injectionCount())
			}
			if y := batchYields(t, ae, got.ID); len(y) != 0 {
				t.Errorf("batch_yielded = %v, want none", y)
			}
		})
	}
}

// バッチの 2 手目（k = 2）がキー・文字のときも、T が動いていれば注入されず target_moved（中断ではなく失敗）。
func TestAgentScopedMoveBeforeSecondKeyOrTypeFails(t *testing.T) {
	for name, op := range map[string]domain.Op{"key": agentKey, "type": agentType} {
		t.Run(name, func(t *testing.T) {
			var ae *agentEnv
			ae = newScopedEnv(t, agentOpts{wrapInput: func(in platform.InputInjector) platform.InputInjector {
				return &afterClickInput{InputInjector: in, after: func(n int) {
					if n == 1 {
						ae.windows.Update(tID, func(w *domain.WindowInfo) { w.Rect.X += 20 })
					}
				}}
			}}, step("call-1", leftClick(mp(physInT, boundsT)), op), finalAnswer("never"))
			got := ae.waitTask(t, ae.startScoped(t).ID)
			requireTaskEnd(t, ae, got, domain.TaskFailed, string(domain.ReasonTargetMoved))
			if ae.clicks() != 1 || len(ae.input.CallsTo("Key")) != 0 || len(ae.input.CallsTo("Text")) != 0 {
				t.Errorf("clicks = %d, keys = %d, texts = %d, want only the first click",
					ae.clicks(), len(ae.input.CallsTo("Key")), len(ae.input.CallsTo("Text")))
			}
			if y := batchYields(t, ae, got.ID); len(y) != 0 {
				t.Errorf("batch_yielded = %v, want none", y)
			}
		})
	}
}

// 前面・座標の最上位・葉が S のメンバーでなければ、注入 0 で failed（#156 の理由）。
func TestAgentScopedNonMemberTargetsStop(t *testing.T) {
	other := otherWindow("9001", domain.Rect{X: 600, Y: 420, W: 100, H: 80}, true)
	cases := []struct {
		name   string
		op     domain.Op
		change func(ae *agentEnv)
		reason domain.GroupReason
	}{
		{"foreground is another window (key)", agentKey, func(ae *agentEnv) { ae.windows.Add(other) }, domain.ReasonTargetNotForeground},
		{"foreground is a cross-process dialog owned by T (type)", agentType, func(ae *agentEnv) {
			ae.windows.Add(ownedWindow("9002", domain.Rect{X: 520, Y: 100, W: 100, H: 100}, platformtest.FakeAppCommonDialogPID, platformtest.FakeAppCommonDialogStart, true))
		}, domain.ReasonRelatedCrossProcess},
		{"foreground is an unrelated same-process window (key)", agentKey, func(ae *agentEnv) {
			ae.windows.Add(sameProcessWindow("9003", domain.Rect{X: 520, Y: 100, W: 100, H: 100}, true))
		}, domain.ReasonUnrelatedSameProcess},
		{"click on another window covering T", leftClick(mp(physInT, boundsT)), func(ae *agentEnv) {
			ae.windows.Add(otherWindow("9004", domain.Rect{X: 250, Y: 250, W: 100, H: 100}, false))
		}, domain.ReasonTargetObscured},
		{"click outside the group", leftClick(domain.Point{X: 400, Y: 300}), func(*agentEnv) {}, domain.ReasonOutsideTargetGroup},
		{"click on an ownerless menu over T", leftClick(mp(physInT, boundsT)), func(ae *agentEnv) {
			ae.windows.Add(domain.WindowInfo{
				ID: "9005", Process: "fakenotepad.exe", PID: platformtest.FakeAppPID, ProcessStart: platformtest.FakeAppStart, Class: domain.ClassMenu,
				Rect: domain.Rect{X: 250, Y: 250, W: 100, H: 100},
			})
		}, domain.ReasonRelatedUnverifiable},
		{"leaf under the click belongs to another process", leftClick(mp(physInT, boundsT)), func(ae *agentEnv) {
			ae.windows.AddChild(tID, platformtest.FakeChild{
				Rect: domain.Rect{X: 250, Y: 250, W: 100, H: 100},
				Leaf: domain.LeafRef{WindowRef: domain.WindowRef{ID: "leaf", PID: 7000, ProcessStart: 7, Class: "Child"}, Process: "other.exe"},
			})
		}, domain.ReasonRelatedCrossProcess},
		{"focus leaf belongs to another process (key)", agentKey, func(ae *agentEnv) {
			ae.windows.SetFocusLeaf(tID, domain.LeafRef{WindowRef: domain.WindowRef{ID: "leaf", PID: 7000, ProcessStart: 7, Class: "Child"}, Process: "other.exe"})
		}, domain.ReasonRelatedCrossProcess},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ae *agentEnv
			ae = newScopedEnv(t, agentOpts{}, withBefore(step("call-1", tc.op), func() { tc.change(ae) }), finalAnswer("never"))
			got := ae.waitTask(t, ae.startScoped(t).ID)
			requireTaskEnd(t, ae, got, domain.TaskFailed, string(tc.reason))
			if n := ae.injectionCount(); n != 0 {
				t.Errorf("injections = %d, want 0", n)
			}
			if ae.model.Calls() != 1 {
				t.Errorf("model calls = %d, want 1", ae.model.Calls())
			}
		})
	}
}

// 台本の 2 手目で前面が非メンバーに替わる → 2 手目は注入されず failed（1 手目は届く）。
func TestAgentScopedSecondStepWithNonMemberForegroundIsNotInjected(t *testing.T) {
	var ae *agentEnv
	ae = newScopedEnv(t, agentOpts{},
		step("call-1", leftClick(mp(physInT, boundsT))),
		withBefore(step("call-2", agentKey), func() { ae.windows.Add(otherWindow("9001", domain.Rect{X: 600, Y: 420, W: 100, H: 80}, true)) }),
		finalAnswer("never"))
	got := ae.waitTask(t, ae.startScoped(t).ID)
	requireTaskEnd(t, ae, got, domain.TaskFailed, string(domain.ReasonTargetNotForeground))
	if ae.clicks() != 1 || len(ae.input.CallsTo("Key")) != 0 {
		t.Errorf("clicks = %d, keys = %d, want 1 and 0", ae.clicks(), len(ae.input.CallsTo("Key")))
	}
}

// 座標は基準 S の原点・縮小率で物理座標に変換される（外接矩形が変わらないバッチの 2 手目も同じ）。
func TestAgentScopedCoordinatesUseTheBaseline(t *testing.T) {
	ae := newScopedEnv(t, agentOpts{},
		step("call-1", leftClick(mp(physInT, boundsT)), leftClick(mp(physFileButton, boundsT))), finalAnswer("done"))
	got := ae.waitTask(t, ae.startScoped(t).ID)
	requireTaskEnd(t, ae, got, domain.TaskCompleted, "")
	moves := ae.input.CallsTo("MoveTo")
	if len(moves) != 2 {
		t.Fatalf("MoveTo calls = %d, want 2", len(moves))
	}
	for i, want := range []domain.Point{physInT, physFileButton} {
		p := moves[i].Args[0].(domain.Point)
		if abs(p.X-want.X) > 2 || abs(p.Y-want.Y) > 2 { // 縮小（0.8 倍）の丸めの範囲
			t.Errorf("click %d at %v, want about %v", i+1, p, want)
		}
	}
	if y := batchYields(t, ae, got.ID); len(y) != 0 {
		t.Errorf("unexpected yield %v", y)
	}
	// バッチの中で画像を撮らない: 撮影は最初の 1 回とバッチのあとの 1 回（モデルの呼び出しごとに 1 回）だけ。
	if ae.capturer.GroupGrabCalls() != ae.model.Calls() {
		t.Errorf("GrabGroup calls = %d, model calls = %d (a capture happened inside the batch)", ae.capturer.GroupGrabCalls(), ae.model.Calls())
	}
	ae.requireNoMonitorCapture(t)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// monitor の指定はスコープ付きでは使えない（グループ合成画像の座標にモニターは無い）: 注入 0・failed。
func TestAgentScopedMonitorArgumentIsRejected(t *testing.T) {
	c := leftClick(domain.Point{X: 10, Y: 10})
	c.Monitor = 0
	ae := newScopedEnv(t, agentOpts{}, step("call-1", c), finalAnswer("never"))
	got := ae.waitTask(t, ae.startScoped(t).ID)
	if got.Status != domain.TaskFailed || ae.injectionCount() != 0 {
		t.Fatalf("task = %s, injections = %d", got.Status, ae.injectionCount())
	}
	if _, err := newAgentScope(domain.WindowRef{}).convert(c); domain.CodeOf(err) != domain.CodeInvalidArgument {
		t.Errorf("convert err = %v, want invalid_argument", err)
	}
}

// ---- 中断（対象グループの変化） ----

// 成功経路（偽 platform 版。実機の代わりにならない）: 1 バッチ内の click→click で、1 手目がメニューを開くと 2 手目は注入されず、失敗にならず、
// 撮り直した画像（メニューが写る）から続け、次の応答の click が新しい S で検証されて届き、保存ダイアログが開き、ファイル名を入力して保存する。
func TestAgentScopedBatchYieldsWhenTheMenuOpensThenSaves(t *testing.T) {
	var clicksAtSecondCall int
	var ae *agentEnv
	ae = newScopedEnv(t, agentOpts{},
		step("call-1", leftClick(mp(physFileButton, boundsT)), leftClick(mp(physFileItem, boundsT))),
		withBefore(step("call-2", leftClick(mp(physFileItem, boundsT))), func() { clicksAtSecondCall = ae.clicks() }),
		step("call-3", agentTypeText("report.txt"), agentKey),
		finalAnswer("saved"))
	got := ae.waitTask(t, ae.startScoped(t).ID)
	requireTaskEnd(t, ae, got, domain.TaskCompleted, "")
	if clicksAtSecondCall != 1 {
		t.Errorf("clicks when the second response arrived = %d, want 1 (the 2nd click of the batch was not injected)", clicksAtSecondCall)
	}
	if saved := ae.app.Saved(); len(saved) != 1 || saved[0] != "report.txt" {
		t.Errorf("saved = %v", saved)
	}
	y := batchYields(t, ae, got.ID)
	if len(y) != 1 || y[0]["executed"] != float64(1) || y[0]["not_executed"] != float64(1) || y[0]["reason"] != "group_changed" ||
		y[0]["added"] != float64(1) || y[0]["removed"] != float64(0) {
		t.Fatalf("batch_yielded = %v", y)
	}
	if ae.capturer.GroupGrabCalls() != ae.model.Calls() {
		t.Errorf("GrabGroup calls = %d, model calls = %d (a capture happened inside a batch)", ae.capturer.GroupGrabCalls(), ae.model.Calls())
	}
	// 中断された 2 手目（実行していない）は agent.step の行を残さない（result ok は「実行して成功」に読めるため）。
	// 実行した 4 手だけが agent.step の行になり、すべて ok。中断は agent.batch_yielded の 1 行だけが示す。
	var stepRows int
	for _, e := range ae.auditOf(got.ID) {
		if e.Event != agentStepAuditEvent {
			continue
		}
		stepRows++
		if e.Result != auditOK || detailOf(t, e)["reason"] == ReasonGroupChanged {
			t.Errorf("agent.step row = %s %v, want only executed ok rows (no row for a yielded op)", e.Result, detailOf(t, e))
		}
	}
	if stepRows != 4 { // 1 手目の click・次の応答の click・type・key の 4 つ（中断された 2 手目は含まない）
		t.Errorf("agent.step rows = %d, want 4 (the yielded op has no row)", stepRows)
	}
	if got.OpenAISteps != 4 || got.FastSteps != 0 {
		t.Errorf("steps = %d openai / %d fast", got.OpenAISteps, got.FastSteps)
	}
	ae.requireNoMonitorCapture(t)
	// 中断後の撮影（2 回目のモデルの呼び出しの画像）にメニュー（T の内側）が写る: メニュー領域の画素はメニューの色。
	img, err := png.Decode(bytes.NewReader(ae.model.images[1]))
	if err != nil {
		t.Fatal(err)
	}
	q := mp(physFileItem, boundsT)
	r, g, b, _ := img.At(q.X, q.Y).RGBA()
	if c := platformtest.WindowColor(menuID); uint8(r>>8) != c.R || uint8(g>>8) != c.G || uint8(b>>8) != c.B {
		t.Errorf("the re-captured image does not show the menu at the item (%d,%d,%d)", r>>8, g>>8, b>>8)
	}
	for _, e := range ae.auditOf(got.ID) {
		if bytes.Contains(e.DetailJSON, []byte("report.txt")) {
			t.Errorf("typed text in audit %s: %s", e.Event, e.DetailJSON)
		}
	}
}

// 右クリックメニューの成功経路（偽 platform 版。実機の代わりにならない）: 右クリックで偽のコンテキストメニューが開き（単独のアクションなので中断ではない）、
// 次の応答（撮り直した画像）で「すべて選択」をクリックすると新しい S で検証されて届く。モニター撮影は 0。
func TestAgentScopedRightClickMenuThenSelectAll(t *testing.T) {
	menuAt := domain.Point{X: 300, Y: 250} // クリック位置が右クリックメニューの左上
	right := leftClick(mp(menuAt, boundsT))
	right.Button = domain.ButtonRight
	selectAll := domain.Point{X: menuAt.X + 10, Y: menuAt.Y + 15} // メニューの上半分
	ae := newScopedEnv(t, agentOpts{},
		step("call-1", right),
		step("call-2", leftClick(mp(selectAll, boundsT))),
		finalAnswer("selected"))
	got := ae.waitTask(t, ae.startScoped(t).ID)
	requireTaskEnd(t, ae, got, domain.TaskCompleted, "")
	if !ae.app.Selected() || ae.app.MenuOpen() != "" {
		t.Errorf("selected = %v, menu = %q, want select-all done and the menu closed", ae.app.Selected(), ae.app.MenuOpen())
	}
	if ae.clicks() != 2 || len(batchYields(t, ae, got.ID)) != 0 {
		t.Errorf("clicks = %d, yields = %v", ae.clicks(), batchYields(t, ae, got.ID))
	}
	// 2 回目のモデル呼び出しの画像に右クリックメニュー（メニューの色）が写る。
	img, err := png.Decode(bytes.NewReader(ae.model.images[1]))
	if err != nil {
		t.Fatal(err)
	}
	q := mp(selectAll, boundsT)
	r, g, b, _ := img.At(q.X, q.Y).RGBA()
	if c := platformtest.WindowColor(menuID); uint8(r>>8) != c.R || uint8(g>>8) != c.G || uint8(b>>8) != c.B {
		t.Errorf("the re-captured image does not show the context menu (%d,%d,%d)", r>>8, g>>8, b>>8)
	}
	ae.requireNoMonitorCapture(t)
}

func agentTypeText(s string) domain.Op { return domain.TypeText{Text: s} }

// k = 1: 撮影後、最初のアクションの前にメンバーが増える（ダイアログが出る）/ 減る → 注入 0、失敗にならず、撮影し直して続行（agent.batch_yielded、実行 0 件）。
// 境界: 同じ場面で「増減だけ」は中断、「T の Rect も変わった」は target_moved の failed。
func TestAgentScopedYieldAtTheFirstActionAndBoundary(t *testing.T) {
	added := func(ae *agentEnv) {
		ae.windows.Add(ownedWindow("9101", domain.Rect{X: 450, Y: 120, W: 100, H: 60}, platformtest.FakeAppPID, platformtest.FakeAppStart, false))
	}
	removed := func(ae *agentEnv) { ae.windows.Remove(menuID) }
	moveT := func(ae *agentEnv) { ae.windows.Update(tID, func(w *domain.WindowInfo) { w.Rect.X += 5 }) }
	cases := []struct {
		name      string
		preMenu   bool
		change    func(ae *agentEnv)
		wantYield bool
		added     float64
		removed   float64
	}{
		{"member added", false, added, true, 1, 0},
		{"member removed", true, removed, true, 0, 1},
		{"member added and T moved", false, func(ae *agentEnv) { added(ae); moveT(ae) }, false, 0, 0},
		{"member removed and T moved", true, func(ae *agentEnv) { removed(ae); moveT(ae) }, false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ae *agentEnv
			ae = newScopedEnv(t, agentOpts{},
				withBefore(step("call-1", leftClick(mp(physInT, boundsT))), func() { tc.change(ae) }),
				step("call-2", leftClick(mp(physInT, boundsT))),
				finalAnswer("done"))
			if tc.preMenu {
				ae.openFileMenu(t)
			}
			got := ae.waitTask(t, ae.startScoped(t).ID)
			if !tc.wantYield {
				requireTaskEnd(t, ae, got, domain.TaskFailed, string(domain.ReasonTargetMoved))
				if ae.injectionCount() != 0 || len(batchYields(t, ae, got.ID)) != 0 {
					t.Errorf("injections = %d, yields = %v", ae.injectionCount(), batchYields(t, ae, got.ID))
				}
				return
			}
			requireTaskEnd(t, ae, got, domain.TaskCompleted, "")
			y := batchYields(t, ae, got.ID)
			if len(y) != 1 || y[0]["executed"] != float64(0) || y[0]["not_executed"] != float64(1) || y[0]["added"] != tc.added || y[0]["removed"] != tc.removed {
				t.Fatalf("batch_yielded = %v", y)
			}
			if ae.clicks() != 1 { // 2 つ目の応答の click だけ（中断した 1 つ目は 0 件）
				t.Errorf("clicks = %d, want 1 (only the next response's click)", ae.clicks())
			}
			if ae.model.Calls() != 3 {
				t.Errorf("model calls = %d, want 3 (the yield used one model call)", ae.model.Calls())
			}
		})
	}
}

// バッチ [click(保存), key(Enter)] で 1 手目がダイアログを閉じる（メンバー減）→ 2 手目は注入されず、failed にならない。
func TestAgentScopedYieldWhenTheFirstActionClosesTheDialog(t *testing.T) {
	ae := newScopedEnv(t, agentOpts{},
		step("call-1", leftClick(mp(physFileButton, boundsT)), leftClick(mp(physFileItem, boundsT))), // メニュー → 中断
		step("call-2", leftClick(mp(physFileItem, boundsT))),                                         // ダイアログが開く
		step("call-3", leftClick(mp(physSaveButton, boundsDialog)), agentKey),
		finalAnswer("done"))
	got := ae.waitTask(t, ae.startScoped(t).ID)
	requireTaskEnd(t, ae, got, domain.TaskCompleted, "")
	if len(ae.app.Saved()) != 1 || len(ae.input.CallsTo("Key")) != 0 {
		t.Errorf("saved = %v, keys = %d, want saved once and the Enter never injected", ae.app.Saved(), len(ae.input.CallsTo("Key")))
	}
	y := batchYields(t, ae, got.ID)
	if len(y) != 2 || y[1]["executed"] != float64(1) || y[1]["removed"] != float64(1) {
		t.Fatalf("batch_yielded = %v", y)
	}
}

// 中断は 1 回のモデル呼び出しを使うので、グループが変わり続けても max_steps・max_duration_sec で終わる。
func TestAgentScopedYieldCountsAgainstLimits(t *testing.T) {
	toggling := func(ae *agentEnv) func() {
		n := 0
		return func() {
			n++
			ae.windows.Remove("9201")
			if n%2 == 1 {
				ae.windows.Add(ownedWindow("9201", domain.Rect{X: 450, Y: 120, W: 100, H: 60}, platformtest.FakeAppPID, platformtest.FakeAppStart, false))
			}
		}
	}
	t.Run("max_steps", func(t *testing.T) {
		var ae *agentEnv
		spec := func(id string) modelStepSpec {
			return withBefore(step(id, leftClick(mp(physInT, boundsT))), func() { toggling(ae)() })
		}
		ae = newScopedEnv(t, agentOpts{}, spec("c1"), spec("c2"), spec("c3"), spec("c4"), spec("c5"))
		task, err := ae.agent.Start(context.Background(), ae.by, StartInput{Prompt: "p", WindowID: tID, MaxSteps: 3})
		if err != nil {
			t.Fatal(err)
		}
		got := ae.waitTask(t, task.ID)
		if got.Status != domain.TaskLimitReached || ae.model.Calls() != 3 {
			t.Fatalf("task = %s, model calls = %d, want limit_reached after 3", got.Status, ae.model.Calls())
		}
		if len(batchYields(t, ae, got.ID)) == 0 {
			t.Error("no yield happened")
		}
	})
	t.Run("max_duration_sec", func(t *testing.T) {
		var ae *agentEnv
		tick := func() { ae.clock.Advance(11 * time.Second) }
		spec := func(id string) modelStepSpec {
			return withBefore(step(id, leftClick(mp(physInT, boundsT))), func() {
				tick()
				ae.windows.Add(ownedWindow("9201", domain.Rect{X: 450, Y: 120, W: 100, H: 60}, platformtest.FakeAppPID, platformtest.FakeAppStart, false))
			})
		}
		ae = newScopedEnv(t, agentOpts{}, spec("c1"), spec("c2"), spec("c3"), spec("c4"))
		task, err := ae.agent.Start(context.Background(), ae.by, StartInput{Prompt: "p", WindowID: tID, MaxDurationSec: 30})
		if err != nil {
			t.Fatal(err)
		}
		got := ae.waitTask(t, task.ID)
		if got.Status != domain.TaskLimitReached {
			t.Fatalf("task = %s, want limit_reached", got.Status)
		}
	})
}

// 中断のあとの次のモデル呼び出しが失敗（5xx 相当）してタスクが failed で終わっても、実行済みの入力は再実行・再送されない。
func TestAgentScopedModelFailureAfterYieldDoesNotReplayInput(t *testing.T) {
	ae := newScopedEnv(t, agentOpts{},
		step("call-1", leftClick(mp(physFileButton, boundsT)), leftClick(mp(physFileItem, boundsT))),
		modelStepSpec{err: context.DeadlineExceeded}, finalAnswer("never"))
	got := ae.waitTask(t, ae.startScoped(t).ID)
	if got.Status != domain.TaskFailed || got.ErrorCode != domain.TaskErrorOpenAI {
		t.Fatalf("task = %s / %s", got.Status, got.ErrorCode)
	}
	if ae.clicks() != 1 || ae.model.Calls() != 2 {
		t.Errorf("clicks = %d, model calls = %d, want 1 and 2 (no retry)", ae.clicks(), ae.model.Calls())
	}
	// 同じアクションを次の応答がもう一度返したときだけ、新しい S で検証されて実行される。
	ae2 := newScopedEnv(t, agentOpts{},
		step("call-1", leftClick(mp(physFileButton, boundsT)), leftClick(mp(physFileItem, boundsT))),
		step("call-2", leftClick(mp(physFileItem, boundsT))), finalAnswer("done"))
	got2 := ae2.waitTask(t, ae2.startScoped(t).ID)
	requireTaskEnd(t, ae2, got2, domain.TaskCompleted, "")
	if ae2.clicks() != 2 || !ae2.app.DialogOpen() {
		t.Errorf("clicks = %d, dialog open = %v", ae2.clicks(), ae2.app.DialogOpen())
	}
}

// ---- 高速パス・クリップボード ----

// window_id つきのタスクは、高速パスが設定で有効・同意済みでも使わない（Chooser・UIA の読み取りが 0 回、fast_steps = 0、handoff_reason なし）。
func TestAgentScopedSkipsTheFastPath(t *testing.T) {
	chooser := newScriptedChooser(chooserStepSpec{res: choice("x", 0.99, 0)})
	ae := newAgentEnv(t, agentOpts{
		app: true, fast: chooser, script: []modelStepSpec{finalAnswer("done")},
		settings: map[string]string{SettingFastpathEnabled: "true", SettingFastpathConsentAt: "1790000000001"},
	})
	if err := ae.secrets.Set(platform.SecretTypeSafe, "ts-test-placeholder"); err != nil {
		t.Fatal(err)
	}
	got := ae.waitTask(t, ae.startScoped(t).ID)
	requireTaskEnd(t, ae, got, domain.TaskCompleted, "")
	if len(chooser.Requests()) != 0 || len(ae.ui.Calls()) != 0 || got.FastSteps != 0 || got.HandoffReason != "" {
		t.Errorf("chooser requests = %d, UIA calls = %d, fast_steps = %d, handoff = %q", len(chooser.Requests()), len(ae.ui.Calls()), got.FastSteps, got.HandoffReason)
	}
	img, err := png.Decode(bytes.NewReader(ae.model.images[0]))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 320 || b.Dy() != 240 {
		t.Errorf("first model image = %v, want the group composite 320x240 (not the 960x540 monitor)", b)
	}
	ae.requireNoMonitorCapture(t)
}

// エージェントのリース（スコープ付き）の ClipboardRead は clipboard_out_of_scope で拒否され（platform の読み取り 0）、ClipboardWrite は通る。
// エージェントの computer ツールにクリップボード操作は無い（internal/openai の mapActions の試験）。
func TestAgentScopedClipboardRule(t *testing.T) {
	var ae *agentEnv
	var readErr, writeErr error
	ae = newScopedEnv(t, agentOpts{}, withBefore(step("call-1"), func() {
		who := domain.Actor{Kind: domain.ActorAgent, ClientID: "client-a", Label: "alice", SessionID: "task-1"}
		_, readErr = ae.control.Do(context.Background(), who, domain.ClipboardRead{})
		_, writeErr = ae.control.Do(context.Background(), who, domain.ClipboardWrite{Text: "hello"})
	}), finalAnswer("done"))
	got := ae.waitTask(t, ae.startScoped(t).ID)
	requireTaskEnd(t, ae, got, domain.TaskCompleted, "")
	requireBlockedReason(t, readErr, domain.ReasonClipboardOutOfScope)
	if writeErr != nil {
		t.Errorf("ClipboardWrite = %v", writeErr)
	}
	if n := len(ae.clipboard.CallsTo("ReadText")); n != 0 {
		t.Errorf("platform ReadText calls = %d, want 0", n)
	}
	if n := len(ae.clipboard.CallsTo("WriteText")); n != 1 {
		t.Errorf("platform WriteText calls = %d, want 1", n)
	}
}

// PUPPET_REMO_DUMP_DIR を指定すると、偽モデルに渡ったグループ合成画像（PNG）をそこへ書く（docs/screenshots/issue-158 用。通常は何もしない）。
// 偽 platform の画像（単色のウィンドウ）で、実機の画像ではない。
func TestAgentScopedDumpModelImages(t *testing.T) {
	dir := os.Getenv("PUPPET_REMO_DUMP_DIR")
	if dir == "" {
		t.Skip("PUPPET_REMO_DUMP_DIR is not set")
	}
	ae := newScopedEnv(t, agentOpts{},
		step("call-1", leftClick(mp(physFileButton, boundsT)), leftClick(mp(physFileItem, boundsT))),
		step("call-2", leftClick(mp(physFileItem, boundsT))),
		finalAnswer("done"))
	ae.waitTask(t, ae.startScoped(t).ID)
	for i, name := range []string{"model-image-1-initial", "model-image-2-menu-opened", "model-image-3-save-dialog-outside-T"} {
		if err := os.WriteFile(filepath.Join(dir, name+".png"), ae.model.images[i], 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
