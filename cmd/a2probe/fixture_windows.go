//go:build windows

package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	pwin "github.com/ousiassllc/puppet-remo/internal/platform/windows"
)

// fixture 側。専用のテスト窓だけを出す。既存のアプリ・文書には触れず、グローバル入力（SendInput・カーソル移動）は使わない。
// probe からの指示（標準入力）は、自分のウィンドウへのウィンドウメッセージに変換するだけ。

// メニュー項目の ID。
const (
	idSaveAs = 101
	idOpen   = 102
	idExit   = 103
	idColor0 = 201
)

// 独自メッセージ（WM_APP から）。
const (
	wmAppOpenSave  = wmApp + 1 // 保存ダイアログを開く（UI スレッドで GetSaveFileNameW を呼ぶ）
	wmAppCloseMenu = wmApp + 2 // 開いているメニューを閉じる（UI スレッドで EndMenu を呼ぶ）
)

// 模様と色。マゼンタ（255,0,255。interferer の色）に近い色は使わない。
var (
	quadrants = [4]uint32{rgb(200, 40, 40), rgb(40, 160, 40), rgb(40, 40, 200), rgb(230, 200, 30)}
	// odColors はオーナードローのメニュー項目の塗り色。画素の完全一致で「項目が写った」と判定する（probe の watched colors と同じ値）。
	odColors = [3]colorRGB{{230, 60, 30}, {30, 170, 60}, {30, 60, 230}}
)

type colorRGB struct{ R, G, B uint8 }

func rgb(r, g, b uint8) uint32 { return uint32(r) | uint32(g)<<8 | uint32(b)<<16 }

// fixtureState は fixture プロセスの状態。ウィンドウは UI スレッドで作り、以後ハンドルは変えない。
type fixtureState struct {
	role    string
	saveDir string

	hT, hEdit, hDecoy, hBlank, hBlack uintptr
	tid                               uint32

	outMu sync.Mutex
	// enumMu は findSaveDialog の列挙結果（コールバックから書く）を守る。
	enumMu    sync.Mutex
	enumFound uintptr
}

var fx fixtureState

// fillBuf・readBuf は save-fill が SendMessageTimeout に渡すバッファ（コマンドは 1 本の goroutine が順に処理する）。
var fillBuf, readBuf [300]uint16

func (s *fixtureState) emit(verb, name string, kv ...string) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = os.Stdout.WriteString(formatMessage(verb, name, kv...) + "\n")
}

func hwndStr(h uintptr) string { return strconv.FormatUint(uint64(uint32(h)), 10) }

func runFixture(args []string) int {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	role := fs.String("role", "", "main|interferer|foreign")
	x := fs.Int("x", 100, "左上 X（物理ピクセル）")
	y := fs.Int("y", 100, "左上 Y")
	w := fs.Int("w", 720, "幅")
	h := fs.Int("h", 520, "高さ")
	saveDir := fs.String("savedir", "", "保存ダイアログの初期フォルダー（probe が作った専用の一時フォルダー）")
	owner := fs.Uint64("owner", 0, "foreign: 所有者にする窓（T）の HWND")
	deadline := fs.Duration("deadline", 150*time.Second, "この時間で自分で終了する")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	runtime.LockOSThread() // 窓・メニュー・メッセージループは同じ OS スレッドに固定する
	pwin.EnableDPIAwareness()
	fx.role = *role
	fx.saveDir = *saveDir
	fx.tid = windows.GetCurrentThreadId()
	time.AfterFunc(*deadline, func() { os.Exit(3) })

	hinst, _, _ := pGetModuleHandleW.Call(0)
	var ok bool
	switch *role {
	case "main":
		ok = createMainWindows(hinst, *x, *y, *w, *h)
	case "interferer":
		ok = createInterferer(hinst, *x, *y, *w, *h)
	case "foreign":
		ok = createForeign(hinst, uintptr(*owner), *x, *y, *w, *h)
	default:
		fmt.Fprintln(os.Stderr, "unknown role")
		return 2
	}
	if !ok {
		return 1
	}
	go readCommands()
	messageLoop()
	return 0
}

func messageLoop() {
	var m msg32
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		_, _, _ = pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func registerClass(hinst uintptr, name string, proc uintptr) bool {
	cursor, _, _ := pLoadCursorW.Call(0, idcArrow)
	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:   proc,
		Instance:  windows.Handle(hinst),
		Cursor:    windows.Handle(cursor),
		ClassName: utf16Ptr(name),
	}
	r, _, _ := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	return r != 0
}

// lastCreateErr は直前の createWindow が失敗したときの GetLastError の値。
var lastCreateErr uint32

func createWindow(exStyle uint32, class, title string, style uint32, x, y, w, h int, parent, menu, hinst uintptr) uintptr {
	hwnd, _, errno := pCreateWindowExW.Call(uintptr(exStyle), uintptr(unsafe.Pointer(utf16Ptr(class))), uintptr(unsafe.Pointer(utf16Ptr(title))),
		uintptr(style), uintptr(int32(x)), uintptr(int32(y)), uintptr(int32(w)), uintptr(int32(h)), parent, menu, hinst, 0)
	lastCreateErr = 0
	if hwnd == 0 {
		if e, ok := errno.(windows.Errno); ok {
			lastCreateErr = uint32(e)
		}
	}
	return hwnd
}

func createMainWindows(hinst uintptr, x, y, w, h int) bool {
	if !registerClass(hinst, "A2ProbeTarget", windows.NewCallback(targetProc)) ||
		!registerClass(hinst, "A2ProbeDecoy", solidWindowProc(func(h uintptr, rc rect32) { fillRectColor(h, rc, rgb(150, 150, 150)); drawText(h, 8, 8, "DECOY") })) ||
		!registerClass(hinst, "A2ProbeBlank", solidWindowProc(nil)) ||
		!registerClass(hinst, "A2ProbeBlack", solidWindowProc(func(h uintptr, rc rect32) { fillRectColor(h, rc, 0) })) {
		return false
	}
	menubar, _, _ := pCreateMenu.Call()
	file, _, _ := pCreatePopupMenu.Call()
	appendMenu(file, mfString, idSaveAs, "名前を付けて保存(&A)...")
	appendMenu(file, mfString, idOpen, "開く(&O)...")
	appendMenu(file, mfSeparator, 0, "")
	appendMenu(file, mfString, idExit, "終了(&X)")
	appendMenu(menubar, mfPopup|mfString, file, "ファイル(&F)")
	color, _, _ := pCreatePopupMenu.Call()
	for i := range odColors {
		// オーナードロー項目: 最後の引数は WM_MEASUREITEM / WM_DRAWITEM の ItemData になる（色の番号）。
		_, _, _ = pAppendMenuW.Call(color, mfOwnerDraw, uintptr(idColor0+i), uintptr(i))
	}
	appendMenu(menubar, mfPopup|mfString, color, "色(&C)")

	fx.hT = createWindow(0, "A2ProbeTarget", "A2 Target", wsOverlapped|wsCaption|wsSysMenu|wsMinimizeBox|wsThickFrame|wsVisible, x, y, w, h, 0, menubar, hinst)
	if fx.hT == 0 {
		return false
	}
	var client rect32
	_, _, _ = pGetClientRect.Call(fx.hT, uintptr(unsafe.Pointer(&client)))
	// 標準の EDIT 子コントロール（メモ帳と同じ作り）。右端に寄せて、右クリックメニューが T の矩形の外へ出るようにする。
	fx.hEdit = createWindow(0, "EDIT", "a2probe", wsChild|wsVisible|wsBorder|esAutoHScroll, int(client.Right)-320, int(client.Bottom)-48, 300, 28, fx.hT, 0, hinst)
	if fx.hEdit == 0 {
		return false
	}
	// 同一プロセスの無関係な窓（所有関係なし）。T の下に並べる。
	rowY := y + h + 30
	fx.hDecoy = createWindow(0, "A2ProbeDecoy", "A2 Decoy", wsOverlapped|wsCaption|wsVisible, x, rowY, 220, 140, 0, 0, hinst)
	fx.hBlank = createWindow(0, "A2ProbeBlank", "A2 Blank", wsOverlapped|wsCaption|wsVisible, x+240, rowY, 220, 140, 0, 0, hinst)
	fx.hBlack = createWindow(0, "A2ProbeBlack", "A2 Black", wsOverlapped|wsCaption|wsVisible, x+480, rowY, 220, 140, 0, 0, hinst)
	if fx.hDecoy == 0 || fx.hBlank == 0 || fx.hBlack == 0 {
		return false
	}
	_, _, _ = pSetForegroundWindow.Call(fx.hT)
	fx.emit("READY", "", "role", "main", "pid", strconv.Itoa(os.Getpid()), "tid", strconv.Itoa(int(fx.tid)),
		"t", hwndStr(fx.hT), "edit", hwndStr(fx.hEdit), "decoy", hwndStr(fx.hDecoy), "blank", hwndStr(fx.hBlank), "black", hwndStr(fx.hBlack))
	return true
}

func createInterferer(hinst uintptr, x, y, w, h int) bool {
	if !registerClass(hinst, "A2ProbeInterferer", solidWindowProc(func(h uintptr, rc rect32) { fillRectColor(h, rc, rgb(255, 0, 255)) })) {
		return false
	}
	// 最前面・非アクティブ・マゼンタ一色。メニューを閉じさせないよう、アクティブにならない窓にする。
	hwnd := createWindow(wsExTopmost|wsExNoActivate|wsExToolWindow, "A2ProbeInterferer", "A2 Interferer", wsPopup|wsVisible, x, y, w, h, 0, 0, hinst)
	if hwnd == 0 {
		return false
	}
	fx.hT = hwnd // 終了指示の宛先
	fx.emit("READY", "", "role", "interferer", "pid", strconv.Itoa(os.Getpid()), "tid", strconv.Itoa(int(fx.tid)), "hwnd", hwndStr(hwnd))
	return true
}

func createForeign(hinst, owner uintptr, x, y, w, h int) bool {
	if !registerClass(hinst, "A2ProbeForeign", solidWindowProc(func(h uintptr, rc rect32) { fillRectColor(h, rc, rgb(60, 60, 60)) })) {
		return false
	}
	// 別プロセスの窓 T を所有者にして、所有される窓を作る。作れなければ hwnd=0 で名乗る（probe は SKIP と記録する）。
	hwnd := createWindow(wsExToolWindow, "A2ProbeForeign", "A2 Foreign", wsPopup|wsCaption|wsVisible, x, y, w, h, owner, 0, hinst)
	fx.hT = hwnd
	if hwnd == 0 {
		fx.emit("READY", "", "role", "foreign", "pid", strconv.Itoa(os.Getpid()), "tid", strconv.Itoa(int(fx.tid)), "hwnd", "0", "err", strconv.Itoa(int(lastCreateErr)))
		return true
	}
	fx.emit("READY", "", "role", "foreign", "pid", strconv.Itoa(os.Getpid()), "tid", strconv.Itoa(int(fx.tid)), "hwnd", hwndStr(hwnd))
	return true
}

func appendMenu(menu uintptr, flags uint32, id uintptr, text string) {
	if text == "" {
		_, _, _ = pAppendMenuW.Call(menu, uintptr(flags), id, 0)
		return
	}
	p := utf16Ptr(text) // Call の引数式の中で uintptr にして、呼び出しの間 p を生かしておく
	_, _, _ = pAppendMenuW.Call(menu, uintptr(flags), id, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
}

// --- ウィンドウプロシージャ ---

func fillRectColor(hdc uintptr, r rect32, c uint32) {
	brush, _, _ := pCreateSolidBrush.Call(uintptr(c))
	_, _, _ = pFillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), brush)
	_, _, _ = pDeleteObject.Call(brush)
}

func drawText(hdc uintptr, x, y int, s string) {
	_, _, _ = pSetBkMode.Call(hdc, transparentBk)
	_, _, _ = pSetTextColor.Call(hdc, uintptr(rgb(255, 255, 255)))
	u := utf16.Encode([]rune(s))
	if len(u) == 0 {
		return
	}
	_, _, _ = pTextOutW.Call(hdc, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)))
}

func paintTarget(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	var rc rect32
	_, _, _ = pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	midX, midY := rc.Right/2, rc.Bottom/2
	fillRectColor(hdc, rect32{0, 0, midX, midY}, quadrants[0])
	fillRectColor(hdc, rect32{midX, 0, rc.Right, midY}, quadrants[1])
	fillRectColor(hdc, rect32{0, midY, midX, rc.Bottom}, quadrants[2])
	fillRectColor(hdc, rect32{midX, midY, rc.Right, rc.Bottom}, quadrants[3])
	pen, _, _ := pCreatePen.Call(psSolid, 3, 0)
	old, _, _ := pSelectObject.Call(hdc, pen)
	_, _, _ = pMoveToEx.Call(hdc, 0, 0, 0)
	_, _, _ = pLineTo.Call(hdc, uintptr(rc.Right), uintptr(rc.Bottom))
	_, _, _ = pSelectObject.Call(hdc, old)
	_, _, _ = pDeleteObject.Call(pen)
	drawText(hdc, 12, 12, "A2PROBE TARGET")
	_, _, _ = pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
}

func defProc(hwnd, msg, wp, lp uintptr) uintptr {
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func targetProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch uint32(msg) {
	case wmPaint:
		paintTarget(hwnd)
		return 0
	case wmEraseBkgnd:
		return 1
	case wmMeasureItem:
		if it := ptrAt[measureItemStruct](lp); it.CtlType == 1 { // ODT_MENU
			it.ItemWidth, it.ItemHeight = 180, 30
			return 1
		}
	case wmDrawItem:
		if it := ptrAt[drawItemStruct](lp); it.CtlType == 1 && int(it.ItemData) < len(odColors) {
			c := odColors[it.ItemData]
			fillRectColor(it.Hdc, it.Item, rgb(c.R, c.G, c.B))
			drawText(it.Hdc, int(it.Item.Left)+8, int(it.Item.Top)+6, "COLOR "+strconv.Itoa(int(it.ItemData)))
			return 1
		}
	case wmCommand:
		switch wp & 0xFFFF {
		case idSaveAs:
			postMessage(hwnd, wmAppOpenSave, 0, 0) // メニューのループを抜けてから開く
			return 0
		case idExit:
			postMessage(hwnd, wmClose, 0, 0)
			return 0
		}
	case wmAppOpenSave:
		openSaveDialog(hwnd)
		return 0
	case wmAppCloseMenu:
		_, _, _ = pEndMenu.Call()
		return 0
	case wmClose:
		_, _, _ = pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		_, _, _ = pPostQuitMessage.Call(0)
		return 0
	}
	return defProc(hwnd, msg, wp, lp)
}

// solidWindowProc は塗りつぶしだけの窓のプロシージャを作る。draw が nil なら何も描かない（PrintWindow が空を返す場合の検出用）。
func solidWindowProc(draw func(hdc uintptr, rc rect32)) uintptr {
	return windows.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
		switch uint32(msg) {
		case wmPaint:
			var ps paintStruct
			hdc, _, _ := pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
			if draw != nil {
				var rc rect32
				_, _, _ = pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
				draw(hdc, rc)
			}
			_, _, _ = pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
			return 0
		case wmEraseBkgnd:
			return 1
		case wmClose:
			_, _, _ = pDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			if hwnd == fx.hT { // interferer / foreign は自分が終了の宛先
				_, _, _ = pPostQuitMessage.Call(0)
			}
			return 0
		}
		return defProc(hwnd, msg, wp, lp)
	})
}

// --- 保存ダイアログ（S3） ---

func multiSZ(parts ...string) []uint16 {
	var out []uint16
	for _, p := range parts {
		out = append(out, utf16.Encode([]rune(p))...)
		out = append(out, 0)
	}
	return append(out, 0)
}

// openSaveDialog は T を所有者にした保存ダイアログ（同一プロセス・同一スレッドのモーダル）を開く。閉じるまで戻らない。
// 保存の指示（OK）が来たら、専用の一時フォルダーの .a2p だけに 1 行書く。
func openSaveDialog(owner uintptr) {
	buf := make([]uint16, 1024)
	filter := multiSZ("A2 probe files (*.a2p)", "*.a2p")
	ofn := &openFileName{
		Owner:       owner,
		Filter:      &filter[0],
		FilterIndex: 1,
		File:        &buf[0],
		MaxFile:     uint32(len(buf)),
		InitialDir:  utf16Ptr(fx.saveDir),
		Title:       utf16Ptr("A2 Probe Save"),
		Flags:       0x00080000 | 0x00000008 | 0x00000002 | 0x00000800 | 0x00000004, // EXPLORER | NOCHANGEDIR | OVERWRITEPROMPT | PATHMUSTEXIST | HIDEREADONLY
		DefExt:      utf16Ptr("a2p"),
	}
	ofn.StructSize = uint32(unsafe.Sizeof(*ofn))
	r, _, _ := pGetSaveFileNameW.Call(uintptr(unsafe.Pointer(ofn)))
	if r == 0 {
		code, _, _ := pCommDlgExtendedError.Call()
		fx.emit("EVT", "save-closed", "saved", "0", "cderr", strconv.Itoa(int(code)))
		return
	}
	path := windows.UTF16ToString(buf)
	if !strings.EqualFold(filepath.Dir(path), filepath.Clean(fx.saveDir)) || !strings.EqualFold(filepath.Ext(path), ".a2p") {
		fx.emit("EVT", "save-closed", "saved", "0", "reason", "outside_savedir")
		return
	}
	if err := os.WriteFile(path, []byte("a2probe\n"), 0o600); err != nil {
		fx.emit("EVT", "save-closed", "saved", "0", "reason", "write_failed")
		return
	}
	fx.emit("EVT", "save-closed", "saved", "1", "name", filepath.Base(path))
}

var enumSaveCallback = sync.OnceValue(func() uintptr {
	return windows.NewCallback(func(hwnd, _ uintptr) uintptr {
		if className(hwnd) != "#32770" {
			return 1
		}
		if owner, _, _ := pGetWindow.Call(hwnd, gwOwnerCmd); owner == fx.hT {
			fx.enumFound = hwnd
			return 0
		}
		return 1
	})
})

// findSaveDialog は UI スレッドの #32770 のうち T に所有されているもの（保存ダイアログ）を返す。なければ 0。
func findSaveDialog() uintptr {
	fx.enumMu.Lock()
	defer fx.enumMu.Unlock()
	fx.enumFound = 0
	_, _, _ = pEnumThreadWindows.Call(uintptr(fx.tid), enumSaveCallback(), 0)
	return fx.enumFound
}

// --- probe からの指示 ---

func readCommands() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		m, err := parseMessage(sc.Text())
		if err != nil || m.Verb != "CMD" {
			fx.emit("ERR", "parse", "reason", "bad_line")
			continue
		}
		handleCommand(m)
	}
	// 標準入力が閉じた（probe が終了した）ら自分も終わる。
	postMessage(fx.hT, wmClose, 0, 0)
	time.AfterFunc(2*time.Second, func() { os.Exit(0) })
}

func fieldInt(m message, key string) (int, bool) {
	n, err := strconv.Atoi(m.Fields[key])
	return n, err == nil
}

func handleCommand(m message) {
	ack := func() { fx.emit("ACK", m.Name) }
	fail := func(reason string) { fx.emit("ERR", m.Name, "reason", reason) }
	if fx.role != "main" {
		if m.Name == "quit" {
			ack()
			postMessage(fx.hT, wmClose, 0, 0)
			return
		}
		fail("not_main")
		return
	}
	switch m.Name {
	case "menubar-open": // name=file|color。メニューバーのメニューを開く（SC_KEYMENU。モーダルなので投函する）
		key := map[string]uintptr{"file": 'f', "color": 'c'}[m.Fields["name"]]
		if key == 0 {
			fail("unknown_menu")
			return
		}
		ack()
		_, _, _ = pSetForegroundWindow.Call(fx.hT) // 自分のプロセスの窓を前面に（メニューは前面でないと閉じることがある）
		postMessage(fx.hT, wmSysCommand, scKeyMenu, key)
	case "context-open": // x=,y= 画面座標。EDIT の標準の右クリックメニューを開く
		x, ok1 := fieldInt(m, "x")
		y, ok2 := fieldInt(m, "y")
		if !ok1 || !ok2 {
			fail("bad_point")
			return
		}
		ack()
		_, _, _ = pSetForegroundWindow.Call(fx.hT)
		postMessage(fx.hEdit, wmContextMenu, fx.hEdit, makeLParam(x, y))
	case "menu-close":
		ack()
		if _, ok := sendTimeout(fx.hT, wmAppCloseMenu, 0, 0, 2*time.Second); !ok {
			fx.emit("ERR", "menu-close", "reason", "send_timeout")
		}
	case "menu-key": // vk=。開いているメニューへキーメッセージを投函する（グローバル入力ではない）
		vk, ok := fieldInt(m, "vk")
		if !ok {
			fail("bad_vk")
			return
		}
		ack()
		scan, _, _ := pMapVirtualKeyW.Call(uintptr(vk), 0)
		down := uintptr(1) | scan<<16
		up := down | 1<<30 | 1<<31
		postMessage(fx.hT, wmKeyDown, uintptr(vk), down)
		postMessage(fx.hT, wmKeyUp, uintptr(vk), up)
	case "save-open":
		ack()
		postMessage(fx.hT, wmAppOpenSave, 0, 0)
	case "save-fill": // name=。ファイル名欄へ書き、読み戻して記録する
		dlg := findSaveDialog()
		if dlg == 0 {
			fail("no_dialog")
			return
		}
		// SendMessageTimeout へ渡すポインタは、スタック上のバッファだと別関数の呼び出しで移動しうるので、ヒープ（パッケージ変数）に置く。
		name := utf16.Encode([]rune(m.Fields["name"]))
		if len(name) >= len(fillBuf) {
			fail("name_too_long")
			return
		}
		clear(fillBuf[:])
		copy(fillBuf[:], name)
		if _, ok := sendTimeout(dlg, cdmSetControlTx, idFilenameCombo, uintptr(unsafe.Pointer(&fillBuf[0])), 2*time.Second); !ok {
			fail("set_timeout")
			return
		}
		clear(readBuf[:])
		n, ok := sendTimeout(dlg, cdmGetSpec, uintptr(len(readBuf)), uintptr(unsafe.Pointer(&readBuf[0])), 2*time.Second)
		got := ""
		if ok && int32(n) > 0 {
			got = windows.UTF16ToString(readBuf[:])
		}
		fx.emit("ACK", m.Name, "match", boolStr(got == m.Fields["name"]))
	case "save-ok", "save-cancel":
		dlg := findSaveDialog()
		if dlg == 0 {
			fail("no_dialog")
			return
		}
		id := uintptr(1) // IDOK
		if m.Name == "save-cancel" {
			id = 2 // IDCANCEL
		}
		ack()
		postMessage(dlg, wmCommand, id, 0)
	case "quit":
		ack()
		postMessage(fx.hT, wmClose, 0, 0)
	default:
		fail("unknown_command")
	}
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
