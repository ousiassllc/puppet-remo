package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは UI を読むだけの操作 FindUIElement（#267）・WaitFor（#266）。入力はしない。
// 検査は get_ui_tree（GetUITree）と同じ経路（Control.Do: キルスイッチ → リース → 引数 → InputGuard の観測の規則）を通る。
// 自ウィンドウは読めない（NFR-S06）: window_id で指した自ウィンドウと、前面が自ウィンドウのときは input_blocked（own_window）。
// title・process_name で探すときは自ウィンドウを候補から外す（見つからなければ window_not_found。自ウィンドウの存在を明かさない）。

// 要素探索・待ちの上限。
const (
	defaultFindResults = 20
	maxFindResults     = 100
	// uiFindScanNodes は 1 回の探索で走査する UI 節点の上限。
	uiFindScanNodes = 20_000

	// MaxWaitForTimeout は wait_for の timeout_ms の上限。MCP のツールのタイムアウトはこの分を足して決める。
	MaxWaitForTimeout   = 60 * time.Second
	defaultWaitInterval = 500 * time.Millisecond
	minWaitInterval     = 100 * time.Millisecond
	maxWaitInterval     = 5 * time.Second
	// waitMatchLimit は待ちで集める一致の件数、waitCandidateLimit はタイムアウト時に添える候補の件数の上限。
	waitMatchLimit     = 20
	waitCandidateLimit = 20
)

// WaitOutcome は WaitFor の結果。
//
//   - Satisfied: 待っていた状態になった（appear: 現れた、disappear: 消えた）。false はタイムアウト
//   - Window: 対象のウィンドウ。window を待つ appear の成功では見つかった窓（複数なら Z オーダーで最上位）、要素を待つときは探した窓
//     （窓がまだ無いときは nil）。disappear の成功では nil
//   - Elements: 要素を待つ appear の成功では一致した要素（最大 20）。
//   - Windows・Elements（タイムアウト時）: 候補。appear は近い候補（指定の項目のどれかに当たるもの。無ければ見えている窓）、
//     disappear は消えずに残っている対象。最大 20 件
//   - Truncated: 一致・候補が上限か走査の打ち切りで欠けているかもしれない
type WaitOutcome struct {
	Satisfied bool
	Elapsed   time.Duration
	Window    *domain.WindowInfo
	Windows   []domain.WindowInfo
	Elements  []domain.UIElementInfo
	Truncated bool
}

// planFindUIElement は FindUIElement の引数を検査し、既定値を入れる。
func planFindUIElement(o domain.FindUIElement) (domain.FindUIElement, error) {
	if err := o.Window.Validate(); err != nil {
		return o, err
	}
	if err := o.Element.Validate(); err != nil {
		return o, err
	}
	if o.MaxResults < 0 || o.MaxResults > maxFindResults {
		return o, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("max_results must be between 0 and %d", maxFindResults))
	}
	if o.MaxResults == 0 {
		o.MaxResults = defaultFindResults
	}
	return o, nil
}

// planWaitFor は WaitFor の引数を検査し、既定値を入れる。
func planWaitFor(o domain.WaitFor) (domain.WaitFor, error) {
	if !o.State.Valid() {
		return o, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf(`state must be "appear" or "disappear", got %q`, o.State))
	}
	if o.Timeout <= 0 || o.Timeout > MaxWaitForTimeout {
		return o, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("timeout_ms must be between 1 and %d", MaxWaitForTimeout.Milliseconds()))
	}
	if o.Interval != 0 && (o.Interval < minWaitInterval || o.Interval > maxWaitInterval) {
		return o, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("interval_ms must be between %d and %d", minWaitInterval.Milliseconds(), maxWaitInterval.Milliseconds()))
	}
	if o.Interval == 0 {
		o.Interval = defaultWaitInterval
	}
	if err := o.Window.Validate(); err != nil {
		return o, err
	}
	if o.Element != nil {
		return o, o.Element.Validate()
	}
	if o.Window.IsZero() {
		return o, domain.ErrInvalidArgument.WithMsg("a window (window_id, title or process_name) or an element is required")
	}
	return o, nil
}

// locateWindow は loc のウィンドウを返す。項目が無ければ前面のウィンドウ。一覧の順（Z オーダー。最上位が先）で最初に合うもの。
// window_id なしの探索では自ウィンドウを外す。見つからなければ ok=false（エラーではない）。
// 選んだのが自ウィンドウ（window_id の指定か前面）なら input_blocked（GetUITree と同じ観測の規則）。
func (c *control) locateWindow(ctx context.Context, loc domain.WindowLocator) (w domain.WindowInfo, ok bool, err error) {
	if loc.IsZero() {
		f, err := c.windows.Foreground()
		if err != nil {
			return domain.WindowInfo{}, false, err
		}
		return f.Foreground, true, c.checkObservable(ctx, f.Foreground)
	}
	list, err := c.windows.List(ctx)
	if err != nil {
		return domain.WindowInfo{}, false, err
	}
	for _, w := range list {
		if !loc.Matches(w) || w.Own && loc.WindowID == "" {
			continue
		}
		return w, true, c.checkObservable(ctx, w)
	}
	return domain.WindowInfo{}, false, nil
}

func (c *control) checkObservable(ctx context.Context, w domain.WindowInfo) error {
	if err := domain.CheckObserveTarget(w); err != nil {
		return c.withOwnWindows(ctx, err)
	}
	return nil
}

// findElement は FindUIElement の実行。
func (c *control) findElement(ctx context.Context, o domain.FindUIElement) (Result, error) {
	win, ok, err := c.locateWindow(ctx, o.Window)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, domain.ErrWindowNotFound.WithMsg("no window matches the window locator")
	}
	res, err := c.ui.Find(ctx, win.ID, o.Element.Matches, o.MaxResults, uiFindScanNodes)
	if err != nil {
		return Result{}, err
	}
	return Result{Found: &res}, nil
}

// pollResult は 1 回の確認の結果。done は待っていた状態になったこと。
type pollResult struct {
	done      bool
	window    *domain.WindowInfo
	windows   []domain.WindowInfo    // 一致したウィンドウ（window を待つとき）
	elements  []domain.UIElementInfo // 一致した要素（要素を待つとき）
	allWins   []domain.WindowInfo    // 窓を探せなかったときの、見えている窓（候補用）
	truncated bool
}

// waitFor は WaitFor の実行。待ちの間隔は Sleep（ctx で中断できる）、経過は Clock で測る。
// 確認のたびにキルスイッチを見る。最後の確認は期限ちょうどに行い、その結果を候補にする。
func (c *control) waitFor(ctx context.Context, o domain.WaitFor) (Result, error) {
	start := c.clock.Now()
	deadline := start.Add(o.Timeout)
	for {
		if c.kill.Active() {
			return Result{}, domain.ErrKillSwitchActive
		}
		last, err := c.pollWait(ctx, o)
		if err != nil {
			return Result{}, err
		}
		out := WaitOutcome{Satisfied: last.done, Elapsed: c.clock.Now().Sub(start), Truncated: last.truncated}
		if last.done {
			if o.State == domain.WaitAppear {
				out.Window = last.window
				if o.Element != nil {
					out.Elements = last.elements
				}
			}
			return Result{Waited: &out}, nil
		}
		remaining := deadline.Sub(c.clock.Now())
		if remaining <= 0 {
			if err := c.fillCandidates(ctx, o, last, &out); err != nil {
				return Result{}, err
			}
			return Result{Waited: &out}, nil
		}
		if err := c.sleep(ctx, min(o.Interval, remaining)); err != nil {
			return Result{}, err
		}
	}
}

// pollWait は対象の今の状態を 1 回確かめる。
func (c *control) pollWait(ctx context.Context, o domain.WaitFor) (pollResult, error) {
	if o.Element == nil {
		return c.pollWindow(ctx, o)
	}
	win, ok, err := c.locateWindow(ctx, o.Window)
	if err != nil {
		return pollResult{}, err
	}
	if !ok {
		return c.windowAbsent(ctx, o)
	}
	res, err := c.ui.Find(ctx, win.ID, o.Element.Matches, waitMatchLimit, uiFindScanNodes)
	if errors.Is(err, domain.ErrWindowNotFound) { // 一覧に出たあとに閉じた
		return c.windowAbsent(ctx, o)
	}
	if err != nil {
		return pollResult{}, err
	}
	found := len(res.Elements) > 0
	return pollResult{
		done:      found == (o.State == domain.WaitAppear),
		window:    &win,
		elements:  res.Elements,
		truncated: res.Truncated,
	}, nil
}

// windowAbsent は、要素を探すウィンドウが無いときの結果。ウィンドウが無ければ要素も無い（appear は待ち続け、disappear は成立）。
// window_id で指したウィンドウが無いまま appear を待っても現れないので、待たずに window_not_found にする
// （HWND は再利用されうるが、list_windows の id を使い回す呼び出しの誤りを早く知らせる）。
func (c *control) windowAbsent(ctx context.Context, o domain.WaitFor) (pollResult, error) {
	if o.State == domain.WaitDisappear {
		return pollResult{done: true}, nil
	}
	if o.Window.WindowID != "" {
		return pollResult{}, domain.ErrWindowNotFound.WithMsg("no window " + o.Window.WindowID)
	}
	list, err := c.windows.List(ctx)
	if err != nil {
		return pollResult{}, err
	}
	return pollResult{allWins: observableWindows(list)}, nil
}

func (c *control) pollWindow(ctx context.Context, o domain.WaitFor) (pollResult, error) {
	list, err := c.windows.List(ctx)
	if err != nil {
		return pollResult{}, err
	}
	var matches []domain.WindowInfo
	for _, w := range observableWindows(list) {
		if o.Window.Matches(w) {
			matches = append(matches, w)
		}
	}
	res := pollResult{done: (len(matches) > 0) == (o.State == domain.WaitAppear), allWins: observableWindows(list)}
	if len(matches) > waitMatchLimit {
		matches, res.truncated = matches[:waitMatchLimit], true
	}
	res.windows = matches
	if len(matches) > 0 {
		res.window = &matches[0]
	}
	return res, nil
}

// observableWindows は list から自ウィンドウを外したもの。
func observableWindows(list []domain.WindowInfo) []domain.WindowInfo {
	out := make([]domain.WindowInfo, 0, len(list))
	for _, w := range list {
		if !w.Own {
			out = append(out, w)
		}
	}
	return out
}

// fillCandidates はタイムアウトした結果 out に、候補（WaitOutcome の注記を参照）を入れる。
func (c *control) fillCandidates(ctx context.Context, o domain.WaitFor, last pollResult, out *WaitOutcome) error {
	out.Window = last.window
	if o.State == domain.WaitDisappear { // 消えずに残っている対象
		out.Windows, out.Elements = last.windows, last.elements
		return nil
	}
	if o.Element != nil && last.window != nil { // 窓はあるが要素が無い: 近い要素
		res, err := c.ui.Find(ctx, last.window.ID, o.Element.MatchesAny, waitCandidateLimit, uiFindScanNodes)
		if err != nil && !errors.Is(err, domain.ErrWindowNotFound) {
			return err
		}
		out.Elements = res.Elements
		out.Truncated = out.Truncated || res.Truncated
		return nil
	}
	out.Windows, out.Truncated = candidateWindows(last.allWins, o.Window)
	return nil
}

// candidateWindows は all から、loc のどれかの項目に当たるウィンドウを先に、残りを続けて、最大 waitCandidateLimit 件にする。
// 上限で欠けたら truncated。
func candidateWindows(all []domain.WindowInfo, loc domain.WindowLocator) (out []domain.WindowInfo, truncated bool) {
	var near, rest []domain.WindowInfo
	for _, w := range all {
		if loc.MatchesAny(w) {
			near = append(near, w)
		} else {
			rest = append(rest, w)
		}
	}
	out = append(near, rest...)
	if len(out) > waitCandidateLimit {
		return out[:waitCandidateLimit], true
	}
	return out, false
}
