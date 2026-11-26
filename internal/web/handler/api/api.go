// Package api はリモートルーターの JSON API（/api/v1。docs/api/endpoints.md §6）。
//
// 運用確認と将来の Lit アイランドのための参照専用 API で、GET の 3 経路（status・monitors・lease）と OpenAPI（openapi.json）だけを持つ。
// 状態を変える経路は設けない（操作は MCP と WebSocket のみ）。OpenAPI は Huma が組み立てる。Huma の既定の docs・schemas・YAML の
// 経路は有効にしない（docs は外部の CDN からスクリプトを読み、CSP に反する）。
//
// Host / Origin・キルスイッチ・WhoIs・許可リストの検査は、ミドルウェア連鎖（web/middleware）が済ませている。
// エラーは problem+json（§2）。内部の失敗の内容はログにだけ残し、応答には出さない。
// service と domain だけを使う（scripts/check-imports.sh）。
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
)

const (
	// Prefix は JSON API のパスの接頭辞。
	Prefix = "/api/v1"

	stateIdle            = "idle"
	statePendingApproval = "pending_approval"
	stateHeld            = "held"
)

// AgentConfig はエージェント設定の読み取り口（service.AgentSettingsService が満たす）。
type AgentConfig interface {
	Config(ctx context.Context) (service.AgentConfig, error)
}

// LeaseState はリースの状態の読み取り口（service.LeaseManager の一部）。
type LeaseState interface {
	Holder() (service.Holder, bool)
	Pending() (service.PendingLease, bool)
}

// Monitors はモニター情報の読み取り口（service.Control の一部）。
type Monitors interface {
	Monitors(ctx context.Context) ([]domain.Monitor, error)
	MaxDimension(ctx context.Context) (int, error)
}

// ViewerActors は X-Viewer-Id を検証してビューアの操作主体を返す（service.ViewerSessions の一部）。
type ViewerActors interface {
	Actor(viewerID string, client domain.Client) (domain.Actor, error)
}

// Deps は Handler の依存先。すべて必須（Logger を除く）。
type Deps struct {
	// Version はアプリの版。
	Version    string
	Status     service.ServerStatusSource
	KillSwitch service.KillSwitchState
	Agent      AgentConfig
	Monitors   Monitors
	Leases     LeaseState
	Viewers    ViewerActors
	Logger     *slog.Logger // nil なら slog.Default()
}

// Handler は /api/v1 の http.Handler。
type Handler struct {
	d   Deps
	log *slog.Logger
	mux *http.ServeMux
}

// New は Handler を作る。
func New(d Deps) (*Handler, error) {
	if d.Status == nil || d.KillSwitch == nil || d.Agent == nil || d.Monitors == nil || d.Leases == nil || d.Viewers == nil {
		return nil, errors.New("api: the JSON API requires the server status, kill switch, agent config, monitors, lease state and viewer sessions")
	}
	h := &Handler{d: d, log: d.Logger, mux: http.NewServeMux()}
	if h.log == nil {
		h.log = slog.Default()
	}
	registry := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	api := humago.New(h.mux, huma.Config{
		OpenAPI: &huma.OpenAPI{
			OpenAPI:    "3.1.0",
			Info:       &huma.Info{Title: "puppet-remo", Version: d.Version, Description: "Read-only JSON API of the remote router."},
			Components: &huma.Components{Schemas: registry},
		},
		Formats:       map[string]huma.Format{"application/json": huma.DefaultJSONFormat, "json": huma.DefaultJSONFormat},
		DefaultFormat: "application/json",
	})
	problemSchema := registry.Schema(reflect.TypeFor[problem](), true, "Problem")
	register := func(op huma.Operation) huma.Operation {
		op.Method = http.MethodGet
		op.Responses = map[string]*huma.Response{"default": {
			Description: "Error (RFC 9457 problem+json)",
			Content:     map[string]*huma.MediaType{"application/problem+json": {Schema: problemSchema}},
		}}
		return op
	}
	huma.Register(api, register(huma.Operation{
		OperationID: "get-status", Path: Prefix + "/status", Summary: "Server status",
	}), h.status)
	huma.Register(api, register(huma.Operation{
		OperationID: "get-monitors", Path: Prefix + "/monitors", Summary: "Monitors",
		Description: "The same entries as `monitors` of the MCP tool `get_screen_info`.",
	}), h.monitors)
	huma.Register(api, register(huma.Operation{
		OperationID: "get-lease", Path: Prefix + "/lease", Summary: "Control lease state",
		Description: "The same body as the MCP tool `get_control_status`. `mine` is meaningful only when the caller passes its `viewer_id` in `X-Viewer-Id`.",
	}), h.lease)

	// 仕様の経路は openapi.json だけ。Huma の OpenAPIPath は .yaml・-3.0 も作るので使わない。
	spec, err := json.Marshal(api.OpenAPI())
	if err != nil {
		return nil, err
	}
	h.mux.HandleFunc("GET "+Prefix+"/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.oai.openapi+json")
		if _, err := w.Write(spec); err != nil {
			h.log.Debug("write openapi response failed", "error", err)
		}
	})
	return h, nil
}

// ServeHTTP は Handler を http.Handler にする。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// problem は problem+json の応答（API 仕様 §2）。huma.StatusError として返す。
type problem struct {
	Type   string      `json:"type" doc:"urn:puppet-remo:error:<code>"`
	Title  string      `json:"title"`
	Status int         `json:"status"`
	Detail string      `json:"detail"`
	Code   domain.Code `json:"code" doc:"Machine-readable error code (API spec §2)"`
}

func (p *problem) GetStatus() int            { return p.Status }
func (p *problem) Error() string             { return p.Title }
func (*problem) ContentType(_ string) string { return "application/problem+json" }
func newProblem(code domain.Code, title, detail string) *problem {
	return &problem{Type: "urn:puppet-remo:error:" + string(code), Title: title, Status: code.HTTPStatus(), Detail: detail, Code: code}
}

// fail は err を problem にする。ドメインエラーはそのコードとメッセージ、それ以外は internal（内容はログにだけ残す）。
func (h *Handler) fail(op string, err error) error {
	var de *domain.Error
	if errors.As(err, &de) && de.Code != domain.CodeInternal {
		msg := de.Msg
		if msg == "" {
			msg = string(de.Code)
		}
		return newProblem(de.Code, msg, msg)
	}
	h.log.Error("json api failed", "op", op, "error", err)
	return newProblem(domain.CodeInternal, "Internal error", "An internal error occurred.")
}

// --- GET /api/v1/status ---

type killSwitchBody struct {
	Active bool `json:"active"`
}

type statusBody struct {
	Version      string         `json:"version"`
	TailnetIP    string         `json:"tailnet_ip" doc:"The first tailnet IP of this host (empty when tailscaled has none)."`
	Port         int            `json:"port"`
	KillSwitch   killSwitchBody `json:"killswitch"`
	AgentEnabled bool           `json:"agent_enabled"`
}

type statusOutput struct{ Body statusBody }

func (h *Handler) status(ctx context.Context, _ *struct{}) (*statusOutput, error) {
	cfg, err := h.d.Agent.Config(ctx)
	if err != nil {
		return nil, h.fail("status", err)
	}
	st := h.d.Status.ServerStatus(ctx)
	body := statusBody{
		Version:      h.d.Version,
		Port:         st.Port,
		KillSwitch:   killSwitchBody{Active: h.d.KillSwitch.Active()},
		AgentEnabled: cfg.Enabled,
	}
	if len(st.TailnetIPs) > 0 {
		body.TailnetIP = st.TailnetIPs[0]
	}
	return &statusOutput{Body: body}, nil
}

// --- GET /api/v1/monitors ---

type sizeBody struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type pointBody struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// monitorBody は MCP の get_screen_info の monitors[] の 1 台と同じ形。
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

type monitorsOutput struct{ Body []monitorBody }

func (h *Handler) monitors(ctx context.Context, _ *struct{}) (*monitorsOutput, error) {
	mons, err := h.d.Monitors.Monitors(ctx)
	if err != nil {
		return nil, h.fail("monitors", err)
	}
	maxDim, err := h.d.Monitors.MaxDimension(ctx)
	if err != nil {
		return nil, h.fail("monitors", err)
	}
	out := make([]monitorBody, 0, len(mons))
	for _, m := range mons {
		scaled := m.ScaledSize(maxDim)
		out = append(out, monitorBody{
			Index:    m.Index,
			Name:     m.Name,
			Primary:  m.Primary,
			Origin:   pointBody{X: m.Origin.X, Y: m.Origin.Y},
			Physical: sizeBody{Width: m.Size.W, Height: m.Size.H},
			DPIScale: m.DPIScale(),
			Scale:    m.Scale(maxDim),
			Scaled:   sizeBody{Width: scaled.W, Height: scaled.H},
		})
	}
	return &monitorsOutput{Body: out}, nil
}

// --- GET /api/v1/lease ---

// holderBody はリースの保持者。task_id は保持者が agent のときだけ。
type holderBody struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	TaskID string `json:"task_id,omitempty"`
}

// leaseBody は MCP の get_control_status の出力と同じ形。
// holder は held のときだけ。mine は held なら保持者が呼び出し元、pending_approval なら承認待ちの要求者が呼び出し元か。
type leaseBody struct {
	State            string      `json:"state" enum:"idle,pending_approval,held"`
	Holder           *holderBody `json:"holder,omitempty"`
	Mine             bool        `json:"mine"`
	IdleRemainingSec *int        `json:"idle_remaining_sec,omitempty"`
}

type leaseInput struct {
	ViewerID string `header:"X-Viewer-Id" doc:"viewer_id of the caller's viewer session (see /viewer). Needed only for mine."`
}

type leaseOutput struct{ Body leaseBody }

func (h *Handler) lease(ctx context.Context, in *leaseInput) (*leaseOutput, error) {
	var who domain.Actor // 呼び出し元を特定できなければ、どの保持者とも一致しない（mine は false）
	if in.ViewerID != "" {
		client, ok := middleware.ClientFrom(ctx)
		if !ok {
			return nil, newProblem(domain.CodeWhoisFailed, "Device identification failed", "The source address could not be identified in the tailnet.")
		}
		var err error
		if who, err = h.d.Viewers.Actor(in.ViewerID, client); err != nil {
			return nil, h.fail("lease", err)
		}
	}
	return &leaseOutput{Body: h.leaseFor(who)}, nil
}

func (h *Handler) leaseFor(who domain.Actor) leaseBody {
	holder, ok := h.d.Leases.Holder()
	if !ok {
		if p, pending := h.d.Leases.Pending(); pending {
			return leaseBody{State: statePendingApproval, Mine: p.Actor.SameSession(who)}
		}
		return leaseBody{State: stateIdle}
	}
	sec := max(0, int(math.Ceil(holder.IdleRemaining.Seconds())))
	b := &holderBody{Kind: string(holder.Actor.Kind), Label: holder.Actor.Label}
	if holder.Actor.Kind == domain.ActorAgent {
		b.TaskID = holder.Actor.SessionID
	}
	return leaseBody{State: stateHeld, Holder: b, Mine: holder.Actor.SameSession(who), IdleRemainingSec: &sec}
}
