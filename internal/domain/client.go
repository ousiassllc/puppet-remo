package domain

import "time"

// ApprovalMode はクライアント別の承認モード（FR-L07）。リース取得時の承認の要否は NeedsApproval が決める。
// 新規クライアントの既定は always。
type ApprovalMode string

const (
	ApprovalAlways    ApprovalMode = "always"
	ApprovalFirstTime ApprovalMode = "first_time"
	ApprovalAuto      ApprovalMode = "auto"
)

// Valid は m が定義済みの承認モードかを返す。
func (m ApprovalMode) Valid() bool {
	switch m {
	case ApprovalAlways, ApprovalFirstTime, ApprovalAuto:
		return true
	}
	return false
}

// Client は許可リストに登録されたクライアント（tailnet デバイス + ユーザー）。
type Client struct {
	ID, StableNodeID, LoginName, DisplayName string
	WebEnabled, MCPEnabled, AgentEnabled     bool
	ApprovalMode                             ApprovalMode
	ApprovedAt                               *time.Time
}
