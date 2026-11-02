package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestVerdictOrderAndStricter(t *testing.T) {
	if domain.VerdictAllow >= domain.VerdictRequireApproval || domain.VerdictRequireApproval >= domain.VerdictDeny {
		t.Fatal("verdicts must be ordered Allow < RequireApproval < Deny")
	}
	all := []domain.Verdict{domain.VerdictAllow, domain.VerdictRequireApproval, domain.VerdictDeny}
	for _, a := range all {
		for _, b := range all {
			got := domain.Stricter(a, b)
			want := a
			if b > a {
				want = b
			}
			if got != want {
				t.Errorf("Stricter(%v, %v) = %v, want %v", a, b, got, want)
			}
			if domain.Stricter(b, a) != got {
				t.Errorf("Stricter is not commutative for %v, %v", a, b)
			}
		}
		if domain.Stricter(a, a) != a {
			t.Errorf("Stricter(%v, %v) is not idempotent", a, a)
		}
	}
}

func TestVerdictString(t *testing.T) {
	tests := map[domain.Verdict]string{
		domain.VerdictAllow:           "allow",
		domain.VerdictRequireApproval: "require_approval",
		domain.VerdictDeny:            "deny",
		domain.Verdict(99):            "unknown",
	}
	for v, want := range tests {
		if got := v.String(); got != want {
			t.Errorf("Verdict(%d).String() = %q, want %q", int(v), got, want)
		}
	}
}

func TestTaskErrorCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"input_blocked", domain.NewError(domain.CodeInputBlocked, "x", nil), domain.TaskErrorInputBlocked},
		{"approval_denied", domain.ErrApprovalDenied, "approval_denied"},
		{"approval_timeout", domain.ErrApprovalTimeout, "approval_timeout"},
		{"kill_switch_active", domain.ErrKillSwitchActive, "kill_switch_active"},
		{"lease_required", domain.ErrLeaseRequired, "lease_required"},
		{"lease_held_by_other", domain.ErrLeaseHeldByOther, "lease_held_by_other"},
		{"lease_revoked", domain.ErrLeaseRevoked, "lease_revoked"},
		{"judge_denied", domain.ErrJudgeDenied, "judge_denied"},
		{"wrapped judge_denied", fmt.Errorf("step 3: %w", domain.ErrJudgeDenied), "judge_denied"},
		{"unsupported_action", domain.ErrUnsupportedAction, "unsupported_action"},
		{"wrapped unsupported_action", fmt.Errorf("openai: %w", domain.ErrUnsupportedAction), "unsupported_action"},
		{"safety_check_denied", domain.ErrSafetyCheckDenied, "safety_check_denied"},
		{"openai_error", fmt.Errorf("%w: %w", domain.ErrOpenAI, errors.New("status 500")), "openai_error"},
		{"unsupported_action が openai_error より優先", fmt.Errorf("%w: %w", domain.ErrOpenAI, domain.ErrUnsupportedAction), "unsupported_action"},
		{"表に無いドメインエラーは internal", domain.ErrWindowNotFound, "internal"},
		{"想定外のエラーは internal", errors.New("boom"), "internal"},
		{"ErrInvalidChoice は失敗理由にならない", domain.ErrInvalidChoice, "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.TaskErrorCode(tt.err); got != tt.want {
				t.Errorf("TaskErrorCode(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// judge_denied・unsupported_action は API のエラーコード表の外（API 仕様 §2）。*Error にしてはならない。
func TestJudgeSentinelsAreNotAPIErrors(t *testing.T) {
	for _, err := range []error{domain.ErrJudgeDenied, domain.ErrUnsupportedAction, domain.ErrInvalidChoice, domain.ErrSafetyCheckDenied, domain.ErrOpenAI} {
		var de *domain.Error
		if errors.As(err, &de) {
			t.Errorf("%v must not be a *domain.Error", err)
		}
		if domain.CodeOf(err) != domain.CodeInternal {
			t.Errorf("CodeOf(%v) = %q, want internal", err, domain.CodeOf(err))
		}
	}
}
