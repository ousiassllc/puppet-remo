package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// このファイルはビューアのウィンドウ選択・グループ表示・手動操作（#157）を、実 Control・実 Streamer・実 LeaseManager・実 ws・実ルーターに
// **偽 platform**（FakeWindows・FakeCapturer・FakeApp）をつないで確かめる。成功経路は偽 platform のもので、Windows 実機の成立性ゲート
// （S1〜S3。実機未確認）の代わりにならない。ws パッケージは platform を import できないため、この組み立てのテストは app に置く。

// scopedPlatform はスコープ付きの試験が使う偽 platform（env.input・env.windows の代わりに差し込む）。
type scopedPlatform struct {
	windows   *platformtest.FakeWindows
	capturer  *platformtest.FakeCapturer
	input     *platformtest.FakeInput
	clipboard *platformtest.FakeClipboard
	app       *platformtest.FakeApp
}

func newScopedPlatform() *scopedPlatform {
	p := &scopedPlatform{
		windows: platformtest.NewFakeWindows(),
		capturer: platformtest.NewFakeCapturer(domain.Monitor{
			Index: 0, Name: "DISPLAY1", Primary: true, Size: domain.Size{W: 1920, H: 1080}, DPI: 96,
		}),
		input:     platformtest.NewFakeInput(),
		clipboard: platformtest.NewFakeClipboard(),
	}
	p.capturer.AttachWindows(p.windows)
	p.app = platformtest.NewFakeApp(p.windows, p.input, p.clipboard)
	return p
}

func (p *scopedPlatform) mutate(cfg *Config) {
	cfg.Platform.Capturer, cfg.Platform.Input, cfg.Platform.Windows, cfg.Platform.Clipboard = p.capturer, p.app, p.windows, p.clipboard
	cfg.Platform.UI = platformtest.NewFakeUIAutomation(p.windows)
}

// injected は注入器に届いた入力の数（ReleaseAll・カーソル位置の読み取りは含まない）。
func (p *scopedPlatform) injected() int {
	n := 0
	for _, c := range p.input.Calls() {
		switch c.Method {
		case "MoveTo", "Click", "Button", "Drag", "Scroll", "Wheel", "Key", "KeyEvent", "Text":
			n++
		}
	}
	return n
}

// wsFeed は WebSocket を読み続け、テキストのメッセージを type ごとのチャネルに、グループ源のフレームの見出しを group に集める
// （ほかの type のメッセージを待つ間に、目的の type のメッセージを捨てない）。切断・閉じコードは closed に入る。
type wsFeed struct {
	t      *testing.T
	closed chan int // 閉じコード（読み取りの失敗）

	mu     sync.Mutex
	byType map[string]chan map[string]any
	group  service.GroupFrameInfo
	frames int
}

func newWSFeed(t *testing.T, c *websocket.Conn) *wsFeed {
	f := &wsFeed{t: t, closed: make(chan int, 1), byType: map[string]chan map[string]any{}}
	go func() {
		for {
			typ, data, err := c.Read(context.Background())
			if err != nil {
				f.closed <- int(websocket.CloseStatus(err))
				return
			}
			if typ == websocket.MessageBinary {
				f.mu.Lock()
				f.frames++
				if info, ok := service.ParseGroupFrameInfo(data); ok {
					f.group = info
				}
				f.mu.Unlock()
				continue
			}
			var m map[string]any
			if json.Unmarshal(data, &m) == nil {
				select {
				case f.channel(m["type"].(string)) <- m:
				default:
				}
			}
		}
	}()
	return f
}

func (f *wsFeed) channel(typ string) chan map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.byType[typ]
	if !ok {
		ch = make(chan map[string]any, 8192)
		f.byType[typ] = ch
	}
	return ch
}

// expect は typ のメッセージのうち pred を満たすものが届くまで読み（同じ type の満たさないものは捨てる）、返す。
func (f *wsFeed) expect(typ, what string, pred func(map[string]any) bool) map[string]any {
	f.t.Helper()
	ch := f.channel(typ)
	timeout := time.After(waitLimit)
	for {
		select {
		case m := <-ch:
			if pred(m) {
				return m
			}
		case <-timeout:
			f.t.Fatalf("timed out waiting for %s", what)
			return nil
		}
	}
}

func (f *wsFeed) target(state, reason string) map[string]any {
	f.t.Helper()
	return f.targetWhere(state, reason, func(map[string]any) bool { return true })
}

func (f *wsFeed) targetWhere(state, reason string, pred func(map[string]any) bool) map[string]any {
	f.t.Helper()
	return f.expect("target", "target "+state+" "+reason, func(m map[string]any) bool {
		r, _ := m["reason"].(string)
		return m["state"] == state && r == reason && pred(m)
	})
}

func (f *wsFeed) groupConfig(w, h, ox, oy int) map[string]any {
	f.t.Helper()
	return f.expect("config", "group config", func(m map[string]any) bool {
		o, _ := m["origin"].(map[string]any)
		return m["source"] == "group" && m["width"] == float64(w) && m["height"] == float64(h) &&
			o != nil && o["x"] == float64(ox) && o["y"] == float64(oy)
	})
}

func (f *wsFeed) errorMsg(code, reason string) map[string]any {
	f.t.Helper()
	return f.expect("error", "error "+code+" "+reason, func(m map[string]any) bool {
		r, _ := m["reason"].(string)
		return m["code"] == code && r == reason
	})
}

// scopedViewer は偽アプリ T を束縛したリースを持つビューア。
type scopedViewer struct {
	t      *testing.T
	e      *env
	p      *scopedPlatform
	c      *websocket.Conn
	f      *wsFeed
	id     string
	actor  domain.Actor
	client domain.Client
}

func newScopedEnv(t *testing.T) (*env, *scopedPlatform) {
	t.Helper()
	p := newScopedPlatform()
	e := newEnv(t, p.mutate)
	// 監査のサムネイル（FR-AU02）は操作のたびにモニターを撮る（ビューアの表示とは別の、ホスト側の記録）。表示がモニター撮影に
	// 切り替わらないことの確認（MonitorCaptureCalls）と混ざらないよう、この試験では切る。
	if err := e.db.Settings().Set(context.Background(), "audit.thumbnails_enabled", json.RawMessage("false")); err != nil {
		t.Fatal(err)
	}
	e.registerClient()
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)
	return e, p
}

// connectViewer は新しい viewer_id でつなぎ、ready を読む（既定の fps。モニター表示で始まる）。リースはまだ取らない。
func connectViewer(t *testing.T, e *env, p *scopedPlatform) *scopedViewer {
	t.Helper()
	client, err := e.db.Clients().Get(context.Background(), "client-1")
	if err != nil {
		t.Fatal(err)
	}
	id := e.app.viewers.Issue(client)
	c, _, err := e.dialWS(id)
	if err != nil {
		t.Fatal(err)
	}
	if m := wsReadJSON(t, c); m["type"] != "ready" || m["source"] != "monitor" {
		t.Fatalf("ready = %v, want the monitor source", m)
	}
	actor, err := e.app.viewers.Actor(id, client)
	if err != nil {
		t.Fatal(err)
	}
	return &scopedViewer{t: t, e: e, p: p, c: c, f: newWSFeed(t, c), id: id, actor: actor, client: client}
}

// selectTarget はスコープ付きのリースを取らせ（HTTP と同じ RequestAcquireScoped の結果を、承認なしの client で）、グループ源の最初の config を待つ。
func (v *scopedViewer) selectTarget() {
	v.t.Helper()
	if _, err := v.e.app.leases.AcquireScoped(context.Background(), v.actor, v.p.app.Ref()); err != nil {
		v.t.Fatalf("AcquireScoped: %v", err)
	}
	v.f.groupConfig(400, 300, 100, 100)
}

func (v *scopedViewer) send(m map[string]any) { wsWriteJSON(v.t, v.c, m) }

// click は合成画像の座標 p のクリック（move → down → up）。
func (v *scopedViewer) click(p domain.Point, button string) {
	v.send(map[string]any{"type": "input.mouse", "action": "move", "x": p.X, "y": p.Y})
	v.send(map[string]any{"type": "input.mouse", "action": "down", "x": p.X, "y": p.Y, "button": button})
	v.send(map[string]any{"type": "input.mouse", "action": "up", "x": p.X, "y": p.Y, "button": button})
}

func (v *scopedViewer) key(code string) {
	v.send(map[string]any{"type": "input.key", "action": "down", "code": code, "key": code})
}

// 合成画像（原点 (100,100)）の座標。偽アプリの物理座標から原点を引いた値。
var (
	imgFileButton = domain.Point{X: 25, Y: 15}   // 物理 (125,115)
	imgFileItem   = domain.Point{X: 80, Y: 37}   // 物理 (180,137)
	imgEdit       = domain.Point{X: 200, Y: 150} // 物理 (300,250)
)

func TestScopedWebSocketShowsOnlyTheGroupAndOperatesThroughTheMenuAndDialog(t *testing.T) {
	e, p := newScopedEnv(t)
	v := connectViewer(t, e, p)
	monitorCalls := p.capturer.MonitorCaptureCalls() // 選ぶ前のモニター表示の分
	v.selectTarget()
	v.f.target("active", "")

	// 選ぶと対象が前面になる（ws が FocusWindow を通す。偽アプリは T が前面のまま）。
	// メニューバーのメニュー → 項目 → 保存ダイアログ（T の矩形の外に出る。画像が広がり、原点が動く）。
	v.click(imgFileButton, "left")
	eventually(t, "the file menu to open", func() bool { return p.app.MenuOpen() == "file" })
	v.click(imgFileItem, "left")
	eventually(t, "the save dialog to open", func() bool { return p.app.DialogOpen() })
	v.f.groupConfig(550, 320, 100, 80)

	// ファイル名の入力と Enter（ダイアログは前面のメンバー）。画像は T だけの大きさに戻る。
	v.send(map[string]any{"type": "input.text", "text": "memo.txt"})
	v.key("Enter")
	eventually(t, "the file to be saved", func() bool { return len(p.app.Saved()) == 1 })
	if got := p.app.Saved()[0]; got != "memo.txt" {
		t.Errorf("saved = %q, want memo.txt", got)
	}
	v.f.groupConfig(400, 300, 100, 100)

	// 右クリックメニューも同様（メニューは T の内側）。
	v.click(imgEdit, "right")
	eventually(t, "the context menu to open", func() bool { return p.app.MenuOpen() == "context" })

	// メンバーが増減しても停止しない。
	if h, _ := e.app.leases.Holder(); h.Scope == nil || h.Scope.Halted() {
		t.Errorf("scope = %+v, want active after the members came and went", h.Scope)
	}
	// 表示はグループ撮影だけ（モニター撮影・画面領域撮影は選んだ後に 1 回も呼ばれない）。
	if got := p.capturer.MonitorCaptureCalls() - monitorCalls; got != 0 {
		t.Errorf("monitor capture was called %d times after the target was selected, want 0", got)
	}
	if p.capturer.GroupGrabCalls() == 0 {
		t.Error("the group was never captured")
	}
	v.f.mu.Lock()
	frames := v.f.frames
	v.f.mu.Unlock()
	if frames == 0 {
		t.Error("no frame was streamed to the viewer")
	}
}

// 非メンバーが前面になると、キー・文字・マウスは注入されず、halted になる（別アプリには何も入らない）。
func TestScopedWebSocketNonMemberForegroundHaltsAndInjectsNothing(t *testing.T) {
	e, p := newScopedEnv(t)
	v := connectViewer(t, e, p)
	v.selectTarget()
	before := p.injected()

	p.windows.Add(domain.WindowInfo{
		ID: "9001", Title: "other-secret-title", Process: "other.exe", PID: 7000, ProcessStart: 7_000, Class: "OtherClass",
		Rect: domain.Rect{X: 800, Y: 100, W: 300, H: 300}, Foreground: true,
	})
	v.key("KeyA")
	m := v.f.errorMsg("input_blocked", "target_not_foreground")
	if m["code"] != "input_blocked" {
		t.Fatalf("error = %v", m)
	}
	halted := v.f.targetWhere("halted", "target_not_foreground", func(m map[string]any) bool { return m["blocked_by"] != nil })
	by, _ := halted["blocked_by"].(map[string]any)
	if by == nil || by["process"] != "other.exe" || by["window_id"] != "9001" {
		t.Errorf("blocked_by = %v, want other.exe / 9001", halted["blocked_by"])
	}
	if strings.Contains(mustJSON(t, halted), "other-secret-title") {
		t.Error("the window title must not be sent in the target message")
	}
	v.send(map[string]any{"type": "input.text", "text": "x"})
	v.click(imgEdit, "left")
	v.f.errorMsg("input_blocked", "target_halted")
	if got := p.injected() - before; got != 0 {
		t.Errorf("%d inputs reached the platform while a non-member was in front, want 0", got)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// T に重なった非メンバー: 重なっていない座標のクリックは入り、重なっている座標は target_obscured で止まる。
func TestScopedWebSocketObscuredPointHaltsButFreePointPasses(t *testing.T) {
	e, p := newScopedEnv(t)
	v := connectViewer(t, e, p)
	v.selectTarget()
	// T（物理 100..500 × 100..400）の右半分を非メンバーが覆う（前面は T のまま）。
	p.windows.Add(domain.WindowInfo{
		ID: "9002", Title: "x", Process: "other.exe", PID: 7000, ProcessStart: 7_000, Class: "OtherClass",
		Rect: domain.Rect{X: 300, Y: 100, W: 400, H: 300},
	})
	v.click(imgFileButton, "left") // 物理 (125,115): 覆われていない
	eventually(t, "the free click to open the menu", func() bool { return p.app.MenuOpen() == "file" })
	v.send(map[string]any{"type": "input.mouse", "action": "move", "x": 300, "y": 100}) // 物理 (400,200): 覆われている
	v.f.errorMsg("input_blocked", "target_obscured")
	v.f.target("halted", "target_obscured")
}

// halted の間は、Esc を含むキー・マウス・文字・クリップボード書き込みを受け付けない。原因を消しても halted のまま。
// 明示的な再開は全条件を再検証し、通れば active、通らなければ halted のまま理由が更新される。
func TestScopedWebSocketHaltedRejectsEverythingAndResumesOnlyExplicitly(t *testing.T) {
	e, p := newScopedEnv(t)
	v := connectViewer(t, e, p)
	v.selectTarget()
	p.windows.Add(domain.WindowInfo{
		ID: "9003", Title: "x", Process: "other.exe", PID: 7000, ProcessStart: 7_000, Class: "OtherClass",
		Rect: domain.Rect{X: 800, Y: 100, W: 300, H: 300}, Foreground: true,
	})
	v.key("KeyA")
	v.f.target("halted", "target_not_foreground")
	before := p.injected()

	for _, in := range []map[string]any{
		{"type": "input.key", "action": "down", "code": "Escape", "key": "Escape"},
		{"type": "input.key", "action": "up", "code": "Escape", "key": "Escape"},
		{"type": "input.mouse", "action": "move", "x": 10, "y": 10},
		{"type": "input.mouse", "action": "down", "x": 10, "y": 10, "button": "left"},
		{"type": "input.mouse", "action": "wheel", "x": 10, "y": 10, "dy": 1},
		{"type": "input.text", "text": "a"},
	} {
		v.send(in)
		v.f.errorMsg("input_blocked", "target_halted")
	}
	// クリップボード書き込み（HTTP）も拒否される。
	res := e.viewerPost(t, v.id, "/viewer/clipboard/send", url.Values{"text": {"secret"}})
	if res.StatusCode != http.StatusConflict || !strings.Contains(res.body, `data-error-code="input_blocked"`) {
		t.Errorf("clipboard write while halted = %d %s, want 409 input_blocked", res.StatusCode, res.body)
	}
	if got := p.injected() - before; got != 0 || len(p.clipboard.CallsTo("WriteText")) != 0 {
		t.Errorf("injected = %d, clipboard writes = %d while halted, want 0", got, len(p.clipboard.CallsTo("WriteText")))
	}

	// 原因（前面の非メンバー）をホスト側で取り除いても、自動では戻らない。
	p.windows.Remove("9003")
	p.windows.Raise(platformtest.FakeAppMainID)
	p.windows.Update(platformtest.FakeAppMainID, func(w *domain.WindowInfo) { w.Foreground = true })
	v.key("KeyB")
	v.f.errorMsg("input_blocked", "target_halted")
	if h, _ := e.app.leases.Holder(); h.Scope == nil || !h.Scope.Halted() {
		t.Fatalf("scope = %+v, want halted after the cause was removed", h.Scope)
	}

	// 原因が残っているうちの再開は halted のまま（理由は更新される）。
	p.windows.Add(domain.WindowInfo{
		ID: "9004", Title: "x", Process: "other2.exe", PID: 7100, ProcessStart: 7_100, Class: "OtherClass",
		Rect: domain.Rect{X: 800, Y: 100, W: 300, H: 300}, Foreground: true,
	})
	v.send(map[string]any{"type": "target.resume"})
	v.f.target("halted", "target_not_foreground")
	if got := p.injected() - before; got != 0 {
		t.Errorf("a failed resume injected %d inputs, want 0", got)
	}
	// 原因を取り除いてから再開すれば active に戻り、入力が通る。
	p.windows.Remove("9004")
	p.windows.Update(platformtest.FakeAppMainID, func(w *domain.WindowInfo) { w.Foreground = true })
	v.send(map[string]any{"type": "target.resume"})
	v.f.target("active", "")
	v.click(imgFileButton, "left")
	eventually(t, "input to pass after the explicit resume", func() bool { return p.app.MenuOpen() == "file" })
}

// 撮影が失敗しても（T・メンバーとも）モニター・画面領域の撮影に切り替わらず、halted になる。
func TestScopedWebSocketCaptureFailureHaltsWithoutMonitorCapture(t *testing.T) {
	t.Run("T が撮れない", func(t *testing.T) {
		e, p := newScopedEnv(t)
		v := connectViewer(t, e, p)
		v.selectTarget()
		monitorCalls := p.capturer.MonitorCaptureCalls()
		before := p.injected()
		p.capturer.SetNotPrintable(platformtest.FakeAppMainID, true)
		v.f.target("halted", "capture_unavailable")
		v.key("KeyA")
		v.f.errorMsg("input_blocked", "target_halted")
		if got := p.capturer.MonitorCaptureCalls() - monitorCalls; got != 0 {
			t.Errorf("monitor capture was called %d times after the capture failed, want 0", got)
		}
		if got := p.injected() - before; got != 0 {
			t.Errorf("%d inputs reached the platform, want 0", got)
		}
	})
	t.Run("関連するメニューが撮れない", func(t *testing.T) {
		e, p := newScopedEnv(t)
		v := connectViewer(t, e, p)
		v.selectTarget()
		monitorCalls := p.capturer.MonitorCaptureCalls()
		p.capturer.SetNotPrintable(platformtest.FakeAppMenuID, true)
		v.click(imgFileButton, "left")
		eventually(t, "the menu to open", func() bool { return p.app.MenuOpen() == "file" })
		v.f.target("halted", "capture_member_unavailable")
		if got := p.capturer.MonitorCaptureCalls() - monitorCalls; got != 0 {
			t.Errorf("monitor capture was called %d times, want 0", got)
		}
	})
}

// 別プロセスの偽ダイアログ（T に所有されるが別プロセス）が前面に出ると、入力は届かず halted。
func TestScopedWebSocketCrossProcessDialogHalts(t *testing.T) {
	e, p := newScopedEnv(t)
	p.app.DialogCrossProcess = true
	v := connectViewer(t, e, p)
	v.selectTarget()
	v.click(imgFileButton, "left")
	eventually(t, "the menu to open", func() bool { return p.app.MenuOpen() == "file" })
	v.click(imgFileItem, "left")
	eventually(t, "the common dialog to open", func() bool { return p.app.DialogOpen() })
	before := p.injected()
	v.send(map[string]any{"type": "input.text", "text": "x"})
	v.f.errorMsg("input_blocked", "related_cross_process")
	v.f.target("halted", "related_cross_process")
	if got := p.injected() - before; got != 0 {
		t.Errorf("%d inputs reached the cross-process dialog, want 0", got)
	}
}

// 所有関係を確かめられないメニューは表示から除外され（capture_partial の通知）、停止しない。入力先になると停止する。
func TestScopedWebSocketUnverifiableMenuIsAPartialNoticeThenHaltsWhenItGetsInput(t *testing.T) {
	e, p := newScopedEnv(t)
	p.app.OwnerlessMenu = true
	v := connectViewer(t, e, p)
	v.selectTarget()
	v.click(imgFileButton, "left")
	eventually(t, "the menu to open", func() bool { return p.app.MenuOpen() == "file" })
	m := v.f.expect("target", "capture_partial notice", func(m map[string]any) bool {
		n, _ := m["notices"].([]any)
		return m["state"] == "active" && len(n) == 1 && n[0] == "capture_partial"
	})
	if ex, _ := m["excluded"].([]any); len(ex) != 1 {
		t.Errorf("excluded = %v, want the menu", m["excluded"])
	}
	// メニューの座標（物理 (180,137) = 画像外。T の内側の点でも最上位がメニュー）へのクリックは related_unverifiable で停止する。
	v.send(map[string]any{"type": "input.mouse", "action": "move", "x": imgFileItem.X, "y": imgFileItem.Y})
	v.f.target("halted", "related_unverifiable")
}

// クリップボードの読み取りは、UI を経由しない直接の要求でもサーバー側で拒否される（platform の読み取りは呼ばない）。書き込みは通る。
func TestScopedClipboardReadIsRejectedByTheServer(t *testing.T) {
	e, p := newScopedEnv(t)
	v := connectViewer(t, e, p)
	v.selectTarget()
	p.clipboard.SetText("host-secret")
	res := e.viewerPost(t, v.id, "/viewer/clipboard/fetch", nil)
	if res.StatusCode != http.StatusConflict || !strings.Contains(res.body, `data-error-code="input_blocked"`) || strings.Contains(res.body, "host-secret") {
		t.Errorf("clipboard fetch = %d %s, want 409 input_blocked without the content", res.StatusCode, res.body)
	}
	if n := len(p.clipboard.CallsTo("ReadText")); n != 0 {
		t.Errorf("the platform clipboard was read %d times, want 0", n)
	}
	res = e.viewerPost(t, v.id, "/viewer/clipboard/send", url.Values{"text": {"from-viewer"}})
	if res.StatusCode != http.StatusOK {
		t.Errorf("clipboard write = %d %s, want 200", res.StatusCode, res.body)
	}
	if n := len(p.clipboard.CallsTo("WriteText")); n != 1 {
		t.Errorf("clipboard writes = %d, want 1", n)
	}
	// ws に読み取りのメッセージ型は無い（未知の type は 4400 で閉じる）。
	v.send(map[string]any{"type": "clipboard.read"})
	select {
	case code := <-v.f.closed:
		if code != 4400 {
			t.Fatalf("closed with %d, want 4400", code)
		}
	case <-time.After(waitLimit):
		t.Fatal("the connection was not closed for the unknown message type")
	}
}

// 複数のブラウザ: スコープ付きリースの入力は保持者のものだけ。
func TestScopedLeaseInputIsOnlyFromTheHolder(t *testing.T) {
	e, p := newScopedEnv(t)
	holder := connectViewer(t, e, p)
	holder.selectTarget()
	other := connectViewer(t, e, p)
	before := p.injected()
	other.click(imgFileButton, "left")
	other.f.errorMsg("lease_held_by_other", "")
	if got := p.injected() - before; got != 0 {
		t.Errorf("%d inputs from a non-holder reached the platform, want 0", got)
	}
	holder.click(imgFileButton, "left")
	eventually(t, "the holder's input to pass", func() bool { return p.app.MenuOpen() == "file" })
}

// リースを解放するとモニター表示へ戻り、target は none。
func TestScopedReleaseReturnsToTheMonitorSource(t *testing.T) {
	e, p := newScopedEnv(t)
	v := connectViewer(t, e, p)
	v.selectTarget()
	e.app.leases.Release(v.actor)
	v.f.expect("config", "monitor config", func(m map[string]any) bool { return m["source"] == "monitor" })
	v.f.target("none", "")
}

// 入力が座標を読んだ画像（原点・大きさ）が、サーバーの今の画像と違うとき（メニュー・ダイアログの開閉で原点が変わる間に届いた入力）は、
// 古い画像の座標を新しい原点で物理座標にして誤った位置へ入れず、何も注入せず、停止にもしない（stale_frame）。申告が今の画像と一致すれば通る。
func TestScopedStaleFrameInputIsRejectedWithoutHaltingOrInjecting(t *testing.T) {
	e, p := newScopedEnv(t)
	v := connectViewer(t, e, p)
	v.selectTarget()
	v.f.target("active", "")

	v.click(imgFileButton, "left")
	eventually(t, "the file menu to open", func() bool { return p.app.MenuOpen() == "file" })
	v.click(imgFileItem, "left")
	eventually(t, "the save dialog to open", func() bool { return p.app.DialogOpen() })
	v.f.groupConfig(550, 320, 100, 80) // サーバーの画像は、原点 (100,80)・550x320 に変わった

	withFrame := func(m map[string]any, ox, oy, w, h int) map[string]any {
		m["ox"], m["oy"], m["w"], m["h"] = ox, oy, w, h
		return m
	}
	capturesBefore, injectedBefore := p.capturer.MonitorCaptureCalls(), p.injected()
	// 変わる前の画像（原点 (100,100)・400x300）の座標。クライアントがまだ新しい config を読んでいない間に送ったもの。
	for _, action := range []string{"move", "down", "up"} {
		m := map[string]any{"type": "input.mouse", "action": action, "x": imgEdit.X, "y": imgEdit.Y}
		if action != "move" {
			m["button"] = "left"
		}
		v.send(withFrame(m, 100, 100, 400, 300))
		v.f.errorMsg("invalid_coordinates", "stale_frame")
	}
	v.send(withFrame(map[string]any{"type": "input.mouse", "action": "wheel", "x": imgEdit.X, "y": imgEdit.Y, "dy": 1}, 100, 100, 400, 300))
	v.f.errorMsg("invalid_coordinates", "stale_frame")
	if got := p.injected() - injectedBefore; got != 0 {
		t.Fatalf("%d stale inputs reached the platform, want 0", got)
	}
	if !p.app.DialogOpen() || p.app.Selected() {
		t.Errorf("a stale click acted on the app (dialog %v, selected %v)", p.app.DialogOpen(), p.app.Selected())
	}

	// 停止にはなっていない: 今の画像を申告した入力（Cancel。物理 (520,160) = 画像 (420,80)）は通り、ダイアログが閉じる。
	at := domain.Point{X: 420, Y: 80}
	v.send(withFrame(map[string]any{"type": "input.mouse", "action": "move", "x": at.X, "y": at.Y}, 100, 80, 550, 320))
	v.send(withFrame(map[string]any{"type": "input.mouse", "action": "down", "x": at.X, "y": at.Y, "button": "left"}, 100, 80, 550, 320))
	v.send(withFrame(map[string]any{"type": "input.mouse", "action": "up", "x": at.X, "y": at.Y, "button": "left"}, 100, 80, 550, 320))
	eventually(t, "the dialog to close", func() bool { return !p.app.DialogOpen() })
	v.f.target("active", "")
	if n := p.capturer.MonitorCaptureCalls(); n != capturesBefore {
		t.Errorf("monitor capture calls = %d, want %d", n, capturesBefore)
	}

	// 4 つ揃っていない申告は不正なメッセージ（4400）。
	v.send(map[string]any{"type": "input.mouse", "action": "move", "x": 1, "y": 1, "ox": 100})
	select {
	case code := <-v.f.closed:
		if code != 4400 {
			t.Fatalf("closed with %d, want 4400", code)
		}
	case <-time.After(waitLimit):
		t.Fatal("the connection was not closed for the partial frame reference")
	}
}

// ツールバーの「モニター表示」「ウィンドウ表示」: スコープ付きのリースを持ったまま config の source で切り替える（D1。モニター表示は権限として残る）。
// グループ表示のあいだはモニターを撮らず、戻せる。
func TestScopedViewerSwitchesBetweenMonitorAndWindowViews(t *testing.T) {
	e, p := newScopedEnv(t)
	v := connectViewer(t, e, p)
	v.selectTarget()
	v.f.target("active", "")
	inGroup := p.capturer.MonitorCaptureCalls() // グループ表示になるまでのモニターの分（選ぶ前の表示）

	v.send(map[string]any{"type": "config", "source": "monitor"})
	v.f.expect("config", "monitor config", func(m map[string]any) bool { return m["source"] == "monitor" })
	eventually(t, "the monitor to be streamed", func() bool { return p.capturer.MonitorCaptureCalls() > inGroup })
	if _, ok := e.app.leases.Holder(); !ok {
		t.Fatal("the scoped lease was lost by switching to the monitor view")
	}

	v.send(map[string]any{"type": "config", "source": "group"})
	v.f.groupConfig(400, 300, 100, 100)
	// 戻ったあとの操作はグループの座標で通り、モニター撮影は増えない。
	v.click(imgFileButton, "left")
	eventually(t, "the file menu to open", func() bool { return p.app.MenuOpen() == "file" })
	back := p.capturer.MonitorCaptureCalls()
	v.click(imgFileItem, "left")
	eventually(t, "the save dialog to open", func() bool { return p.app.DialogOpen() })
	v.f.groupConfig(550, 320, 100, 80)
	if n := p.capturer.MonitorCaptureCalls(); n != back {
		t.Errorf("monitor capture calls while back in the window view = %d, want %d", n, back)
	}
}

// 「ウィンドウ表示」はこのビューアがスコープ付きのリースを保持しているときだけ。保持していない・他のビューアが保持している場合は拒否し、
// モニター表示のまま（グループ源にもならず、他者の対象も写らない）。
func TestScopedViewerCannotSwitchToTheWindowViewWithoutTheLease(t *testing.T) {
	e, p := newScopedEnv(t)
	other := connectViewer(t, e, p)
	other.send(map[string]any{"type": "config", "source": "group"})
	other.f.errorMsg("invalid_argument", "")

	holder := connectViewer(t, e, p)
	holder.selectTarget()
	other.send(map[string]any{"type": "config", "source": "group"})
	other.f.errorMsg("invalid_argument", "")
	if n := p.capturer.GroupGrabCalls(); n == 0 {
		t.Fatal("the holder's group was never captured") // 保持者の表示は動いている
	}
	other.send(map[string]any{"type": "ping"})
	other.f.expect("pong", "pong after the rejected switch", func(map[string]any) bool { return true })
}

type viewerResponse struct {
	StatusCode int
	body       string
}

// viewerPost は viewer_id つきの POST（リモートルーター経由。X-Puppet-Remo: 1）。
func (e *env) viewerPost(t *testing.T, viewerID, path string, form url.Values) viewerResponse {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+testTailnetIP+":8765"+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Puppet-Remo", "1")
	req.Header.Set("X-Viewer-Id", viewerID)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Transport: remoteTransport{e: e}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return viewerResponse{StatusCode: resp.StatusCode, body: string(b)}
}
