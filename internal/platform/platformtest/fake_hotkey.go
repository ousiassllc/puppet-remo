package platformtest

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.Hotkey = (*FakeHotkey)(nil)

// FakeHotkey は platform.Hotkey の偽の実装。キーは押されないので、Trigger で押下を模擬する。
type FakeHotkey struct {
	Recorder

	mu   sync.Mutex
	regs map[string]*hotkeyReg // 正規化した spec → 登録
}

type hotkeyReg struct{ fn func() }

func NewFakeHotkey() *FakeHotkey { return &FakeHotkey{regs: make(map[string]*hotkeyReg)} }

// normalizeSpec は spec を小文字・空白除去で正規化する。空のトークンを含むと ok=false。
func normalizeSpec(spec string) (string, bool) {
	tokens := strings.Split(spec, "+")
	for i, t := range tokens {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			return "", false
		}
		tokens[i] = t
	}
	return strings.Join(tokens, "+"), true
}

func (f *FakeHotkey) Register(spec string, fn func()) (func(), error) {
	if err := f.enterSync("Register", spec); err != nil {
		return nil, err
	}
	key, ok := normalizeSpec(spec)
	if !ok {
		return nil, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("invalid hotkey spec %q", spec))
	}
	if fn == nil {
		return nil, domain.ErrInvalidArgument.WithMsg("hotkey callback is nil")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, dup := f.regs[key]; dup {
		return nil, fmt.Errorf("hotkey %q is already registered", spec)
	}
	reg := &hotkeyReg{fn: fn}
	f.regs[key] = reg
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.regs[key] == reg {
			delete(f.regs, key)
		}
	}, nil
}

// Trigger は spec のホットキーの押下を模擬し、登録されていれば fn を呼んで true を返す。
// fn はロックの外で、呼び出し元の goroutine で同期的に呼ぶ。
func (f *FakeHotkey) Trigger(spec string) bool {
	key, ok := normalizeSpec(spec)
	if !ok {
		return false
	}
	f.mu.Lock()
	reg := f.regs[key]
	f.mu.Unlock()
	if reg == nil {
		return false
	}
	reg.fn()
	return true
}

// Registered は登録中の spec（正規化後）を昇順で返す。
func (f *FakeHotkey) Registered() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.regs))
	for k := range f.regs {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
