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

// メニューが開いたら、T の矩形の外へ出るメニューが合成に入り、画像が広がる。座標は Origin + p。
func TestGrabGroupFrameGrowsWhenAnOwnedMenuOpensOutsideT(t *testing.T) {
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
