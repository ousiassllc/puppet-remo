package app

import (
	"context"
	"log/slog"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/service"
)

// このファイルは、Wails のウィンドウの生成・破棄の窓口（Windows）と、承認ダイアログを開閉する購読側（approvalDialogs）。
// Wails に触れる実装は wails_windows.go（実機でしか確かめられない）。ここは Wails に依存せず、偽の Windows（FakeWindows）でテストできる
// （docs/components/overview.md §7・§10。NFR-O03/O04）。

// Window は開いているウィンドウ 1 つ。
type Window interface {
	// Close はウィンドウを閉じる。すでに閉じている（ユーザーが閉じた・破棄済み）ときは何もしない。
	Close()
}

// Windows はウィンドウの生成・破棄（Wails v3 のラッパ）。M3 のオーバーレイ（ShowOverlay・HideOverlay）は #69 が足す。
type Windows interface {
	// Main はホスト GUI のメインウィンドウ（閉じてもトレイに残る。Shell が使う）。
	Main() MainWindow
	// OpenApproval は承認要求 id の承認ダイアログ（専用のウィンドウ。URL は /approvals/{id}）を開いて返す。
	// ウィンドウはキャプチャ除外（ContentProtectionEnabled。NFR-S06）で作り、閉じるのは approvalDialogs（承認の終了・シャットダウン）。
	OpenApproval(id string) Window
}

// approvalDialogs は Bus の approval を購読して承認ダイアログを開き、承認が終わったら（許可・拒否・60 秒の失効・取り下げ）閉じる（FR-H05）。
//
// 判断（docs に定めが無い部分は、拒否側に倒す保守的な動きにした）:
//   - ダイアログは同時に 1 つだけ開く（M3 の kind は lease だけで、pending_approval の間は他者の取得が断られるため、同時に 2 つは出ない）。
//     開いている間に来た別の要求は、そのダイアログが終わるまで開かない。まだ承認待ちなら、終わったあとに要求した順に開く。
//     待つ間に 60 秒が過ぎれば失効して拒否になる（Approval.Pending が複数を返すときの扱いは docs に無く、この動きは暫定）。
//   - 利用者が許可も拒否もせずにウィンドウを閉じても、承認待ちは続き、60 秒の失効で拒否になる（再度は開かない）。
type approvalDialogs struct {
	windows  Windows
	approval service.Approval
	bus      *service.Bus
	log      *slog.Logger

	mu   sync.Mutex
	open map[string]Window // 承認要求の ID → 開いているダイアログ
}

func newApprovalDialogs(w Windows, approval service.Approval, bus *service.Bus, log *slog.Logger) *approvalDialogs {
	return &approvalDialogs{windows: w, approval: approval, bus: bus, log: log, open: map[string]Window{}}
}

// Watch は購読を（戻る前に）済ませて、承認イベントに従ってダイアログを開閉する goroutine を始める。ctx の終了で、開いている
// ダイアログをすべて閉じてから、返したチャネルを閉じる（シャットダウンで閉じる）。購読を取りこぼさないよう、起動時にすでにある
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

// sync は承認待ちの今の状態に開閉を合わせる: 終わった要求のダイアログは閉じ、まだ開いていない lease の要求は開く。
// Bus は遅い購読者のイベントを捨てうるので、イベントの種類や ID ではなく、Pending を真実の源にする。
func (d *approvalDialogs) sync() {
	pending := d.approval.Pending()
	live := make(map[string]bool, len(pending))
	for _, r := range pending {
		live[r.ID] = true
	}

	var stale []Window
	d.mu.Lock()
	for id, w := range d.open {
		if !live[id] {
			stale = append(stale, w)
			delete(d.open, id)
		}
	}
	busy := len(d.open) > 0
	d.mu.Unlock()
	for _, w := range stale {
		w.Close()
	}

	for _, r := range pending {
		if r.Kind != service.ApprovalKindLease { // safety_check・judge_action は M4
			continue
		}
		d.mu.Lock()
		_, shown := d.open[r.ID]
		d.mu.Unlock()
		if shown {
			continue
		}
		if busy {
			d.log.Warn("approval dialog is already open; this request waits for it to end", "approval_id", r.ID)
			continue
		}
		w := d.windows.OpenApproval(r.ID)
		d.mu.Lock()
		d.open[r.ID] = w
		d.mu.Unlock()
		busy = true
	}
}

func (d *approvalDialogs) closeAll() {
	d.mu.Lock()
	ws := make([]Window, 0, len(d.open))
	for id, w := range d.open {
		ws = append(ws, w)
		delete(d.open, id)
	}
	d.mu.Unlock()
	for _, w := range ws {
		w.Close()
	}
}
