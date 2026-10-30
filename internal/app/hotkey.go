package app

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// KillSwitchHotkey は、キルスイッチのグローバルホットキー（FR-H06、FR-S02）の登録を持つ。
// 設定の変更では登録し直す。新しいホットキーを登録できなければ、これまでの登録を残す。
type KillSwitchHotkey struct {
	hotkey  platform.Hotkey
	trigger func()

	mu      sync.Mutex
	spec    string
	release func()
}

// NewKillSwitchHotkey は、押されたら trigger を呼ぶホットキーの登録口を作る。まだ何も登録しない。
func NewKillSwitchHotkey(h platform.Hotkey, trigger func()) *KillSwitchHotkey {
	return &KillSwitchHotkey{hotkey: h, trigger: trigger}
}

// Bind は spec を登録する。すでに登録中の spec と同じ（大文字小文字を除く）なら何もしない。別の spec なら、新しい spec を登録できたあとに
// 古い登録を解除する（登録に失敗したら古い登録のまま、エラーを返す）。
func (k *KillSwitchHotkey) Bind(spec string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.release != nil && strings.EqualFold(k.spec, spec) {
		return nil
	}
	unregister, err := k.hotkey.Register(spec, k.trigger)
	if err != nil {
		return fmt.Errorf("register kill switch hotkey %q: %w", spec, err)
	}
	if k.release != nil {
		k.release()
	}
	k.spec, k.release = spec, unregister
	return nil
}

// Spec は登録中のホットキー。登録がなければ空。
func (k *KillSwitchHotkey) Spec() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.spec
}

// Close は登録を解除する。複数回呼んでよい。
func (k *KillSwitchHotkey) Close() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.release != nil {
		k.release()
		k.release, k.spec = nil, ""
	}
}
