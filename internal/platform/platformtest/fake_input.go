package platformtest

import (
	"context"
	"math"
	"slices"
	"sync"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.InputInjector = (*FakeInput)(nil)

// FakeInput は platform.InputInjector の偽の実装。注入はせず、呼び出しの記録とカーソル・押下状態の追跡だけを行う。
type FakeInput struct {
	Recorder

	mu      sync.Mutex
	cursor  domain.Point
	held    map[string]bool        // KeyEvent で押下中のキー（Code）
	buttons map[domain.Button]bool // Button で押下中のボタン
}

func NewFakeInput() *FakeInput {
	return &FakeInput{held: make(map[string]bool), buttons: make(map[domain.Button]bool)}
}

// HeldButtons は Button で押下中のボタンを昇順で返す。
func (f *FakeInput) HeldButtons() []domain.Button {
	f.mu.Lock()
	defer f.mu.Unlock()
	buttons := make([]domain.Button, 0, len(f.buttons))
	for b := range f.buttons {
		buttons = append(buttons, b)
	}
	slices.Sort(buttons)
	return buttons
}

// SetCursor はカーソル位置を設定する。
func (f *FakeInput) SetCursor(p domain.Point) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursor = p
}

// HeldKeys は KeyEvent で押下中のキー（Code）を昇順で返す。
func (f *FakeInput) HeldKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.held))
	for k := range f.held {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func checkMods(mods []domain.Modifier) error {
	for _, m := range mods {
		if !m.Valid() {
			return domain.ErrInvalidArgument.WithMsg("unknown modifier: " + string(m))
		}
	}
	return nil
}

// cursorNow は記録せずにカーソル位置を返す（FakeApp も使う）。
func (f *FakeInput) cursorNow() domain.Point {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cursor
}

func (f *FakeInput) moveTo(p domain.Point) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursor = p
}

func (f *FakeInput) MoveTo(ctx context.Context, p domain.Point) error {
	if err := f.enter(ctx, "MoveTo", p); err != nil {
		return err
	}
	f.moveTo(p)
	return nil
}

func (f *FakeInput) Click(ctx context.Context, b domain.Button, count int, mods []domain.Modifier) error {
	if err := f.enter(ctx, "Click", b, count, slices.Clone(mods)); err != nil {
		return err
	}
	if !b.Valid() {
		return domain.ErrInvalidArgument.WithMsg("unknown button: " + string(b))
	}
	if count < 1 || count > 3 {
		return domain.ErrInvalidArgument.WithMsg("click count must be 1 to 3")
	}
	return checkMods(mods)
}

func (f *FakeInput) Button(ctx context.Context, b domain.Button, a domain.ButtonAction) error {
	if err := f.enter(ctx, "Button", b, a); err != nil {
		return err
	}
	if !b.Valid() {
		return domain.ErrInvalidArgument.WithMsg("unknown button: " + string(b))
	}
	if !a.Valid() {
		return domain.ErrInvalidArgument.WithMsg("unknown button action: " + string(a))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if a == domain.ButtonActionDown {
		f.buttons[b] = true
	} else {
		delete(f.buttons, b)
	}
	return nil
}

func (f *FakeInput) Drag(ctx context.Context, from, to domain.Point, mods []domain.Modifier) error {
	if err := f.enter(ctx, "Drag", from, to, slices.Clone(mods)); err != nil {
		return err
	}
	if err := checkMods(mods); err != nil {
		return err
	}
	f.moveTo(to)
	return nil
}

func (f *FakeInput) Scroll(ctx context.Context, at domain.Point, dir domain.Direction, amount int) error {
	if err := f.enter(ctx, "Scroll", at, dir, amount); err != nil {
		return err
	}
	if !dir.Valid() {
		return domain.ErrInvalidArgument.WithMsg("unknown direction: " + string(dir))
	}
	if amount <= 0 {
		return domain.ErrInvalidArgument.WithMsg("scroll amount must be positive")
	}
	f.moveTo(at)
	return nil
}

func (f *FakeInput) Wheel(ctx context.Context, at domain.Point, dx, dy float64) error {
	if err := f.enter(ctx, "Wheel", at, dx, dy); err != nil {
		return err
	}
	for _, v := range []float64{dx, dy} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return domain.ErrInvalidArgument.WithMsg("wheel delta must be finite")
		}
	}
	f.moveTo(at)
	return nil
}

func (f *FakeInput) Key(ctx context.Context, k domain.KeyCombo) error {
	if err := f.enter(ctx, "Key", k); err != nil {
		return err
	}
	return k.Validate()
}

func (f *FakeInput) KeyEvent(ctx context.Context, e domain.KeyEvent) error {
	if err := f.enter(ctx, "KeyEvent", e); err != nil {
		return err
	}
	if !e.Action.Valid() {
		return domain.ErrInvalidArgument.WithMsg("unknown key action: " + string(e.Action))
	}
	if e.Code == "" {
		return domain.ErrInvalidArgument.WithMsg("key code is empty")
	}
	if err := checkMods(e.Mods); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.Action == domain.KeyDown {
		f.held[e.Code] = true
	} else {
		delete(f.held, e.Code)
	}
	return nil
}

func (f *FakeInput) Text(ctx context.Context, s string) error {
	return f.enter(ctx, "Text", s)
}

func (f *FakeInput) ReleaseAll() error {
	if err := f.enterSync("ReleaseAll"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	clear(f.buttons)
	clear(f.held)
	return nil
}

func (f *FakeInput) CursorPos() (domain.Point, error) {
	if err := f.enterSync("CursorPos"); err != nil {
		return domain.Point{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cursor, nil
}
