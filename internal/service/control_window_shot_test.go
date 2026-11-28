package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// このファイルは screenshot の window_id（WindowScreenshot。#269。FR-M26）のテスト。tgtEnv の偽アプリ（T は 1001）を対象にする。
// モニターは ctlMonitor（3200x1800、最大辺 1568 → 縮小率 0.49）。

func (te *tgtEnv) windowShot(req WindowShotRequest) (WindowShot, error) {
	te.t.Helper()
	return te.ctl.WindowScreenshot(context.Background(), req)
}

func decodePNG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func wantInputBlocked(t *testing.T, err error, reason string) *domain.Error {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || !errors.Is(err, domain.ErrInputBlocked) {
		t.Fatalf("err = %v, want input_blocked(%s)", err, reason)
	}
	if got := de.Detail["reason"]; got != reason {
		t.Errorf("reason = %v, want %s", got, reason)
	}
	return de
}

// window_id のウィンドウだけの画像が返り、画素はそのウィンドウのもの。モニター撮影（Grab）は呼ばない。
func TestWindowScreenshotReturnsOnlyThatWindow(t *testing.T) {
	te := newTgtEnv(t)
	shot, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID})
	if err != nil {
		t.Fatal(err)
	}
	if shot.MIME != "image/png" || shot.WindowID != platformtest.FakeAppMainID || shot.Monitor != 0 {
		t.Fatalf("shot = %+v", shot)
	}
	// T の物理矩形 (100,100,400,300) → 縮小座標 (49,49)-(245,196)。画像の大きさと Rect が一致し、物理の大きさは T のもの。
	if want := (domain.Rect{X: 49, Y: 49, W: 196, H: 147}); shot.Rect != want {
		t.Errorf("Rect = %v, want %v", shot.Rect, want)
	}
	if shot.Scaled != (domain.Size{W: 196, H: 147}) || shot.Physical != (domain.Size{W: 400, H: 300}) {
		t.Errorf("Scaled = %v, Physical = %v", shot.Scaled, shot.Physical)
	}
	img := decodePNG(t, shot.Data)
	if b := img.Bounds(); b.Dx() != 196 || b.Dy() != 147 {
		t.Fatalf("image = %v, want 196x147", b)
	}
	want := platformtest.WindowColor(platformtest.FakeAppMainID)
	r, g, b, _ := img.At(98, 70).RGBA()
	if int(r>>8)-int(want.R) > 2 || int(want.R)-int(r>>8) > 2 || int(g>>8)-int(want.G) > 2 || int(want.G)-int(g>>8) > 2 || int(b>>8)-int(want.B) > 2 || int(want.B)-int(b>>8) > 2 {
		t.Errorf("pixel = %d,%d,%d, want %v (the window's own color)", r>>8, g>>8, b>>8, want)
	}
	if len(shot.Members) != 1 || shot.Members[0].WindowID != platformtest.FakeAppMainID || shot.Members[0].Relation != domain.RelationTarget || shot.Members[0].Rect != shot.Rect {
		t.Errorf("Members = %+v", shot.Members)
	}
	if len(shot.Omitted) != 0 {
		t.Errorf("Omitted = %+v", shot.Omitted)
	}
	if shot.Scale != 1568.0/3200.0 {
		t.Errorf("Scale = %v", shot.Scale)
	}
	te.capturer.AssertNoMonitorCapture(t)
	if te.capturer.GroupGrabCalls() != 1 {
		t.Errorf("GrabGroup calls = %d, want 1", te.capturer.GroupGrabCalls())
	}
}

// jpeg を選べ、不明な形式は拒否する。
func TestWindowScreenshotFormat(t *testing.T) {
	te := newTgtEnv(t)
	shot, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID, Format: "jpeg"})
	if err != nil || shot.MIME != "image/jpeg" {
		t.Fatalf("shot = %+v, err = %v", shot, err)
	}
	if _, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID, Format: "gif"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

// 画像の座標は縮小座標へそのまま対応づく: ウィンドウ内の点（物理）→ 画像の点 → Rect を足した縮小座標が、モニターの縮小座標（クリックの x・y）と ±1 で一致する。
func TestWindowScreenshotCoordinatesMapToMonitorScaledCoordinates(t *testing.T) {
	te := newTgtEnv(t)
	shot, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID})
	if err != nil {
		t.Fatal(err)
	}
	phys := domain.Point{X: 300, Y: 250} // T の中の点
	want := scaled(phys)                 // モニターの縮小スクリーンショットでの座標
	// 画像上の点: T の左上からの物理オフセットに縮小率を掛けたもの
	px := int(float64(phys.X-platformtest.FakeAppLayout.Main.X)*shot.Scale + 0.5)
	py := int(float64(phys.Y-platformtest.FakeAppLayout.Main.Y)*shot.Scale + 0.5)
	if dx, dy := shot.Rect.X+px-want.X, shot.Rect.Y+py-want.Y; dx < -1 || dx > 1 || dy < -1 || dy > 1 {
		t.Errorf("mapped (%d,%d), monitor scaled %v", shot.Rect.X+px, shot.Rect.Y+py, want)
	}
}

// window_id が不正・存在しない・空のとき、撮らずに拒否する。
func TestWindowScreenshotRejectsUnknownWindow(t *testing.T) {
	tests := []struct {
		name     string
		windowID string
		want     error
	}{
		{"存在しない id", "99999", domain.ErrWindowNotFound},
		{"形式が不正", "not-a-number", domain.ErrWindowNotFound},
		{"空", "", domain.ErrInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			te := newTgtEnv(t)
			_, err := te.windowShot(WindowShotRequest{WindowID: tt.windowID})
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if te.capturer.GroupGrabCalls() != 0 {
				t.Error("GrabGroup was called")
			}
			te.capturer.AssertNoMonitorCapture(t)
		})
	}
}

// 閉じたウィンドウは window_not_found。
func TestWindowScreenshotRejectsAClosedWindow(t *testing.T) {
	te := newTgtEnv(t)
	te.windows.Remove(platformtest.FakeAppMainID)
	if _, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID}); !errors.Is(err, domain.ErrWindowNotFound) {
		t.Fatalf("err = %v, want window_not_found", err)
	}
}

// 自ウィンドウ（NFR-S06）は撮らない（include_owned でも同じ）。own_window で拒否し、範囲を手がかりに付ける。
func TestWindowScreenshotNeverCapturesOwnWindows(t *testing.T) {
	for _, includeOwned := range []bool{false, true} {
		te := newTgtEnv(t)
		te.windows.Add(domain.WindowInfo{
			ID: "9001", Title: "Nefos Remote", Process: "puppet-remo.exe", PID: 777, ProcessStart: 5, Class: "WailsWindow",
			Own: true, Rect: domain.Rect{X: 1600, Y: 0, W: 800, H: 900},
		})
		_, err := te.windowShot(WindowShotRequest{WindowID: "9001", IncludeOwned: includeOwned})
		de := wantInputBlocked(t, err, domain.ReasonOwnWindow)
		if _, ok := de.Detail[DetailOwnWindows]; !ok {
			t.Errorf("includeOwned=%v: detail has no own_windows: %+v", includeOwned, de.Detail)
		}
		if te.capturer.GroupGrabCalls() != 0 {
			t.Errorf("includeOwned=%v: GrabGroup was called for an own window", includeOwned)
		}
		te.capturer.AssertNoMonitorCapture(t)
	}
}

// 自ウィンドウの上に重なっていても、対象の画像に自ウィンドウは写らず（対象自身の内容だけ）、own_windows に範囲が出る。
func TestWindowScreenshotListsOwnWindowsAndDoesNotIncludeThem(t *testing.T) {
	te := newTgtEnv(t)
	te.windows.Add(domain.WindowInfo{
		ID: "9001", Title: "Nefos Remote", Process: "puppet-remo.exe", PID: 777, ProcessStart: 5, Class: "WailsWindow",
		Own: true, Rect: domain.Rect{X: 300, Y: 150, W: 400, H: 300},
	})
	shot, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID, IncludeOwned: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(shot.Members) != 1 || shot.Members[0].WindowID != platformtest.FakeAppMainID {
		t.Fatalf("Members = %+v, want T only", shot.Members)
	}
	if len(shot.OwnWindows) != 1 || shot.OwnWindows[0].Monitor != 0 {
		t.Errorf("OwnWindows = %+v, want the own window", shot.OwnWindows)
	}
	for _, c := range te.capturer.CallsTo("GrabGroup") {
		for _, ref := range c.Args[0].([]domain.WindowRef) {
			if ref.ID == "9001" {
				t.Errorf("own window was passed to GrabGroup")
			}
		}
	}
}

// 昇格・最小化のウィンドウは撮らず、理由つきで拒否する。
func TestWindowScreenshotRefusesElevatedAndMinimized(t *testing.T) {
	te := newTgtEnv(t)
	te.windows.Add(domain.WindowInfo{
		ID: "9002", Title: "admin", Process: "admin.exe", PID: 800, ProcessStart: 6, Class: "AdminClass", Elevated: true,
		Rect: domain.Rect{X: 700, Y: 0, W: 300, H: 300},
	})
	_, err := te.windowShot(WindowShotRequest{WindowID: "9002"})
	_ = wantInputBlocked(t, err, domain.ReasonElevatedWindow)

	te.windows.Update(platformtest.FakeAppMainID, func(w *domain.WindowInfo) { w.Minimized = true })
	_, err = te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID})
	_ = wantInputBlocked(t, err, string(domain.ReasonTargetMinimized))
	if te.capturer.GroupGrabCalls() != 0 {
		t.Error("GrabGroup was called")
	}
}

// 同一性を確かめる値が不明なウィンドウは撮らない（fail closed）。
func TestWindowScreenshotRefusesAnUnverifiableIdentity(t *testing.T) {
	te := newTgtEnv(t)
	te.windows.Add(domain.WindowInfo{ID: "9003", Title: "x", Process: "x.exe", PID: 900, Rect: domain.Rect{X: 0, Y: 0, W: 50, H: 50}})
	_, err := te.windowShot(WindowShotRequest{WindowID: "9003"})
	_ = wantInputBlocked(t, err, string(domain.ReasonTargetUnsupported))
}

// 撮れない（PrintWindow の失敗）ときは、モニター撮影に切り替えず capture_unavailable。
func TestWindowScreenshotDoesNotFallBackToTheMonitor(t *testing.T) {
	te := newTgtEnv(t)
	te.capturer.SetNotPrintable(platformtest.FakeAppMainID, true)
	_, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID})
	_ = wantInputBlocked(t, err, string(domain.ReasonCaptureUnavailable))
	te.capturer.AssertNoMonitorCapture(t)
}

// GroupCapturer を持たない Capturer は capture_unavailable（Grab へ切り替えない）。
func TestWindowScreenshotWithoutGroupCapturer(t *testing.T) {
	te := newTgtEnv(t)
	te.useControl(te.app, plainCapturer{te.capturer})
	_, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID})
	_ = wantInputBlocked(t, err, string(domain.ReasonCaptureUnavailable))
	te.capturer.AssertNoMonitorCapture(t)
}

func TestWindowScreenshotRefusesDuringKillSwitch(t *testing.T) {
	te := newTgtEnv(t)
	if err := te.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID}); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Fatalf("err = %v, want kill_switch_active", err)
	}
	if te.capturer.GroupGrabCalls() != 0 {
		t.Error("GrabGroup was called")
	}
}

// 撮る直前に別のウィンドウに置き換わった（HWND の再利用）なら、撮らず window_not_found。
func TestWindowScreenshotRejectsAReplacedWindow(t *testing.T) {
	te := newTgtEnv(t)
	te.useControl(te.app, replacingCapturer{FakeCapturer: te.capturer, onGrab: func() {
		te.windows.Update(platformtest.FakeAppMainID, func(w *domain.WindowInfo) { w.PID = 4999 })
	}})
	_, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID})
	if !errors.Is(err, domain.ErrWindowNotFound) {
		t.Fatalf("err = %v, want window_not_found", err)
	}
}

// include_owned を付けないと、開いているダイアログは写らない（T だけ。省略時の挙動）。付けると同一プロセスのダイアログが重なり、
// Rect はダイアログを含む外接矩形になる。メンバーは前面が先で、それぞれの Rect を縮小座標で返す。
func TestWindowScreenshotIncludeOwned(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	if err := te.click(ctlAlice, ptFileButton); err != nil {
		t.Fatal(err)
	}
	if err := te.click(ctlAlice, ptFileItem); err != nil { // 保存ダイアログは T の右上へはみ出す
		t.Fatal(err)
	}

	plain, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Members) != 1 || plain.Physical != (domain.Size{W: 400, H: 300}) {
		t.Fatalf("default shot = members %+v, physical %v, want T only", plain.Members, plain.Physical)
	}

	with, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID, IncludeOwned: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(with.Members) != 2 || with.Members[0].WindowID != platformtest.FakeAppDialogID || with.Members[0].Relation != domain.RelationOwned ||
		with.Members[1].WindowID != platformtest.FakeAppMainID {
		t.Fatalf("Members = %+v, want the dialog (front) then T", with.Members)
	}
	// T (100,100,400,300) と ダイアログ (450,80,200,120) の外接矩形 (100,80)-(650,400)
	if with.Physical != (domain.Size{W: 550, H: 320}) {
		t.Errorf("Physical = %v, want 550x320", with.Physical)
	}
	if want := (domain.Rect{X: 49, Y: 39, W: 270, H: 157}); with.Rect != want || with.Scaled != (domain.Size{W: want.W, H: want.H}) {
		t.Errorf("Rect = %v, Scaled = %v, want %v", with.Rect, with.Scaled, want)
	}
	if want := (domain.Rect{X: 49, Y: 49, W: 196, H: 147}); with.Members[1].Rect != want {
		t.Errorf("T rect = %v, want %v", with.Members[1].Rect, want)
	}
	if b := decodePNG(t, with.Data).Bounds(); b.Dx() != with.Scaled.W || b.Dy() != with.Scaled.H {
		t.Errorf("image = %v, want %v", b, with.Scaled)
	}
	te.capturer.AssertNoMonitorCapture(t)
}

// include_owned: 別プロセスの所有ダイアログは自動では写さず、omitted に理由を出す。所有関係を確かめられないメニューも同じ。
func TestWindowScreenshotIncludeOwnedOmitsCrossProcessAndUnverifiable(t *testing.T) {
	te := newTgtEnv(t)
	te.app.DialogCrossProcess = true
	te.acquire(ctlAlice)
	if err := te.click(ctlAlice, ptFileButton); err != nil {
		t.Fatal(err)
	}
	if err := te.click(ctlAlice, ptFileItem); err != nil {
		t.Fatal(err)
	}
	shot, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID, IncludeOwned: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(shot.Members) != 1 || len(shot.Omitted) != 1 || shot.Omitted[0].WindowID != platformtest.FakeAppDialogID ||
		shot.Omitted[0].Reason != domain.ReasonRelatedCrossProcess {
		t.Fatalf("Members = %+v, Omitted = %+v, want T only and the dialog omitted (related_cross_process)", shot.Members, shot.Omitted)
	}
}

// include_owned: 撮れないメンバーは停止せず omitted（capture_member_unavailable）。対象が撮れなければエラー。
func TestWindowScreenshotIncludeOwnedMemberNotPrintable(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	if err := te.click(ctlAlice, ptFileButton); err != nil {
		t.Fatal(err)
	}
	if err := te.click(ctlAlice, ptFileItem); err != nil {
		t.Fatal(err)
	}
	te.capturer.SetNotPrintable(platformtest.FakeAppDialogID, true)
	shot, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID, IncludeOwned: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(shot.Members) != 1 || len(shot.Omitted) != 1 || shot.Omitted[0].Reason != domain.ReasonCaptureMemberUnavailable {
		t.Fatalf("Members = %+v, Omitted = %+v", shot.Members, shot.Omitted)
	}
	te.capturer.SetNotPrintable(platformtest.FakeAppMainID, true)
	_, err = te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppMainID, IncludeOwned: true})
	_ = wantInputBlocked(t, err, string(domain.ReasonCaptureUnavailable))
}

// include_owned の window_id がダイアログ（メンバー）自身なら、そのダイアログを T とする別のグループ（T は撮らない）。
func TestWindowScreenshotOfADialogIsJustThatDialog(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	if err := te.click(ctlAlice, ptFileButton); err != nil {
		t.Fatal(err)
	}
	if err := te.click(ctlAlice, ptFileItem); err != nil {
		t.Fatal(err)
	}
	shot, err := te.windowShot(WindowShotRequest{WindowID: platformtest.FakeAppDialogID})
	if err != nil {
		t.Fatal(err)
	}
	if len(shot.Members) != 1 || shot.Members[0].WindowID != platformtest.FakeAppDialogID || shot.Physical != (domain.Size{W: 200, H: 120}) {
		t.Fatalf("shot = %+v", shot)
	}
}

// plainCapturer は platform.Capturer だけを公開する（GroupCapturer を持たない Capturer）。
type plainCapturer struct{ platform.Capturer }

// replacingCapturer は GrabGroup の直前に onGrab を実行する（一覧を読んだあと・撮る前にウィンドウが変わる競合の再現）。
type replacingCapturer struct {
	*platformtest.FakeCapturer
	onGrab func()
}

func (r replacingCapturer) GrabGroup(ctx context.Context, members []domain.WindowRef) (platform.GroupImage, error) {
	r.onGrab()
	return r.FakeCapturer.GrabGroup(ctx, members)
}
