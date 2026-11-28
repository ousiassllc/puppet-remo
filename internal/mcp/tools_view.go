package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
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

func rectOf(r domain.Rect) rectBody { return rectBody{X: r.X, Y: r.Y, Width: r.W, Height: r.H} }

// ownWindowBody は puppet-remo 自身のウィンドウが占める範囲（NFR-S06）。ウィンドウはスクリーンショットに写らず、入力は拒否される。
// rect は monitor の縮小スクリーンショットの座標（AI が使う座標系。物理座標ではない）。名前・ID などは含まない。
type ownWindowBody struct {
	Monitor int      `json:"monitor"`
	Rect    rectBody `json:"rect"`
}

// ownWindowBodies は regions を出力の形にする。空でも nil にせず、JSON では [] になる（「無い」ことを示す）。
func ownWindowBodies(regions []domain.OwnRegion) []ownWindowBody {
	out := make([]ownWindowBody, 0, len(regions))
	for _, r := range regions {
		out = append(out, ownWindowBody{Monitor: r.Monitor, Rect: rectOf(r.Rect)})
	}
	return out
}

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
	// OwnWindows は puppet-remo のウィンドウの、このスクリーンショット上の範囲。ウィンドウは画像に写らないが、入力は拒否される。
	OwnWindows []ownWindowBody `json:"own_windows"`

	// 以下は window_id を指定した撮影（ウィンドウ単位）のときだけ入る。省略時は従来の出力と同じ（省略される）。
	// WindowID は撮ったウィンドウ。Monitor は座標系の基準のモニター（対象が最も重なるモニター。Scale もこのモニターの縮小率）。
	WindowID string `json:"window_id,omitempty"`
	// Rect は画像の左上と大きさを Monitor の縮小スクリーンショットの座標で表したもの。画像の点 (px, py) は縮小座標 (rect.x + px, rect.y + py)。
	Rect *rectBody `json:"rect,omitempty"`
	// Windows は画像に写っているウィンドウ（対象を含む。前面が先）。rect は Rect と同じ座標系。
	Windows []shotWindowBody `json:"windows,omitempty"`
	// Omitted は include_owned で、画像に入れなかった関連ウィンドウ（別プロセス・所有関係を確かめられない・撮れなかった）。
	Omitted []shotOmittedBody `json:"omitted,omitempty"`
}

// shotWindowBody は window_id つきの screenshot の画像に写っているウィンドウ 1 つ。
type shotWindowBody struct {
	ID       string   `json:"id"`
	Relation string   `json:"relation"` // target / owned / child / menu
	Rect     rectBody `json:"rect"`
}

// shotOmittedBody は include_owned で画像に入れなかった関連ウィンドウ。タイトルは含めない。
type shotOmittedBody struct {
	ID     string `json:"id"`
	Reason string `json:"reason"` // related_cross_process / related_unverifiable / capture_member_unavailable
}

type cursorBody struct {
	X       int `json:"x"`
	Y       int `json:"y"`
	Monitor int `json:"monitor"`
}

type screenshotIn struct {
	Monitor      *int   `json:"monitor,omitempty" jsonschema:"monitor index from get_screen_info (default: the primary monitor); cannot be combined with window_id"`
	Format       string `json:"format,omitempty" jsonschema:"image format: png (default) or jpeg"`
	WindowID     string `json:"window_id,omitempty" jsonschema:"window id from list_windows: capture only that window (without bringing it to the foreground). Default: the whole monitor"`
	IncludeOwned bool   `json:"include_owned,omitempty" jsonschema:"with window_id: also compose the window's dialogs, menus and child windows of the same process into the image (default false)"`
}

// screenshotArgs は screenshot の監査用の引数の要約。
func screenshotArgs(in screenshotIn) map[string]any {
	args := map[string]any{}
	if in.Monitor != nil {
		args["monitor"] = *in.Monitor
	}
	if in.Format != "" {
		args["format"] = in.Format
	}
	if in.WindowID != "" {
		args["window_id"] = in.WindowID
	}
	if in.IncludeOwned {
		args["include_owned"] = true
	}
	return args
}

// addViewTools は閲覧系のツール（リース不要）を登録する: FR-M01〜M03。
func (s *Server) addViewTools() {
	addAuditedTool(s, "get_screen_info",
		"List the monitors: index, name, whether it is primary, origin and physical size, DPI scale, "+
			"and the screenshot scale and scaled size. All coordinates in other tools are in the scaled size.",
		true, noArgsSummary, func(ctx context.Context, _ domain.Actor, _ noArgs) (toolResult, error) {
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

	addAuditedTool(s, "screenshot",
		"Take a screenshot of a monitor. The image is scaled down so that its long side fits the configured maximum; "+
			"coordinates for the other tools are pixels of this scaled image. "+
			"puppet-remo's own windows are never captured, so the image shows what lies behind them, but input there is blocked "+
			"(input_blocked, reason own_window); own_windows in the result lists where they are, as rects in this image's pixels. "+
			"With window_id (from list_windows) only that window is captured, even if other windows cover it, and it is not brought to the foreground: "+
			"the result's rect is where the image's top-left lies in the scaled coordinates of its monitor, so a point (px, py) of the image is (rect.x + px, rect.y + py) "+
			"for the click tools (a window partly off the monitor has coordinates outside the monitor's screenshot). "+
			"include_owned also composes the window's dialogs, menus and child windows of the same process (windows lists what is in the image, "+
			"omitted what was left out and why). A minimized, elevated, or puppet-remo window cannot be captured (input_blocked with a reason, "+
			"or capture_unavailable); an unknown window_id is window_not_found. The whole monitor is never captured instead.",
		true, screenshotArgs, func(ctx context.Context, _ domain.Actor, in screenshotIn) (toolResult, error) {
			if in.WindowID != "" {
				return s.windowScreenshot(ctx, in)
			}
			if in.IncludeOwned {
				return toolResult{}, domain.ErrInvalidArgument.WithMsg("include_owned needs window_id")
			}
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

				OwnWindows: ownWindowBodies(shot.OwnWindows),
			}
			return screenshotResult(meta, shot.Data, shot.MIME)
		})

	addAuditedTool(s, "cursor_position",
		"Get the mouse cursor position in the scaled coordinates of the monitor it is on.",
		true, noArgsSummary, func(ctx context.Context, _ domain.Actor, _ noArgs) (toolResult, error) {
			c, err := s.control.CursorPosition(ctx)
			if err != nil {
				return toolResult{}, err
			}
			return toolResult{Structured: cursorBody{X: c.Point.X, Y: c.Point.Y, Monitor: c.Monitor}}, nil
		})
}

// screenshotResult は meta と画像を、画像コンテンツとメタ情報の JSON のテキストコンテンツで返す。
func screenshotResult(meta screenshotBody, data []byte, mime string) (toolResult, error) {
	text, err := json.Marshal(meta)
	if err != nil {
		return toolResult{}, fmt.Errorf("encode screenshot meta: %w", err)
	}
	return toolResult{
		Structured: meta,
		Content: []mcpsdk.Content{
			&mcpsdk.ImageContent{Data: data, MIMEType: mime},
			&mcpsdk.TextContent{Text: string(text)},
		},
	}, nil
}

// windowScreenshot は window_id つきの screenshot（ウィンドウ単位。FR-M26）。monitor とは同時に指定できない
// （座標系の基準のモニターは対象のウィンドウが最も重なるモニターで、サーバーが決める）。
func (s *Server) windowScreenshot(ctx context.Context, in screenshotIn) (toolResult, error) {
	if in.Monitor != nil {
		return toolResult{}, domain.ErrInvalidArgument.WithMsg("monitor cannot be combined with window_id")
	}
	shot, err := s.control.WindowScreenshot(ctx, service.WindowShotRequest{WindowID: in.WindowID, Format: in.Format, IncludeOwned: in.IncludeOwned})
	if err != nil {
		return toolResult{}, err
	}
	rect := rectOf(shot.Rect)
	meta := screenshotBody{
		Monitor:    shot.Monitor,
		Physical:   sizeOf(shot.Physical),
		Scaled:     sizeOf(shot.Scaled),
		Scale:      shot.Scale,
		OwnWindows: ownWindowBodies(shot.OwnWindows),
		WindowID:   shot.WindowID,
		Rect:       &rect,
		Windows:    make([]shotWindowBody, 0, len(shot.Members)),
	}
	for _, m := range shot.Members {
		meta.Windows = append(meta.Windows, shotWindowBody{ID: m.WindowID, Relation: string(m.Relation), Rect: rectOf(m.Rect)})
	}
	for _, o := range shot.Omitted {
		meta.Omitted = append(meta.Omitted, shotOmittedBody{ID: o.WindowID, Reason: string(o.Reason)})
	}
	return screenshotResult(meta, shot.Data, shot.MIME)
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
