package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// noArgs は引数のないツールの入力。
type noArgs struct{}

type sizeBody struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type pointBody struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type rectBody struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func sizeOf(s domain.Size) sizeBody { return sizeBody{Width: s.W, Height: s.H} }

// monitorBody は get_screen_info のモニター 1 台（API 仕様 §3.3）。
type monitorBody struct {
	Index    int       `json:"index"`
	Name     string    `json:"name"`
	Primary  bool      `json:"primary"`
	Origin   pointBody `json:"origin"`
	Physical sizeBody  `json:"physical"`
	DPIScale float64   `json:"dpi_scale"`
	Scale    float64   `json:"scale"`
	Scaled   sizeBody  `json:"scaled"`
}

type screenInfoBody struct {
	Monitors []monitorBody `json:"monitors"`
}

// screenshotBody は screenshot のメタ情報（画像コンテンツと並べて返す）。
type screenshotBody struct {
	Monitor  int      `json:"monitor"`
	Physical sizeBody `json:"physical"`
	Scaled   sizeBody `json:"scaled"`
	Scale    float64  `json:"scale"`
}

type cursorBody struct {
	X       int `json:"x"`
	Y       int `json:"y"`
	Monitor int `json:"monitor"`
}

type screenshotIn struct {
	Monitor *int   `json:"monitor,omitempty" jsonschema:"monitor index from get_screen_info (default: the primary monitor)"`
	Format  string `json:"format,omitempty" jsonschema:"image format: png (default) or jpeg"`
}

// addViewTools は閲覧系のツール（リース不要）を登録する: FR-M01〜M03。
func (s *Server) addViewTools() {
	addTool(s, "get_screen_info",
		"List the monitors: index, name, whether it is primary, origin and physical size, DPI scale, "+
			"and the screenshot scale and scaled size. All coordinates in other tools are in the scaled size.",
		true, func(ctx context.Context, _ domain.Actor, _ noArgs) (toolResult, error) {
			mons, err := s.control.Monitors(ctx)
			if err != nil {
				return toolResult{}, err
			}
			maxDim, err := s.control.MaxDimension(ctx)
			if err != nil {
				return toolResult{}, err
			}
			out := screenInfoBody{Monitors: make([]monitorBody, 0, len(mons))}
			for _, m := range mons {
				out.Monitors = append(out.Monitors, monitorBody{
					Index:    m.Index,
					Name:     m.Name,
					Primary:  m.Primary,
					Origin:   pointBody{X: m.Origin.X, Y: m.Origin.Y},
					Physical: sizeOf(m.Size),
					DPIScale: m.DPIScale(),
					Scale:    m.Scale(maxDim),
					Scaled:   sizeOf(m.ScaledSize(maxDim)),
				})
			}
			return toolResult{Structured: out}, nil
		})

	addTool(s, "screenshot",
		"Take a screenshot of a monitor. The image is scaled down so that its long side fits the configured maximum; "+
			"coordinates for the other tools are pixels of this scaled image.",
		true, func(ctx context.Context, _ domain.Actor, in screenshotIn) (toolResult, error) {
			monitor, err := monitorArg(in.Monitor)
			if err != nil {
				return toolResult{}, err
			}
			shot, err := s.control.Screenshot(ctx, monitor, in.Format)
			if err != nil {
				return toolResult{}, err
			}
			meta := screenshotBody{
				Monitor:  shot.Monitor,
				Physical: sizeOf(shot.Physical),
				Scaled:   sizeOf(shot.Scaled),
				Scale:    shot.Scale,
			}
			text, err := json.Marshal(meta)
			if err != nil {
				return toolResult{}, fmt.Errorf("encode screenshot meta: %w", err)
			}
			return toolResult{
				Structured: meta,
				Content: []mcpsdk.Content{
					&mcpsdk.ImageContent{Data: shot.Data, MIMEType: shot.MIME},
					&mcpsdk.TextContent{Text: string(text)},
				},
			}, nil
		})

	addTool(s, "cursor_position",
		"Get the mouse cursor position in the scaled coordinates of the monitor it is on.",
		true, func(ctx context.Context, _ domain.Actor, _ noArgs) (toolResult, error) {
			c, err := s.control.CursorPosition(ctx)
			if err != nil {
				return toolResult{}, err
			}
			return toolResult{Structured: cursorBody{X: c.Point.X, Y: c.Point.Y, Monitor: c.Monitor}}, nil
		})
}

// monitorArg は monitor 引数を Op の Monitor にする。省略はプライマリ。
func monitorArg(m *int) (int, error) {
	if m == nil {
		return domain.MonitorPrimary, nil
	}
	if *m < 0 {
		return 0, domain.ErrInvalidArgument.WithMsg("monitor must not be negative")
	}
	return *m, nil
}
