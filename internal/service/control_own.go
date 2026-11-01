package service

import (
	"context"
	"errors"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// DetailOwnWindows は own_window の *domain.Error.Detail に入れる、puppet-remo のウィンドウの範囲（[]domain.OwnRegion）のキー。
// 縮小座標（AI が使う座標系）で、モニターごとの範囲を指す（docs/api/endpoints.md §3.4）。
const DetailOwnWindows = "own_windows"

// ownRegions は今の puppet-remo のウィンドウの範囲を、縮小座標でモニターごとに返す。
// 範囲は AI が避けるための手がかりで、取れなくても本来の結果（拒否・スクリーンショット）は変えないため、
// モニター・ウィンドウ・設定のどれかが取れなければ ok=false にして、呼び出し側が手がかりなしで続けられるようにする。
func (c *control) ownRegions(ctx context.Context) (regions []domain.OwnRegion, ok bool) {
	mons, err := c.capture.Monitors(ctx)
	if err != nil {
		return nil, false
	}
	maxDim, err := c.MaxDimension(ctx)
	if err != nil {
		return nil, false
	}
	return c.ownRegionsOn(ctx, mons, maxDim)
}

// ownRegionsOn は取得済みの mons・maxDim で ownRegions と同じ範囲を求める。
func (c *control) ownRegionsOn(ctx context.Context, mons []domain.Monitor, maxDim int) (regions []domain.OwnRegion, ok bool) {
	wins, err := c.windows.List(ctx)
	if err != nil {
		return nil, false
	}
	return domain.OwnWindowRegions(wins, mons, maxDim), true
}

// withOwnWindows は、InputGuard の拒否が own_window なら、その Detail に puppet-remo のウィンドウの範囲を足す。
// 範囲が取れなかった、または別の理由のエラーは、そのまま返す。
func (c *control) withOwnWindows(ctx context.Context, err error) error {
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeInputBlocked || de.Detail["reason"] != domain.ReasonOwnWindow {
		return err
	}
	regions, ok := c.ownRegions(ctx)
	if !ok {
		return err
	}
	if regions == nil {
		regions = []domain.OwnRegion{}
	}
	return de.WithDetail(DetailOwnWindows, regions)
}
