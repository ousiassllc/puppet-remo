package service

// AuditRequirement は FR-AU01 の列挙 1 項目と、それを記録するイベント名の対応（docs/architecture/er.md の「イベントのカタログ」）。
// 網羅テスト（audit_catalog_test.go）が、この表・ソースが実際に追記するイベント名・ER の表の 3 つの食い違いを検出する。
// Events が空の項目は、Pending に書いた Issue が実装するまで未記録（その Issue が表を埋める）。
type AuditRequirement struct {
	Item    string   // FR-AU01 の項目（エージェントのものは M4 なので含めない）
	Events  []string // 記録するイベント名。tool.<名前> の族は AuditToolEventPrefix で表す
	Pending string   // 未実装のとき、実装する Issue（Events は空）
}

// AuditToolEventPrefix は操作系・閲覧系ツールと Web 入力のイベント名の接頭辞（ToolAuditEntry。"tool." + ツール名）。
const AuditToolEventPrefix = "tool."

// AuditCatalog は FR-AU01（agent 以外）と記録するイベント名の対応。ここに無いイベントを足したら、網羅テストが失敗する。
var AuditCatalog = []AuditRequirement{
	{Item: "接続要求の作成", Events: []string{"auth.connreq_create"}},
	{Item: "接続要求の許可", Events: []string{"auth.connreq_approve"}},
	{Item: "接続要求の拒否", Events: []string{"auth.connreq_deny"}},
	{Item: "認証失敗", Events: []string{"auth.bearer_failed", "auth.rate_limited", "auth.connreq_rate_limited"}},
	{Item: "トークン発行・失効", Events: []string{"token.issue", "token.revoke"}},
	{Item: "リースの取得・解放・剥奪", Events: []string{"lease.acquire", "lease.release", "lease.revoke"}},
	{Item: "リースの取得要求・承認・拒否（承認モード）", Pending: "#66"},
	{Item: "MCP ツール呼び出し・Web UI からの入力操作", Events: []string{AuditToolEventPrefix + "*"}},
	{Item: "キルスイッチ発動・解除", Events: []string{"killswitch.trigger", "killswitch.release"}},
	{Item: "設定変更", Events: []string{
		"settings.update", "settings.launchable_app.create", "settings.launchable_app.update", "settings.launchable_app.delete",
		"client.replace", "client.update", "client.delete",
	}},
	{Item: "更新の確認・適用（kind = update）", Pending: "#74"},
}

// auditCatalogEvents は AuditCatalog の具体的なイベント名（tool.* の族を除く）の集合。
func auditCatalogEvents() map[string]string {
	out := map[string]string{}
	for _, r := range AuditCatalog {
		for _, ev := range r.Events {
			if ev != AuditToolEventPrefix+"*" {
				out[ev] = r.Item
			}
		}
	}
	return out
}
