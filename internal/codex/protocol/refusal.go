package protocol

// サーバーの要求への拒否の答え。アプリが許すのは desktop の関数ツールの呼び出し（item/tool/call）だけで、
// コマンド・ファイル変更・追加の権限・ユーザーへの質問・MCP・トークンの供給（アプリは出さないのでエラー）はすべて拒否する。

// DecisionResponse は承認要求（コマンド・ファイル変更・古い形式のパッチと実行）への答え。
type DecisionResponse struct {
	Decision string `json:"decision"`
}

// ActionResponse は MCP の elicitation への答え。
type ActionResponse struct {
	Action string `json:"action"`
}

// PermissionsResponse は追加の権限の要求への答え: 何も許さない。
type PermissionsResponse struct {
	Permissions struct{} `json:"permissions"`
	Scope       string   `json:"scope"`
}

// AnswersResponse はツールのユーザーへの質問への答え: 回答なし。
type AnswersResponse struct {
	Answers map[string]any `json:"answers"`
}

// 拒否の値。
const (
	DecisionDecline     = "decline" // item/commandExecution・item/fileChange
	DecisionAbort       = "abort"   // 古い applyPatchApproval・execCommandApproval（ReviewDecision）
	ActionDecline       = "decline"
	PermissionScopeTurn = "turn"
)

// Refusal は method への拒否の答え。ok が false の要求（未知のもの、拒否ではなくエラーで返す
// account/chatgptAuthTokens/refresh・attestation/generate）はエラー応答にする。
// item/tool/call の拒否は、desktop 以外の関数・thread の無い呼び出しに返す「失敗で出力なし」。
func Refusal(method string) (answer any, ok bool) {
	switch method {
	case ReqCommandApproval, ReqFileChangeApproval:
		return DecisionResponse{Decision: DecisionDecline}, true
	case ReqLegacyPatchApproval, ReqLegacyExecApproval:
		return DecisionResponse{Decision: DecisionAbort}, true
	case ReqPermissionsApproval:
		return PermissionsResponse{Scope: PermissionScopeTurn}, true
	case ReqToolUserInput:
		return AnswersResponse{Answers: map[string]any{}}, true
	case ReqMCPElicitation:
		return ActionResponse{Action: ActionDecline}, true
	case ReqDynamicToolCall:
		return DynamicToolResponse{ContentItems: []ContentItem{}}, true
	}
	return nil, false
}

// IsPlainItem はスレッドの項目の型が、画面操作のターンに許される項目か: ユーザーのメッセージの反復・モデルのテキストと推論・
// 計画・文脈の整理・desktop の関数ツールの呼び出し。それ以外（これから足される型も）は道具の使用で、ターンを中断する。
func IsPlainItem(itemType string) bool {
	switch itemType {
	case "userMessage", "agentMessage", "reasoning", "plan", "hookPrompt", "contextCompaction", ItemDynamicToolRun:
		return true
	}
	return false
}
