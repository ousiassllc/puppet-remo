package service

import (
	"context"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// 起動後のウィンドウの確認（FR-M19、Issue #46）。プロセスの作成に成功したあと、起動したプロセス（pid）の
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
}

// observeLaunchedWindow は起動した pid のウィンドウが前面に出るまで（または観測の上限まで）観測し、最後の観測を返す。
// 前面のウィンドウの pid が pid に一致したらすぐに返す。ctx の取り消しで待ちが中断されたら、そこまでの観測を返す
// （起動は成立しているので、起動の結果は失敗にしない）。
func (c *control) observeLaunchedWindow(ctx context.Context, pid uint32) launchObservation {
	var obs launchObservation
	for attempt := 1; ; attempt++ {
		obs = c.observeWindows(ctx, pid)
		if (obs.foreground != nil && obs.foreground.PID == pid) || attempt >= launchWindowPollAttempts {
			return obs
		}
		if err := c.sleep(ctx, launchWindowPollInterval); err != nil {
			return obs
		}
	}
}

// observeWindows は 1 回の観測。一覧が読めなかったときは、前面のウィンドウだけで pid のウィンドウの有無を判断する。
func (c *control) observeWindows(ctx context.Context, pid uint32) launchObservation {
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
				break
			}
		}
	}
	return obs
}

// reusedExistingWindow は、起動したプロセスのウィンドウが無いまま、別のプロセスのウィンドウが前面にある
// （既存のウィンドウが前に出ただけと見なす）か。前面を確かめられなかったときは false（window が付かない）。
func (o launchObservation) reusedExistingWindow() bool {
	return o.foreground != nil && !o.hasPIDWindow
}
