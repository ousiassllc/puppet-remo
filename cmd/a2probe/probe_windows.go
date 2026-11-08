//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	pwin "github.com/ousiassllc/puppet-remo/internal/platform/windows"
)

// 終了コード。FAIL が 1 件でもあれば 1（ツールの失敗ではなく、成立しなかったという測定結果）。
const (
	exitOK             = 0
	exitMeasuredFail   = 1
	exitUsage          = 2
	exitNotInteractive = 3
	exitToolError      = 4
)

// prober は 1 回の測定の状態。
type prober struct {
	ctx context.Context
	out string
	rep *Report
	wm  *pwin.WindowManager

	mainF, interF, foreignF *fixtureProc
	// verified は照合済みの fixture の PID → 役。撮影・記録の対象はこの PID の窓だけ。
	verified map[uint32]string
	// hwnds は READY で名乗り、OS の事実（PID・クラス）と照合した窓。
	hwnds map[uintptr]string

	tHwnd    uintptr
	tRef     domain.WindowRef
	interHwn uintptr
	parking  image.Rectangle // interferer の待機位置
	saveDir  string
	// captured は撮った画像を（段階, 名前）で引けるようにする（合成・参照比較用）。
	pngCount int
}

func runProbe(args []string) int {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	out := fs.String("out", "", "出力先フォルダー（既定: %TEMP%\\a2probe-out-<日時>）")
	timeout := fs.Duration("timeout", 120*time.Second, "全体の制限時間")
	monitor := fs.Int("monitor", -1, "テスト窓を置くモニターの Index（既定: プライマリ）")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *out == "" {
		*out = filepath.Join(os.TempDir(), "a2probe-out-"+time.Now().Format("20060102-150405"))
	}
	if entries, err := os.ReadDir(*out); err == nil && len(entries) > 0 {
		fmt.Fprintln(os.Stderr, "出力先が空でない。既存のファイルを上書きしないので、新しい --out を指定する:", *out)
		return exitUsage
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "出力先を作れない:", err)
		return exitToolError
	}
	if !foregroundExists() {
		fmt.Fprintln(os.Stderr, "対話デスクトップが使えない（前面のウィンドウがない。ロック中・切断中の可能性）。実行しない。")
		return exitNotInteractive
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	wm, err := pwin.NewWindowManager()
	if err != nil {
		fmt.Fprintln(os.Stderr, "WindowManager:", err)
		return exitToolError
	}
	p := &prober{ctx: ctx, out: *out, rep: newReport(), wm: wm, verified: map[uint32]string{}, hwnds: map[uintptr]string{}}

	runErr := func() (err error) {
		defer p.cleanup() // panic でも fixture を終了し、作った一時フォルダーを消す
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("probe の内部エラー（panic）: %v", r)
			}
		}()
		return p.run(*monitor)
	}()
	if runErr != nil {
		p.rep.Add("RUN", "測定の実行", StatusFail, "%v", runErr)
	}
	if ctx.Err() != nil {
		p.rep.Add("RUN.timeout", "全体の制限時間", StatusFail, "制限時間（%v）に達した。以降の項目は測れていない: %v", *timeout, ctx.Err())
	}
	p.summarize()
	if err := p.writeOutputs(); err != nil {
		fmt.Fprintln(os.Stderr, "出力に失敗:", err)
		return exitToolError
	}
	fmt.Println(p.rep.Text())
	fmt.Println("出力先:", p.out)
	if p.rep.Failed() {
		return exitMeasuredFail
	}
	return exitOK
}

// deadlineArg は fixture が自分で終了するまでの時間（probe の制限時間より少し長く）。
func (p *prober) deadlineArg() string {
	d := 150 * time.Second
	if dl, ok := p.ctx.Deadline(); ok {
		d = time.Until(dl) + 30*time.Second
	}
	return "--deadline=" + d.Round(time.Second).String()
}

func (p *prober) writeOutputs() error {
	if err := os.WriteFile(filepath.Join(p.out, "report.txt"), []byte(p.rep.Text()), 0o600); err != nil {
		return err
	}
	js, err := p.rep.JSON()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.out, "result.json"), js, 0o600)
}

func (p *prober) cleanup() {
	p.interF.quit()
	p.foreignF.quit()
	p.mainF.quit()
	if p.saveDir != "" {
		if err := os.RemoveAll(p.saveDir); err != nil {
			p.rep.Note("一時フォルダーの削除に失敗（手で削除する）: %v", err)
		}
	}
}

func (p *prober) sleep(d time.Duration) {
	select {
	case <-time.After(d):
	case <-p.ctx.Done():
	}
}

// --- 環境と起動 ---

func (p *prober) run(monitorIdx int) error {
	mons, err := pwin.NewCapturer().Monitors(p.ctx) // 配置の取得だけ。撮影はしない
	if err != nil {
		return fmt.Errorf("monitors: %w", err)
	}
	if len(mons) == 0 {
		return errors.New("モニターが 1 台も取れない")
	}
	mon := mons[0]
	for _, m := range mons {
		if (monitorIdx < 0 && m.Primary) || m.Index == monitorIdx {
			mon = m
		}
	}
	p.recordEnv(mons, mon)
	p.checkNotepad()

	saveDir := filepath.Join(os.TempDir(), fmt.Sprintf("a2probe-%s-%06d", time.Now().Format("20060102-150405"), rand.IntN(1_000_000)))
	if err := os.Mkdir(saveDir, 0o700); err != nil { // 既存なら失敗にする（自分が作ったフォルダーだけを cleanup で消す）
		return err
	}
	p.saveDir = saveDir

	w := min(720, mon.Size.W-120)
	h := min(520, mon.Size.H-300)
	tx, ty := mon.Origin.X+60, mon.Origin.Y+60
	p.mainF, err = startFixture(p.ctx, "main", p.deadlineArg(), "--x="+strconv.Itoa(tx), "--y="+strconv.Itoa(ty), "--w="+strconv.Itoa(w), "--h="+strconv.Itoa(h), "--savedir="+p.saveDir)
	if err != nil {
		return err
	}
	p.verified[p.mainF.pid] = "main"
	p.parking = image.Rect(mon.Origin.X+mon.Size.W-160, mon.Origin.Y+20, mon.Origin.X+mon.Size.W-20, mon.Origin.Y+100)
	p.interF, err = startFixture(p.ctx, "interferer", p.deadlineArg(), "--x="+strconv.Itoa(p.parking.Min.X), "--y="+strconv.Itoa(p.parking.Min.Y), "--w="+strconv.Itoa(p.parking.Dx()), "--h="+strconv.Itoa(p.parking.Dy()))
	if err != nil {
		return err
	}
	p.verified[p.interF.pid] = "interferer"

	if err := p.verifyMain(); err != nil {
		return err
	}
	if err := p.verifyInterferer(); err != nil {
		return err
	}
	if err := p.startForeign(tx+w+20, ty+h/2); err != nil {
		p.rep.Add("M1.foreign", "別プロセスが T に所有された窓", StatusSkip, "fixture を起動できなかった: %v", err)
	}

	p.baseline()
	p.sysMenuAndDialog()
	p.ownerDrawMenu()
	p.contextMenu()
	p.timing()
	return nil
}

func (p *prober) recordEnv(mons []domain.Monitor, chosen domain.Monitor) {
	e := p.rep.Env
	v := windows.RtlGetVersion()
	e["os_version"] = fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	e["go"] = runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH
	e["probe_elevated"] = strconv.FormatBool(windows.GetCurrentProcessToken().IsElevated())
	e["monitor_count"] = strconv.Itoa(len(mons))
	for _, m := range mons {
		e[fmt.Sprintf("monitor_%d", m.Index)] = fmt.Sprintf("primary=%t origin=(%d,%d) size=%dx%d dpi=%.0f", m.Primary, m.Origin.X, m.Origin.Y, m.Size.W, m.Size.H, m.DPI)
	}
	e["test_monitor"] = strconv.Itoa(chosen.Index)
	e["gpu"] = "（取得しない。GPU 描画アプリの黒画像は fixture では測れない。必要なら手で記録する）"
}

// verifyMain は main fixture の窓が、名乗りどおり fixture の PID のものであることを OS の事実で照合し、T の同一性（WindowRef）を取る。
func (p *prober) verifyMain() error {
	f := p.mainF
	want := map[string]string{"t": "A2ProbeTarget", "edit": "EDIT", "decoy": "A2ProbeDecoy", "blank": "A2ProbeBlank", "black": "A2ProbeBlack"}
	for key, class := range want {
		n, err := strconv.ParseUint(f.ready.Fields[key], 10, 32)
		if err != nil {
			return fmt.Errorf("READY の %s が読めない", key)
		}
		hwnd := uintptr(n)
		if windowPID(hwnd) != f.pid || className(hwnd) != class {
			return fmt.Errorf("照合失敗: %s の窓（クラス・PID）が名乗りと一致しない。撮影しない", key)
		}
		p.hwnds[hwnd] = key
		if key == "t" {
			p.tHwnd = hwnd
		}
	}
	all, err := p.wm.ListAll(p.ctx)
	if err != nil {
		return err
	}
	for _, w := range all {
		if w.ID == strconv.FormatUint(uint64(uint32(p.tHwnd)), 10) {
			if w.PID != f.pid || !domain.RefOf(w).Known() {
				return errors.New("照合失敗: T の同一性（ID・PID・開始時刻・クラス）が揃わない")
			}
			p.tRef = domain.RefOf(w)
			p.rep.Add("M0.identity-main", "main fixture の窓の同一性を照合（READY の PID/HWND ↔ OS の事実）", StatusPass,
				"pid=%d t=%s class=%s process_start=%d。decoy/blank/black/edit も同じ PID・期待のクラス", f.pid, w.ID, w.Class, w.ProcessStart)
			p.rep.Env["t_dpi"] = strconv.Itoa(int(windowDPI(p.tHwnd)))
			return nil
		}
	}
	return errors.New("照合失敗: T が ListAll に出てこない（非表示・cloaked の可能性）")
}

func (p *prober) verifyInterferer() error {
	n, err := strconv.ParseUint(p.interF.ready.Fields["hwnd"], 10, 32)
	if err != nil {
		return errors.New("interferer の READY が読めない")
	}
	hwnd := uintptr(n)
	if windowPID(hwnd) != p.interF.pid || className(hwnd) != "A2ProbeInterferer" {
		return errors.New("照合失敗: interferer の窓が名乗りと一致しない")
	}
	p.interHwn = hwnd
	p.hwnds[hwnd] = "interferer"
	p.rep.Add("M0.identity-interferer", "interferer fixture の窓の同一性を照合", StatusPass, "pid=%d class=A2ProbeInterferer", p.interF.pid)
	return nil
}

func (p *prober) startForeign(x, y int) error {
	f, err := startFixture(p.ctx, "foreign", p.deadlineArg(), "--owner="+strconv.FormatUint(uint64(uint32(p.tHwnd)), 10),
		"--x="+strconv.Itoa(x), "--y="+strconv.Itoa(y), "--w=200", "--h=120")
	if err != nil {
		return err
	}
	p.foreignF = f
	p.verified[f.pid] = "foreign"
	n, _ := strconv.ParseUint(f.ready.Fields["hwnd"], 10, 32)
	if n == 0 {
		p.rep.Add("M1.foreign", "別プロセスが T に所有された窓", StatusSkip, "別プロセスの窓を T の所有者配下に作れなかった（CreateWindowEx の GetLastError=%s）。related_cross_process は測れない", f.ready.Fields["err"])
		return nil
	}
	hwnd := uintptr(n)
	if windowPID(hwnd) != f.pid || className(hwnd) != "A2ProbeForeign" {
		return errors.New("照合失敗: foreign の窓が名乗りと一致しない")
	}
	p.hwnds[hwnd] = "foreign"
	return nil
}

// --- 窓の事実の記録と判定 ---

func chainFacts(chain []domain.WindowLink) []LinkFact {
	out := make([]LinkFact, 0, len(chain))
	for _, l := range chain {
		out = append(out, LinkFact{ID: l.ID, PID: l.PID, ProcessStart: l.ProcessStart, Class: l.Class, Unverifiable: l.Unverifiable})
	}
	return out
}

func styleText(s domain.WindowStyle) string {
	var parts []string
	for _, kv := range []struct {
		on   bool
		name string
	}{{s.Popup, "popup"}, {s.Child, "child"}, {s.Tool, "tool"}, {s.Transparent, "transparent"}, {s.NoActivate, "noactivate"}} {
		if kv.on {
			parts = append(parts, kv.name)
		}
	}
	return strings.Join(parts, ",")
}

// snapshot は ListAll を ResolveGroup(T) にかけ、fixture の PID の窓だけを記録して返す。他アプリの窓は記録しない（タイトルも件数も）。
func (p *prober) snapshot(stage string) (domain.Group, []domain.WindowInfo, error) {
	all, err := p.wm.ListAll(p.ctx)
	if err != nil {
		return domain.Group{}, nil, err
	}
	g := domain.ResolveGroup(p.tRef, all)
	for _, w := range all {
		role, ok := p.verified[w.PID]
		if !ok {
			continue
		}
		f := WindowFact{
			Stage: stage, Role: role, ID: w.ID, PID: w.PID, ProcessStart: w.ProcessStart, Class: w.Class,
			Rect: [4]int{w.Rect.X, w.Rect.Y, w.Rect.W, w.Rect.H}, Style: styleText(w.Style),
			OwnerChain: chainFacts(w.OwnerChain), MenuOwnerID: w.MenuOwnerID, MenuOwnerChain: chainFacts(w.MenuOwnerChain),
		}
		f.Member, f.Relation, f.Reason, f.Cause = membership(g, w.ID)
		p.rep.Windows = append(p.rep.Windows, f)
	}
	return g, all, nil
}

func membership(g domain.Group, id string) (member bool, relation, reason, cause string) {
	if m, ok := g.Member(id); ok {
		return true, string(m.Relation), "", ""
	}
	for _, n := range g.NonMembers {
		if n.Window.ID == id {
			return false, "", string(n.Reason), string(n.Cause)
		}
	}
	return false, "", "", ""
}

func hwndID(h uintptr) string { return strconv.FormatUint(uint64(uint32(h)), 10) }

func toRectangle(r domain.Rect) image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }

// findWindow は all から id の窓を探す。
func findWindow(all []domain.WindowInfo, id string) (domain.WindowInfo, bool) {
	i := slices.IndexFunc(all, func(w domain.WindowInfo) bool { return w.ID == id })
	if i < 0 {
		return domain.WindowInfo{}, false
	}
	return all[i], true
}

// waitNewWindow は main fixture のプロセスの class の窓のうち、known に無いものが現れるのを待つ。
func (p *prober) waitNewWindow(class string, known map[string]bool, timeout time.Duration) (domain.WindowInfo, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && p.ctx.Err() == nil {
		all, err := p.wm.ListAll(p.ctx)
		if err == nil {
			for _, w := range all {
				if w.PID == p.mainF.pid && w.Class == class && !known[w.ID] {
					return w, true
				}
			}
		}
		p.sleep(100 * time.Millisecond)
	}
	return domain.WindowInfo{}, false
}

// currentIDs は main fixture の class の窓の ID の集合。
func (p *prober) currentIDs(class string) map[string]bool {
	ids := map[string]bool{}
	all, err := p.wm.ListAll(p.ctx)
	if err != nil {
		return ids
	}
	for _, w := range all {
		if w.PID == p.mainF.pid && w.Class == class {
			ids[w.ID] = true
		}
	}
	return ids
}

func (p *prober) windowGone(id string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && p.ctx.Err() == nil {
		all, err := p.wm.ListAll(p.ctx)
		if err == nil {
			if _, ok := findWindow(all, id); !ok {
				return true
			}
		}
		p.sleep(100 * time.Millisecond)
	}
	return false
}

// --- 撮影 ---

// errNotVerified は照合済みの fixture の窓でないものを撮ろうとしたこと。撮らない。
var errNotVerified = errors.New("照合済みの fixture の窓ではない。撮影しない")

// mayCapture は hwnd が照合済みの fixture の窓か（READY で名乗って OS の事実と一致した窓、または照合済みの main の PID のメニュー・ダイアログ）を確かめる。
// PID は撮影のたびに OS から取り直す（HWND が別プロセスに再利用されても通さない）。
func (p *prober) mayCapture(hwnd uintptr) (role string, err error) {
	role, ok := captureAllowed(p.verified, p.hwnds, windowPID(hwnd), hwnd, className(hwnd))
	if !ok {
		return "", errNotVerified
	}
	return role, nil
}

var allWatch = []color.RGBA{
	{R: 230, G: 60, B: 30, A: 255}, {R: 30, G: 170, B: 60, A: 255}, {R: 30, G: 60, B: 230, A: 255}, // オーナードローの項目色
	{R: 200, G: 40, B: 40, A: 255}, {R: 40, G: 160, B: 40, A: 255}, {R: 40, G: 40, B: 200, A: 255}, {R: 230, G: 200, B: 30, A: 255}, // T の模様
}

// capture は hwnd を PrintWindow(flags) で撮って統計をとり、記録する。画像は画素の検査用に返す。pngName が空でなければ PNG を出力する。
func (p *prober) capture(stage string, hwnd uintptr, flags uint32, pngName string) (CaptureFact, *image.RGBA, error) {
	role, err := p.mayCapture(hwnd)
	if err != nil {
		return CaptureFact{}, nil, err
	}
	res := printWindow(hwnd, flags)
	frame := domain.Rect{}
	if all, e := p.wm.ListAll(p.ctx); e == nil {
		if w, ok := findWindow(all, hwndID(hwnd)); ok {
			frame = w.Rect
		}
	}
	cf := CaptureFact{
		Stage: stage, Role: role, Class: className(hwnd), ID: hwndID(hwnd), Flags: fmt.Sprintf("0x%x", flags),
		Returned: res.Returned, Win32Error: res.Win32Err, ElapsedMs: float64(res.Elapsed.Microseconds()) / 1000,
		WindowRect: [4]int{res.WindowRect.Min.X, res.WindowRect.Min.Y, res.WindowRect.Dx(), res.WindowRect.Dy()},
		FrameRect:  [4]int{frame.X, frame.Y, frame.W, frame.H},
	}
	if res.Img == nil {
		cf.Verdict = "no_image"
		if res.Err != nil {
			cf.Win32Error = strings.TrimSpace(cf.Win32Error + " " + res.Err.Error())
		}
		p.rep.Captures = append(p.rep.Captures, cf)
		return cf, nil, res.Err
	}
	cf.Size = [2]int{res.Img.Bounds().Dx(), res.Img.Bounds().Dy()}
	st := analyzePixels(res.Img, allWatch)
	// 内容の有無（verdict）はクライアント領域で判定する。タイトルバー・枠が写るだけで「内容あり」にならないようにする。
	client := clientArea(hwnd, res.WindowRect)
	cs := st
	if sub, ok := res.Img.SubImage(client).(*image.RGBA); ok && !client.Empty() {
		cs = analyzePixels(sub, nil)
	}
	cf.Verdict = cs.verdict()
	cf.ClientRect = [4]int{client.Min.X, client.Min.Y, client.Dx(), client.Dy()}
	cf.UntouchedPct = pct(cs.Untouched, cs.Total)
	cf.BlackPct = pct(cs.Black, cs.Total)
	cf.Magenta = st.Magenta
	cf.Distinct = st.Distinct
	cf.Watched = st.Watched
	if pngName != "" {
		cf.PNG = p.writePNG(pngName, res.Img)
	}
	p.rep.Captures = append(p.rep.Captures, cf)
	return cf, res.Img, nil
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) * 100 / float64(total)
}

// writePNG は画像を出力先に PNG で書く（不透明にして書く。人が見るため）。書いたファイル名を返す。失敗したら空。
func (p *prober) writePNG(name string, img *image.RGBA) string {
	p.pngCount++
	file := fmt.Sprintf("%02d-%s.png", p.pngCount, name)
	opaque := image.NewRGBA(img.Bounds())
	copy(opaque.Pix, img.Pix)
	for i := 3; i < len(opaque.Pix); i += 4 {
		opaque.Pix[i] = 255
	}
	f, err := os.OpenFile(filepath.Join(p.out, file), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		p.rep.Note("PNG を書けない（%s）: %v", name, err)
		return ""
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, opaque); err != nil {
		p.rep.Note("PNG のエンコードに失敗（%s）: %v", name, err)
		return ""
	}
	return file
}
