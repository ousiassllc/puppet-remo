package domain

import (
	"errors"
	"fmt"
	"maps"
)

// Code はエラーコード。API 仕様 §2 のエラーコード表の code と 1 対 1 に対応する。
type Code string

const (
	CodeInvalidHost         Code = "invalid_host"
	CodeInvalidOrigin       Code = "invalid_origin"
	CodeMissingHeader       Code = "missing_header"
	CodeWhoisFailed         Code = "whois_failed"
	CodeDeviceNotRegistered Code = "device_not_registered"
	CodeClientDisabled      Code = "client_disabled"
	CodeUnauthorized        Code = "unauthorized"
	CodeRateLimited         Code = "rate_limited"
	CodeKillSwitchActive    Code = "kill_switch_active"
	CodeLeaseRequired       Code = "lease_required"
	CodeLeaseHeldByOther    Code = "lease_held_by_other"
	CodeLeaseRevoked        Code = "lease_revoked"
	CodeApprovalDenied      Code = "approval_denied"
	CodeApprovalTimeout     Code = "approval_timeout"
	CodeInvalidCoordinates  Code = "invalid_coordinates"
	CodeInvalidArgument     Code = "invalid_argument"
	CodeConflict            Code = "conflict"
	CodeWindowNotFound      Code = "window_not_found"
	CodeInputBlocked        Code = "input_blocked"
	CodeDesktopUnavailable  Code = "desktop_unavailable"
	CodeAgentDisabled       Code = "agent_disabled"
	CodeTaskNotFound        Code = "task_not_found"
	CodeAppNotFound         Code = "app_not_found"
	CodeLaunchFailed        Code = "launch_failed"
	CodeInternal            Code = "internal"
)

// codeStatus は Code → HTTP ステータス（API 仕様 §2 の表）。
var codeStatus = map[Code]int{
	CodeInvalidHost:         400,
	CodeInvalidOrigin:       403,
	CodeMissingHeader:       403,
	CodeWhoisFailed:         403,
	CodeDeviceNotRegistered: 403,
	CodeClientDisabled:      403,
	CodeUnauthorized:        401,
	CodeRateLimited:         429,
	CodeKillSwitchActive:    503,
	CodeLeaseRequired:       409,
	CodeLeaseHeldByOther:    409,
	CodeLeaseRevoked:        409,
	CodeApprovalDenied:      403,
	CodeApprovalTimeout:     408,
	CodeInvalidCoordinates:  400,
	CodeInvalidArgument:     400,
	CodeConflict:            409,
	CodeWindowNotFound:      404,
	CodeInputBlocked:        409,
	CodeDesktopUnavailable:  503,
	CodeAgentDisabled:       403,
	CodeTaskNotFound:        404,
	CodeAppNotFound:         404,
	CodeLaunchFailed:        409,
	CodeInternal:            500,
}

// Valid は c が API 仕様のエラーコード表にあるコードかを返す。
func (c Code) Valid() bool {
	_, ok := codeStatus[c]
	return ok
}

// HTTPStatus は c に対応する HTTP ステータスを返す。表にないコードは 500。
func (c Code) HTTPStatus() int {
	if s, ok := codeStatus[c]; ok {
		return s
	}
	return 500
}

// Error はコードつきのドメインエラー。errors.As で取り出し、errors.Is で Code により判定する。
type Error struct {
	Code   Code
	Msg    string
	Detail map[string]any
}

// NewError は Error を作る。detail は複製して保持する。
func NewError(code Code, msg string, detail map[string]any) *Error {
	return &Error{Code: code, Msg: msg, Detail: maps.Clone(detail)}
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Msg)
}

// Is は target が同じ Code の *Error のとき true。Msg と Detail は比較しない。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// WithMsg は Msg を差し替えた複製を返す。Code は変わらないので errors.Is の判定は保たれる。
func (e *Error) WithMsg(msg string) *Error {
	return &Error{Code: e.Code, Msg: msg, Detail: maps.Clone(e.Detail)}
}

// WithDetail は Detail に key を足した複製を返す。
func (e *Error) WithDetail(key string, value any) *Error {
	d := maps.Clone(e.Detail)
	if d == nil {
		d = make(map[string]any, 1)
	}
	d[key] = value
	return &Error{Code: e.Code, Msg: e.Msg, Detail: d}
}

// CodeOf は err 連鎖の最初の *Error の Code を返す。ドメインエラーでなければ CodeInternal。
// err が nil のときは空文字を返す。
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return CodeInternal
}

// API 仕様 §2 / FR-M17 のコードに 1 対 1 で対応するエラー。errors.Is で判定する。
var (
	ErrLeaseRequired      = NewError(CodeLeaseRequired, "the operation requires the control lease", nil)
	ErrLeaseHeldByOther   = NewError(CodeLeaseHeldByOther, "the lease is held by another session", nil)
	ErrLeaseRevoked       = NewError(CodeLeaseRevoked, "the lease was revoked", nil)
	ErrApprovalDenied     = NewError(CodeApprovalDenied, "the host denied the request", nil)
	ErrApprovalTimeout    = NewError(CodeApprovalTimeout, "no approval response within the time limit", nil)
	ErrKillSwitchActive   = NewError(CodeKillSwitchActive, "the kill switch is active", nil)
	ErrInvalidCoordinates = NewError(CodeInvalidCoordinates, "coordinates are out of range", nil)
	ErrInvalidArgument    = NewError(CodeInvalidArgument, "invalid argument", nil)
	ErrWindowNotFound     = NewError(CodeWindowNotFound, "window not found", nil)
	ErrInputBlocked       = NewError(CodeInputBlocked, "input is blocked for this target", nil)
	ErrDesktopUnavailable = NewError(CodeDesktopUnavailable, "the desktop is unavailable", nil)
	ErrAgentDisabled      = NewError(CodeAgentDisabled, "the agent is disabled", nil)
	ErrTaskNotFound       = NewError(CodeTaskNotFound, "task not found", nil)
	ErrAppNotFound        = NewError(CodeAppNotFound, "app not found", nil)
	// ErrLaunchFailed の reason は NewLaunchFailed で付ける。メッセージにも Detail にもパス・引数は入れない。
	ErrLaunchFailed = NewError(CodeLaunchFailed, "the app could not be launched", nil)
)
