package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// API 仕様 §2 のエラーコード表（code → HTTP ステータス）。表と実装のずれを検出する。
var apiErrorTable = map[string]int{
	"invalid_host":          400,
	"invalid_origin":        403,
	"missing_header":        403,
	"whois_failed":          403,
	"device_not_registered": 403,
	"client_disabled":       403,
	"unauthorized":          401,
	"rate_limited":          429,
	"kill_switch_active":    503,
	"lease_required":        409,
	"lease_held_by_other":   409,
	"lease_revoked":         409,
	"approval_denied":       403,
	"approval_timeout":      408,
	"invalid_coordinates":   400,
	"invalid_argument":      400,
	"conflict":              409,
	"window_not_found":      404,
	"input_blocked":         409,
	"desktop_unavailable":   503,
	"agent_disabled":        403,
	"task_not_found":        404,
	"app_not_found":         404,
	"launch_failed":         409,
	"internal":              500,
}

func TestCodeTable(t *testing.T) {
	codes := []domain.Code{
		domain.CodeInvalidHost, domain.CodeInvalidOrigin, domain.CodeMissingHeader, domain.CodeWhoisFailed,
		domain.CodeDeviceNotRegistered, domain.CodeClientDisabled, domain.CodeUnauthorized, domain.CodeRateLimited,
		domain.CodeKillSwitchActive, domain.CodeLeaseRequired, domain.CodeLeaseHeldByOther, domain.CodeLeaseRevoked,
		domain.CodeApprovalDenied, domain.CodeApprovalTimeout, domain.CodeInvalidCoordinates, domain.CodeInvalidArgument,
		domain.CodeConflict, domain.CodeWindowNotFound, domain.CodeInputBlocked, domain.CodeDesktopUnavailable,
		domain.CodeAgentDisabled, domain.CodeTaskNotFound, domain.CodeAppNotFound, domain.CodeLaunchFailed, domain.CodeInternal,
	}
	if len(codes) != len(apiErrorTable) {
		t.Fatalf("%d codes, API table has %d", len(codes), len(apiErrorTable))
	}
	for _, c := range codes {
		want, ok := apiErrorTable[string(c)]
		if !ok {
			t.Errorf("code %q is not in the API table", c)
			continue
		}
		if !c.Valid() {
			t.Errorf("%q.Valid() = false", c)
		}
		if got := c.HTTPStatus(); got != want {
			t.Errorf("%q.HTTPStatus() = %d, want %d", c, got, want)
		}
	}
}

func TestUnknownCode(t *testing.T) {
	c := domain.Code("no_such_code")
	if c.Valid() || c.HTTPStatus() != 500 {
		t.Errorf("Valid = %v, HTTPStatus = %d", c.Valid(), c.HTTPStatus())
	}
}

func TestSentinelErrors(t *testing.T) {
	tests := []struct {
		err  *domain.Error
		code domain.Code
	}{
		{domain.ErrLeaseRequired, domain.CodeLeaseRequired},
		{domain.ErrLeaseHeldByOther, domain.CodeLeaseHeldByOther},
		{domain.ErrLeaseRevoked, domain.CodeLeaseRevoked},
		{domain.ErrApprovalDenied, domain.CodeApprovalDenied},
		{domain.ErrApprovalTimeout, domain.CodeApprovalTimeout},
		{domain.ErrKillSwitchActive, domain.CodeKillSwitchActive},
		{domain.ErrInvalidCoordinates, domain.CodeInvalidCoordinates},
		{domain.ErrInvalidArgument, domain.CodeInvalidArgument},
		{domain.ErrWindowNotFound, domain.CodeWindowNotFound},
		{domain.ErrInputBlocked, domain.CodeInputBlocked},
		{domain.ErrDesktopUnavailable, domain.CodeDesktopUnavailable},
		{domain.ErrAgentDisabled, domain.CodeAgentDisabled},
		{domain.ErrTaskNotFound, domain.CodeTaskNotFound},
		{domain.ErrAppNotFound, domain.CodeAppNotFound},
		{domain.ErrLaunchFailed, domain.CodeLaunchFailed},
	}
	seen := map[domain.Code]bool{}
	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			if tt.err.Code != tt.code || !tt.code.Valid() {
				t.Errorf("Code = %q, want %q", tt.err.Code, tt.code)
			}
			if seen[tt.code] {
				t.Errorf("duplicate sentinel for %q", tt.code)
			}
			seen[tt.code] = true
			if !errors.Is(tt.err, tt.err) {
				t.Error("errors.Is(err, err) = false")
			}
			for _, other := range tests {
				if other.code != tt.code && errors.Is(tt.err, other.err) {
					t.Errorf("%q matches %q", tt.code, other.code)
				}
			}
		})
	}
}

func TestErrorWrapping(t *testing.T) {
	derived := domain.ErrLeaseHeldByOther.WithMsg("held by agent").WithDetail("holder_kind", "agent")
	wrapped := fmt.Errorf("acquire: %w", derived)

	if !errors.Is(wrapped, domain.ErrLeaseHeldByOther) {
		t.Error("errors.Is on a derived, wrapped error = false")
	}
	if errors.Is(wrapped, domain.ErrLeaseRequired) {
		t.Error("errors.Is matched a different code")
	}
	var de *domain.Error
	if !errors.As(wrapped, &de) || de.Msg != "held by agent" || de.Detail["holder_kind"] != "agent" {
		t.Errorf("errors.As = %+v", de)
	}
	if got, want := de.Error(), "lease_held_by_other: held by agent"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got := domain.NewError(domain.CodeInternal, "", nil).Error(); got != "internal" {
		t.Errorf("Error() without Msg = %q", got)
	}
}

func TestErrorDerivationDoesNotMutate(t *testing.T) {
	base := domain.NewError(domain.CodeConflict, "base", map[string]any{"a": 1})
	derived := base.WithDetail("b", 2).WithMsg("derived")

	if len(base.Detail) != 1 || base.Msg != "base" {
		t.Errorf("base mutated: %+v", base)
	}
	if len(derived.Detail) != 2 || derived.Msg != "derived" {
		t.Errorf("derived = %+v", derived)
	}

	in := map[string]any{"k": "v"}
	e := domain.NewError(domain.CodeConflict, "", in)
	in["k"] = "changed"
	if e.Detail["k"] != "v" {
		t.Error("NewError kept a reference to the caller's map")
	}

	if domain.ErrInvalidArgument.Detail != nil {
		t.Error("sentinel has detail")
	}
	_ = domain.ErrInvalidArgument.WithDetail("x", 1)
	if domain.ErrInvalidArgument.Detail != nil {
		t.Error("sentinel was mutated by WithDetail")
	}
}

func TestCodeOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want domain.Code
	}{
		{"nil", nil, ""},
		{"sentinel", domain.ErrInputBlocked, domain.CodeInputBlocked},
		{"wrapped", fmt.Errorf("x: %w", domain.ErrWindowNotFound), domain.CodeWindowNotFound},
		{"joined", errors.Join(errors.New("a"), domain.ErrTaskNotFound), domain.CodeTaskNotFound},
		{"domain 以外は internal", errors.New("boom"), domain.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.CodeOf(tt.err); got != tt.want {
				t.Errorf("CodeOf = %q, want %q", got, tt.want)
			}
		})
	}
}
