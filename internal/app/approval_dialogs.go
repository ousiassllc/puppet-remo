package app

import (
	"context"
	"log/slog"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/service"
)

// approvalDialogs は Bus の approval を購読して承認ダイアログを開き、承認が終わったら（許可・拒否・60 秒の失効・取り下げ）閉じる（FR-H05）。
//
// 振る舞い（docs/components/overview.md §7、§13.6）:
//   - 種類（lease・safety_check・judge_action。FR-H05）にかかわらず、承認要求ごとにダイアログを開く。safety_check・judge_action は内容の説明（Detail）の
//     分だけ背が高いウィンドウにする（Windows.OpenApproval の kind）。
//   - ダイアログは同時に 1 つだけ開く。重なった要求は到着順（Approval.Pending の順）に待たせ、今の要求が終わったら（approval-closed）、
//     まだ承認待ちの次の要求のダイアログを開く。待たされた要求の 60 秒は、その要求自身の到着時刻から数える
//     （ExpiresAt。待っている間に失効した要求は Pending に残らないので開かない）。
//   - 利用者が許可も拒否もせずにウィンドウを閉じても、承認待ちは続き、60 秒の失効で拒否になる。閉じたダイアログは開き直さない。
//     その要求が終わるまでは「今の要求」のままなので、待っている次の要求のダイアログも、その要求が終わってから開く。
type approvalDialogs struct {
	windows  Windows
	approval service.Approval
	bus      *service.Bus
	log      *slog.Logger

	mu      sync.Mutex
	current string          // 今のダイアログの承認要求の ID（ウィンドウを利用者が閉じていても、要求が終わるまで保つ）。なければ空
	window  Window          // current のダイアログ
	opened  map[string]bool // 開いたことのある要求の ID（承認待ちの間だけ覚える。開き直さないため）
}

func newApprovalDialogs(w Windows, approval service.Approval, bus *service.Bus, log *slog.Logger) *approvalDialogs {
	return &approvalDialogs{windows: w, approval: approval, bus: bus, log: log, opened: map[string]bool{}}
}

// Watch は購読を（戻る前に）済ませて、承認イベントに従ってダイアログを開閉する goroutine を始める。ctx の終了で、開いている
// ダイアログを閉じてから、返したチャネルを閉じる（シャットダウンで閉じる）。購読を取りこぼさないよう、起動時にすでにある
// 承認待ちも 1 度確かめる。
func (d *approvalDialogs) Watch(ctx context.Context) <-chan struct{} {
	events, cancel := d.bus.Subscribe(service.TopicApproval, service.TopicApprovalClosed)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		defer d.closeAll()
		d.sync()
		for {
			select {
			case <-ctx.Done():
				return
			case <-events:
				d.sync()
			}
		}
	}()
	return done
}

// sync は承認待ちの今の状態に開閉を合わせる: 今の要求が終わっていればそのダイアログを閉じ、ダイアログが空いていれば、
// まだ開いていない承認待ちの要求のうち到着が最も早いものを開く。
// Bus は遅い購読者のイベントを捨てうるので、イベントの種類や ID ではなく、Pending を真実の源にする。
// 呼び出しは Watch の goroutine だけ（テストは直接呼んで開閉の順序を決定的にする）。
func (d *approvalDialogs) sync() {
	pending := d.approval.Pending()
	live := make(map[string]bool, len(pending))
	for _, r := range pending {
		live[r.ID] = true
	}

	var stale Window
	d.mu.Lock()
	for id := range d.opened {
		if !live[id] {
			delete(d.opened, id)
		}
	}
	if d.current != "" && !live[d.current] {
		stale = d.window
		d.current, d.window = "", nil
	}
	d.mu.Unlock()
	if stale != nil {
		stale.Close()
	}

	d.mu.Lock()
	busy := d.current != ""
	d.mu.Unlock()
	if busy {
		return
	}
	for _, r := range pending {
		d.mu.Lock()
		seen := d.opened[r.ID]
		d.mu.Unlock()
		if seen {
			continue
		}
		w := d.windows.OpenApproval(r.ID, r.Kind)
		d.mu.Lock()
		d.current, d.window = r.ID, w
		d.opened[r.ID] = true
		d.mu.Unlock()
		return
	}
}

func (d *approvalDialogs) closeAll() {
	d.mu.Lock()
	w := d.window
	d.current, d.window = "", nil
	d.mu.Unlock()
	if w != nil {
		w.Close()
	}
}
