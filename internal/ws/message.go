package ws

import (
	"encoding/json"
	"errors"
	"math"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// このファイルは WebSocket のテキストフレーム（JSON）の符号化・復号（docs/api/endpoints.md §5.3）。

// サーバー → クライアントのメッセージの type。
const (
	typeReady      = "ready"
	typeConfig     = "config"
	typeLease      = "lease"
	typeKillSwitch = "killswitch"
	typeError      = "error"
	typePong       = "pong"
)

// クライアント → サーバーのメッセージの type。
const (
	typeClientConfig = "config"
	typeInputMouse   = "input.mouse"
	typeInputKey     = "input.key"
	typeInputText    = "input.text"
	typeClientPing   = "ping"
)

// maxWheelNotches は 1 メッセージの wheel の dx / dy の絶対値の上限（ノッチ。API 仕様 §5.3）。超過は閉じコード 4400。
const maxWheelNotches = 50

// errInvalidMessage は、クライアントのメッセージを解釈できない（形式・値の範囲）。閉じコード 4400 にする。
var errInvalidMessage = errors.New("ws: invalid message")

type sizeBody struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type pointBody struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// monitorBody は ready の monitors[] の 1 台。MCP の get_screen_info と同じ項目から、縮小に関するもの（scale・scaled）を除いた形
// （ストリームの座標は縮小しない物理ピクセル）。
type monitorBody struct {
	Index    int       `json:"index"`
	Name     string    `json:"name"`
	Primary  bool      `json:"primary"`
	Origin   pointBody `json:"origin"`
	Physical sizeBody  `json:"physical"`
	DPIScale float64   `json:"dpi_scale"`
}

func monitorBodies(mons []domain.Monitor) []monitorBody {
	out := make([]monitorBody, 0, len(mons))
	for _, m := range mons {
		out = append(out, monitorBody{
			Index: m.Index, Name: m.Name, Primary: m.Primary,
			Origin:   pointBody{X: m.Origin.X, Y: m.Origin.Y},
			Physical: sizeBody{Width: m.Size.W, Height: m.Size.H},
			DPIScale: m.DPIScale(),
		})
	}
	return out
}

type holderBody struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// leaseBody は ready の lease と、lease メッセージの本体。holder は idle のとき省く。
type leaseBody struct {
	State  string      `json:"state"` // idle | held（pending_approval は M3）
	Holder *holderBody `json:"holder,omitempty"`
	Mine   bool        `json:"mine"`
}

type readyMessage struct {
	Type     string        `json:"type"`
	Monitors []monitorBody `json:"monitors"`
	Monitor  int           `json:"monitor"`
	Width    int           `json:"width"`
	Height   int           `json:"height"`
	Quality  int           `json:"quality"`
	FPS      int           `json:"fps"`
	Lease    leaseBody     `json:"lease"`
}

type configMessage struct {
	Type    string `json:"type"`
	Monitor int    `json:"monitor"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Quality int    `json:"quality"`
	FPS     int    `json:"fps"`
}

type leaseMessage struct {
	Type string `json:"type"`
	leaseBody
}

type killSwitchMessage struct {
	Type   string `json:"type"`
	Active bool   `json:"active"`
}

type errorMessage struct {
	Type    string      `json:"type"`
	Code    domain.Code `json:"code"`
	Message string      `json:"message"`
}

type typeOnly struct {
	Type string `json:"type"`
}

// errorFor は入力の拒否（Control.Do のエラー）を error メッセージにする。ドメインエラー以外（想定外）は internal にし、
// 詳細は出さない（API 仕様 §2）。
func errorFor(err error) errorMessage {
	code := domain.CodeOf(err)
	msg := "internal error"
	if code != domain.CodeInternal {
		msg = string(code)
		var de *domain.Error
		if errors.As(err, &de) && de.Msg != "" {
			msg = de.Msg
		}
	}
	return errorMessage{Type: typeError, Code: code, Message: msg}
}

// clientMessage はクライアント → サーバーのメッセージの和集合。型ごとに使う項目だけを検査する。
// 値が必須のものは、無い（null）と 0 を区別するためにポインタで持つ。
type clientMessage struct {
	Type string `json:"type"`

	// config
	Monitor *int `json:"monitor"`
	Quality *int `json:"quality"`
	FPS     *int `json:"fps"`

	// input.mouse
	Action string   `json:"action"`
	X      *int     `json:"x"`
	Y      *int     `json:"y"`
	Button string   `json:"button"`
	DX     *float64 `json:"dx"`
	DY     *float64 `json:"dy"`

	// input.key（Action も使う）
	Code      string   `json:"code"`
	Key       string   `json:"key"`
	Modifiers []string `json:"modifiers"`

	// input.text
	Text *string `json:"text"`
}

// decodeClientMessage は text フレームの本文を復号する。JSON として読めない・type が無い場合は errInvalidMessage。
func decodeClientMessage(data []byte) (clientMessage, error) {
	var m clientMessage
	if err := json.Unmarshal(data, &m); err != nil || m.Type == "" {
		return clientMessage{}, errInvalidMessage
	}
	return m, nil
}

// viewerConfig は config メッセージを service.ViewerConfig にする。monitor を省いたときは今のモニター（current）のまま。
// quality（1〜100）・fps（1〜30）の範囲外は errInvalidMessage（閉じコード 4400）。モニターの範囲は Configure が検査する。
func (m clientMessage) viewerConfig(current service.ViewerConfig) (service.ViewerConfig, error) {
	c := service.ViewerConfig{Monitor: current.Monitor}
	if m.Monitor != nil {
		if *m.Monitor < 0 {
			return service.ViewerConfig{}, errInvalidMessage
		}
		c.Monitor = *m.Monitor
	}
	if m.Quality != nil {
		if *m.Quality < 1 || *m.Quality > 100 {
			return service.ViewerConfig{}, errInvalidMessage
		}
		c.Quality = *m.Quality
	}
	if m.FPS != nil {
		if *m.FPS < 1 || *m.FPS > 30 {
			return service.ViewerConfig{}, errInvalidMessage
		}
		c.FPS = *m.FPS
	}
	return c, nil
}

// op は input.* メッセージを domain.Op にする。座標はストリームの座標のまま（Control が物理座標へ変換する）。
// monitor は選択中のモニターの Index。形式・値の不備（未知の action・button、必須の項目の欠け、wheel の絶対値 > 50）は errInvalidMessage。
func (m clientMessage) op(monitor int) (domain.Op, error) {
	switch m.Type {
	case typeInputMouse:
		return m.mouseOp(monitor)
	case typeInputKey:
		return m.keyOp()
	case typeInputText:
		if m.Text == nil {
			return nil, errInvalidMessage
		}
		return domain.TypeText{Text: *m.Text}, nil
	}
	return nil, errInvalidMessage
}

func (m clientMessage) mouseOp(monitor int) (domain.Op, error) {
	if m.X == nil || m.Y == nil {
		return nil, errInvalidMessage
	}
	at := domain.Point{X: *m.X, Y: *m.Y}
	switch m.Action {
	case "move":
		return domain.PointerMove{At: at, Monitor: monitor}, nil
	case "down", "up":
		b := domain.Button(m.Button)
		if !b.Valid() {
			return nil, errInvalidMessage
		}
		return domain.MouseButton{At: at, Button: b, Action: domain.ButtonAction(m.Action), Monitor: monitor}, nil
	case "wheel":
		var dx, dy float64
		if m.DX != nil {
			dx = *m.DX
		}
		if m.DY != nil {
			dy = *m.DY
		}
		if !validNotches(dx) || !validNotches(dy) {
			return nil, errInvalidMessage
		}
		return domain.Wheel{At: at, DX: dx, DY: dy, Monitor: monitor}, nil
	}
	return nil, errInvalidMessage
}

func validNotches(n float64) bool { return !math.IsNaN(n) && math.Abs(n) <= maxWheelNotches }

func (m clientMessage) keyOp() (domain.Op, error) {
	a := domain.KeyAction(m.Action)
	if !a.Valid() || m.Code == "" {
		return nil, errInvalidMessage
	}
	mods := make([]domain.Modifier, 0, len(m.Modifiers))
	for _, s := range m.Modifiers {
		mod := domain.Modifier(s)
		if !mod.Valid() {
			return nil, errInvalidMessage
		}
		mods = append(mods, mod)
	}
	return domain.KeyEvent{Action: a, Code: m.Code, Key: m.Key, Mods: mods}, nil
}
