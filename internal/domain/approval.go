package domain

import "time"

// NeedsApproval は、リース取得にホストの承認が要るかを返す（FR-L07）。
//   - always: 常に要る
//   - first_time: approvedAt（clients.approved_at）が nil（未承認）のときだけ要る
//   - auto: 要らない
//
// 定義外の値は承認を要るものとして扱う（fail-closed）。
func NeedsApproval(mode ApprovalMode, approvedAt *time.Time) bool {
	switch mode {
	case ApprovalAuto:
		return false
	case ApprovalFirstTime:
		return approvedAt == nil
	default:
		return true
	}
}
