package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

// scene は偽のデスクトップの状態。時間（時計・グラフ・進捗）と、偽の入力（カーソル・メモ帳へのタイプ）で変わる。
// 画面の変化を Streamer の差分タイルで見せるため、変わるのは画面の一部だけにしている。
type scene struct {
	mu      sync.Mutex
	cursor  domain.Point
	typed   []string // メモ帳へ打たれた行（最後の要素が入力中の行）
	clicks  int
	lastOps []string // 直近の入力（新しいものが後ろ。最大 4 件）
	start   time.Time
}

func newScene() *scene {
	return &scene{cursor: domain.Point{X: 760, Y: 420}, typed: []string{""}, start: time.Now()}
}

func (s *scene) note(op string) {
	s.lastOps = append(s.lastOps, op)
	if len(s.lastOps) > 4 {
		s.lastOps = s.lastOps[1:]
	}
}

func (s *scene) moveTo(p domain.Point, op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursor = p
	if op != "" {
		s.note(op)
	}
}

func (s *scene) click(op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clicks++
	s.note(op)
}

func (s *scene) noteOnly(op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.note(op)
}

// typeText はメモ帳へ文字を足す。"\n" は改行、"\b" は 1 文字削除。
func (s *scene) typeText(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range text {
		last := len(s.typed) - 1
		switch r {
		case '\n':
			s.typed = append(s.typed, "")
		case '\b':
			if rs := []rune(s.typed[last]); len(rs) > 0 {
				s.typed[last] = string(rs[:len(rs)-1])
			}
		default:
			s.typed[last] += string(r)
		}
	}
	if len(s.typed) > 6 {
		s.typed = s.typed[len(s.typed)-6:]
	}
}

var (
	colTitle   = color.RGBA{0x1f, 0x4e, 0x8c, 0xff}
	colTitle2  = color.RGBA{0x2b, 0x6c, 0x5e, 0xff}
	colWindow  = color.RGBA{0xf4, 0xf6, 0xf9, 0xff}
	colText    = color.RGBA{0x22, 0x2b, 0x38, 0xff}
	colMuted   = color.RGBA{0x6b, 0x77, 0x88, 0xff}
	colTask    = color.RGBA{0x14, 0x1a, 0x26, 0xff}
	colAccent  = color.RGBA{0x3b, 0x82, 0xf6, 0xff}
	colGood    = color.RGBA{0x22, 0xa5, 0x5e, 0xff}
	colWhite   = color.RGBA{0xff, 0xff, 0xff, 0xff}
	colBlack   = color.RGBA{0x00, 0x00, 0x00, 0xff}
	colGraphBg = color.RGBA{0x10, 0x16, 0x20, 0xff}
)

func fill(img *image.RGBA, r image.Rectangle, c color.Color) {
	draw.Draw(img, r.Intersect(img.Bounds()), image.NewUniform(c), image.Point{}, draw.Src)
}

func text(img *image.RGBA, x, y int, c color.Color, s string) {
	d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: basicfont.Face7x13, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

// wallpaper はモニターごとの背景（縦のグラデーション。DISPLAY2 は色味を変える）。
func wallpaper(index, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		t := float64(y) / float64(h)
		var c color.RGBA
		if index == 0 {
			c = color.RGBA{R: uint8(24 + 30*t), G: uint8(58 + 70*t), B: uint8(110 + 80*(1-t)), A: 255}
		} else {
			c = color.RGBA{R: uint8(70 + 50*t), G: uint8(40 + 30*t), B: uint8(100 + 60*(1-t)), A: 255}
		}
		fill(img, image.Rect(0, y, w, y+1), c)
	}
	return img
}

func icon(img *image.RGBA, x, y int, c color.Color, label string) {
	fill(img, image.Rect(x, y, x+44, y+44), c)
	fill(img, image.Rect(x+6, y+6, x+38, y+14), colWhite)
	text(img, x+22-len(label)*7/2, y+62, colWhite, label)
}

func window(img *image.RGBA, r image.Rectangle, title string, bar color.Color) image.Rectangle {
	fill(img, r.Inset(-2), color.RGBA{0, 0, 0, 90})
	fill(img, r, colWindow)
	fill(img, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+26), bar)
	text(img, r.Min.X+10, r.Min.Y+17, colWhite, title)
	for i := range 3 {
		fill(img, image.Rect(r.Max.X-24-i*22, r.Min.Y+8, r.Max.X-12-i*22, r.Min.Y+18), color.RGBA{255, 255, 255, 220})
	}
	return image.Rect(r.Min.X, r.Min.Y+26, r.Max.X, r.Max.Y)
}

// render は monitor の今の画面を新しい画像として描く（Streamer が所有してよい）。
func (s *scene) render(base *image.RGBA, index int, now time.Time) *image.RGBA {
	s.mu.Lock()
	cursor, clicks := s.cursor, s.clicks
	typed := append([]string(nil), s.typed...)
	ops := append([]string(nil), s.lastOps...)
	s.mu.Unlock()

	b := base.Bounds()
	w, h := b.Dx(), b.Dy()
	img := image.NewRGBA(b)
	copy(img.Pix, base.Pix)

	label := "DISPLAY1"
	if index == 1 {
		label = "DISPLAY2"
	}
	icon(img, 24, 24, color.RGBA{0xf5, 0xb0, 0x42, 0xff}, "Docs")
	icon(img, 24, 110, color.RGBA{0x4c, 0xc2, 0x9a, 0xff}, "Photos")
	icon(img, 24, 196, color.RGBA{0xe0, 0x6c, 0x75, 0xff}, "Trash")
	text(img, w-90, 20, colWhite, label)

	// メモ帳（入力が反映される）。
	notes := image.Rect(w*12/100, h*8/100, w*62/100, h*70/100)
	body := window(img, notes, "Notes - demo-notes.txt", colTitle)
	fill(img, body.Inset(8), colWhite)
	y := body.Min.Y + 30
	for _, l := range []string{"Meeting notes (demo data)", "- check the remote viewer", "- try the shortcut menu", "- clipboard send / fetch"} {
		text(img, body.Min.X+20, y, colText, l)
		y += 18
	}
	y += 10
	fill(img, image.Rect(body.Min.X+20, y-8, body.Max.X-20, y-7), colMuted)
	y += 12
	for i, l := range typed {
		line := l
		if i == len(typed)-1 && now.UnixMilli()/500%2 == 0 {
			line += "_"
		}
		text(img, body.Min.X+20, y, colText, line)
		y += 18
	}
	// 直近の入力（ホストの偽の入力注入が受けたもの）。
	oy := body.Max.Y - 12 - 16*len(ops)
	fill(img, image.Rect(body.Min.X+8, oy-20, body.Max.X-8, body.Max.Y-8), color.RGBA{0xe8, 0xee, 0xf7, 0xff})
	text(img, body.Min.X+16, oy-6, colMuted, fmt.Sprintf("Injected input (fake injector)  clicks=%d", clicks))
	for i, o := range ops {
		text(img, body.Min.X+16, oy+12+16*i, colText, o)
	}

	// システムモニター（時間で変わる: 進捗・グラフ）。
	mon := image.Rect(w*66/100, h*8/100, w*94/100, h*50/100)
	mb := window(img, mon, "System Monitor", colTitle2)
	text(img, mb.Min.X+14, mb.Min.Y+24, colText, "Backup to NAS (demo)")
	pct := int(now.Sub(s.start).Seconds()*6) % 101
	bar := image.Rect(mb.Min.X+14, mb.Min.Y+34, mb.Max.X-14, mb.Min.Y+50)
	fill(img, bar, color.RGBA{0xd5, 0xdb, 0xe4, 0xff})
	fill(img, image.Rect(bar.Min.X, bar.Min.Y, bar.Min.X+bar.Dx()*pct/100, bar.Max.Y), colGood)
	text(img, mb.Min.X+14, mb.Min.Y+68, colMuted, fmt.Sprintf("%3d%%", pct))
	graph := image.Rect(mb.Min.X+14, mb.Min.Y+80, mb.Max.X-14, mb.Max.Y-14)
	fill(img, graph, colGraphBg)
	text(img, graph.Min.X+6, graph.Min.Y+14, colMuted, "CPU")
	t := float64(now.UnixMilli()) / 1000
	for x := 0; x < graph.Dx()-4; x += 6 {
		v := 0.5 + 0.35*math.Sin((t*1.5)+float64(x)/22) + 0.1*math.Sin(t*3.1+float64(x)/7)
		bh := int(v * float64(graph.Dy()-24))
		fill(img, image.Rect(graph.Min.X+2+x, graph.Max.Y-2-bh, graph.Min.X+2+x+4, graph.Max.Y-2), colAccent)
	}

	// タスクバー（時計は 1 秒ごとに変わる）。
	tb := image.Rect(0, h-40, w, h)
	fill(img, tb, colTask)
	fill(img, image.Rect(8, h-34, 56, h-6), colAccent)
	text(img, 16, h-15, colWhite, "Start")
	for i, name := range []string{"Notes", "System Monitor"} {
		r := image.Rect(70+i*150, h-34, 210+i*150, h-6)
		fill(img, r, color.RGBA{0x2a, 0x34, 0x48, 0xff})
		text(img, r.Min.X+10, h-15, colWhite, name)
	}
	text(img, w-140, h-22, colWhite, now.Format("15:04:05"))
	text(img, w-140, h-8, colMuted, now.Format("2006-01-02"))

	drawCursor(img, cursor)
	return img
}

// drawCursor は矢印のカーソルを描く（白地に黒の縁）。
func drawCursor(img *image.RGBA, p domain.Point) {
	const rows = 18
	for dy := range rows {
		half := dy * 2 / 3
		for dx := 0; dx <= half+1; dx++ {
			c := color.Color(colWhite)
			if dx == 0 || dx == half+1 || dy == rows-1 {
				c = colBlack
			}
			img.Set(p.X+dx, p.Y+dy, c)
		}
	}
}

// capturer は scene を画面として流す platform.Capturer。モニターは 2 枚（DISPLAY2 は小さい）。
type capturer struct {
	scene    *scene
	monitors []domain.Monitor
	bases    map[int]*image.RGBA
}

var _ platform.Capturer = (*capturer)(nil)

func newCapturer(s *scene) *capturer {
	c := &capturer{scene: s, bases: map[int]*image.RGBA{}, monitors: []domain.Monitor{
		{Index: 0, Name: "DISPLAY1", Primary: true, Size: domain.Size{W: 1280, H: 720}, DPI: 96},
		{Index: 1, Name: "DISPLAY2", Origin: domain.Point{X: 1280}, Size: domain.Size{W: 1024, H: 576}, DPI: 96},
	}}
	for _, m := range c.monitors {
		c.bases[m.Index] = wallpaper(m.Index, m.Size.W, m.Size.H)
	}
	return c
}

func (c *capturer) Monitors(context.Context) ([]domain.Monitor, error) {
	return append([]domain.Monitor(nil), c.monitors...), nil
}

func (c *capturer) base(monitor int) (*image.RGBA, error) {
	b, ok := c.bases[monitor]
	if !ok {
		return nil, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("unknown monitor %d", monitor))
	}
	return b, nil
}

func (c *capturer) Grab(_ context.Context, monitor int) (*image.RGBA, error) {
	b, err := c.base(monitor)
	if err != nil {
		return nil, err
	}
	return c.scene.render(b, monitor, time.Now()), nil
}

func (c *capturer) Watch(ctx context.Context, monitor int, fps int) (<-chan platform.Frame, <-chan error, error) {
	b, err := c.base(monitor)
	if err != nil {
		return nil, nil, err
	}
	if fps <= 0 {
		return nil, nil, domain.ErrInvalidArgument.WithMsg("fps must be positive")
	}
	frames := make(chan platform.Frame)
	errs := make(chan error)
	go func() {
		defer close(frames)
		defer close(errs)
		tick := time.NewTicker(time.Second / time.Duration(fps))
		defer tick.Stop()
		var seq uint32
		for {
			img := c.scene.render(b, monitor, time.Now())
			seq++
			select {
			case frames <- platform.Frame{Image: img, Dirty: []image.Rectangle{img.Bounds()}, Seq: seq}:
			case <-ctx.Done():
				return
			}
			select {
			case <-tick.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	return frames, errs, nil
}

// input は platformtest.FakeInput（呼び出しの記録）に、scene への反映とログ出力を足した偽の入力注入。
// ここへ届いた呼び出しが、ブラウザの操作がホストの入力注入まで届いた証拠になる。
type input struct {
	*platformtest.FakeInput
	scene *scene
	log   *slog.Logger
}

func (i *input) MoveTo(ctx context.Context, p domain.Point) error {
	if err := i.FakeInput.MoveTo(ctx, p); err != nil {
		return err
	}
	i.scene.moveTo(p, "")
	i.log.Info("input", "op", "MoveTo", "x", p.X, "y", p.Y)
	return nil
}

func (i *input) Click(ctx context.Context, b domain.Button, count int, mods []domain.Modifier) error {
	if err := i.FakeInput.Click(ctx, b, count, mods); err != nil {
		return err
	}
	cur, _ := i.CursorPos()
	i.scene.click(fmt.Sprintf("Click %s x%d at (%d,%d)", b, count, cur.X, cur.Y))
	i.log.Info("input", "op", "Click", "button", b, "count", count)
	return nil
}

func (i *input) Button(ctx context.Context, b domain.Button, a domain.ButtonAction) error {
	if err := i.FakeInput.Button(ctx, b, a); err != nil {
		return err
	}
	if a == domain.ButtonActionDown {
		cur, _ := i.CursorPos()
		i.scene.click(fmt.Sprintf("Button %s down at (%d,%d)", b, cur.X, cur.Y))
	}
	i.log.Info("input", "op", "Button", "button", b, "action", a)
	return nil
}

func (i *input) Key(ctx context.Context, k domain.KeyCombo) error {
	if err := i.FakeInput.Key(ctx, k); err != nil {
		return err
	}
	combo := k.Key
	if len(k.Mods) > 0 {
		mods := make([]string, len(k.Mods))
		for n, m := range k.Mods {
			mods[n] = string(m)
		}
		combo = strings.Join(mods, "+") + "+" + k.Key
	}
	i.scene.noteOnly("Key " + combo)
	i.log.Info("input", "op", "Key", "combo", combo)
	return nil
}

func (i *input) KeyEvent(ctx context.Context, e domain.KeyEvent) error {
	if err := i.FakeInput.KeyEvent(ctx, e); err != nil {
		return err
	}
	if e.Action == domain.KeyDown && len(e.Mods) == 0 {
		switch r := []rune(e.Key); {
		case e.Key == "Enter":
			i.scene.typeText("\n")
		case e.Key == "Backspace":
			i.scene.typeText("\b")
		case len(r) == 1:
			i.scene.typeText(e.Key)
		}
	}
	i.log.Info("input", "op", "KeyEvent", "action", e.Action, "code", e.Code, "key", e.Key, "mods", e.Mods)
	return nil
}

func (i *input) Text(ctx context.Context, s string) error {
	if err := i.FakeInput.Text(ctx, s); err != nil {
		return err
	}
	i.scene.typeText(s)
	i.scene.noteOnly(fmt.Sprintf("Text %q", s))
	i.log.Info("input", "op", "Text", "chars", len([]rune(s)))
	return nil
}

func (i *input) Wheel(ctx context.Context, at domain.Point, dx, dy float64) error {
	if err := i.FakeInput.Wheel(ctx, at, dx, dy); err != nil {
		return err
	}
	i.scene.noteOnly(fmt.Sprintf("Wheel dx=%.1f dy=%.1f", dx, dy))
	i.log.Info("input", "op", "Wheel", "dx", dx, "dy", dy)
	return nil
}
