package platformtest_test

import (
	"context"
	"image"
	"image/color"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

func TestFakeWindowsListAllKeepsUntitledWindows(t *testing.T) {
	ctx := context.Background()
	w := platformtest.NewFakeWindows(
		domain.WindowInfo{ID: "m", Class: domain.ClassMenu, Rect: domain.Rect{W: 10, H: 10}},
		domain.WindowInfo{ID: "t", Title: "T", Rect: domain.Rect{W: 10, H: 10}, Foreground: true},
	)
	all, err := w.ListAll(ctx)
	if err != nil || len(all) != 2 || all[0].ID != "m" {
		t.Errorf("ListAll = %+v, %v; want both windows, topmost first", all, err)
	}
}

func TestFakeWindowsFactsRoundTrip(t *testing.T) {
	in := domain.WindowInfo{
		ID: "m", Class: domain.ClassMenu, ProcessStart: 5, PID: 7,
		OwnerChain:     []domain.WindowLink{{ID: "o", PID: 7, ProcessStart: 5, Class: "C"}, domain.UnverifiableLink()},
		MenuOwnerID:    "o",
		MenuOwnerChain: []domain.WindowLink{{ID: "o", PID: 7, ProcessStart: 5, Class: "C"}},
		Style:          domain.WindowStyle{Popup: true, Tool: true, NoActivate: true},
	}
	all, _ := platformtest.NewFakeWindows(in).ListAll(context.Background())
	if len(all) != 1 || all[0].Class != in.Class || all[0].ProcessStart != 5 || len(all[0].OwnerChain) != 2 || !all[0].OwnerChain[1].Unverifiable ||
		all[0].MenuOwnerID != "o" || len(all[0].MenuOwnerChain) != 1 || !all[0].Style.NoActivate {
		t.Errorf("ListAll = %+v, want the facts passed through", all)
	}
}

func TestFakeWindowsLeafAndFocusLeaf(t *testing.T) {
	cross := domain.LeafRef{WindowRef: domain.WindowRef{ID: "child", PID: 99, ProcessStart: 5000, Class: "Chrome_Child"}, Process: "other.exe"}
	w := platformtest.NewFakeWindows(domain.WindowInfo{ID: "t", PID: 10, ProcessStart: 1, Class: "T", Rect: domain.Rect{X: 0, Y: 0, W: 100, H: 100}, Foreground: true})
	w.AddChild("t", platformtest.FakeChild{Rect: domain.Rect{X: 50, Y: 50, W: 20, H: 20}, Leaf: cross})
	w.SetFocusLeaf("t", cross)

	if hit, ok, err := w.At(domain.Point{X: 55, Y: 55}); err != nil || !ok || hit.Top.ID != "t" || hit.Leaf != cross {
		t.Errorf("At(child) = %+v, %v, %v; want top t and the cross-process leaf (pre-GA_ROOT)", hit, ok, err)
	}
	if hit, _, _ := w.At(domain.Point{X: 5, Y: 5}); hit.Leaf != domain.LeafOf(hit.Top) {
		t.Errorf("At(no child) leaf = %+v, want the top window itself", hit.Leaf)
	}
	if fg, err := w.Foreground(); err != nil || fg.Foreground.ID != "t" || fg.FocusLeaf != cross {
		t.Errorf("Foreground = %+v, %v; want the focus leaf kept", fg, err)
	}
}

func TestFakeWindowsReplaceReusesHWND(t *testing.T) {
	w := platformtest.NewFakeWindows(
		domain.WindowInfo{ID: "a", PID: 1, ProcessStart: 1, Class: "A", Foreground: true},
		domain.WindowInfo{ID: "b", PID: 2, ProcessStart: 2, Class: "B"},
	)
	if !w.Replace("b", domain.WindowInfo{ID: "ignored", PID: 3, ProcessStart: 3, Class: "Z"}) {
		t.Fatal("Replace found no window b")
	}
	all, _ := w.ListAll(context.Background())
	if len(all) != 2 || all[1].ID != "b" || all[1].PID != 3 || all[1].Class != "Z" || all[0].ID != "a" || !all[0].Foreground {
		t.Errorf("ListAll = %+v, want b replaced in place (same ID and z-order, other PID/start/class)", all)
	}
	if w.Replace("nope", domain.WindowInfo{}) {
		t.Error("Replace of a missing window returned true")
	}
}

func TestFakeWindowsAddRaiseRemove(t *testing.T) {
	ctx := context.Background()
	w := platformtest.NewFakeWindows(domain.WindowInfo{ID: "a", Foreground: true}, domain.WindowInfo{ID: "b"})
	w.Add(domain.WindowInfo{ID: "m"})
	all, _ := w.ListAll(ctx)
	if all[0].ID != "m" || !all[1].Foreground {
		t.Errorf("Add = %+v; want m on top without taking the foreground", all)
	}
	w.Raise("b")
	if all, _ = w.ListAll(ctx); all[0].ID != "b" {
		t.Errorf("Raise = %+v; want b on top", all)
	}
	w.Remove("m")
	if all, _ = w.ListAll(ctx); len(all) != 2 {
		t.Errorf("Remove = %+v; want m gone", all)
	}
	if !w.Update("a", func(x *domain.WindowInfo) { x.ID = "hijack"; x.Title = "x" }) {
		t.Fatal("Update found no window a")
	}
	if all, _ = w.ListAll(ctx); all[1].ID != "a" || all[1].Title != "x" {
		t.Errorf("Update = %+v; want the title changed and the ID kept", all)
	}
}

func fillRGBA(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

// groupFixture は T・同一プロセスのメニュー（T の外にはみ出す）・T を覆う別プロセスのウィンドウを持つ偽の platform。
func groupFixture() (*platformtest.FakeWindows, *platformtest.FakeCapturer, []domain.WindowRef) {
	t := domain.WindowInfo{ID: "t", PID: 10, ProcessStart: 1, Class: "T", Rect: domain.Rect{X: -100, Y: 50, W: 100, H: 100}, Foreground: true}
	menu := domain.WindowInfo{ID: "m", PID: 10, ProcessStart: 1, Class: domain.ClassMenu, Rect: domain.Rect{X: -20, Y: 120, W: 60, H: 60}}
	stranger := domain.WindowInfo{ID: "s", PID: 99, ProcessStart: 9, Class: "S", Rect: domain.Rect{X: -60, Y: 60, W: 200, H: 200}}
	w := platformtest.NewFakeWindows(stranger, menu, t) // 前面が先
	c := platformtest.NewFakeCapturer()
	c.AttachWindows(w)
	return w, c, []domain.WindowRef{domain.RefOf(menu), domain.RefOf(t)}
}

func TestFakeCapturerGrabGroupComposesMembersOnTransparentCanvas(t *testing.T) {
	_, c, members := groupFixture()
	got, err := c.GrabGroup(context.Background(), members)
	if err != nil {
		t.Fatal(err)
	}
	// 外接矩形: T (-100,50)-(0,150) と メニュー (-20,120)-(40,180) → (-100,50)-(40,180)。負の原点を含む。
	if got.Origin != (domain.Point{X: -100, Y: 50}) || got.Image.Bounds() != image.Rect(0, 0, 140, 130) {
		t.Fatalf("Origin %+v, bounds %v; want origin (-100,50), 140x130", got.Origin, got.Image.Bounds())
	}
	for _, m := range got.Members {
		if m.Status != platform.CaptureOK {
			t.Errorf("member %s status %s, want ok", m.Ref.ID, m.Status)
		}
	}
	tc, mc := platformtest.WindowColor("t"), platformtest.WindowColor("m")
	at := func(x, y int) color.RGBA { return got.Image.RGBAAt(x-got.Origin.X, y-got.Origin.Y) }
	if at(-90, 60) != tc {
		t.Errorf("pixel in T = %v, want T's color %v", at(-90, 60), tc)
	}
	if at(30, 170) != mc {
		t.Errorf("pixel in the menu outside T = %v, want the menu's color %v", at(30, 170), mc)
	}
	if at(-10, 130) != mc {
		t.Errorf("pixel where the menu overlaps T = %v, want the menu (front) color", at(-10, 130))
	}
	if at(30, 60) != (color.RGBA{}) {
		t.Errorf("pixel outside every member = %v, want transparent (the canvas is empty; no desktop under it)", at(30, 60))
	}
	// 非メンバー s が T・メニューを覆っているが、その色は画像に一切ない。
	sc := platformtest.WindowColor("s")
	for i := 0; i < len(got.Image.Pix); i += 4 {
		if (color.RGBA{R: got.Image.Pix[i], G: got.Image.Pix[i+1], B: got.Image.Pix[i+2], A: got.Image.Pix[i+3]}) == sc {
			t.Fatal("the image contains a non-member's pixel")
		}
	}
	if c.GroupGrabCalls() != 1 {
		t.Errorf("GroupGrabCalls = %d, want 1", c.GroupGrabCalls())
	}
	c.AssertNoMonitorCapture(t)
}

func TestFakeCapturerGrabGroupSetWindowImage(t *testing.T) {
	_, c, members := groupFixture()
	c.SetWindowImage("t", fillRGBA(100, 100, color.RGBA{R: 1, G: 2, B: 3, A: 255}))
	got, err := c.GrabGroup(context.Background(), members[1:])
	if err != nil || got.Image.RGBAAt(5, 5) != (color.RGBA{R: 1, G: 2, B: 3, A: 255}) {
		t.Errorf("GrabGroup = %v, %v; want the SetWindowImage pixels", got.Image, err)
	}
}

func TestFakeCapturerGrabGroupNotPrintableAndGone(t *testing.T) {
	w, c, members := groupFixture()
	c.SetNotPrintable("m", true)
	got, err := c.GrabGroup(context.Background(), members)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := got.Status("m"); m.Status != platform.CaptureNotPrintable || m.Rect != (domain.Rect{}) {
		t.Errorf("menu status = %+v, want not_printable without a rect", m)
	}
	if m, _ := got.Status("t"); m.Status != platform.CaptureOK {
		t.Errorf("T status = %+v, want ok (a member that cannot be captured is distinguishable from T)", m)
	}
	if got.Image.Bounds() != image.Rect(0, 0, 100, 100) {
		t.Errorf("bounds %v, want T only (the menu is not composed and nothing fills in for it)", got.Image.Bounds())
	}

	// HWND の再利用: T の ID は同じで PID が違う → gone。撮れたものがなければ画像は nil。
	c.SetNotPrintable("m", false)
	w.Replace("t", domain.WindowInfo{PID: 55, ProcessStart: 5, Class: "T", Rect: domain.Rect{X: -100, Y: 50, W: 100, H: 100}})
	got, _ = c.GrabGroup(context.Background(), members[1:])
	if m, _ := got.Status("t"); m.Status != platform.CaptureGone || got.Image != nil {
		t.Errorf("reused T: status %+v, image %v; want gone and no image", m, got.Image)
	}
	c.AssertNoMonitorCapture(t)
}

func TestFakeCapturerGrabGroupWithoutWindowsAndInjectedError(t *testing.T) {
	ctx := context.Background()
	if _, err := platformtest.NewFakeCapturer().GrabGroup(ctx, nil); err == nil {
		t.Error("GrabGroup without attached windows succeeded")
	}
	_, c, members := groupFixture()
	c.SetError("GrabGroup", domain.ErrDesktopUnavailable)
	if _, err := c.GrabGroup(ctx, members); err == nil {
		t.Error("GrabGroup with an injected error succeeded")
	}
	c.AssertNoMonitorCapture(t) // 失敗してもモニター撮影に切り替わらない
	if _, err := c.Grab(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if c.MonitorCaptureCalls() != 1 {
		t.Errorf("MonitorCaptureCalls = %d, want 1 after one Grab", c.MonitorCaptureCalls())
	}
}

func click(t *testing.T, in platform.InputInjector, b domain.Button, p domain.Point) {
	t.Helper()
	ctx := context.Background()
	if err := in.MoveTo(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := in.Click(ctx, b, 1, nil); err != nil {
		t.Fatal(err)
	}
}

func center(r domain.Rect) domain.Point { return domain.Point{X: r.X + r.W/2, Y: r.Y + r.H/2} }

func newApp(t *testing.T) (*platformtest.FakeApp, *platformtest.FakeWindows, *platformtest.FakeCapturer, *platformtest.FakeClipboard) {
	t.Helper()
	w := platformtest.NewFakeWindows()
	clip := platformtest.NewFakeClipboard()
	app := platformtest.NewFakeApp(w, platformtest.NewFakeInput(), clip)
	c := platformtest.NewFakeCapturer()
	c.AttachWindows(w)
	return app, w, c, clip
}

func groupOf(t *testing.T, app *platformtest.FakeApp, w *platformtest.FakeWindows) domain.Group {
	t.Helper()
	all, err := w.ListAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g := domain.ResolveGroup(app.Ref(), all)
	if !g.Resolved() {
		t.Fatalf("T is not resolved: %s", g.TargetReason)
	}
	return g
}

func refs(g domain.Group) []domain.WindowRef {
	out := make([]domain.WindowRef, 0, len(g.Members))
	for _, m := range g.Members {
		out = append(out, domain.RefOf(m.Window))
	}
	return out
}

// S1・S3 の偽 platform 版: メニューバーのメニュー → 項目 → 保存ダイアログ → ファイル名 → 保存。実機の代わりにならない。
func TestFakeAppFileMenuToSaveDialog(t *testing.T) {
	ctx := context.Background()
	app, w, c, _ := newApp(t)
	L := platformtest.FakeAppLayout

	click(t, app, domain.ButtonLeft, center(L.FileButton))
	if app.MenuOpen() != "file" {
		t.Fatal("the File menu did not open")
	}
	g := groupOf(t, app, w)
	if m, ok := g.Member(platformtest.FakeAppMenuID); !ok || m.Relation != domain.RelationMenu {
		t.Fatalf("the menu is not a menu member of the group: %+v", g)
	}
	if fg, _ := w.Foreground(); fg.Foreground.ID != platformtest.FakeAppMainID {
		t.Errorf("foreground = %s, want T (a menu does not take the foreground)", fg.Foreground.ID)
	}
	img, err := c.GrabGroup(ctx, refs(g))
	if err != nil {
		t.Fatal(err)
	}
	if mc, _ := img.Status(platformtest.FakeAppMenuID); mc.Status != platform.CaptureOK {
		t.Errorf("menu capture = %+v, want ok (the fake menu image is composed)", mc)
	}

	click(t, app, domain.ButtonLeft, center(L.FileMenu))
	if app.MenuOpen() != "" || !app.DialogOpen() {
		t.Fatalf("menu %q dialog %v; want the menu closed and the save dialog open", app.MenuOpen(), app.DialogOpen())
	}
	g = groupOf(t, app, w)
	d, ok := g.Member(platformtest.FakeAppDialogID)
	if !ok || d.Relation != domain.RelationOwned {
		t.Fatalf("the save dialog is not an owned member: %+v", g.NonMembers)
	}
	if fg, _ := w.Foreground(); fg.Foreground.ID != platformtest.FakeAppDialogID {
		t.Errorf("foreground = %s, want the save dialog", fg.Foreground.ID)
	}
	if b := g.CheckFocus(mustForeground(t, w)); b.Blocked() {
		t.Errorf("CheckFocus = %+v, want the dialog (a member) to pass", b)
	}
	if r, ok := g.Bounds(); !ok || r.X+r.W <= L.Main.X+L.Main.W {
		t.Errorf("Bounds = %+v, %v; want the dialog to extend past T's right edge", r, ok)
	}

	if err := app.Text(ctx, "memo.txt"); err != nil {
		t.Fatal(err)
	}
	click(t, app, domain.ButtonLeft, center(L.SaveButton))
	if app.DialogOpen() || len(app.Saved()) != 1 || app.Saved()[0] != "memo.txt" {
		t.Errorf("dialog %v saved %v; want the dialog closed and memo.txt saved", app.DialogOpen(), app.Saved())
	}
	if g = groupOf(t, app, w); len(g.Members) != 1 {
		t.Errorf("members after close = %d, want only T", len(g.Members))
	}
	c.AssertNoMonitorCapture(t)
}

func mustForeground(t *testing.T, w *platformtest.FakeWindows) domain.FocusInfo {
	t.Helper()
	f, err := w.Foreground()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// S2 の偽 platform 版: 右クリックメニュー → 「すべて選択」「貼り付け」。
func TestFakeAppContextMenu(t *testing.T) {
	app, w, _, clip := newApp(t)
	L := platformtest.FakeAppLayout
	clip.SetText("貼り付け文字列")

	at := domain.Point{X: 200, Y: 200}
	click(t, app, domain.ButtonRight, at)
	if app.MenuOpen() != "context" {
		t.Fatal("the context menu did not open")
	}
	if _, ok := groupOf(t, app, w).Member(platformtest.FakeAppMenuID); !ok {
		t.Error("the context menu is not a member")
	}
	click(t, app, domain.ButtonLeft, domain.Point{X: at.X + 10, Y: at.Y + 5})
	if !app.Selected() || app.MenuOpen() != "" {
		t.Errorf("selected %v menu %q; want select-all done and the menu closed", app.Selected(), app.MenuOpen())
	}
	click(t, app, domain.ButtonRight, at)
	click(t, app, domain.ButtonLeft, domain.Point{X: at.X + 10, Y: at.Y + L.MenuSize.H - 5})
	if app.Document() != "貼り付け文字列" {
		t.Errorf("document = %q, want the pasted clipboard text", app.Document())
	}
	if n := len(clip.CallsTo("ReadText")); n != 0 {
		t.Errorf("ReadText called %d times by the fake app's paste, want 0", n)
	}
}

func TestFakeAppMenuClosesOnOutsideClickAndEscape(t *testing.T) {
	ctx := context.Background()
	app, _, _, _ := newApp(t)
	click(t, app, domain.ButtonRight, domain.Point{X: 200, Y: 200})
	click(t, app, domain.ButtonLeft, domain.Point{X: 900, Y: 900}) // どのウィンドウでもない
	if app.MenuOpen() != "" {
		t.Error("the menu stayed open after a click outside every window")
	}
	click(t, app, domain.ButtonRight, domain.Point{X: 200, Y: 200})
	if err := app.Key(ctx, domain.KeyCombo{Key: "escape"}); err != nil || app.MenuOpen() != "" {
		t.Errorf("menu %q err %v; want the menu closed by Escape", app.MenuOpen(), err)
	}
}

func TestFakeAppDialogEnterAndCancel(t *testing.T) {
	ctx := context.Background()
	app, _, _, _ := newApp(t)
	L := platformtest.FakeAppLayout
	open := func() {
		click(t, app, domain.ButtonLeft, center(L.FileButton))
		click(t, app, domain.ButtonLeft, center(L.FileMenu))
	}
	open()
	_ = app.Text(ctx, "a.txt")
	if err := app.Key(ctx, domain.KeyCombo{Key: "enter"}); err != nil || len(app.Saved()) != 1 {
		t.Errorf("saved %v err %v; want a.txt saved by Enter", app.Saved(), err)
	}
	open()
	click(t, app, domain.ButtonLeft, center(L.Cancel))
	if app.DialogOpen() || len(app.Saved()) != 1 {
		t.Errorf("dialog %v saved %v; want closed without saving", app.DialogOpen(), app.Saved())
	}
}

// 実際の Z オーダー・前面で宛先が決まる: T を別プロセスのウィンドウが覆う座標のクリックはメニューを開かない。
func TestFakeAppClickGoesToTopmostWindow(t *testing.T) {
	app, w, _, _ := newApp(t)
	L := platformtest.FakeAppLayout
	w.Add(domain.WindowInfo{ID: "cover", PID: 99, ProcessStart: 9, Class: "C", Rect: L.Main})
	click(t, app, domain.ButtonLeft, center(L.FileButton))
	if app.MenuOpen() != "" {
		t.Error("the click went through a covering window to T")
	}
}

func TestFakeAppVariants(t *testing.T) {
	app, w, _, _ := newApp(t)
	app.OwnerlessMenu = true
	click(t, app, domain.ButtonRight, domain.Point{X: 200, Y: 200})
	g := groupOf(t, app, w)
	n, ok := g.NonMember(platformtest.FakeAppMenuID)
	if !ok || n.Reason != domain.ReasonRelatedUnverifiable {
		t.Errorf("ownerless menu = %+v, %v; want a related_unverifiable non-member", n, ok)
	}

	app2, w2, _, _ := newApp(t)
	app2.DialogCrossProcess = true
	L := platformtest.FakeAppLayout
	click(t, app2, domain.ButtonLeft, center(L.FileButton))
	click(t, app2, domain.ButtonLeft, center(L.FileMenu))
	g2 := groupOf(t, app2, w2)
	n, ok = g2.NonMember(platformtest.FakeAppDialogID)
	if !ok || n.Reason != domain.ReasonRelatedCrossProcess || n.PID != platformtest.FakeAppCommonDialogPID {
		t.Errorf("cross-process dialog = %+v, %v; want related_cross_process with its PID", n, ok)
	}
	if b := g2.CheckFocus(mustForeground(t, w2)); b.Reason != domain.ReasonRelatedCrossProcess {
		t.Errorf("CheckFocus on a cross-process dialog = %+v, want related_cross_process", b)
	}
}
