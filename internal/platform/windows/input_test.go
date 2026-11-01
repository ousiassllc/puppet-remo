package windows

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// fakeDriver は driver の偽の実装。絶対移動はカーソルに反映し、送った入力の列を記録する。
type fakeDriver struct {
	mu     sync.Mutex
	screen domain.Rect
	cursor domain.Point
	// jitter はカーソルが 1 画素ずれる（絶対座標の丸め）ことを再現する。
	jitter bool
	calls  [][]inputStep
	setPos []domain.Point

	// failOn は n 回目（1 始まり）の send を err で失敗させる。0 なら失敗させない。
	failOn  int
	failErr error
	sends   int
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{screen: domain.Rect{X: -1920, Y: 0, W: 3840, H: 1080}}
}

func (d *fakeDriver) send(steps []inputStep) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sends++
	d.calls = append(d.calls, append([]inputStep(nil), steps...))
	if d.failOn != 0 && d.sends == d.failOn {
		return d.failErr
	}
	for _, s := range steps {
		if s.kind == stepMouse && s.flags&mouseeventfAbsolute != 0 {
			d.cursor = domain.Point{
				X: int(s.dx)*d.screen.W/absoluteRange + d.screen.X,
				Y: int(s.dy)*d.screen.H/absoluteRange + d.screen.Y,
			}
			if d.jitter {
				d.cursor.X++
			}
		}
	}
	return nil
}

func (d *fakeDriver) cursorPos() (domain.Point, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cursor, nil
}

func (d *fakeDriver) setCursorPos(p domain.Point) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cursor = p
	d.setPos = append(d.setPos, p)
	return nil
}

func (d *fakeDriver) virtualScreen() (domain.Rect, error) { return d.screen, nil }

func (d *fakeDriver) scanKey(r rune) (uint16, bool, bool) {
	switch r {
	case '-':
		return vkOemMinus, false, true
	case '!':
		return '1', true, true
	}
	return 0, false, false
}

func (d *fakeDriver) sent() [][]inputStep {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]inputStep(nil), d.calls...)
}

func newTestInput(t *testing.T) (*Input, *fakeDriver) {
	t.Helper()
	d := newFakeDriver()
	in := newInput(d)
	t.Cleanup(func() { _ = in.Close() })
	return in, d
}

// 偽の driver 上の Input が、platform.InputInjector の契約を満たす。
func TestInputContract(t *testing.T) {
	platformtest.RunInputContract(t, func(t *testing.T) platform.InputInjector {
		in, _ := newTestInput(t)
		return in
	}, domain.Point{X: 100, Y: 100})
}

func TestMoveToUsesVirtualDesktopAbsoluteCoordinates(t *testing.T) {
	in, d := newTestInput(t)
	p := domain.Point{X: -1000, Y: 500}
	if err := in.MoveTo(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	calls := d.sent()
	if len(calls) != 1 || len(calls[0]) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	want := absMoveStep(normalizeAbs(p.X, d.screen.X, d.screen.W), normalizeAbs(p.Y, d.screen.Y, d.screen.H))
	if calls[0][0] != want {
		t.Errorf("step = %+v, want %+v", calls[0][0], want)
	}
	got, _ := in.CursorPos()
	if got != p {
		t.Errorf("cursor = %v, want %v", got, p)
	}
}

func TestMoveToOutsideDesktopIsInvalidCoordinates(t *testing.T) {
	in, d := newTestInput(t)
	for _, p := range []domain.Point{{X: -1921, Y: 0}, {X: 1920, Y: 0}, {X: 0, Y: 1080}, {X: 0, Y: -1}} {
		err := in.MoveTo(context.Background(), p)
		if !errors.Is(err, domain.ErrInvalidCoordinates) {
			t.Errorf("MoveTo(%v) = %v, want invalid_coordinates", p, err)
		}
	}
	if len(d.sent()) != 0 {
		t.Error("nothing should be sent for an out-of-range point")
	}
}

func TestMoveToCorrectsOnePixelRoundingError(t *testing.T) {
	in, d := newTestInput(t)
	d.jitter = true
	p := domain.Point{X: 640, Y: 360}
	if err := in.MoveTo(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if len(d.setPos) != 1 || d.setPos[0] != p {
		t.Errorf("SetCursorPos calls = %v, want [%v]", d.setPos, p)
	}
	if got, _ := in.CursorPos(); got != p {
		t.Errorf("cursor = %v, want %v", got, p)
	}
}

func TestClickSendsModifiersAroundButtonPairs(t *testing.T) {
	in, d := newTestInput(t)
	if err := in.Click(context.Background(), domain.ButtonRight, 2, []domain.Modifier{domain.ModCtrl, domain.ModShift, domain.ModCtrl}); err != nil {
		t.Fatal(err)
	}
	ctrl, shift := keySpec{VK: vkControl}, keySpec{VK: vkShift}
	down, _ := buttonStep(domain.ButtonRight, false)
	up, _ := buttonStep(domain.ButtonRight, true)
	want := [][]inputStep{{keyDownStep(ctrl), keyDownStep(shift), down, up, down, up, keyUpStep(shift), keyUpStep(ctrl)}}
	if got := d.sent(); !reflect.DeepEqual(got, want) {
		t.Errorf("sent = %+v, want %+v", got, want)
	}
}

func TestClickReleasesModifiersWhenSendFails(t *testing.T) {
	in, d := newTestInput(t)
	d.failOn, d.failErr = 1, domain.ErrInputBlocked
	err := in.Click(context.Background(), domain.ButtonLeft, 1, []domain.Modifier{domain.ModAlt})
	if !errors.Is(err, domain.ErrInputBlocked) {
		t.Fatalf("error = %v, want input_blocked", err)
	}
	calls := d.sent()
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want the failed send and a cleanup send", calls)
	}
	cleanup := calls[1]
	if len(cleanup) != 2 || cleanup[0] != (inputStep{kind: stepMouse, flags: mouseeventfLeftUp}) ||
		cleanup[1] != keyUpStep(keySpec{VK: vkMenu}) {
		t.Errorf("cleanup = %+v, want left button up then Alt up", cleanup)
	}
}

func TestDragPressesMovesThroughPathAndReleases(t *testing.T) {
	in, d := newTestInput(t)
	from, to := domain.Point{X: 100, Y: 100}, domain.Point{X: 300, Y: 260}
	if err := in.Drag(context.Background(), from, to, []domain.Modifier{domain.ModShift}); err != nil {
		t.Fatal(err)
	}
	calls := d.sent()
	flat := make([]inputStep, 0)
	for _, c := range calls {
		flat = append(flat, c...)
	}
	down, _ := buttonStep(domain.ButtonLeft, false)
	up, _ := buttonStep(domain.ButtonLeft, true)
	shift := keySpec{VK: vkShift}
	downIdx, upIdx := -1, -1
	for i, s := range flat {
		switch s {
		case down:
			downIdx = i
		case up:
			upIdx = i
		}
	}
	if downIdx < 0 || upIdx < downIdx {
		t.Fatalf("button down at %d, up at %d: %+v", downIdx, upIdx, flat)
	}
	if flat[downIdx-1] != keyDownStep(shift) {
		t.Errorf("Shift must be pressed just before the button: %+v", flat[downIdx-1])
	}
	if flat[len(flat)-1] != keyUpStep(shift) || flat[len(flat)-2] != up {
		t.Errorf("must end with button up then Shift up: %+v", flat[len(flat)-2:])
	}
	moves := 0
	for _, s := range flat[downIdx:upIdx] {
		if s.kind == stepMouse && s.flags&mouseeventfMove != 0 {
			moves++
		}
	}
	if moves != dragSteps {
		t.Errorf("%d moves while the button is down, want %d", moves, dragSteps)
	}
	if got, _ := in.CursorPos(); got != to {
		t.Errorf("cursor = %v, want %v", got, to)
	}
}

func TestDragReleasesWhenCanceledMidway(t *testing.T) {
	in, d := newTestInput(t)
	ctx, cancel := context.WithCancel(context.Background())
	// 3 回目の send（移動 → 押下 → 最初のドラッグ移動）の後で取り消す。
	calls := 0
	in.drv = &cancelAfterDriver{fakeDriver: d, after: 3, cancel: cancel, count: &calls}
	err := in.Drag(ctx, domain.Point{X: 10, Y: 10}, domain.Point{X: 500, Y: 500}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	flat := []inputStep{}
	for _, c := range d.sent() {
		flat = append(flat, c...)
	}
	up, _ := buttonStep(domain.ButtonLeft, true)
	if flat[len(flat)-1] != up {
		t.Errorf("the button must be released after cancellation: %+v", flat[len(flat)-1])
	}
}

// cancelAfterDriver は after 回目の send の後に cancel を呼ぶ。
type cancelAfterDriver struct {
	*fakeDriver
	after  int
	cancel context.CancelFunc
	count  *int
}

func (d *cancelAfterDriver) send(steps []inputStep) error {
	err := d.fakeDriver.send(steps)
	*d.count++
	if *d.count == d.after {
		d.cancel()
	}
	return err
}

func TestWheelAndScrollSendVerticalAndHorizontalEvents(t *testing.T) {
	in, d := newTestInput(t)
	at := domain.Point{X: 200, Y: 200}
	if err := in.Scroll(context.Background(), at, domain.DirDown, 2); err != nil {
		t.Fatal(err)
	}
	if err := in.Wheel(context.Background(), at, 0.5, -1); err != nil {
		t.Fatal(err)
	}
	var wheels []inputStep
	for _, c := range d.sent() {
		for _, s := range c {
			if s.kind == stepMouse && s.flags&(mouseeventfWheel|mouseeventfHWheel) != 0 {
				wheels = append(wheels, s)
			}
		}
	}
	want := []inputStep{
		wheelStep(false, -120), wheelStep(false, -120), // down 2 ノッチ = 負
		wheelStep(false, 120), // dy = -1（上）= 正
		wheelStep(true, 60),   // dx = 0.5（右）
	}
	if !reflect.DeepEqual(wheels, want) {
		t.Errorf("wheels = %+v, want %+v", wheels, want)
	}
}

func TestWheelRejectsTooLargeNotches(t *testing.T) {
	in, _ := newTestInput(t)
	err := in.Wheel(context.Background(), domain.Point{X: 1, Y: 1}, 0, maxWheelNotches+1)
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("error = %v, want invalid_argument", err)
	}
	err = in.Scroll(context.Background(), domain.Point{X: 1, Y: 1}, domain.DirUp, maxWheelNotches+1)
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("error = %v, want invalid_argument", err)
	}
}

func TestKeyCombo(t *testing.T) {
	ctrl, shift := keySpec{VK: vkControl}, keySpec{VK: vkShift}
	tests := []struct {
		name  string
		combo string
		want  []inputStep
	}{
		{"ctrl+shift+t", "ctrl+shift+t", []inputStep{
			keyDownStep(ctrl), keyDownStep(shift), keyDownStep(keySpec{VK: 'T'}), keyUpStep(keySpec{VK: 'T'}),
			keyUpStep(shift), keyUpStep(ctrl)}},
		{"Enter", "Enter", []inputStep{keyDownStep(keySpec{VK: vkReturn}), keyUpStep(keySpec{VK: vkReturn})}},
		{"win+left", "win+left", []inputStep{
			keyDownStep(keySpec{VK: vkLWin, Extended: true}),
			keyDownStep(keySpec{VK: vkLeft, Extended: true}), keyUpStep(keySpec{VK: vkLeft, Extended: true}),
			keyUpStep(keySpec{VK: vkLWin, Extended: true})}},
		{"symbol", "ctrl+-", []inputStep{
			keyDownStep(ctrl), keyDownStep(keySpec{VK: vkOemMinus}), keyUpStep(keySpec{VK: vkOemMinus}), keyUpStep(ctrl)}},
		{"symbol needing shift adds Shift", "!", []inputStep{
			keyDownStep(shift), keyDownStep(keySpec{VK: '1'}), keyUpStep(keySpec{VK: '1'}), keyUpStep(shift)}},
		{"symbol needing shift with shift given", "shift+!", []inputStep{
			keyDownStep(shift), keyDownStep(keySpec{VK: '1'}), keyUpStep(keySpec{VK: '1'}), keyUpStep(shift)}},
	}
	for _, tt := range tests {
		in, d := newTestInput(t)
		combo, err := domain.ParseKeyCombo(tt.combo)
		if err != nil {
			t.Fatalf("%s: parse: %v", tt.name, err)
		}
		if err := in.Key(context.Background(), combo); err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if got := d.sent(); len(got) != 1 || !reflect.DeepEqual(got[0], tt.want) {
			t.Errorf("%s: sent = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestKeyRejectsKeyMissingFromLayout(t *testing.T) {
	in, d := newTestInput(t)
	err := in.Key(context.Background(), domain.KeyCombo{Key: "€"})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("error = %v, want invalid_argument", err)
	}
	if len(d.sent()) != 0 {
		t.Error("nothing should be sent")
	}
}

func TestKeyEventTracksHeldKeysAndReleaseAll(t *testing.T) {
	in, d := newTestInput(t)
	ctx := context.Background()
	for _, e := range []domain.KeyEvent{
		{Action: domain.KeyDown, Code: "ControlLeft"},
		{Action: domain.KeyDown, Code: "KeyA"},
		{Action: domain.KeyDown, Code: "KeyA"}, // 自動リピート。二重に記録しない
		{Action: domain.KeyDown, Code: "ArrowLeft"},
		{Action: domain.KeyUp, Code: "KeyA"},
	} {
		if err := in.KeyEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	before := len(d.sent())
	if err := in.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	calls := d.sent()
	if len(calls) != before+1 {
		t.Fatalf("ReleaseAll sent %d calls, want 1", len(calls)-before)
	}
	want := []inputStep{keyUpStep(keySpec{VK: vkLeft, Extended: true}), keyUpStep(keySpec{VK: vkLControl})}
	if got := calls[len(calls)-1]; !reflect.DeepEqual(got, want) {
		t.Errorf("ReleaseAll sent %+v, want %+v (reverse press order, A already released)", got, want)
	}
	if err := in.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	if len(d.sent()) != len(calls) {
		t.Error("a second ReleaseAll must not send anything")
	}
}

func TestButtonSendsOneStepAndReleaseAllReleasesHeldButtons(t *testing.T) {
	in, d := newTestInput(t)
	ctx := context.Background()
	for _, c := range []struct {
		b domain.Button
		a domain.ButtonAction
	}{
		{domain.ButtonLeft, domain.ButtonActionDown},
		{domain.ButtonLeft, domain.ButtonActionDown}, // 二重に記録しない
		{domain.ButtonRight, domain.ButtonActionDown},
		{domain.ButtonRight, domain.ButtonActionUp},
		{domain.ButtonMiddle, domain.ButtonActionDown},
	} {
		if err := in.Button(ctx, c.b, c.a); err != nil {
			t.Fatal(err)
		}
	}
	calls := d.sent()
	if got, want := calls[2], []inputStep{mustButtonStep(t, domain.ButtonRight, false)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("right down sent %+v, want %+v", got, want)
	}
	before := len(calls)
	if err := in.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	calls = d.sent()
	if len(calls) != before+1 {
		t.Fatalf("ReleaseAll sent %d calls, want 1", len(calls)-before)
	}
	want := []inputStep{mustButtonStep(t, domain.ButtonMiddle, true), mustButtonStep(t, domain.ButtonLeft, true)}
	if got := calls[len(calls)-1]; !reflect.DeepEqual(got, want) {
		t.Errorf("ReleaseAll sent %+v, want %+v (reverse press order, right already released)", got, want)
	}
	if err := in.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	if len(d.sent()) != len(calls) {
		t.Error("a second ReleaseAll must not send anything")
	}
}

func TestButtonRejectsInvalidArguments(t *testing.T) {
	in, d := newTestInput(t)
	ctx := context.Background()
	for name, err := range map[string]error{
		"unknown button": in.Button(ctx, "x", domain.ButtonActionDown),
		"unknown action": in.Button(ctx, domain.ButtonLeft, "hold"),
	} {
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: error = %v, want invalid_argument", name, err)
		}
	}
	if len(d.sent()) != 0 {
		t.Error("nothing should be sent")
	}
}

func mustButtonStep(t *testing.T, b domain.Button, up bool) inputStep {
	t.Helper()
	step, ok := buttonStep(b, up)
	if !ok {
		t.Fatalf("buttonStep(%s) failed", b)
	}
	return step
}

func TestKeyEventUnknownCode(t *testing.T) {
	in, d := newTestInput(t)
	err := in.KeyEvent(context.Background(), domain.KeyEvent{Action: domain.KeyDown, Code: "NoSuchCode"})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("error = %v, want invalid_argument", err)
	}
	if len(d.sent()) != 0 {
		t.Error("nothing should be sent")
	}
}

// KeyEvent で押下中の修飾キーがある間の Click は、その修飾キーを押し直さず、離しもしない。
func TestClickKeepsModifierHeldByKeyEvent(t *testing.T) {
	in, d := newTestInput(t)
	ctx := context.Background()
	if err := in.KeyEvent(ctx, domain.KeyEvent{Action: domain.KeyDown, Code: "ShiftLeft"}); err != nil {
		t.Fatal(err)
	}
	if err := in.Click(ctx, domain.ButtonLeft, 1, []domain.Modifier{domain.ModShift}); err != nil {
		t.Fatal(err)
	}
	calls := d.sent()
	down, _ := buttonStep(domain.ButtonLeft, false)
	up, _ := buttonStep(domain.ButtonLeft, true)
	if got, want := calls[len(calls)-1], []inputStep{down, up}; !reflect.DeepEqual(got, want) {
		t.Errorf("click sent %+v, want only the button pair %+v", got, want)
	}
}

func TestReleaseAllDropsWheelRemainder(t *testing.T) {
	in, d := newTestInput(t)
	ctx := context.Background()
	at := domain.Point{X: 5, Y: 5}
	if err := in.Wheel(ctx, at, 0, 0.004); err != nil { // 0.48 → 0
		t.Fatal(err)
	}
	if err := in.ReleaseAll(); err != nil {
		t.Fatal(err)
	}
	if err := in.Wheel(ctx, at, 0, 0.004); err != nil {
		t.Fatal(err)
	}
	for _, c := range d.sent() {
		for _, s := range c {
			if s.kind == stepMouse && s.flags&mouseeventfWheel != 0 {
				t.Errorf("unexpected wheel event %+v", s)
			}
		}
	}
}

func TestTextSendsUnicodeInChunksWithoutSplittingCharacters(t *testing.T) {
	in, d := newTestInput(t)
	text := ""
	for range textChunk + 10 {
		text += "😀"
	}
	if err := in.Text(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	calls := d.sent()
	if len(calls) < 2 {
		t.Fatalf("expected the text to be split, got %d call(s)", len(calls))
	}
	total := 0
	for _, c := range calls {
		if len(c)%4 != 0 || !c[len(c)-1].up {
			t.Errorf("chunk of %d steps splits a character", len(c))
		}
		total += len(c)
	}
	if want := (textChunk + 10) * 4; total != want {
		t.Errorf("total steps = %d, want %d", total, want)
	}
}

// 先頭の 1 文字（2 件）の後に BMP 外の文字が続くと、2*textChunk 件の境界が上位サロゲートの解放の直後に来る。
// そこで切ると、サロゲートペアが別々の SendInput に分かれる。
func TestTextNeverSplitsASurrogatePairAcrossSendInputCalls(t *testing.T) {
	in, d := newTestInput(t)
	text := "a"
	for range textChunk + 10 {
		text += "😀"
	}
	if err := in.Text(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	calls := d.sent()
	if len(calls) < 2 {
		t.Fatalf("expected the text to be split, got %d call(s)", len(calls))
	}
	for i, c := range calls {
		if !endsCharacter(c[len(c)-1]) {
			t.Errorf("chunk %d ends in the middle of a character: %+v", i, c[len(c)-1])
		}
	}
}

func TestTextStopsWhenSendFailsAndReportsError(t *testing.T) {
	in, d := newTestInput(t)
	d.failOn, d.failErr = 1, domain.ErrDesktopUnavailable
	err := in.Text(context.Background(), "abc")
	if !errors.Is(err, domain.ErrDesktopUnavailable) {
		t.Errorf("error = %v, want desktop_unavailable", err)
	}
}
