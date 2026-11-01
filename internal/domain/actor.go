package domain

// ActorKind は操作主体の種別。リースの保持者種別とも、監査ログの actor とも対応する。
type ActorKind string

const (
	ActorMCP    ActorKind = "mcp"
	ActorWeb    ActorKind = "web"
	ActorAgent  ActorKind = "agent"
	ActorHost   ActorKind = "host"
	ActorSystem ActorKind = "system"
)

// CanHoldLease は、この種別がリースの保持者になれるかを返す。
// 保持者種別は mcp（M1）と web（M2）。agent は M4 で加わる（FR-L01）。
func (k ActorKind) CanHoldLease() bool {
	return k == ActorMCP || k == ActorWeb
}

// CanRevokeLease は、この種別が他者のリースを強制剥奪できるかを返す（FR-L06）。Web クライアントとホスト所有者だけ。
func (k ActorKind) CanRevokeLease() bool {
	return k == ActorWeb || k == ActorHost
}

// Actor は操作主体。
type Actor struct {
	Kind      ActorKind
	ClientID  string // 未登録・host・system は空
	Label     string // 表示名のスナップショット
	SessionID string // MCP セッション / viewer_id / task_id（監査用の文字列）
}

// SameSession は a と other が同じセッションか（リース保持者の同一性）を返す。
// 種別・クライアント・セッション ID がすべて一致するときだけ true。Label は比較しない。
func (a Actor) SameSession(other Actor) bool {
	return a.Kind == other.Kind && a.ClientID == other.ClientID && a.SessionID == other.SessionID
}
