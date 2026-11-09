package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// GrabGroupFrame は T と G のメンバーだけの合成を返し、撮れないときはモニター撮影に切り替えず理由つきの ErrInputBlocked にする。
func TestGrabGroupFrameComposesOnlyTheMembers(t *testing.T) {
	te := newTgtEnv(t)
	frame, err := te.ctl.GrabGroupFrame(context.Background(), te.app.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if frame.Image == nil || len(frame.Members) != 1 || frame.Members[0].Ref != te.app.Ref() || frame.Members[0].Relation != domain.RelationTarget {
		t.Fatalf("frame = %+v, want T only", frame)
	}
	if frame.Partial() {
		t.Errorf("Excluded = %+v, want none", frame.Excluded)
	}
	te.capturer.AssertNoMonitorCapture(t)
}

// 保存ダイアログが開いたら、T の矩形の外へ出るダイアログが合成に入り、画像が広がる。座標は Origin + p。
func TestGrabGroupFrameGrowsWhenAnOwnedDialogOpensOutsideT(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	before, err := te.ctl.GrabGroupFrame(context.Background(), te.app.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if err := te.click(ctlAlice, ptFileButton); err != nil {
		t.Fatal(err)
	}
	if err := te.click(ctlAlice, ptFileItem); err != nil { // 保存ダイアログは T の右へはみ出す
		t.Fatal(err)
	}
	after, err := te.ctl.GrabGroupFrame(context.Background(), te.app.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Members) != 2 {
		t.Fatalf("Members = %+v, want T and the dialog", after.Members)
	}
	if after.Image.Bounds().Dx() <= before.Image.Bounds().Dx() {
		t.Errorf("width = %d, want wider than %d (the dialog sticks out of T)", after.Image.Bounds().Dx(), before.Image.Bounds().Dx())
	}
	// 保存ダイアログは T の右上へはみ出す（y=80 < T の y=100）ので、原点が上へ動く。合成の外接矩形は両方を含む。
	if want := (domain.Point{X: platformtest.FakeAppLayout.Main.X, Y: platformtest.FakeAppLayout.Dialog.Y}); after.Origin != want {
		t.Errorf("Origin = %v, want %v", after.Origin, want)
	}
	te.capturer.AssertNoMonitorCapture(t)
}

func TestGrabGroupFrameStopsWithoutFallingBackToTheMonitor(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(te *tgtEnv)
		wantReason domain.GroupReason
	}{
		{"T が撮れない", func(te *tgtEnv) { te.capturer.SetNotPrintable(platformtest.FakeAppMainID, true) }, domain.ReasonCaptureUnavailable},
		{"T が閉じた", func(te *tgtEnv) { te.windows.SetWindows() }, domain.ReasonTargetClosed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			te := newTgtEnv(t)
			tt.setup(te)
			frame, err := te.ctl.GrabGroupFrame(context.Background(), te.app.Ref())
			var de *domain.Error
			if !errors.As(err, &de) || !errors.Is(err, domain.ErrInputBlocked) {
				t.Fatalf("err = %v, want input_blocked", err)
			}
			if got := de.Detail["reason"]; got != string(tt.wantReason) {
				t.Errorf("reason = %v, want %s", got, tt.wantReason)
			}
			if frame.Image != nil {
				t.Error("Image must be nil on failure")
			}
			te.capturer.AssertNoMonitorCapture(t)
		})
	}
}

func TestGrabGroupFrameRefusesDuringKillSwitch(t *testing.T) {
	te := newTgtEnv(t)
	if err := te.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := te.ctl.GrabGroupFrame(context.Background(), te.app.Ref()); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Fatalf("err = %v, want kill_switch_active", err)
	}
	te.capturer.AssertNoMonitorCapture(t)
}

// 右クリックメニュー（relation=menu）が合成に入り、メンバーは前面が先の順で、Rect は合成に置いた矩形になる。
func TestGrabGroupFrameIncludesTheContextMenuInFrontOfT(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	if err := te.do(ctlAlice, clickAt(domain.ButtonRight, ptContext)); err != nil {
		t.Fatal(err)
	}
	frame, err := te.ctl.GrabGroupFrame(context.Background(), te.app.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Members) != 2 || frame.Members[0].Ref.ID != platformtest.FakeAppMenuID || frame.Members[0].Relation != domain.RelationMenu ||
		frame.Members[1].Ref.ID != platformtest.FakeAppMainID {
		t.Fatalf("Members = %+v, want the menu (front) then T", frame.Members)
	}
	for _, m := range frame.Members {
		if m.Rect.Empty() {
			t.Errorf("member %s has an empty Rect", m.Ref.ID)
		}
	}
	te.capturer.AssertNoMonitorCapture(t)
}

// 所有者を確かめられないメニューは撮らず合成に入れず、Excluded に出る（capture_partial の通知。停止しない）。
func TestGrabGroupFrameExcludesAnUnverifiableMenu(t *testing.T) {
	te := newTgtEnv(t)
	te.app.OwnerlessMenu = true
	te.acquire(ctlAlice)
	if err := te.click(ctlAlice, ptFileButton); err != nil {
		t.Fatal(err)
	}
	frame, err := te.ctl.GrabGroupFrame(context.Background(), te.app.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Members) != 1 || !frame.Partial() || len(frame.Excluded) != 1 || frame.Excluded[0].WindowID != platformtest.FakeAppMenuID {
		t.Fatalf("Members = %+v Excluded = %+v, want T only and the menu excluded", frame.Members, frame.Excluded)
	}
}

// T 以外の検証できたメンバー（メニュー）が撮れないときは capture_member_unavailable。モニター撮影に切り替えない。
func TestGrabGroupFrameStopsWhenAVerifiedMemberCannotBeCaptured(t *testing.T) {
	te := newTgtEnv(t)
	te.acquire(ctlAlice)
	if err := te.click(ctlAlice, ptFileButton); err != nil {
		t.Fatal(err)
	}
	te.capturer.SetNotPrintable(platformtest.FakeAppMenuID, true)
	_, err := te.ctl.GrabGroupFrame(context.Background(), te.app.Ref())
	var de *domain.Error
	if !errors.As(err, &de) || !errors.Is(err, domain.ErrInputBlocked) || de.Detail["reason"] != string(domain.ReasonCaptureMemberUnavailable) {
		t.Fatalf("err = %v, want input_blocked capture_member_unavailable", err)
	}
	te.capturer.AssertNoMonitorCapture(t)
}
