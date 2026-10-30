package mcp

import (
	"context"
	"slices"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

type windowBody struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Process    string   `json:"process_name"`
	PID        uint32   `json:"pid"`
	Rect       rectBody `json:"rect"`
	Monitor    int      `json:"monitor"`
	Foreground bool     `json:"foreground"`
	Minimized  bool     `json:"minimized"`
	Elevated   bool     `json:"elevated"`
}

type windowsBody struct {
	Windows []windowBody `json:"windows"`
}

type focusWindowIn struct {
	ID string `json:"id" jsonschema:"window id from list_windows"`
}

type clipboardWriteIn struct {
	Text string `json:"text" jsonschema:"text to put on the clipboard, up to 1 MiB"`
}

type clipboardBody struct {
	Text string `json:"text"`
}

type uiTreeIn struct {
	WindowID string `json:"window_id,omitempty" jsonschema:"window id from list_windows (default: the foreground window)"`
	MaxDepth int    `json:"max_depth,omitempty" jsonschema:"maximum tree depth (default 5)"`
	MaxNodes int    `json:"max_nodes,omitempty" jsonschema:"maximum number of nodes (default 500)"`
}

type uiNodeBody struct {
	ID            string       `json:"id"`
	Role          string       `json:"role"`
	Name          string       `json:"name"`
	Value         string       `json:"value,omitempty"`
	ValueRedacted bool         `json:"value_redacted,omitempty"`
	Rect          rectBody     `json:"rect"`
	Enabled       bool         `json:"enabled"`
	Children      []uiNodeBody `json:"children,omitempty"`
}

type uiTreeBody struct {
	WindowID  string     `json:"window_id"`
	Truncated bool       `json:"truncated"`
	Root      uiNodeBody `json:"root"`
}

// addWindowTools はウィンドウ・クリップボード・UI ツリーのツールを登録する: FR-M11〜M14。
func (s *Server) addWindowTools() {
	addTool(s, "list_windows",
		"List the top-level windows. rect is in the scaled coordinates of the monitor the window overlaps most, "+
			"clipped to that monitor. Input into elevated windows is not possible.",
		true, func(ctx context.Context, _ domain.Actor, _ noArgs) (toolResult, error) {
			wins, err := s.control.Windows(ctx)
			if err != nil {
				return toolResult{}, err
			}
			conv, err := s.converter(ctx)
			if err != nil {
				return toolResult{}, err
			}
			out := windowsBody{Windows: make([]windowBody, 0, len(wins))}
			for _, w := range wins {
				out.Windows = append(out.Windows, windowBody{
					ID:         w.ID,
					Title:      w.Title,
					Process:    w.Process,
					PID:        w.PID,
					Rect:       conv.rect(w.Monitor, w.Rect),
					Monitor:    w.Monitor,
					Foreground: w.Foreground,
					Minimized:  w.Minimized,
					Elevated:   w.Elevated,
				})
			}
			return toolResult{Structured: out}, nil
		})

	addTool(s, "focus_window", "Bring a window to the foreground. Needs the control lease (acquired implicitly).",
		false, func(ctx context.Context, who domain.Actor, in focusWindowIn) (toolResult, error) {
			return s.do(ctx, who, domain.FocusWindow{ID: in.ID})
		})

	addTool(s, "clipboard_read",
		"Read the clipboard text (up to 1 MiB; empty when the clipboard holds no text). Needs the control lease (acquired implicitly).",
		true, func(ctx context.Context, who domain.Actor, _ noArgs) (toolResult, error) {
			res, err := s.control.Do(ctx, who, domain.ClipboardRead{})
			if err != nil {
				return toolResult{}, err
			}
			return toolResult{Structured: clipboardBody{Text: res.Text}}, nil
		})

	addTool(s, "clipboard_write", "Write text to the clipboard (up to 1 MiB). Needs the control lease (acquired implicitly).",
		false, func(ctx context.Context, who domain.Actor, in clipboardWriteIn) (toolResult, error) {
			return s.do(ctx, who, domain.ClipboardWrite{Text: in.Text})
		})

	addTool(s, "get_ui_tree",
		"Read the UI Automation tree of a window. rect is in scaled coordinates; password fields have value_redacted instead of value. "+
			"Needs the control lease (acquired implicitly).",
		true, func(ctx context.Context, who domain.Actor, in uiTreeIn) (toolResult, error) {
			res, err := s.control.Do(ctx, who, domain.GetUITree{WindowID: in.WindowID, MaxDepth: in.MaxDepth, MaxNodes: in.MaxNodes})
			if err != nil {
				return toolResult{}, err
			}
			if res.Tree == nil {
				return toolResult{}, domain.ErrWindowNotFound
			}
			conv, err := s.converter(ctx)
			if err != nil {
				return toolResult{}, err
			}
			win := s.treeWindow(ctx, in.WindowID)
			monitor := conv.monitorOf(win, res.Tree.Rect)
			root, truncated := conv.node(monitor, *res.Tree)
			id := in.WindowID
			if id == "" {
				id = win.ID
			}
			return toolResult{Structured: uiTreeBody{WindowID: id, Truncated: truncated, Root: root}}, nil
		})
}

// treeWindow は get_ui_tree の対象ウィンドウを返す（指定の ID、省略時は前面）。見つからなければゼロ値。
func (s *Server) treeWindow(ctx context.Context, id string) domain.WindowInfo {
	wins, err := s.control.Windows(ctx)
	if err != nil {
		return domain.WindowInfo{}
	}
	i := slices.IndexFunc(wins, func(w domain.WindowInfo) bool {
		if id != "" {
			return w.ID == id
		}
		return w.Foreground
	})
	if i < 0 {
		return domain.WindowInfo{}
	}
	return wins[i]
}

// converter は物理座標を縮小座標へ直す。モニター一覧と縮小の最大辺はツール 1 回の間で固定する。
type converter struct {
	monitors []domain.Monitor
	maxDim   int
}

func (s *Server) converter(ctx context.Context) (converter, error) {
	mons, err := s.control.Monitors(ctx)
	if err != nil {
		return converter{}, err
	}
	maxDim, err := s.control.MaxDimension(ctx)
	if err != nil {
		return converter{}, err
	}
	return converter{monitors: mons, maxDim: maxDim}, nil
}

func (c converter) monitor(index int) (domain.Monitor, bool) {
	i := slices.IndexFunc(c.monitors, func(m domain.Monitor) bool { return m.Index == index })
	if i < 0 {
		return domain.Monitor{}, false
	}
	return c.monitors[i], true
}

// monitorOf は UI ツリーの座標を縮小するモニターの Index を返す。ウィンドウが分かればそのモニター、
// 分からなければ矩形の中心を含むモニター、それもなければプライマリ（なければ先頭）。
func (c converter) monitorOf(win domain.WindowInfo, root domain.Rect) int {
	if win.ID != "" {
		if _, ok := c.monitor(win.Monitor); ok {
			return win.Monitor
		}
	}
	if m, ok := domain.MonitorAt(c.monitors, domain.Point{X: root.X + root.W/2, Y: root.Y + root.H/2}); ok {
		return m.Index
	}
	for _, m := range c.monitors {
		if m.Primary {
			return m.Index
		}
	}
	if len(c.monitors) > 0 {
		return c.monitors[0].Index
	}
	return domain.MonitorPrimary
}

// rect は物理座標の矩形を、モニターの範囲に切り詰めたうえで縮小座標にする。
// 範囲外（モニターが不明・最小化などで重ならない）なら空の矩形。
func (c converter) rect(monitor int, r domain.Rect) rectBody {
	m, ok := c.monitor(monitor)
	if !ok {
		return rectBody{}
	}
	x0, y0 := max(r.X, m.Origin.X), max(r.Y, m.Origin.Y)
	x1, y1 := min(r.X+r.W, m.Origin.X+m.Size.W), min(r.Y+r.H, m.Origin.Y+m.Size.H)
	if x1 <= x0 || y1 <= y0 {
		return rectBody{}
	}
	// 右下は排他的な端なので、最後のピクセルを縮小してから 1 を足す（範囲内の点は縮小画像の内側に収まる）。
	tl := m.PhysicalToScaled(domain.Point{X: x0, Y: y0}, c.maxDim)
	br := m.PhysicalToScaled(domain.Point{X: x1 - 1, Y: y1 - 1}, c.maxDim)
	return rectBody{X: tl.X, Y: tl.Y, Width: br.X - tl.X + 1, Height: br.Y - tl.Y + 1}
}

// node は UI ツリーを縮小座標にし、どこかの節点が打ち切られていたかを返す。
func (c converter) node(monitor int, n domain.UINode) (uiNodeBody, bool) {
	out := uiNodeBody{
		ID:            n.ID,
		Role:          n.Role,
		Name:          n.Name,
		ValueRedacted: n.ValueRedacted,
		Rect:          c.rect(monitor, n.Rect),
		Enabled:       n.Enabled,
	}
	if !n.ValueRedacted {
		out.Value = n.Value
	}
	truncated := n.Truncated
	if len(n.Children) > 0 {
		out.Children = make([]uiNodeBody, 0, len(n.Children))
		for _, k := range n.Children {
			child, t := c.node(monitor, k)
			out.Children = append(out.Children, child)
			truncated = truncated || t
		}
	}
	return out, truncated
}
