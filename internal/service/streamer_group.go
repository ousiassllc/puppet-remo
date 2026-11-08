package service

import (
	"context"
	"image"
	"slices"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは Streamer のグループ源（スコープ付きリースの対象ウィンドウ T と関連ウィンドウの合成画像。#157）。
// モニター源の Capturer.Watch の代わりに、GroupGrabber（Control.GrabGroupFrame）を閲覧者の FPS で撮り直す。
// デスクトップ・画面領域・モニターは撮らない。撮れないときは画面全体に切り替えず、閲覧者に GroupStatus.Err で知らせる。

// GroupGrabber はグループ源の撮影元。Control が満たす（GrabGroupFrame）。
type GroupGrabber interface {
	GrabGroupFrame(ctx context.Context, ref domain.WindowRef) (GroupFrame, error)
}

// GroupStatus はグループ源の最新の撮影の状態（Viewer.Group）。Err が nil でなければ撮影の失敗（ErrInputBlocked の reason が
// capture_unavailable / capture_member_unavailable / target_* など。Members・Excluded は空）。
type GroupStatus struct {
	// Members は撮れて合成に入ったメンバーの数（T を含む）。
	Members int
	// Excluded は所有関係を確かめられず、撮らず合成に入れなかったウィンドウ（capture_partial の通知）。
	Excluded []domain.BlockedBy
	Err      error
}

func (a GroupStatus) equal(b GroupStatus) bool {
	return a.Err == nil && b.Err == nil && a.Members == b.Members && slices.Equal(a.Excluded, b.Excluded)
}

// sourceFrame は源が流す 1 コマ。origin はグループ源だけ（物理座標 = origin + 画像の座標）。
type sourceFrame struct {
	img    *image.RGBA
	origin domain.Point
}

// watchFunc は源の撮影を始める。ctx の取り消しで流すチャネルを閉じる。
type watchFunc func(ctx context.Context, fps int) (<-chan sourceFrame, <-chan error, error)

func (s *streamer) watchFunc(src sourceKey) watchFunc {
	if src.group() {
		return func(ctx context.Context, fps int) (<-chan sourceFrame, <-chan error, error) {
			return s.watchGroup(ctx, src, fps)
		}
	}
	return func(ctx context.Context, fps int) (<-chan sourceFrame, <-chan error, error) {
		return s.watchMonitor(ctx, src.monitor, fps)
	}
}

// watchMonitor は Capturer.Watch のコマを sourceFrame にして流す。
func (s *streamer) watchMonitor(ctx context.Context, monitor, fps int) (<-chan sourceFrame, <-chan error, error) {
	in, errs, err := s.cfg.Capturer.Watch(ctx, monitor, fps)
	if err != nil {
		return nil, nil, err
	}
	out := make(chan sourceFrame)
	go func() {
		defer close(out)
		for fr := range in { // Capturer は取り消し後にチャネルを閉じる
			select {
			case out <- sourceFrame{img: fr.Image}:
			case <-ctx.Done():
			}
		}
	}()
	return out, errs, nil
}

// watchGroup は fps の周期で GrabGroupFrame を呼び、撮れた合成画像を流す。失敗は流さず、閲覧者へ GroupStatus.Err で知らせて次の周期にやり直す。
// 撮影の途中で fps が変わったら run が取り消して作り直す。
func (s *streamer) watchGroup(ctx context.Context, src sourceKey, fps int) (<-chan sourceFrame, <-chan error, error) {
	if s.cfg.Groups == nil {
		return nil, nil, domain.ErrInvalidArgument.WithMsg("window groups cannot be streamed")
	}
	out := make(chan sourceFrame)
	errs := make(chan error)
	go func() {
		defer close(out)
		defer close(errs)
		var last GroupStatus
		interval := time.Second / time.Duration(fps)
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			timer.Reset(interval)
			gf, err := s.cfg.Groups.GrabGroupFrame(ctx, src.target)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				last = GroupStatus{}
				s.notifyGroup(src, GroupStatus{Err: err})
				continue
			}
			if st := (GroupStatus{Members: len(gf.Members), Excluded: gf.Excluded}); !st.equal(last) {
				last = st
				s.notifyGroup(src, st)
			}
			select {
			case out <- sourceFrame{img: gf.Image, origin: gf.Origin}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, errs, nil
}
