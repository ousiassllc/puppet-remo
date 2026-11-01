package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// GUIHandler はホスト GUI のハンドラ。Wails の AssetOptions.Handler に渡す。ソケットは開かない。
func (a *App) GUIHandler() http.Handler { return a.gui }

// RemoteHandler はリモートルーター。tailnet の IP の Listener が配信する（テストでは直接呼べる）。
func (a *App) RemoteHandler() http.Handler { return a.remote }

// KillSwitch はシェル（トレイ）が使うキルスイッチの窓口。
func (a *App) KillSwitch() KillSwitchControl { return a.kill }

// Bus は GUI・シェルが購読する通知。
func (a *App) Bus() *service.Bus { return a.bus }

// PendingRequests は保留中の接続要求の件数（トレイの表示に使う）。
func (a *App) PendingRequests(ctx context.Context) (int, error) {
	reqs, err := a.admin.PendingRequests(ctx)
	return len(reqs), err
}

// ServerStatus は tailnet・Listen の状態（GUI のダッシュボードと同じもの）。
func (a *App) ServerStatus(ctx context.Context) service.ServerStatus {
	return a.tracker.ServerStatus(ctx)
}

// allowedHosts は Host ヘッダーとして許す tailnet の IP・MagicDNS 名と、設定のポート（NFR-S02）。
func (a *App) allowedHosts() ([]string, int) {
	st := a.tracker.ServerStatus(context.Background())
	hosts := slices.Clone(st.TailnetIPs)
	if st.DNSName != "" {
		hosts = append(hosts, st.DNSName)
	}
	return hosts, int(a.port.Load())
}

// triggerKillSwitch はホットキー・トレイからのキルスイッチ。失敗は（段ごとの失敗を含めて）ログに残す。
// 発動状態は失敗しても変わらない（service.KillSwitch.Trigger の契約）。
func (a *App) triggerKillSwitch(source string) {
	if err := a.kill.Trigger(source); err != nil {
		a.log.Error("kill switch trigger reported errors", "source", source, "err", err)
	}
}

// toast は GUI へ通知を出す（購読者がいなければ捨てられる）。
func (a *App) toast(level, msg string) {
	a.bus.Publish(service.Event{Topic: service.TopicToast, Data: service.Toast{Level: level, Message: msg}})
}

// onSettingsChange は設定の保存後に、影響のあるコンポーネントへ変更を渡す（HostSettingsConfig.OnChange）。
func (a *App) onSettingsChange(old, updated service.HostSettings) {
	if old.Port != updated.Port {
		a.log.Info("server port changed", "old", old.Port, "new", updated.Port)
		a.port.Store(int32(updated.Port))
		a.listen.Reload()
	}
	if old.KillSwitchHotkey != updated.KillSwitchHotkey {
		if err := a.hotkey.Bind(updated.KillSwitchHotkey); err != nil {
			a.log.Error("kill switch hotkey was not changed", "err", err)
			a.toast("error", "ホットキーを登録できませんでした。これまでのホットキーを使い続けます。（"+updated.KillSwitchHotkey+"）")
		}
	}
	if old.LeaseIdleTimeoutSec != updated.LeaseIdleTimeoutSec {
		if err := a.leases.SetIdleTimeout(time.Duration(updated.LeaseIdleTimeoutSec) * time.Second); err != nil {
			a.log.Error("lease idle timeout was not changed", "err", err)
		}
	}
}

// Start は常駐の処理を動かす: リース変化の通知、tailscaled の監視、リモートルーターの Listen、保守、ホットキー。
// ctx の終了または Shutdown で止まる。1 度しか呼べない。
func (a *App) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	if a.started || a.stopped {
		a.mu.Unlock()
		cancel()
		return errors.New("app: Start was already called, or the app is shut down")
	}
	a.started = true
	a.cancel = cancel
	a.mu.Unlock()

	bridgeDone := service.BridgeLeaseEvents(runCtx, a.leases, a.bus)
	a.spawn(func() { <-bridgeDone })
	a.spawn(func() {
		if err := a.tracker.Run(runCtx); err != nil {
			a.log.Error("tailscaled status tracker stopped", "err", err)
		}
	})
	a.spawn(func() { a.maint.Run(runCtx) })
	a.spawn(func() { _ = a.listen.Run(runCtx) }) // 異常終了は supervisor がログに残す

	// ホットキーの登録に失敗しても（他のアプリが同じキーを使っているなど）、アプリは動かす。
	// トレイとダッシュボードのキルスイッチは使える。
	if err := a.hotkey.Bind(a.initialKey); err != nil {
		a.log.Error("kill switch hotkey is not active; use the tray or the dashboard", "err", err)
		a.toast("error", "キルスイッチのホットキーを登録できませんでした。トレイまたはダッシュボードから発動できます。")
	} else {
		a.log.Info("kill switch hotkey registered", "hotkey", a.initialKey)
	}
	return nil
}

func (a *App) spawn(fn func()) {
	a.running.Add(1)
	go func() {
		defer a.running.Done()
		fn()
	}()
}

// Shutdown はアプリを止める（docs/components/overview.md §9 の終了順）: ホットキーの解除 → Listener の停止 →
// MCP セッションを閉じる → リースの解放 → 押下中の入力の解放 → 監査のキューの書き切り → 資源（DB など）を閉じる。
// 各段の失敗は後続を止めず、まとめて返す。複数回呼んでもよい（2 回目以降は何もしない）。
func (a *App) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return nil
	}
	a.stopped = true
	cancel := a.cancel
	a.mu.Unlock()
	a.log.Info("puppet-remo is shutting down")

	var errs []error
	a.hotkey.Close()
	if cancel != nil {
		cancel()
	}
	if err := waitGroup(ctx, &a.running); err != nil {
		errs = append(errs, fmt.Errorf("stop background tasks: %w", err))
	}
	if err := a.mcp.CloseSessions(ctx); err != nil {
		errs = append(errs, fmt.Errorf("close mcp sessions: %w", err))
	}
	a.leases.ReleaseAll(string(domain.ReleaseDisconnect))
	if err := a.cfg.Platform.Input.ReleaseAll(); err != nil {
		errs = append(errs, fmt.Errorf("release held input: %w", err))
	}
	if err := a.audit.Close(ctx); err != nil {
		errs = append(errs, fmt.Errorf("flush audit: %w", err))
	}
	for _, c := range a.cfg.Closers {
		if err := c.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close resource: %w", err))
		}
	}
	return errors.Join(errs...)
}

// waitGroup は wg の完了を、ctx の期限まで待つ。
func waitGroup(ctx context.Context, wg *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
