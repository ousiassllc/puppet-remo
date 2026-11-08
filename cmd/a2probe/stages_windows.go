//go:build windows

package main

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	pwin "github.com/ousiassllc/puppet-remo/internal/platform/windows"
)

const (
	pwFull   = pwRenderFullConten // PW_RENDERFULLCONTENT: 主の方式
	pwPlain  = 0                  // 比較用（フラグなし）
	vkDown   = 0x28
	vkReturn = 0x0D

	// 一致判定の閾値（提案値。実測を見て調整する）。
	sameThreshold  = 0.999 // 重ね前後で画素が一致する割合
	minWatchPixels = 500   // オーナードローの色が写ったとみなす画素数
	minPatternPx   = 1000  // T の模様の各色が写ったとみなす画素数
)

// rectOf は hwnd の見た目の枠（ListAll の Rect）を返す。
func (p *prober) rectOf(hwnd uintptr) (image.Rectangle, bool) {
	all, err := p.wm.ListAll(p.ctx)
	if err != nil {
		return image.Rectangle{}, false
	}
	w, ok := findWindow(all, hwndID(hwnd))
	if !ok {
		return image.Rectangle{}, false
	}
	return toRectangle(w.Rect), true
}

func statusOf(ok bool) Status {
	if ok {
		return StatusPass
	}
	return StatusFail
}

// captureDetail は撮影結果の要約（検査の Detail に使う）。
func captureDetail(cf CaptureFact) string {
	d := fmt.Sprintf("PrintWindow(%s)=%t verdict=%s size=%dx%d window=%v frame=%v client=%v untouched=%.1f%% black=%.1f%% 色数=%d %.1fms",
		cf.Flags, cf.Returned, cf.Verdict, cf.Size[0], cf.Size[1], cf.WindowRect, cf.FrameRect, cf.ClientRect, cf.UntouchedPct, cf.BlackPct, cf.Distinct, cf.ElapsedMs)
	if cf.Win32Error != "" {
		d += " err=" + cf.Win32Error
	}
	if cf.PNG != "" {
		d += " png=" + cf.PNG
	}
	return d
}

// patternOK は T の模様の 4 色がそれぞれ minPatternPx 以上写っているか。
func patternOK(cf CaptureFact) bool {
	for _, c := range allWatch[3:] {
		if cf.Watched[colorKey(c)] < minPatternPx {
			return false
		}
	}
	return true
}

func watchedDetail(cf CaptureFact, cs []color.RGBA) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("%s=%d", colorKey(c), cf.Watched[colorKey(c)]))
	}
	return "色の画素数 " + strings.Join(parts, " ")
}

// --- 基準（メニューを開く前） ---

func (p *prober) baseline() {
	g, _, err := p.snapshot("baseline")
	if err != nil {
		p.rep.Add("M1.baseline", "基準の ListAll", StatusFail, "%v", err)
		return
	}
	expect := func(id, title string, hwnd uintptr, reason domain.GroupReason) {
		if hwnd == 0 {
			return
		}
		member, rel, why, cause := membership(g, hwndID(hwnd))
		ok := !member && why == string(reason)
		p.rep.Add(id, title, statusOf(ok), "期待=非メンバー(%s) 実際: member=%t relation=%s reason=%s cause=%s", reason, member, rel, why, cause)
	}
	expect("M1.decoy", "同一プロセスの無所有の窓は G に入らない（PID 一致だけで許可しない）", p.hwndByName("decoy"), domain.ReasonUnrelatedSameProcess)
	expect("M1.interferer", "別プロセスの無関係な窓は G に入らない", p.interHwn, domain.ReasonUnrelatedOtherProcess)
	if fh := p.hwndByName("foreign"); fh != 0 {
		expect("M1.foreign", "別プロセスが T に所有された窓は G に入らず停止の対象（related_cross_process）", fh, domain.ReasonRelatedCrossProcess)
	}

	cf, _, err := p.capture("baseline", p.tHwnd, pwFull, "t-baseline")
	if err != nil {
		p.rep.Add("M3.T", "T を PrintWindow で撮る", StatusFail, "%v", err)
	} else {
		p.rep.Add("M3.T", "T を PrintWindow(PW_RENDERFULLCONTENT) で撮る（模様の 4 色が写る）", statusOf(cf.Returned && cf.Verdict == verdictContent && patternOK(cf)),
			"%s\n%s", captureDetail(cf), watchedDetail(cf, allWatch[3:]))
	}
	if cf0, _, err := p.capture("baseline", p.tHwnd, pwPlain, ""); err == nil {
		p.rep.Add("M3.T.flag0", "（比較）T を PrintWindow(フラグなし) で撮る", StatusInfo, "%s", captureDetail(cf0))
	}

	// M6: 空・黒の窓を検出できるか（GPU 描画アプリが黒く写る場合に「写った」と誤判定しないための検査器の確認）。
	for _, c := range []struct {
		key  string
		want func(string) bool
		desc string
	}{
		{"blank", func(v string) bool { return v != verdictContent }, "描かない窓（クライアント領域が空）を内容なしと判定できる"},
		{"black", func(v string) bool { return v == verdictAllBlack }, "黒一色の窓を all_black と判定できる"},
	} {
		h := p.hwndByName(c.key)
		cf, _, err := p.capture("baseline", h, pwFull, "m6-"+c.key)
		if err != nil {
			p.rep.Add("M6."+c.key, c.desc, StatusFail, "%v", err)
			continue
		}
		p.rep.Add("M6."+c.key, c.desc, statusOf(c.want(cf.Verdict)), "%s", captureDetail(cf))
	}

	// M4(a)(c): T に別プロセスの窓（マゼンタ）を半分重ねる・完全に重ねる。
	if tr, ok := p.rectOf(p.tHwnd); ok {
		half := image.Rect(tr.Min.X+tr.Dx()/2, tr.Min.Y, tr.Max.X, tr.Max.Y)
		p.occlusion("baseline-half", "M4a", "T に別プロセスの窓を半分重ねても他アプリの画素が写らない", []uintptr{p.tHwnd}, half)
		p.occlusion("baseline-full", "M4c", "T を別プロセスの窓で完全に隠しても撮影結果が変わらない", []uintptr{p.tHwnd}, tr)
	}
}

func (p *prober) hwndByName(name string) uintptr {
	for h, n := range p.hwnds {
		if n == name {
			return h
		}
	}
	return 0
}

// occlusion は cover の位置に interferer（別プロセス・最前面・マゼンタ一色）を重ね、targets の PrintWindow が重ねる前と同じで、マゼンタ画素が 0 であることを確かめる。
// 重なりを At で確かめられない（interferer が最上位にならない）ものは SKIP にする。
func (p *prober) occlusion(stage, id, title string, targets []uintptr, cover image.Rectangle) {
	type ref struct {
		hwnd uintptr
		img  *image.RGBA
	}
	var refs []ref
	for _, t := range targets {
		_, img, err := p.capture(stage+"-ref", t, pwFull, "")
		if err != nil {
			p.rep.Add(id, title, StatusFail, "重ねる前の撮影に失敗: %v", err)
			return
		}
		refs = append(refs, ref{t, img})
	}
	if !placeTopmost(p.interHwn, cover) {
		p.rep.Add(id, title, StatusSkip, "interferer を動かせなかった")
		return
	}
	defer func() { placeTopmost(p.interHwn, p.parking); p.sleep(200 * time.Millisecond) }()
	p.sleep(350 * time.Millisecond)

	var detail []string
	ok, skipped := true, 0
	for i, t := range targets {
		tr, found := p.rectOf(t)
		overlap := tr.Intersect(cover)
		if !found || overlap.Empty() {
			skipped++
			detail = append(detail, fmt.Sprintf("%s: 重なる範囲がない（SKIP）", p.label(t)))
			continue
		}
		pt := domain.Point{X: overlap.Min.X + overlap.Dx()/2, Y: overlap.Min.Y + overlap.Dy()/2}
		hit, hok, err := p.wm.At(pt)
		if err != nil || !hok || hit.Top.ID != hwndID(p.interHwn) {
			skipped++
			detail = append(detail, fmt.Sprintf("%s: 重なりを確認できない（At の最上位が interferer ではない。SKIP）", p.label(t)))
			continue
		}
		cf, img, err := p.capture(stage, t, pwFull, "")
		if err != nil {
			ok = false
			detail = append(detail, fmt.Sprintf("%s: 撮影に失敗: %v", p.label(t), err))
			continue
		}
		same := sameShare(refs[i].img, img)
		good := cf.Returned && cf.Magenta == 0 && same >= sameThreshold
		ok = ok && good
		detail = append(detail, fmt.Sprintf("%s: 重なり確認済み(interferer が最上位) PrintWindow=%t マゼンタ画素=%d 重ねる前との一致=%.4f", p.label(t), cf.Returned, cf.Magenta, same))
	}
	st := statusOf(ok)
	if skipped == len(targets) {
		st = StatusSkip
	}
	p.rep.Add(id, title, st, "%s", strings.Join(detail, "\n"))
}

func (p *prober) label(h uintptr) string {
	if n, ok := p.hwnds[h]; ok {
		return n
	}
	return className(h)
}

// --- メニュー（S1・S2） ---

type menuSpec struct {
	id    string
	title string
	open  func() error
	watch []color.RGBA // 完全一致で要求する色（オーナードロー）。空なら要求しない
}

// openMenu はメニューを開き、新しい #32768 が現れるのを待つ。
func (p *prober) openMenu(sp menuSpec) (domain.WindowInfo, bool) {
	known := p.currentIDs(domain.ClassMenu)
	if err := sp.open(); err != nil {
		p.rep.Add("M1."+sp.id, sp.title, StatusFail, "メニューを開く指示に失敗: %v", err)
		return domain.WindowInfo{}, false
	}
	w, ok := p.waitNewWindow(domain.ClassMenu, known, 5*time.Second)
	if !ok {
		p.rep.Add("M1."+sp.id, sp.title, StatusFail, "メニュー（#32768）が 5 秒以内に現れなかった（fixture が前面でなくなった・フォーカスを失った等。所有関係・撮影は測れていない）")
		return domain.WindowInfo{}, false
	}
	p.sleep(450 * time.Millisecond) // 表示アニメーションが終わるのを待つ
	return w, true
}

func linksText(chain []domain.WindowLink) string {
	parts := make([]string, 0, len(chain))
	for _, l := range chain {
		if l.Unverifiable {
			parts = append(parts, "(unverifiable)")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s[pid=%d %s]", l.ID, l.PID, l.Class))
	}
	return "[" + strings.Join(parts, " → ") + "]"
}

// analyzeMenu は開いているメニュー w について M1（所有関係）・M2（クラス）・M3（写り）・M5（座標）を測る。T を含む G の基準（ref 画像）は M4 で別に撮る。
func (p *prober) analyzeMenu(sp menuSpec, w domain.WindowInfo) (domain.WindowInfo, domain.Group) {
	stage := "menu-" + sp.id
	g, all, err := p.snapshot(stage)
	if err != nil {
		p.rep.Add("M1."+sp.id, sp.title, StatusFail, "ListAll に失敗: %v", err)
		return w, g
	}
	if cur, ok := findWindow(all, w.ID); ok {
		w = cur
	}
	member, rel, why, cause := membership(g, w.ID)
	tw, _ := findWindow(all, hwndID(p.tHwnd))
	p.rep.Add("M1."+sp.id, sp.title+": メニューが G のメンバー（relation=menu）になる", statusOf(member && rel == string(domain.RelationMenu)),
		"member=%t relation=%s reason=%s cause=%s\nメニュー自身 pid=%d class=%s style=[%s]\nhwndMenuOwner=%s MenuOwnerChain=%s OwnerChain=%s\nT が前面=%t",
		member, rel, why, cause, w.PID, w.Class, styleText(w.Style), w.MenuOwnerID, linksText(w.MenuOwnerChain), linksText(w.OwnerChain), tw.Foreground)
	p.rep.Add("M2."+sp.id, sp.title+": クラスが #32768", statusOf(w.Class == domain.ClassMenu), "class=%s", w.Class)

	hwnd, _ := strconv.ParseUint(w.ID, 10, 32)
	cf, _, cerr := p.capture(stage, uintptr(hwnd), pwFull, "menu-"+sp.id)
	switch {
	case cerr != nil:
		p.rep.Add("M3."+sp.id, sp.title+": PrintWindow で写る", StatusFail, "%v", cerr)
	default:
		ok := cf.Returned && cf.Verdict == verdictContent
		detail := captureDetail(cf)
		if len(sp.watch) > 0 {
			for _, c := range sp.watch {
				ok = ok && cf.Watched[colorKey(c)] >= minWatchPixels
			}
			detail += "\n" + watchedDetail(cf, sp.watch)
		} else {
			detail += "\n（システム描画。項目の写りは PNG を人が目視して確認する）"
		}
		p.rep.Add("M3."+sp.id, sp.title+": PrintWindow(PW_RENDERFULLCONTENT) で写る", statusOf(ok), "%s", detail)
	}
	if cf0, _, err := p.capture(stage, uintptr(hwnd), pwPlain, ""); err == nil {
		p.rep.Add("M3."+sp.id+".flag0", sp.title+": （比較）PrintWindow(フラグなし)", StatusInfo, "%s", captureDetail(cf0))
	}
	p.geometry(stage, "M5."+sp.id, sp.title, g)
	mr := toRectangle(w.Rect)
	p.guardCheck("M10."+sp.id, sp.title, g, domain.Point{X: mr.Min.X + mr.Dx()/2, Y: mr.Min.Y + mr.Dy()/2})
	return w, g
}

// closeMenu はメニューを閉じる指示を出し、消えるのを確かめる。
func (p *prober) closeMenu(id string, menuID string) {
	if _, err := p.mainF.command(p.ctx, "menu-close"); err != nil {
		p.rep.Add("M1."+id+".close", "メニューを閉じる", StatusInfo, "閉じる指示に失敗: %v", err)
		return
	}
	gone := p.windowGone(menuID, 3*time.Second)
	p.rep.Add("M1."+id+".close", "メニューを閉じる（EndMenu。fixture 側の操作）", StatusInfo, "メニューが消えた=%t", gone)
}

func (p *prober) sysMenuAndDialog() {
	sp := menuSpec{id: "sys", title: "S1 メニューバーのメニュー（システム描画）", open: func() error {
		_, err := p.mainF.command(p.ctx, "menubar-open", "name", "file")
		return err
	}}
	w, ok := p.openMenu(sp)
	if !ok {
		return
	}
	w, _ = p.analyzeMenu(sp, w)
	if hw, err := strconv.ParseUint(w.ID, 10, 32); err == nil {
		mr := toRectangle(w.Rect)
		cover := mr.Inset(-8)
		p.occlusion("menu-sys", "M4b", "開いたメニューに別プロセスの窓を重ねても他アプリの画素が写らない（T も同時）", []uintptr{uintptr(hw), p.tHwnd}, cover)
		if g, _, err := p.snapshot("menu-sys-occluded"); err == nil {
			p.groupOcclusion("M4b.grabgroup", "開いたメニューと T に別プロセスの窓を重ねても、製品の GrabGroup の合成に他アプリの画素が混ざらない", g, mr.Union(toRectangle(g.Target.Rect)))
		}
	}

	// S1 → S3 の連鎖: 開いているメニューへキーメッセージを投函して「名前を付けて保存」を選ぶ（グローバル入力ではない）。
	knownDlg := p.currentIDs("#32770")
	var chainErr error
	for _, vk := range []int{vkDown, vkReturn} {
		if _, err := p.mainF.command(p.ctx, "menu-key", "vk", strconv.Itoa(vk)); err != nil {
			chainErr = err
			break
		}
		p.sleep(150 * time.Millisecond)
	}
	dlg, found := domain.WindowInfo{}, false
	if chainErr == nil {
		dlg, found = p.waitNewWindow("#32770", knownDlg, 5*time.Second)
	}
	p.rep.Add("M9.menu-select", "メニュー項目の選択（投函したキーメッセージ）で保存ダイアログが開く（S1 の「項目を選んで操作が完了する」の fixture 版）", StatusInfo,
		"opened=%t err=%v（失敗しても S1 の写り・所有関係の結果は変わらない。実入力での項目選択は最終ゲートで確認する）", found, chainErr)
	if !found {
		p.closeMenu("sys", w.ID)
		if _, err := p.mainF.command(p.ctx, "save-open"); err != nil {
			p.rep.Add("M1.dialog", "S3 保存ダイアログ", StatusFail, "保存ダイアログを開く指示に失敗: %v", err)
			return
		}
		if dlg, found = p.waitNewWindow("#32770", knownDlg, 5*time.Second); !found {
			p.rep.Add("M1.dialog", "S3 保存ダイアログ", StatusFail, "保存ダイアログ（#32770）が 5 秒以内に現れなかった")
			return
		}
	}
	p.dialogStage(dlg)
}

func (p *prober) ownerDrawMenu() {
	sp := menuSpec{id: "od", title: "S1 メニューバーのメニュー（オーナードロー。項目の色で自動判定）", watch: allWatch[:3], open: func() error {
		_, err := p.mainF.command(p.ctx, "menubar-open", "name", "color")
		return err
	}}
	w, ok := p.openMenu(sp)
	if !ok {
		return
	}
	w, _ = p.analyzeMenu(sp, w)
	p.closeMenu("od", w.ID)
}

func (p *prober) contextMenu() {
	sp := menuSpec{id: "ctx", title: "S2 右クリックメニュー（標準の EDIT の文脈メニュー）", open: func() error {
		er, ok := getWindowRect(p.hwndByName("edit"))
		if !ok {
			return fmt.Errorf("EDIT の矩形を取れない")
		}
		_, err := p.mainF.command(p.ctx, "context-open", "x", strconv.Itoa(er.Max.X-10), "y", strconv.Itoa(er.Min.Y+er.Dy()/2))
		return err
	}}
	w, ok := p.openMenu(sp)
	if !ok {
		return
	}
	w, _ = p.analyzeMenu(sp, w)
	p.closeMenu("ctx", w.ID)
}

// --- 保存ダイアログ（S3） ---

func (p *prober) dialogStage(dlg domain.WindowInfo) {
	p.sleep(600 * time.Millisecond)
	g, all, err := p.snapshot("dialog")
	if err != nil {
		p.rep.Add("M1.dialog", "S3 保存ダイアログ", StatusFail, "%v", err)
		return
	}
	if cur, ok := findWindow(all, dlg.ID); ok {
		dlg = cur
	}
	member, rel, why, cause := membership(g, dlg.ID)
	p.rep.Add("M1.dialog", "S3 同一プロセスの保存ダイアログが owner 鎖で T に至りメンバーになる（relation=owned）", statusOf(member && rel == string(domain.RelationOwned)),
		"member=%t relation=%s reason=%s cause=%s\nダイアログ pid=%d(T と同じ=%t) class=%s OwnerChain=%s",
		member, rel, why, cause, dlg.PID, dlg.PID == p.tRef.PID, dlg.Class, linksText(dlg.OwnerChain))
	p.rep.Add("M2.dialog", "S3 保存ダイアログのクラスが #32770", statusOf(dlg.Class == "#32770"), "class=%s", dlg.Class)

	hw, _ := strconv.ParseUint(dlg.ID, 10, 32)
	hwnd := uintptr(hw)
	cf, _, cerr := p.capture("dialog", hwnd, pwFull, "dialog")
	if cerr != nil {
		p.rep.Add("M3.dialog", "S3 保存ダイアログが PrintWindow で写る", StatusFail, "%v", cerr)
	} else {
		p.rep.Add("M3.dialog", "S3 保存ダイアログが PrintWindow(PW_RENDERFULLCONTENT) で写る", statusOf(cf.Returned && cf.Verdict == verdictContent),
			"%s\n（内容は PNG を人が目視して確認する。シェルの描画部分が黒くならないか）", captureDetail(cf))
	}
	p.geometry("dialog", "M5.dialog", "S3 保存ダイアログ", g)
	dr := toRectangle(dlg.Rect)
	p.guardCheck("M10.dialog", "S3 保存ダイアログ", g, domain.Point{X: dr.Min.X + dr.Dx()/2, Y: dr.Min.Y + dr.Dy()/2})
	p.occlusion("dialog", "M4.dialog", "保存ダイアログに別プロセスの窓を重ねても他アプリの画素が写らない", []uintptr{hwnd}, toRectangle(dlg.Rect).Inset(-8))

	name := fmt.Sprintf("a2probe-%s-%d.a2p", time.Now().Format("150405"), time.Now().UnixNano()%100000)
	ack, err := p.mainF.command(p.ctx, "save-fill", "name", name)
	filled := err == nil && ack.Fields["match"] == "1"
	p.rep.Add("S3.fill", "S3 ファイル名を入力できる（CDM_SETCONTROLTEXT → CDM_GETSPEC で読み戻し）", statusOf(filled), "err=%v readback_match=%s", err, ack.Fields["match"])
	if _, _, err := p.capture("dialog-filled", hwnd, pwFull, "dialog-filled"); err != nil {
		p.rep.Note("入力後のダイアログを撮れなかった: %v", err)
	}
	if _, err := p.mainF.command(p.ctx, "save-ok"); err != nil {
		p.rep.Add("S3.save", "S3 保存して閉じる", StatusFail, "OK の指示に失敗: %v", err)
		return
	}
	ev, err := p.mainF.waitFor(p.ctx, 10*time.Second, func(m message) bool { return m.Verb == "EVT" && m.Name == "save-closed" })
	if err != nil {
		p.rep.Add("S3.save", "S3 保存して閉じる", StatusFail, "ダイアログが閉じた通知を受け取れなかった: %v", err)
		return
	}
	body, rerr := os.ReadFile(filepath.Join(p.saveDir, name))
	saved := ev.Fields["saved"] == "1" && rerr == nil && strings.TrimSpace(string(body)) == "a2probe"
	gone := p.windowGone(dlg.ID, 3*time.Second)
	p.rep.Add("S3.save", "S3 ファイル名を入力して保存し、ダイアログが閉じる（fixture 内のメッセージ駆動）", statusOf(saved && gone),
		"saved=%s file_written=%t dialog_gone=%t（実入力での保存は最終ゲート）", ev.Fields["saved"], rerr == nil, gone)
}

// guardCheck は実機の事実（Foreground・At）を、製品と同じ検証（Group.CheckFocus・CheckHit）に通す。メニュー・ダイアログが開いている間、
// キー・文字の宛先（前面と hwndFocus の葉）と、メンバー上の座標の宛先が G の中として通ることを確かめる（通らなければ、入力は止まる＝S1〜S3 は成り立たない）。
// 記録するのは理由コードだけ（前面が他アプリになっていたときの名前は書かない）。
func (p *prober) guardCheck(id, title string, g domain.Group, pt domain.Point) {
	fi, ferr := p.wm.Foreground()
	focus := g.CheckFocus(fi)
	hit, ok, herr := p.wm.At(pt)
	hb := g.CheckHit(pt, hit)
	_, fgIsFixture := p.verified[fi.Foreground.PID]
	leafSamePID := fi.FocusLeaf.PID == fi.Foreground.PID
	pass := ferr == nil && herr == nil && ok && !focus.Blocked() && !hb.Blocked()
	p.rep.Add(id, title+": 実機の前面・フォーカス先・座標の宛先が製品と同じ検証（CheckFocus・CheckHit）を通る", statusOf(pass),
		"CheckFocus reason=%q（前面が fixture=%t 葉は前面と同じ PID=%t 葉の同一性=%t）err=%v\nCheckHit(メンバー上の点) reason=%q 点に窓あり=%t err=%v",
		focus.Reason, fgIsFixture, leafSamePID, fi.FocusLeaf.Known(), ferr, hb.Reason, ok, herr)
}

// --- 合成と座標の往復（M5） ---

// geometry は G のメンバーを撮って合成し、(1) 外接矩形が全メンバーの見た目の枠を含む、(2) 合成画像上の点 → 物理座標 → At → メンバー が一致する、を確かめる。
// 合成画像は PNG に出す。デスクトップ・モニターの画像は使わない。
func (p *prober) geometry(stage, id, title string, g domain.Group) {
	var members []pwin.GroupLayer
	var labels []string
	for _, m := range g.Members {
		hw, err := strconv.ParseUint(m.Window.ID, 10, 32)
		if err != nil {
			continue
		}
		cf, img, err := p.capture(stage+"-compose", uintptr(hw), pwFull, "")
		if err != nil || !cf.Returned || img == nil {
			continue
		}
		wr := image.Rect(cf.WindowRect[0], cf.WindowRect[1], cf.WindowRect[0]+cf.WindowRect[2], cf.WindowRect[1]+cf.WindowRect[3])
		members = append(members, pwin.GroupLayer{Frame: toRectangle(m.Window.Rect), Window: wr, Img: img})
		labels = append(labels, m.Window.Class)
	}
	canvas, originPt, _ := pwin.ComposeGroup(members) // 製品と同じ合成（A-2 の GrabGroup が使う）
	origin := image.Pt(originPt.X, originPt.Y)
	if canvas == nil {
		p.rep.Add(id, title+": グループ合成と座標の往復", StatusFail, "合成に置けるメンバーがない")
		return
	}
	png := p.writePNG("compose-"+stage, canvas)

	bounds := canvas.Bounds().Add(origin)
	contains := true
	for _, m := range members {
		contains = contains && m.Frame.In(bounds)
	}
	protrude := false
	if len(members) > 1 {
		t := toRectangle(g.Target.Rect)
		for _, m := range members {
			protrude = protrude || !m.Frame.In(t)
		}
	}

	total, onMember := 0, 0
	for _, fy := range []float64{0.1, 0.3, 0.5, 0.7, 0.9} {
		for _, fx := range []float64{0.1, 0.3, 0.5, 0.7, 0.9} {
			pt := image.Pt(int(float64(canvas.Bounds().Dx())*fx), int(float64(canvas.Bounds().Dy())*fy))
			if canvas.RGBAAt(pt.X, pt.Y).A != 255 { // 何も置かれていない（透明）点は対象外
				continue
			}
			total++
			hit, ok, err := p.wm.At(domain.Point{X: origin.X + pt.X, Y: origin.Y + pt.Y})
			if err == nil && ok {
				if _, isMember := g.Member(hit.Top.ID); isMember {
					onMember++
				}
			}
		}
	}
	roundtrip := total >= 10 && onMember == total
	p.rep.Add(id, title+": 合成の外接矩形が全メンバーを含み、合成画像上の点 → 物理座標(Origin+p) → At → メンバーが一致する",
		statusOf(contains && roundtrip),
		"メンバー=%v 合成=%dx%d origin=(%d,%d) 外接矩形が全メンバーを含む=%t T の矩形の外にはみ出すメンバーあり=%t 往復の一致=%d/%d（不一致は、点の上に別のウィンドウが重なっている可能性）png=%s",
		labels, canvas.Bounds().Dx(), canvas.Bounds().Dy(), origin.X, origin.Y, contains, protrude, onMember, total, png)
	p.grabGroupCheck(id+".grabgroup", title, g, canvas, origin)
}

// groupRefs は G の撮影対象（T と、最小化されていない・矩形が空でないメンバー。製品の control.grabGroup と同じ選び方）。
func groupRefs(g domain.Group) []domain.WindowRef {
	var refs []domain.WindowRef
	for _, m := range g.Members {
		if m.Relation == domain.RelationTarget || !m.Window.Minimized && !m.Window.Rect.Empty() {
			refs = append(refs, domain.RefOf(m.Window))
		}
	}
	return refs
}

// grabGroupCheck は製品の Capturer.GrabGroup（A-2 の実装）を fixture の G に対して呼び、全メンバーが撮れ、合成が probe 自身の合成と一致することを確かめる。
// 呼ぶ対象は照合済みの fixture のメンバーだけ（mayCapture を通したものに限る）。
func (p *prober) grabGroupCheck(id, title string, g domain.Group, probeCanvas *image.RGBA, probeOrigin image.Point) {
	refs := groupRefs(g)
	for _, r := range refs {
		hw, _ := strconv.ParseUint(r.ID, 10, 32)
		if _, err := p.mayCapture(uintptr(hw)); err != nil {
			p.rep.Add(id, title+": 製品の GrabGroup", StatusSkip, "照合済みの fixture の窓だけに限る: %v", err)
			return
		}
	}
	gi, err := pwin.NewCapturer().GrabGroup(p.ctx, refs)
	if err != nil {
		p.rep.Add(id, title+": 製品の GrabGroup", StatusFail, "%v", err)
		return
	}
	var bad []string
	for _, m := range gi.Members {
		if m.Status != "ok" {
			bad = append(bad, m.Ref.ID+":"+string(m.Status))
		}
	}
	same := 0.0
	if gi.Image != nil && probeCanvas != nil && gi.Origin.X == probeOrigin.X && gi.Origin.Y == probeOrigin.Y {
		same = sameShare(gi.Image, probeCanvas)
	}
	p.rep.Add(id, title+": 製品の Capturer.GrabGroup が全メンバー ok で、probe の合成と一致する", statusOf(len(bad) == 0 && gi.Image != nil && same >= sameThreshold),
		"撮れなかったメンバー=%v 合成=%v origin=(%d,%d) probe の合成との一致=%.4f（メニューのアニメーション・キャレット等で 1 を割ることがある。PNG で確認）",
		bad, imageSize(gi.Image), gi.Origin.X, gi.Origin.Y, same)
}

func imageSize(img *image.RGBA) string {
	if img == nil {
		return "nil"
	}
	return strconv.Itoa(img.Bounds().Dx()) + "x" + strconv.Itoa(img.Bounds().Dy())
}

// groupOcclusion は G のメンバーの上に別プロセスのマゼンタの窓を重ねた状態で、製品の GrabGroup の合成にマゼンタが 0 画素であることを確かめる
// （他アプリの画素が混ざらない。重なりは At で確認し、確認できなければ SKIP）。
func (p *prober) groupOcclusion(id, title string, g domain.Group, cover image.Rectangle) {
	refs := groupRefs(g)
	if !placeTopmost(p.interHwn, cover) {
		p.rep.Add(id, title, StatusSkip, "interferer を動かせなかった")
		return
	}
	defer func() { placeTopmost(p.interHwn, p.parking); p.sleep(200 * time.Millisecond) }()
	p.sleep(350 * time.Millisecond)
	pt := domain.Point{X: cover.Min.X + cover.Dx()/2, Y: cover.Min.Y + cover.Dy()/2}
	if hit, ok, err := p.wm.At(pt); err != nil || !ok || hit.Top.ID != hwndID(p.interHwn) {
		p.rep.Add(id, title, StatusSkip, "重なりを確認できない（At の最上位が interferer ではない）")
		return
	}
	gi, err := pwin.NewCapturer().GrabGroup(p.ctx, refs)
	if err != nil || gi.Image == nil {
		p.rep.Add(id, title, StatusFail, "GrabGroup が失敗: %v", err)
		return
	}
	st := analyzePixels(gi.Image, nil)
	p.rep.Add(id, title, statusOf(st.Magenta == 0), "重なり確認済み 合成=%s マゼンタ画素=%d（非メンバーの画素が合成に混ざらない）", imageSize(gi.Image), st.Magenta)
}

// --- 所要時間（M7）と代表アプリの下調べ（M8） ---

func (p *prober) timing() {
	if p.tHwnd == 0 || p.ctx.Err() != nil {
		return
	}
	if _, err := p.mayCapture(p.tHwnd); err != nil {
		return
	}
	const n = 20
	ms := make([]float64, 0, n)
	var size image.Point
	for range n {
		res := printWindow(p.tHwnd, pwFull)
		if !res.Returned {
			continue
		}
		size = res.Img.Bounds().Size()
		ms = append(ms, float64(res.Elapsed.Microseconds())/1000)
	}
	if len(ms) == 0 {
		p.rep.Add("M7", "PrintWindow の所要時間（T）", StatusInfo, "成功した撮影が 0 回")
		return
	}
	slices.Sort(ms)
	sum := 0.0
	for _, v := range ms {
		sum += v
	}
	avg := sum / float64(len(ms))
	p95 := ms[min(len(ms)-1, int(float64(len(ms))*0.95))]
	p.rep.Add("M7", "PrintWindow の所要時間（T、連続 20 回。実測 FPS の材料）", StatusInfo,
		"size=%dx%d 成功=%d/%d avg=%.1fms p95=%.1fms max=%.1fms（1 枚あたりの上限 FPS ≒ %.0f。メンバー数・合成の分は別）", size.X, size.Y, len(ms), n, avg, p95, ms[len(ms)-1], 1000/avg)
}

// checkNotepad は代表アプリの候補（メモ帳）の存在・版を、ファイルの属性だけで調べる。実行しない・窓を読まない。
func (p *prober) checkNotepad() {
	sysDir, err := windows.GetSystemDirectory()
	if err != nil {
		p.rep.Add("M8", "代表アプリの下調べ（メモ帳。ファイルの属性のみ）", StatusInfo, "システムフォルダーを取れない: %v", err)
		return
	}
	exe := filepath.Join(sysDir, "notepad.exe")
	var lines []string
	if fi, err := os.Stat(exe); err != nil {
		lines = append(lines, "System32\\notepad.exe: なし")
	} else {
		lines = append(lines, fmt.Sprintf("System32\\notepad.exe: あり size=%d version=%s", fi.Size(), fileVersion(exe)))
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		_, err := os.Stat(filepath.Join(local, "Microsoft", "WindowsApps", "notepad.exe"))
		lines = append(lines, fmt.Sprintf("WindowsApps の notepad.exe（パッケージ版メモ帳の実行エイリアス）: %s", map[bool]string{true: "あり", false: "なし"}[err == nil]))
	}
	lines = append(lines, "Win11 のパッケージ版メモ帳は XAML でメニューが #32768 でない可能性がある（[INFERENCE]）。どちらが起動するか・プロセス構成は、実アプリの観察（別の OK が要る）で確かめる。")
	p.rep.Add("M8", "代表アプリの下調べ（メモ帳。ファイルの属性のみ。起動・窓の読み取りはしない）", StatusInfo, "%s", strings.Join(lines, "\n"))
}

func fileVersion(path string) string {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return "?"
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return "?"
	}
	var info *windows.VS_FIXEDFILEINFO
	var n uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&info), &n); err != nil || info == nil {
		return "?"
	}
	return fmt.Sprintf("%d.%d.%d.%d", info.FileVersionMS>>16, info.FileVersionMS&0xffff, info.FileVersionLS>>16, info.FileVersionLS&0xffff)
}

// --- まとめ ---

func (p *prober) statusOfIDs(ids ...string) string {
	pass := 0
	for _, id := range ids {
		found := false
		for _, c := range p.rep.Checks {
			if c.ID != id {
				continue
			}
			found = true
			switch c.Status {
			case StatusFail:
				return "FAIL（" + id + "）"
			case StatusPass:
				pass++
			}
		}
		if !found {
			return "未測定（" + id + "）"
		}
	}
	if pass == len(ids) {
		return "PASS"
	}
	return "一部 SKIP"
}

// summarize は S1〜S3 に対する部分結果を記録に足す。fixture でのメッセージ駆動の結果であり、実アプリ・実入力での最終合格ではない。
func (p *prober) summarize() {
	p.rep.Note("S1（メニューバー）の部分結果: システム描画 %s / オーナードロー %s", p.statusOfIDs("M1.sys", "M2.sys", "M3.sys", "M4b"), p.statusOfIDs("M1.od", "M2.od", "M3.od"))
	p.rep.Note("S2（右クリックメニュー）の部分結果: %s", p.statusOfIDs("M1.ctx", "M2.ctx", "M3.ctx"))
	p.rep.Note("S3（同一プロセスの保存ダイアログ）の部分結果: %s", p.statusOfIDs("M1.dialog", "M2.dialog", "M3.dialog", "S3.fill", "S3.save"))
	p.rep.Note("これらは専用 fixture でのメッセージ駆動の結果で、S1〜S3 の最終合格（代表アプリ・スコープ付きの実入力・GrabGroup）ではない。FAIL・SKIP の意味と次の判断は、報告の「実機確認の読み方」に従う。")
	p.rep.Note("取らなかったもの: 他アプリの窓のタイトル・文書・件数、デスクトップ・画面領域・モニターの画像。グローバル入力（SendInput・カーソル移動）は使っていない。")
}
