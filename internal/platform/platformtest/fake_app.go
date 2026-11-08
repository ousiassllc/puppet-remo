package platformtest

import (
	"context"
	"slices"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// 偽アプリ（FakeApp）のウィンドウの ID・プロセス・クラス。
const (
	FakeAppMainID   = "1001" // T（メインウィンドウ）
	FakeAppMenuID   = "1002" // メニュー（メニューバーのドロップダウンと右クリックメニューで共用。同時には 1 つ）
	FakeAppDialogID = "1003" // 保存ダイアログ（T に所有される）

	FakeAppPID   uint32 = 4120
	FakeAppStart uint64 = 133_000_000_000_000_000
	FakeAppClass        = "FakeNotepad"

	// 別プロセスのダイアログ（FakeApp.DialogCrossProcess）の PID・開始時刻。エクスプローラーの共通ダイアログのように T に所有される。
	FakeAppCommonDialogPID   uint32 = 5230
	FakeAppCommonDialogStart uint64 = 133_000_000_500_000_000
)

// FakeAppLayout は偽アプリの配置（仮想デスクトップ上の物理座標）。
var FakeAppLayout = struct {
	Main       domain.Rect // T
	FileButton domain.Rect // メニューバーの「ファイル」
	FileMenu   domain.Rect // 「ファイル」のメニュー。項目は「名前を付けて保存」の 1 つ（メニュー全体）
	EditArea   domain.Rect // 右クリックでコンテキストメニューが出る領域
	MenuSize   domain.Size // 右クリックメニューの大きさ。上半分が「すべて選択」、下半分が「貼り付け」。クリック位置が左上
	Dialog     domain.Rect // 保存ダイアログ。T の右へはみ出す
	SaveButton domain.Rect
	Cancel     domain.Rect
}{
	Main:       domain.Rect{X: 100, Y: 100, W: 400, H: 300},
	FileButton: domain.Rect{X: 105, Y: 105, W: 40, H: 20},
	FileMenu:   domain.Rect{X: 105, Y: 125, W: 150, H: 25},
	EditArea:   domain.Rect{X: 100, Y: 150, W: 400, H: 250},
	MenuSize:   domain.Size{W: 120, H: 60},
	Dialog:     domain.Rect{X: 450, Y: 80, W: 200, H: 120},
	SaveButton: domain.Rect{X: 460, Y: 160, W: 50, H: 25},
	Cancel:     domain.Rect{X: 520, Y: 160, W: 50, H: 25},
}

// FakeApp は「クリックでメニュー・ダイアログが現れる・消える」偽のアプリ（Win32 版メモ帳の代わり。S1〜S3 の偽 platform 版で、実機の代わりにならない）。
// platform.InputInjector として FakeInput を包み、注入された入力に応じて FakeWindows のウィンドウを増減させる。
// 入力の宛先は FakeWindows の実際の Z オーダー・前面で決まるので、覆われた座標のクリックや前面でないウィンドウへの入力は効かない。
//
//   - 「ファイル」（FileButton）を左クリック → メニュー（FakeAppMenuID。#32768。T の MenuOwnerChain つき、アクティブにならない）が出る
//   - メニューの「名前を付けて保存」をクリック → メニューが消え、保存ダイアログ（FakeAppDialogID。T に所有される同一プロセス）が前面に出る
//   - ダイアログへの Text はファイル名になり、保存ボタンか Enter で Saved に記録して閉じる。キャンセルか Escape で閉じる
//   - 編集領域の右クリック → 右クリックメニュー。「すべて選択」で Selected、「貼り付け」でクリップボードのテキストを Document に足す
//   - ほかの場所のクリック・Escape でメニューは閉じる
type FakeApp struct {
	*FakeInput // Click・Text・Key 以外はそのまま FakeInput

	// OwnerlessMenu が true のとき、メニューは hwndMenuOwner を持たない（トレイのメニューのように所有関係を確かめられない）。
	OwnerlessMenu bool
	// DialogCrossProcess が true のとき、保存ダイアログは別プロセス（FakeAppCommonDialogPID）が T に所有されて出す（共通ダイアログ）。
	DialogCrossProcess bool

	windows   *FakeWindows
	clipboard *FakeClipboard

	mu       sync.Mutex
	menu     string // "" / "file" / "context"
	dialog   bool
	filename string
	document string
	selected bool
	saved    []string
}

var _ platform.InputInjector = (*FakeApp)(nil)

// NewFakeApp は T（FakeAppMainID。前面）だけがある状態の偽アプリを windows に作る。windows の既存のウィンドウは置き換える。
// clipboard は貼り付けに使う（nil なら貼り付けは何もしない）。ReadText の呼び出し回数には数えない。
func NewFakeApp(windows *FakeWindows, input *FakeInput, clipboard *FakeClipboard) *FakeApp {
	a := &FakeApp{FakeInput: input, windows: windows, clipboard: clipboard}
	windows.SetWindows(a.MainWindow())
	return a
}

// Reset は偽アプリを初期状態（T だけが前面。メニュー・ダイアログなし。保存・文書・選択を消し、OwnerlessMenu・DialogCrossProcess を false）に戻す。
// windows の T 以外のウィンドウも消えるので、呼び出し側が必要なら足し直す（E2E のテスト間の初期化）。
func (a *FakeApp) Reset() {
	a.OwnerlessMenu, a.DialogCrossProcess = false, false
	a.mu.Lock()
	a.menu, a.dialog, a.filename, a.document, a.selected, a.saved = "", false, "", "", false, nil
	a.mu.Unlock()
	a.windows.SetWindows(a.MainWindow())
}

// MainWindow は T の事実。
func (a *FakeApp) MainWindow() domain.WindowInfo {
	return domain.WindowInfo{
		ID: FakeAppMainID, Title: "無題 - FakeNotepad", Process: "fakenotepad.exe", PID: FakeAppPID, ProcessStart: FakeAppStart,
		Class: FakeAppClass, Rect: FakeAppLayout.Main, Foreground: true,
	}
}

// Ref は T の WindowRef。
func (a *FakeApp) Ref() domain.WindowRef { return domain.RefOf(a.MainWindow()) }

func (a *FakeApp) mainLink() domain.WindowLink {
	return domain.WindowLink{ID: FakeAppMainID, PID: FakeAppPID, ProcessStart: FakeAppStart, Class: FakeAppClass}
}

// Saved は保存されたファイル名（保存ボタン・Enter で確定したもの）。
func (a *FakeApp) Saved() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.saved)
}

// Document は T に入力されたテキスト（Text と貼り付け）。
func (a *FakeApp) Document() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.document
}

// Selected は「すべて選択」が選ばれたか。
func (a *FakeApp) Selected() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.selected
}

// MenuOpen は開いているメニューの種類（"file" / "context"、開いていなければ空）。
func (a *FakeApp) MenuOpen() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.menu
}

// DialogOpen は保存ダイアログが開いているか。
func (a *FakeApp) DialogOpen() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dialog
}

func (a *FakeApp) Click(ctx context.Context, b domain.Button, count int, mods []domain.Modifier) error {
	if err := a.FakeInput.Click(ctx, b, count, mods); err != nil {
		return err
	}
	a.click(b, a.cursorNow())
	return nil
}

func (a *FakeApp) Text(ctx context.Context, s string) error {
	if err := a.FakeInput.Text(ctx, s); err != nil {
		return err
	}
	fg, err := a.windows.foreground()
	if err != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch fg.ID {
	case FakeAppDialogID:
		a.filename += s
	case FakeAppMainID:
		a.document += s
	}
	return nil
}

func (a *FakeApp) Key(ctx context.Context, k domain.KeyCombo) error {
	if err := a.FakeInput.Key(ctx, k); err != nil {
		return err
	}
	if len(k.Mods) != 0 {
		return nil
	}
	a.pressKey(k.Key)
	return nil
}

// Button は Web UI の down / up。ボタンを離したとき、カーソルの位置へのクリックとして配る（MCP の Click と同じ効果）。
func (a *FakeApp) Button(ctx context.Context, b domain.Button, act domain.ButtonAction) error {
	if err := a.FakeInput.Button(ctx, b, act); err != nil {
		return err
	}
	if act == domain.ButtonActionUp {
		a.click(b, a.cursorNow())
	}
	return nil
}

// KeyEvent は Web UI の down / up。修飾キーのない Enter・Escape の down を、Key と同じに配る。
func (a *FakeApp) KeyEvent(ctx context.Context, e domain.KeyEvent) error {
	if err := a.FakeInput.KeyEvent(ctx, e); err != nil {
		return err
	}
	if e.Action != domain.KeyDown || len(e.Mods) != 0 {
		return nil
	}
	switch e.Code {
	case "Enter":
		a.pressKey("enter")
	case "Escape":
		a.pressKey("escape")
	}
	return nil
}

// pressKey は修飾キーのない key（enter・escape）を前面のウィンドウに配る。
func (a *FakeApp) pressKey(key string) {
	fg, err := a.windows.foreground()
	if err != nil {
		return
	}
	switch key {
	case "enter":
		if fg.ID == FakeAppDialogID {
			a.closeDialog(true)
		}
	case "escape":
		switch {
		case a.MenuOpen() != "":
			a.closeMenu()
		case fg.ID == FakeAppDialogID:
			a.closeDialog(false)
		}
	}
}

// click は p への b のクリックを、p の最上位のウィンドウに配る。
func (a *FakeApp) click(b domain.Button, p domain.Point) {
	hit, ok := a.windows.hitTest(p)
	if !ok {
		a.closeMenu()
		return
	}
	switch hit.Top.ID {
	case FakeAppMenuID:
		a.clickMenu(p)
	case FakeAppDialogID:
		a.closeMenu()
		switch {
		case FakeAppLayout.SaveButton.Contains(p):
			a.closeDialog(true)
		case FakeAppLayout.Cancel.Contains(p):
			a.closeDialog(false)
		}
	case FakeAppMainID:
		wasMenu := a.MenuOpen()
		a.closeMenu()
		switch {
		case b == domain.ButtonLeft && FakeAppLayout.FileButton.Contains(p) && wasMenu != "file":
			a.openMenu("file", FakeAppLayout.FileMenu)
		case b == domain.ButtonRight && FakeAppLayout.EditArea.Contains(p):
			a.openMenu("context", domain.Rect{X: p.X, Y: p.Y, W: FakeAppLayout.MenuSize.W, H: FakeAppLayout.MenuSize.H})
		}
	default:
		a.closeMenu()
	}
}

func (a *FakeApp) clickMenu(p domain.Point) {
	a.mu.Lock()
	kind := a.menu
	a.mu.Unlock()
	menu, ok := a.windows.find(FakeAppMenuID)
	if !ok {
		return
	}
	a.closeMenu()
	switch kind {
	case "file":
		a.openDialog()
	case "context":
		a.mu.Lock()
		defer a.mu.Unlock()
		if p.Y < menu.Rect.Y+menu.Rect.H/2 {
			a.selected = true
		} else if a.clipboard != nil {
			a.document += a.clipboard.peek()
		}
	}
}

func (a *FakeApp) openMenu(kind string, r domain.Rect) {
	m := domain.WindowInfo{
		ID: FakeAppMenuID, Process: "fakenotepad.exe", PID: FakeAppPID, ProcessStart: FakeAppStart, Class: domain.ClassMenu,
		Rect: r, Style: domain.WindowStyle{Popup: true, Tool: true, NoActivate: true},
	}
	if !a.OwnerlessMenu {
		m.MenuOwnerID = FakeAppMainID
		m.MenuOwnerChain = []domain.WindowLink{a.mainLink()}
	}
	a.windows.Add(m)
	a.mu.Lock()
	a.menu = kind
	a.mu.Unlock()
}

func (a *FakeApp) closeMenu() {
	a.windows.Remove(FakeAppMenuID)
	a.mu.Lock()
	a.menu = ""
	a.mu.Unlock()
}

func (a *FakeApp) openDialog() {
	d := domain.WindowInfo{
		ID: FakeAppDialogID, Title: "名前を付けて保存", Process: "fakenotepad.exe", PID: FakeAppPID, ProcessStart: FakeAppStart, Class: "#32770",
		Rect: FakeAppLayout.Dialog, OwnerChain: []domain.WindowLink{a.mainLink()}, Style: domain.WindowStyle{Popup: true},
	}
	if a.DialogCrossProcess {
		d.Process, d.PID, d.ProcessStart = "explorer.exe", FakeAppCommonDialogPID, FakeAppCommonDialogStart
	}
	a.windows.Add(d)
	a.windows.activate(FakeAppDialogID)
	a.mu.Lock()
	a.dialog, a.filename = true, ""
	a.mu.Unlock()
}

func (a *FakeApp) closeDialog(save bool) {
	a.windows.Remove(FakeAppDialogID)
	a.windows.activate(FakeAppMainID)
	a.mu.Lock()
	defer a.mu.Unlock()
	if save && a.dialog {
		a.saved = append(a.saved, a.filename)
	}
	a.dialog, a.filename = false, ""
}
