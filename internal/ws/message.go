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
	typeTarget     = "target"
)

// クライアント → サーバーのメッセージの type。
const (
	typeClientConfig = "config"
	typeInputMouse   = "input.mouse"
	typeInputKey     = "input.key"
	typeInputText    = "input.text"
	typeClientPing   = "ping"
	// typeClientTargetResume は停止（halted）中のスコープ付きリースの、保持者の明示的な再開（Control.ResumeTarget。#157）。
	typeClientTargetResume = "target.resume"
)

// 配信の源（ready・config の source。API 仕様 §5.3）。
const (
	sourceMonitor = "monitor"
	sourceGroup   = "group"
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

// leaseBody は ready の lease と、lease メッセージの本体。holder は idle のときだけ省く（API 仕様 §5.3）。
// pending_approval では holder が承認待ちの要求者、mine が要求者本人かを表す（held では保持者）。
type leaseBody struct {
	State  string      `json:"state"` // idle | pending_approval | held
	Holder *holderBody `json:"holder,omitempty"`
	Mine   bool        `json:"mine"`
}

type readyMessage struct {
	Type     string        `json:"type"`
	Monitors []monitorBody `json:"monitors"`
	Monitor  int           `json:"monitor"`
	Source   string        `json:"source"`           // monitor | group
	Width    int           `json:"width"`            // グループ源は合成画像の大きさ（最初のコマまでは 0）
	Height   int           `json:"height"`           //
	Origin   *pointBody    `json:"origin,omitempty"` // グループ源の原点（物理座標 = origin + 画像の座標）。最初のコマまでは無い
	Quality  int           `json:"quality"`
	FPS      int           `json:"fps"`
	Lease    leaseBody     `json:"lease"`
}

type configMessage struct {
	Type    string     `json:"type"`
	Monitor int        `json:"monitor"`
	Source  string     `json:"source"`
	Width   int        `json:"width"`
	Height  int        `json:"height"`
	Origin  *pointBody `json:"origin,omitempty"`
	Quality int        `json:"quality"`
	FPS     int        `json:"fps"`
}

// blockedByBody は停止の原因のウィンドウ（blocked_by・excluded[]）。ウィンドウのタイトルは含めない。
type blockedByBody struct {
	WindowID string `json:"window_id,omitempty"`
	Process  string `json:"process,omitempty"`
	PID      uint32 `json:"pid,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// targetMessage はこのビューアのスコープ付きリース（対象ウィンドウ）の状態（#157）。state は none（スコープ付きのリースを持たない）・
// active・halted。halted のとき reason が停止の理由で、reselect が true なら再開できず選び直しが要る。
type targetMessage struct {
	Type      string          `json:"type"`
	State     string          `json:"state"`
	Process   string          `json:"process,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Reselect  bool            `json:"reselect,omitempty"`
	BlockedBy *blockedByBody  `json:"blocked_by,omitempty"`
	Members   int             `json:"members"`
	Notices   []string        `json:"notices,omitempty"`  // capture_partial（通知。停止ではない）
	Excluded  []blockedByBody `json:"excluded,omitempty"` // 所有関係を確かめられず、表示から除外したウィンドウ
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
	// Reason は input_blocked の detail.reason（own_window・target_halted・clipboard_out_of_scope など）。無ければ省く。
	Reason string `json:"reason,omitempty"`
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
	m := errorMessage{Type: typeError, Code: code, Message: msg}
	if code == domain.CodeInputBlocked || code == domain.CodeInvalidCoordinates {
		m.Reason = detailString(err, "reason")
	}
	return m
}

// detailString は err の domain.Error の detail[key]（文字列）。無ければ空。
func detailString(err error, key string) string {
	var de *domain.Error
	if errors.As(err, &de) {
		if s, ok := de.Detail[key].(string); ok {
			return s
		}
	}
	return ""
}

// clientMessage はクライアント → サーバーのメッセージの和集合。型ごとに使う項目だけを検査する。
// 値が必須のものは、無い（null）と 0 を区別するためにポインタで持つ。
type clientMessage struct {
	Type string `json:"type"`

	// config
	Monitor *int `json:"monitor"`
	Quality *int `json:"quality"`
	FPS     *int `json:"fps"`
	// Source は配信の源の切り替え（monitor | group）。省けば今の源のまま。monitor を今のものと違う値で指定すると monitor 源になる。
	Source *string `json:"source"`

	// input.mouse
	//
	// OX・OY・W・H は、グループ源で座標 (x, y) を読んだ画像の原点と大きさ（クライアントが最後に適用した config・フレームのもの。#157）。
	// 4 つ揃っていて、サーバーの今の画像の原点・大きさと違えば、座標はもう古いので注入しない（stale_frame。停止にはしない）。省けば検査しない
	// （モニター源・古いクライアント）。
	OX     *int     `json:"ox"`
	OY     *int     `json:"oy"`
	W      *int     `json:"w"`
	H      *int     `json:"h"`
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
// 配信の源（Target）は今のまま。monitor を今のものと違う値にすると monitor 源に戻る（source は呼び出し側が扱う）。
// quality（1〜100）・fps（1〜30）の範囲外は errInvalidMessage（閉じコード 4400）。モニターの範囲は Configure が検査する。
func (m clientMessage) viewerConfig(current service.ViewerConfig) (service.ViewerConfig, error) {
	c := service.ViewerConfig{Monitor: current.Monitor, Target: current.Target}
	if m.Monitor != nil {
		if *m.Monitor < 0 {
			return service.ViewerConfig{}, errInvalidMessage
		}
		if *m.Monitor != current.Monitor {
			c.Target = domain.WindowRef{}
		}
		c.Monitor = *m.Monitor
	}
	if m.Source != nil && *m.Source != sourceMonitor && *m.Source != sourceGroup {
		return service.ViewerConfig{}, errInvalidMessage
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

// frameRef は入力が読んだグループ源の画像の原点と大きさ（clientMessage の ox・oy・w・h）。無ければ nil。1〜3 個だけ付いているメッセージは
// errInvalidMessage（4 つ揃えて送る契約）。
func (m clientMessage) frameRef() (*groupGeom, error) {
	n := 0
	for _, p := range []*int{m.OX, m.OY, m.W, m.H} {
		if p != nil {
			n++
		}
	}
	switch n {
	case 0:
		return nil, nil
	case 4:
		return &groupGeom{known: true, width: *m.W, height: *m.H, origin: domain.Point{X: *m.OX, Y: *m.OY}}, nil
	}
	return nil, errInvalidMessage
}
