package domain_test

import (
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestNeedsApproval(t *testing.T) {
	approved := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		mode       domain.ApprovalMode
		approvedAt *time.Time
		want       bool
	}{
		{"always は常に要る（未承認）", domain.ApprovalAlways, nil, true},
		{"always は承認済みでも要る", domain.ApprovalAlways, &approved, true},
		{"first_time は未承認なら要る", domain.ApprovalFirstTime, nil, true},
		{"first_time は承認済みなら要らない", domain.ApprovalFirstTime, &approved, false},
		{"auto は要らない（未承認）", domain.ApprovalAuto, nil, false},
		{"auto は要らない（承認済み）", domain.ApprovalAuto, &approved, false},
		{"空の値は fail closed で要る", "", &approved, true},
		{"未知の値は fail closed で要る", "sometimes", &approved, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.NeedsApproval(tt.mode, tt.approvedAt); got != tt.want {
				t.Errorf("NeedsApproval(%q, %v) = %v, want %v", tt.mode, tt.approvedAt, got, tt.want)
			}
		})
	}
}
