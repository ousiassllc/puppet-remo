package service

import (
	"context"
	"sync/atomic"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// defaultMaxPendingThumbs はキューにあるサムネイルの件数の既定の上限（1 枚はおよそ数 KiB〜数十 KiB）。
const defaultMaxPendingThumbs = 32

// thumbSaver は Audit のサムネイルの保存に要るものと、キューにあるサムネイルの件数。
// ゼロ値（store が nil）はサムネイルを保存しない。
type thumbSaver struct {
	store        repository.ThumbStore
	ids          domain.IDGen
	settings     repository.Settings
	purgeOverCap func(ctx context.Context) error
	maxPending   int32
	pending      atomic.Int32
}

// reserve はキューに 1 枚分の枠を取る。満杯なら false（枠は取らない）。
func (s *thumbSaver) reserve() bool {
	if s.pending.Add(1) > s.maxPending {
		s.pending.Add(-1)
		return false
	}
	return true
}

// release は reserve で取った枠を返す。thumb が nil（枠を取っていない）なら何もしない。
func (s *thumbSaver) release(thumb *Thumbnail) {
	if thumb != nil {
		s.pending.Add(-1)
	}
}

// ThumbnailsEnabled は、サムネイルを保存できる構成で、設定 audit.thumbnails_enabled（既定 true。FR-AU02）が有効かを返す。
// 呼び出し側（Control）は、無効なら画面を撮らない。設定が読めない・真偽値でないときは、画面を残さない側（無効）に倒す。
func (a *Audit) ThumbnailsEnabled(ctx context.Context) bool {
	if a.thumbs.store == nil {
		return false
	}
	on, err := loadBool(ctx, a.thumbs.settings, settingAuditThumbnailsEnabled, true)
	if err != nil {
		a.log.Warn("cannot read audit.thumbnails_enabled; thumbnails are disabled", "err", err)
		return false
	}
	return on
}

// RecordWithThumb は Record に縮小スクリーンショットを付ける（FR-AU02）。呼び出しを遅らせず、失敗を返さない。
// 書き込み goroutine が Append → ThumbStore.Save → AttachThumb → 容量の確認の順に行い、保存の失敗はエントリに影響しない
// （運用ログに残すだけ）。サムネイルを保存できない構成・無効な設定・キューのサムネイルの件数の上限のときは、画像だけを捨てて Record と同じに動く。
func (a *Audit) RecordWithThumb(ctx context.Context, e repository.AuditEntry, t Thumbnail) {
	if len(t.JPEG) == 0 || a.thumbs.store == nil {
		a.enqueue(ctx, e, nil)
		return
	}
	if !a.thumbs.reserve() {
		a.log.Warn("audit thumbnail dropped: too many thumbnails are waiting to be saved", "event", e.Event)
		a.enqueue(ctx, e, nil)
		return
	}
	a.enqueue(ctx, e, &t)
}

// saveThumb は追記した行 id にサムネイルを保存して紐づける。失敗は運用ログに残して戻る（エントリは追記済み）。
// 紐づけに失敗したら、保存したファイルを消す（消せなければ孤児ファイルとして次の起動時の掃除に任せる）。
func (a *Audit) saveThumb(ctx context.Context, id int64, e repository.AuditEntry, t Thumbnail) {
	if !a.ThumbnailsEnabled(ctx) {
		return
	}
	s := &a.thumbs
	rel := e.TS.UTC().Format("2006/01/02") + "/" + s.ids.NewID() + ".jpg"
	if err := s.store.Save(ctx, rel, t.JPEG); err != nil {
		a.log.Warn("audit thumbnail save failed", "event", e.Event, "err", err)
		return
	}
	meta := repository.ThumbMeta{RelPath: rel, SizeBytes: int64(len(t.JPEG)), Width: t.Width, Height: t.Height, CreatedAt: a.clock.Now()}
	if err := a.store.AttachThumb(ctx, id, meta); err != nil {
		a.log.Warn("audit thumbnail attach failed", "event", e.Event, "err", err)
		if rerr := s.store.Remove(rel); rerr != nil {
			a.log.Warn("audit thumbnail file cleanup failed", "err", rerr)
		}
		return
	}
	if s.purgeOverCap != nil {
		if err := s.purgeOverCap(ctx); err != nil {
			a.log.Warn("audit thumbnail capacity check failed", "err", err)
		}
	}
}
