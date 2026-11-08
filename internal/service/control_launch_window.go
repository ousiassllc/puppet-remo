package service

import (
	"context"
	"strings"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 起動後のウィンドウの確認（FR-M19、Issue #46・#55・#85）。プロセスの作成に成功したあと、起動したアプリのウィンドウが
// 前面に出るのを、launchWindowPollInterval 間隔で最大 launchWindowPollAttempts 回まで観測する。
// 待ちは Control の Sleep（テストが差し替える）で行い、所要時間は見ない（List・Foreground の時間は含まない。
// 通常の合計はおよそ interval × (attempts - 1) = 3 秒）。
const (
	launchWindowPollInterval = 200 * time.Millisecond
	launchWindowPollAttempts = 16
)

// launchObservation は起動後のウィンドウの観測結果。
type launchObservation struct {
	// foreground は最後の観測で前面にあったウィンドウ。前面を確かめられなかったとき（Foreground の失敗）は nil。
	// 起動したアプリのウィンドウとは限らないので、呼び出し側へは返さない（reusedExistingWindow の判定にだけ使う。#85）。
	foreground *domain.WindowInfo
	// launched は最後の観測で見つかった、起動したアプリのウィンドウ。次の順で選ぶ:
	//  1. 起動したプロセス（pid）のトップレベルウィンドウ
	//  2. 起動の前の一覧に無かった新しいウィンドウのうち、プロセス名が起動した実行ファイルと同じもの
	//     （Windows 11 のメモ帳のように、起動したプロセスがすぐ別のプロセスへ引き渡す場合）
	// どちらも無ければ nil。別のアプリの前面のウィンドウでは代用しない（返したウィンドウへキーを送ると別アプリに入力される。#85）。
	launched *domain.WindowInfo
	// hasPIDWindow は、最後の観測で起動したプロセスのトップレベルウィンドウが 1 つでもあったか（最小化・背面を含む）。
	hasPIDWindow bool
	// hasNewWindow は、最後の観測で、起動の前の一覧に無かった（ID が新しい）ウィンドウが 1 つでもあったか（プロセス名は問わない）。
	// 起動の前の一覧が読めなかったとき、またはこの観測で一覧が読めなかったときは false。puppet-remo 自身のウィンドウは数えない。
	hasNewWindow bool
}

// windowIDs は今のウィンドウの ID の集合。自プロセスのウィンドウ（承認ダイアログ・オーバーレイなど）は含めない。
// 一覧が読めなかったときは nil（空の集合とは区別する: nil は「起動の前の状態が分からない」）。
func (c *control) windowIDs(ctx context.Context) map[string]struct{} {
	wins, err := c.windows.List(ctx)
	if err != nil {
		return nil
	}
	ids := make(map[string]struct{}, len(wins))
	for _, w := range wins {
		if !w.Own {
			ids[w.ID] = struct{}{}
		}
	}
	return ids
}

// observeLaunchedWindow は起動したアプリのウィンドウが前面に出るまで（または観測の上限まで）観測し、最後の観測を返す。
// before は起動の前のウィンドウの ID（windowIDs。nil なら新規のウィンドウの判定をしない）。exePath は許可リストの実行ファイル。
// 起動したアプリのウィンドウが前面に出たらすぐに返す。ctx の取り消しで待ちが中断されたら、そこまでの観測を返す
// （起動は成立しているので、起動の結果は失敗にしない）。
func (c *control) observeLaunchedWindow(ctx context.Context, pid uint32, exePath string, before map[string]struct{}) launchObservation {
	exe := exeBaseName(exePath)
	var obs launchObservation
	for attempt := 1; ; attempt++ {
		obs = c.observeWindows(ctx, pid, exe, before)
		if (obs.launched != nil && obs.launched.Foreground) || attempt >= launchWindowPollAttempts {
			return obs
		}
		if err := c.sleep(ctx, launchWindowPollInterval); err != nil {
			return obs
		}
	}
}

// observeWindows は 1 回の観測。一覧が読めなかったときは、前面のウィンドウだけで pid のウィンドウの有無を判断する。
func (c *control) observeWindows(ctx context.Context, pid uint32, exe string, before map[string]struct{}) launchObservation {
	var obs launchObservation
	if focus, err := c.windows.Foreground(); err == nil {
		fg := focus.Foreground
		fg.Foreground = true
		obs.foreground = &fg
		if fg.PID == pid {
			obs.hasPIDWindow = true
			obs.launched = &fg
			return obs
		}
	}
	wins, err := c.windows.List(ctx)
	if err != nil {
		return obs
	}
	var pidWin, newWin *domain.WindowInfo
	for i := range wins {
		w := &wins[i]
		if obs.foreground != nil && w.ID == obs.foreground.ID {
			w.Foreground = true
		}
		if w.PID == pid {
			obs.hasPIDWindow = true
			pidWin = preferredWindow(pidWin, w)
		}
		if _, known := before[w.ID]; before != nil && !known && !w.Own {
			obs.hasNewWindow = true
			if exe != "" && strings.EqualFold(w.Process, exe) {
				newWin = preferredWindow(newWin, w)
			}
		}
	}
	if pidWin != nil {
		obs.launched = pidWin
	} else {
		obs.launched = newWin
	}
	return obs
}

// preferredWindow は同じ候補の集合から、前面のもの、次に最小化されていないものを選ぶ（同順位なら先に見つけたもの）。cur は nil でもよい。
func preferredWindow(cur, w *domain.WindowInfo) *domain.WindowInfo {
	switch {
	case cur == nil:
		return w
	case !cur.Foreground && w.Foreground:
		return w
	case !cur.Foreground && cur.Minimized && !w.Minimized:
		return w
	}
	return cur
}

// exeBaseName は実行ファイルのパスからファイル名を取る（区切りは \ と / の両方。WindowInfo.Process と比べる）。
func exeBaseName(p string) string {
	return p[strings.LastIndexAny(p, `\/`)+1:]
}

// reusedExistingWindow は、既存のウィンドウが前に出ただけと見なせるか: 前面のウィンドウが確かめられ、
// 起動したプロセスのウィンドウが無く、起動の前に無かった新しいウィンドウも開いていない。
// 起動したプロセスがすぐ終了して別 pid の新しいウィンドウを開くアプリ（Windows 11 のメモ帳など）は、
// 新しいウィンドウがあるので false になる。前面を確かめられなかったときは false。
// true のときは起動したアプリのウィンドウ（launched）が無いので、結果に window は付かない
// （前面にある別のアプリのウィンドウを返さない。#85）。
func (o launchObservation) reusedExistingWindow() bool {
	return o.foreground != nil && !o.hasPIDWindow && !o.hasNewWindow
}
