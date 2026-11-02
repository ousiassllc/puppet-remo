package service

import (
	"context"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 起動後のウィンドウの確認（FR-M19、Issue #46・#55）。プロセスの作成に成功したあと、起動したプロセス（pid）の
// ウィンドウが前面に出るのを、launchWindowPollInterval 間隔で最大 launchWindowPollAttempts 回まで観測する。
// 待ちは Control の Sleep（テストが差し替える）で行い、所要時間は見ない（List・Foreground の時間は含まない。
// 通常の合計はおよそ interval × (attempts - 1) = 3 秒）。
const (
	launchWindowPollInterval = 200 * time.Millisecond
	launchWindowPollAttempts = 16
)

// launchObservation は起動後のウィンドウの観測結果。
type launchObservation struct {
	// foreground は最後の観測で前面にあったウィンドウ。前面を確かめられなかったとき（Foreground の失敗）は nil。
	foreground *domain.WindowInfo
	// hasPIDWindow は、最後の観測で起動したプロセスのトップレベルウィンドウが 1 つでもあったか（最小化・背面を含む）。
	hasPIDWindow bool
	// hasNewWindow は、最後の観測で、起動の前の一覧に無かった（ID が新しい）ウィンドウが 1 つでもあったか。
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

// observeLaunchedWindow は起動した pid のウィンドウが前面に出るまで（または観測の上限まで）観測し、最後の観測を返す。
// before は起動の前のウィンドウの ID（windowIDs。nil なら新規のウィンドウの判定をしない）。
// 前面のウィンドウの pid が pid に一致したらすぐに返す。ctx の取り消しで待ちが中断されたら、そこまでの観測を返す
// （起動は成立しているので、起動の結果は失敗にしない）。
func (c *control) observeLaunchedWindow(ctx context.Context, pid uint32, before map[string]struct{}) launchObservation {
	var obs launchObservation
	for attempt := 1; ; attempt++ {
		obs = c.observeWindows(ctx, pid, before)
		if (obs.foreground != nil && obs.foreground.PID == pid) || attempt >= launchWindowPollAttempts {
			return obs
		}
		if err := c.sleep(ctx, launchWindowPollInterval); err != nil {
			return obs
		}
	}
}

// observeWindows は 1 回の観測。一覧が読めなかったときは、前面のウィンドウだけで pid のウィンドウの有無を判断する。
func (c *control) observeWindows(ctx context.Context, pid uint32, before map[string]struct{}) launchObservation {
	var obs launchObservation
	if fg, err := c.windows.Foreground(); err == nil {
		obs.foreground = &fg
		obs.hasPIDWindow = fg.PID == pid
	}
	if obs.hasPIDWindow {
		return obs
	}
	if wins, err := c.windows.List(ctx); err == nil {
		for _, w := range wins {
			if w.PID == pid {
				obs.hasPIDWindow = true
			}
			if _, known := before[w.ID]; before != nil && !known && !w.Own {
				obs.hasNewWindow = true
			}
		}
	}
	return obs
}

// reusedExistingWindow は、既存のウィンドウが前に出ただけと見なせるか: 前面のウィンドウが確かめられ、
// 起動したプロセスのウィンドウが無く、起動の前に無かった新しいウィンドウも開いていない。
// 起動したプロセスがすぐ終了して別 pid の新しいウィンドウを開くアプリ（Windows 11 のメモ帳など）は、
// 新しいウィンドウがあるので false になる。前面を確かめられなかったときは false（window が付かない）。
func (o launchObservation) reusedExistingWindow() bool {
	return o.foreground != nil && !o.hasPIDWindow && !o.hasNewWindow
}
