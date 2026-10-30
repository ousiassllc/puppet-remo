package domain

import "time"

// ApprovalMode はクライアント別の承認モード（FR-L07）。
// M1 は承認なしでリースを取得するため参照せず、既定値 always で保存するだけ。適用は M3。
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
