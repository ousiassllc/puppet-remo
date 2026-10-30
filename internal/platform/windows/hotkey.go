package windows

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.Hotkey = (*Hotkey)(nil)

// hotkeyLoop はメッセージループ専用の OS スレッド。RegisterHotKey したスレッドにだけ WM_HOTKEY が届くので、
// 登録・解除と fn の呼び出しをすべてこのスレッドの上で行う。
type hotkeyLoop interface {
	// Do は fn をループのスレッドの上で実行し、終わるまで待つ。閉じていたらエラー。
	Do(fn func() error) error
	// Post は fn をループのスレッドの上で実行するよう予約し、待たない。Do と Post は予約した順に実行される。
	Post(fn func())
	// Register と Unregister は Do / Post の fn の中（ループのスレッドの上）でだけ呼ぶ。
	Register(id int32, chord hotkeyChord) error
	Unregister(id int32)
	// Close はループを止める。登録中のホットキーはすべて解除する。複数回呼んでも安全。
	Close()
}

type hotkeyEntry struct {
	id     int32
	chord  hotkeyChord
	fn     func()
	active bool // mu で守る
}

// Hotkey は platform.Hotkey の実装。fn は専用のメッセージループのスレッドから呼ぶ。
// fn の中から Register を呼んではならない（ループの待ちで固まる）。unregister は fn の中から呼んでよい。
type Hotkey struct {
	loop hotkeyLoop

	mu      sync.Mutex
	nextID  int32
	byChord map[hotkeyChord]*hotkeyEntry
	byID    map[int32]*hotkeyEntry
}

func newHotkey(loop hotkeyLoop) *Hotkey {
	return &Hotkey{
		loop:    loop,
		nextID:  1,
		byChord: make(map[hotkeyChord]*hotkeyEntry),
		byID:    make(map[int32]*hotkeyEntry),
	}
}

// Register は platform.Hotkey.Register。修飾キーが 1 つもない spec は ErrInvalidArgument（hotkeyParse）。
// 他のアプリが同じ組み合わせを使っているときは、OS のエラーを返す。
func (h *Hotkey) Register(spec string, fn func()) (unregister func(), err error) {
	chord, err := hotkeyParse(spec)
	if err != nil {
		return nil, err
	}
	if fn == nil {
		return nil, domain.ErrInvalidArgument.WithMsg("hotkey callback is nil")
	}
	entry, err := h.add(spec, chord, fn)
	if err != nil {
		return nil, err
	}
	if err := h.loop.Do(func() error { return h.loop.Register(entry.id, chord) }); err != nil {
		h.remove(entry)
		return nil, fmt.Errorf("hotkey: register %q: %w", strings.TrimSpace(spec), err)
	}
	return func() {
		if h.remove(entry) {
			h.loop.Post(func() { h.loop.Unregister(entry.id) })
		}
	}, nil
}

func (h *Hotkey) add(spec string, chord hotkeyChord, fn func()) (*hotkeyEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, dup := h.byChord[chord]; dup {
		return nil, fmt.Errorf("hotkey %q is already registered", strings.TrimSpace(spec))
	}
	entry := &hotkeyEntry{id: h.nextID, chord: chord, fn: fn, active: true}
	h.nextID++
	h.byChord[chord] = entry
	h.byID[entry.id] = entry
	return entry, nil
}

// remove は登録を外す。外したら true（すでに外していれば false）。外した後は fire が fn を呼ばない。
func (h *Hotkey) remove(entry *hotkeyEntry) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !entry.active {
		return false
	}
	entry.active = false
	delete(h.byChord, entry.chord)
	delete(h.byID, entry.id)
	return true
}

// fire は id のホットキーが押されたときにループのスレッドから呼ばれ、登録中なら fn を呼ぶ。
func (h *Hotkey) fire(id int32) {
	h.mu.Lock()
	entry := h.byID[id]
	h.mu.Unlock()
	if entry != nil {
		entry.fn()
	}
}

// Close はメッセージループのスレッドを止め、登録中のホットキーをすべて解除する。複数回呼んでも安全。
// fn（ループのスレッド）の中から呼んではならない（ループの終了を待つので固まる）。
func (h *Hotkey) Close() { h.loop.Close() }
