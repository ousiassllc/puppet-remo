package platformtest_test

import (
	"context"
	"errors"
	"image"
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

func seededWindows() *platformtest.FakeWindows {
	return platformtest.NewFakeWindows(
		domain.WindowInfo{ID: "100", Title: "メモ帳", Process: "notepad.exe", PID: 4120, Rect: domain.Rect{X: 100, Y: 100, W: 400, H: 300}, Foreground: true},
		domain.WindowInfo{ID: "200", Title: "Explorer", Process: "explorer.exe", PID: 800, Rect: domain.Rect{X: 0, Y: 0, W: 600, H: 480}},
		domain.WindowInfo{ID: "300", Title: "Hidden", Process: "hidden.exe", PID: 900, Rect: domain.Rect{X: 0, Y: 0, W: 600, H: 480}, Minimized: true},
	)
}

func seededUIA(w *platformtest.FakeWindows) *platformtest.FakeUIAutomation {
	u := platformtest.NewFakeUIAutomation(w)
	u.SetTree("100", domain.UINode{
		ID: "root", Role: "Window", Name: "メモ帳", Enabled: true,
		Children: []domain.UINode{
			{ID: "a", Role: "MenuBar", Enabled: true, Children: []domain.UINode{{ID: "a1", Role: "MenuItem", Name: "ファイル", Enabled: true}}},
			{ID: "b", Role: "Edit", Enabled: true, ValueRedacted: true},
		},
	})
	u.SetSnapshot("100", domain.UISnapshot{
		Hash: "h1",
		Elements: []domain.UIElement{
			{ControlType: 50000, Name: "OK", Enabled: true},
			{ControlType: 50000, Name: "Cancel", Enabled: true},
			{ControlType: 50011, Name: "ファイル", Enabled: true},
		},
	})
	return u
}

func TestFakeCapturerContract(t *testing.T) {
	platformtest.RunCapturerContract(t, func(*testing.T) platform.Capturer {
		return platformtest.NewFakeCapturer(
			domain.Monitor{Index: 0, Primary: true, Size: domain.Size{W: 64, H: 48}},
			domain.Monitor{Index: 2, Origin: domain.Point{X: 64}, Size: domain.Size{W: 32, H: 24}},
		)
	})
}

func TestFakeInputContract(t *testing.T) {
	platformtest.RunInputContract(t, func(*testing.T) platform.InputInjector { return platformtest.NewFakeInput() }, domain.Point{X: 10, Y: 20})
}

func TestFakeWindowsContract(t *testing.T) {
	platformtest.RunWindowManagerContract(t, func(*testing.T) platform.WindowManager { return seededWindows() })
}

func TestFakeUIAutomationContract(t *testing.T) {
	platformtest.RunUIAutomationContract(t, func(*testing.T) platform.UIAutomation { return seededUIA(seededWindows()) })
}

func TestFakeClipboardContract(t *testing.T) {
	platformtest.RunClipboardContract(t, func(*testing.T) platform.Clipboard { return platformtest.NewFakeClipboard() })
}

func TestFakeSecretStoreContract(t *testing.T) {
	platformtest.RunSecretStoreContract(t, func(*testing.T) platform.SecretStore { return platformtest.NewFakeSecretStore() })
}

func TestFakeHotkeyContract(t *testing.T) {
	platformtest.RunHotkeyContract(t, func(*testing.T) platform.Hotkey { return platformtest.NewFakeHotkey() })
}

func TestRecorderRecordsCallsAndInjectsErrors(t *testing.T) {
	in := platformtest.NewFakeInput()
	ctx := context.Background()
	boom := errors.New("boom")

	if err := in.MoveTo(ctx, domain.Point{X: 1, Y: 2}); err != nil {
		t.Fatal(err)
	}
	in.SetError("Click", boom)
	// 注入したエラーは引数の検査より前に返る。
	if err := in.Click(ctx, "x", 99, nil); !errors.Is(err, boom) {
		t.Fatalf("Click error = %v, want the injected error", err)
	}
	in.SetError("Click", nil)
	if err := in.Click(ctx, domain.ButtonRight, 2, nil); err != nil {
		t.Fatal(err)
	}

	got := in.Calls()
	want := []platformtest.Call{
		{Method: "MoveTo", Args: []any{domain.Point{X: 1, Y: 2}}},
		{Method: "Click", Args: []any{domain.Button("x"), 99, []domain.Modifier(nil)}},
		{Method: "Click", Args: []any{domain.ButtonRight, 2, []domain.Modifier(nil)}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Calls() = %+v, want %+v", got, want)
	}
	if n := len(in.CallsTo("Click")); n != 2 {
		t.Errorf("CallsTo(Click) = %d calls, want 2", n)
	}
	in.Reset()
	if n := len(in.Calls()); n != 0 {
		t.Errorf("Calls() after Reset = %d, want 0", n)
	}
}

func TestFakeInputTracksHeldKeys(t *testing.T) {
	in := platformtest.NewFakeInput()
	ctx := context.Background()
	for _, e := range []domain.KeyEvent{
		{Action: domain.KeyDown, Code: "KeyA"},
		{Action: domain.KeyDown, Code: "ShiftLeft"},
		{Action: domain.KeyUp, Code: "KeyA"},
	} {
		if err := in.KeyEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := in.HeldKeys(), []string{"ShiftLeft"}; !reflect.DeepEqual(got, want) {
		t.Errorf("HeldKeys() = %v, want %v", got, want)
	}
	if err := in.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	if got := in.HeldKeys(); len(got) != 0 {
		t.Errorf("HeldKeys() after ReleaseAll = %v, want none", got)
	}
}

func TestFakeInputTracksHeldButtons(t *testing.T) {
	in := platformtest.NewFakeInput()
	ctx := context.Background()
	for _, c := range []struct {
		b domain.Button
		a domain.ButtonAction
	}{
		{domain.ButtonLeft, domain.ButtonActionDown},
		{domain.ButtonRight, domain.ButtonActionDown},
		{domain.ButtonLeft, domain.ButtonActionUp},
	} {
		if err := in.Button(ctx, c.b, c.a); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := in.HeldButtons(), []domain.Button{domain.ButtonRight}; !reflect.DeepEqual(got, want) {
		t.Errorf("HeldButtons() = %v, want %v", got, want)
	}
	if err := in.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	if got := in.HeldButtons(); len(got) != 0 {
		t.Errorf("HeldButtons() after ReleaseAll = %v, want none", got)
	}
}

func TestFakeWindowsAtAndFocus(t *testing.T) {
	w := seededWindows()
	ctx := context.Background()

	// 最上位のウィンドウが優先され、最小化されたウィンドウは対象外。
	if got, ok, err := w.At(domain.Point{X: 150, Y: 150}); err != nil || !ok || got.ID != "100" {
		t.Errorf("At(150,150) = %q, %v, %v; want window 100", got.ID, ok, err)
	}
	if got, ok, err := w.At(domain.Point{X: 10, Y: 10}); err != nil || !ok || got.ID != "200" {
		t.Errorf("At(10,10) = %q, %v, %v; want window 200", got.ID, ok, err)
	}
	if _, ok, _ := w.At(domain.Point{X: 5000, Y: 5000}); ok {
		t.Error("At(5000,5000) found a window")
	}

	if err := w.Focus(ctx, "300"); err != nil {
		t.Fatal(err)
	}
	fg, err := w.Foreground()
	if err != nil || fg.ID != "300" || fg.Minimized {
		t.Errorf("Foreground() = %+v, %v; want window 300 restored", fg, err)
	}
	list, _ := w.List(ctx)
	if list[0].ID != "300" || list[1].Foreground || list[2].Foreground {
		t.Errorf("List() = %+v; want 300 first and the only foreground window", list)
	}
}

// クリックスルーのオーバーレイは At に現れず、その下の実ウィンドウが返る。オーバーレイだけの場所は「ウィンドウなし」。
func TestFakeWindowsAtSkipsOverlay(t *testing.T) {
	w := platformtest.NewFakeWindows(
		domain.WindowInfo{ID: "ov", Own: true, Overlay: true, Rect: domain.Rect{X: 0, Y: 0, W: 1000, H: 1000}},
		domain.WindowInfo{ID: "dlg", Own: true, Rect: domain.Rect{X: 100, Y: 100, W: 200, H: 200}},
	)
	if got, ok, err := w.At(domain.Point{X: 150, Y: 150}); err != nil || !ok || got.ID != "dlg" {
		t.Errorf("At(150,150) = %q, %v, %v; want the dialog under the overlay", got.ID, ok, err)
	}
	if got, ok, err := w.At(domain.Point{X: 800, Y: 800}); err != nil || ok {
		t.Errorf("At(800,800) = %q, %v, %v; want no window (overlay only)", got.ID, ok, err)
	}
}

func TestFakeWindowsForegroundWithoutWindow(t *testing.T) {
	_, err := platformtest.NewFakeWindows().Foreground()
	if !errors.Is(err, domain.ErrDesktopUnavailable) {
		t.Errorf("Foreground() error = %v, want ErrDesktopUnavailable", err)
	}
}

func TestFakeCapturerWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := platformtest.NewFakeCapturer()
		frame := platform.Frame{Image: image.NewRGBA(image.Rect(0, 0, 2, 2)), Dirty: []image.Rectangle{image.Rect(0, 0, 1, 1)}, Seq: 7}
		c.SetWatch([]platform.Frame{frame}, []error{domain.ErrDesktopUnavailable})

		ctx, cancel := context.WithCancel(context.Background())
		frames, errs, err := c.Watch(ctx, 0, 30)
		if err != nil {
			t.Fatal(err)
		}
		if got := <-frames; got.Seq != 7 {
			t.Errorf("frame Seq = %d, want 7", got.Seq)
		}
		if got := <-errs; !errors.Is(got, domain.ErrDesktopUnavailable) {
			t.Errorf("error = %v, want ErrDesktopUnavailable", got)
		}
		cancel()
		synctest.Wait()
		if _, ok := <-frames; ok {
			t.Error("frames is still open after cancel")
		}
		if _, ok := <-errs; ok {
			t.Error("errs is still open after cancel")
		}

		if _, _, err := c.Watch(context.Background(), 0, 0); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Watch(fps 0) error = %v, want ErrInvalidArgument", err)
		}
		if _, _, err := c.Watch(context.Background(), 9, 30); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Watch(unknown monitor) error = %v, want ErrInvalidArgument", err)
		}
	})
}

func TestFakeCapturerSetImage(t *testing.T) {
	c := platformtest.NewFakeCapturer()
	want := image.NewRGBA(image.Rect(0, 0, 3, 3))
	c.SetImage(0, want)
	got, err := c.Grab(context.Background(), 0)
	if err != nil || got != want {
		t.Errorf("Grab = %p, %v; want the injected image", got, err)
	}
}

func TestFakeHotkeyTrigger(t *testing.T) {
	h := platformtest.NewFakeHotkey()
	fired := 0
	unregister, err := h.Register("Ctrl+Alt+Shift+K", func() { fired++ })
	if err != nil {
		t.Fatal(err)
	}
	if !h.Trigger("ctrl+alt+shift+k") || fired != 1 {
		t.Errorf("Trigger fired %d times, want 1", fired)
	}
	if got, want := h.Registered(), []string{"ctrl+alt+shift+k"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Registered() = %v, want %v", got, want)
	}
	unregister()
	if h.Trigger("Ctrl+Alt+Shift+K") || fired != 1 {
		t.Errorf("Trigger after unregister fired the callback (%d)", fired)
	}
}

func TestFakeHotkeyStaleUnregisterKeepsNewRegistration(t *testing.T) {
	h := platformtest.NewFakeHotkey()
	old, err := h.Register("ctrl+k", func() {})
	if err != nil {
		t.Fatal(err)
	}
	old()
	if _, err := h.Register("ctrl+k", func() {}); err != nil {
		t.Fatal(err)
	}
	old() // 解除済みの古い unregister は、新しい登録を消さない
	if len(h.Registered()) != 1 {
		t.Errorf("Registered() = %v, want the new registration to remain", h.Registered())
	}
}

func TestFakeSecretStoreDoesNotRecordValues(t *testing.T) {
	s := platformtest.NewFakeSecretStore()
	if err := s.Set(platform.SecretOpenAI, "fake-value-not-recorded"); err != nil {
		t.Fatal(err)
	}
	for _, c := range s.Calls() {
		for _, a := range c.Args {
			if a == "fake-value-not-recorded" {
				t.Errorf("the secret value appears in the call record: %+v", c)
			}
		}
	}
}

func TestFakeClipboardAndPower(t *testing.T) {
	cb := platformtest.NewFakeClipboard()
	cb.SetText("seeded")
	if got, err := cb.ReadText(context.Background()); err != nil || got != "seeded" {
		t.Errorf("ReadText = %q, %v", got, err)
	}
	boom := errors.New("boom")
	cb.SetError("WriteText", boom)
	if err := cb.WriteText(context.Background(), "x"); !errors.Is(err, boom) {
		t.Errorf("WriteText error = %v, want the injected error", err)
	}

	p := platformtest.NewFakePower()
	p.KeepAwake(true)
	if !p.Awake() || len(p.CallsTo("KeepAwake")) != 1 {
		t.Error("KeepAwake(true) was not recorded")
	}
	p.KeepAwake(false)
	if p.Awake() {
		t.Error("KeepAwake(false) did not clear the state")
	}
}

func TestFakeUIAutomationDefaultsAndPruning(t *testing.T) {
	w := seededWindows()
	u := platformtest.NewFakeUIAutomation(w)
	ctx := context.Background()

	// 未設定のウィンドウは、根だけのツリーと要素なしのスナップショットを返す。
	root, err := u.Tree(ctx, "200", 5, 10)
	if err != nil || root.Name != "Explorer" || len(root.Children) != 0 {
		t.Errorf("default Tree = %+v, %v", root, err)
	}
	snap, err := u.Snapshot(ctx, "200", 10)
	if err != nil || snap.Window.ID != "200" || snap.Hash != "empty" {
		t.Errorf("default Snapshot = %+v, %v", snap, err)
	}

	// 前面ウィンドウが変わると、windowID が空の呼び出しの対象も変わる。
	if err := w.Focus(ctx, "200"); err != nil {
		t.Fatal(err)
	}
	if snap, _ := u.Snapshot(ctx, "", 10); snap.Window.ID != "200" {
		t.Errorf("Snapshot(\"\") used window %q, want the foreground window 200", snap.Window.ID)
	}

	// 深さ 1 に刈ると、孫は消えて子が Truncated になる。
	u = seededUIA(seededWindows())
	pruned, err := u.Tree(ctx, "100", 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned.Children) != 2 || len(pruned.Children[0].Children) != 0 || !pruned.Children[0].Truncated || pruned.Children[1].Truncated {
		t.Errorf("pruned tree = %+v", pruned)
	}
}

func TestFakeAppLauncherContract(t *testing.T) {
	const (
		exe      = `C:\Apps\Tool\tool.exe`
		denied   = `C:\Apps\Denied\denied.exe`
		elevated = `C:\Apps\Admin\admin.exe`
		dir      = `C:\Apps\Tool`
	)
	platformtest.RunAppLauncherContract(t, func(*testing.T) platform.AppLauncher {
		l := platformtest.NewFakeAppLauncher()
		l.AddExecutable(exe)
		l.AddExecutable(denied)
		l.DenyAccess(denied)
		l.AddExecutable(elevated)
		l.RequireElevation(elevated)
		l.AddDir(dir)
		return l
	}, platformtest.AppLauncherFixture{
		ExePath: exe, Dir: dir, MissingPath: `C:\Apps\Gone\gone.exe`, MissingDir: `C:\Apps\Gone`,
		DeniedPath: denied, ElevatedPath: elevated,
	})
}

func TestFakeInstallerContract(t *testing.T) {
	const installer = `C:\Temp\puppet-remo-update\installer.exe`
	platformtest.RunInstallerContract(t, func(*testing.T) platform.Installer {
		i := platformtest.NewFakeInstaller()
		i.AddInstaller(installer)
		return i
	}, platformtest.InstallerFixture{Path: installer, MissingPath: `C:\Temp\puppet-remo-update\gone.exe`})
}

func TestFakeInstallerRecordsRunsAndInjectsErrors(t *testing.T) {
	const path = `C:\Temp\a.exe`
	i := platformtest.NewFakeInstaller()
	i.AddInstaller(path)
	ctx := context.Background()
	var seen []string
	i.OnRun(func(p string) { seen = append(seen, p) })

	if err := i.Run(ctx, path); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	i.SetError("Run", boom)
	if err := i.Run(ctx, path); !errors.Is(err, boom) {
		t.Fatalf("Run error = %v, want the injected error", err)
	}
	if got := i.Started(); !reflect.DeepEqual(got, []string{path}) || !reflect.DeepEqual(seen, []string{path}) {
		t.Errorf("Started = %v, OnRun saw %v; want only the first run", got, seen)
	}
	if got := i.CallsTo("Run"); len(got) != 2 {
		t.Errorf("CallsTo(Run) = %+v, want 2 calls", got)
	}
}

func TestFakeFolderOpenerContract(t *testing.T) {
	const dir = `C:\Users\demo\AppData\Local\puppet-remo\logs`
	platformtest.RunFolderOpenerContract(t, func(*testing.T) platform.FolderOpener {
		f := platformtest.NewFakeFolderOpener()
		f.AddFolder(dir)
		return f
	}, platformtest.FolderOpenerFixture{Dir: dir, MissingDir: `C:\Users\demo\gone`})
}

func TestFakeFolderOpenerRecordsOpensAndInjectsErrors(t *testing.T) {
	const dir = `C:\logs`
	f := platformtest.NewFakeFolderOpener()
	f.AddFolder(dir)
	ctx := context.Background()

	if err := f.OpenFolder(ctx, dir); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	f.SetError("OpenFolder", boom)
	if err := f.OpenFolder(ctx, dir); !errors.Is(err, boom) {
		t.Fatalf("OpenFolder error = %v, want the injected error", err)
	}
	if got := f.Opened(); !reflect.DeepEqual(got, []string{dir}) {
		t.Errorf("Opened = %v, want only the first open", got)
	}
	if got := f.CallsTo("OpenFolder"); len(got) != 2 || !reflect.DeepEqual(got[0].Args, []any{dir}) {
		t.Errorf("CallsTo(OpenFolder) = %+v, want 2 calls with the directory", got)
	}
}

func TestFakeFolderOpenerOpensOnlyKnownFoldersUnlessAcceptAny(t *testing.T) {
	ctx := context.Background()
	f := platformtest.NewFakeFolderOpener()
	if err := f.OpenFolder(ctx, `C:\elsewhere`); !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("OpenFolder(unknown) = %v, want platform.ErrNotFound", err)
	}
	f.AcceptAny()
	if err := f.OpenFolder(ctx, `C:\elsewhere`); err != nil {
		t.Fatalf("OpenFolder(any) = %v", err)
	}
	if err := f.OpenFolder(ctx, "relative"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("OpenFolder(relative) = %v, want ErrInvalidArgument even with AcceptAny", err)
	}
}

func TestFakeFilePickerContract(t *testing.T) {
	platformtest.RunFilePickerContract(t, func(*testing.T) (platform.FilePicker, platformtest.FilePickerDriver) {
		f := platformtest.NewFakeFilePicker()
		return f, f
	})
}

func TestFakeAppLauncherRecordsLaunchesAndInjectsErrors(t *testing.T) {
	l := platformtest.NewFakeAppLauncher()
	l.AddExecutable(`C:\a.exe`)
	ctx := context.Background()
	spec := platform.LaunchSpec{ExePath: `C:\a.exe`, Args: []string{"x"}}

	if _, err := l.Launch(ctx, spec); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	l.SetError("Launch", boom)
	if _, err := l.Launch(ctx, spec); !errors.Is(err, boom) {
		t.Fatalf("Launch error = %v, want the injected error", err)
	}
	if got := l.CallsTo("Launch"); len(got) != 2 || !reflect.DeepEqual(got[0].Args, []any{spec}) {
		t.Errorf("CallsTo(Launch) = %+v, want 2 calls with the spec", got)
	}
}

func TestFakeFilePickerDoesNotOpenForCanceledContext(t *testing.T) {
	f := platformtest.NewFakeFilePicker()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := f.PickExecutable(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("PickExecutable error = %v, want context.Canceled", err)
	}
	waitCtx, waitCancel := context.WithCancel(context.Background())
	waitCancel()
	if err := f.WaitOpen(waitCtx); !errors.Is(err, context.Canceled) {
		t.Errorf("WaitOpen = %v, want no dialog to have opened", err)
	}
}
