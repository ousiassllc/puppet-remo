package service

import "context"

// AgentStep はエージェントのループが Control.Do を呼ぶ前に ctx へ載せる、監査の行に付ける情報（FR-G15・G23）。
// Control は ActorAgent のアクションごとに 1 行（kind = agent、event = agent.step）を記録し、No を step_no に、Route と ExtraDetail を
// detail_json に入れる。ActorAgent 以外の呼び出しでは使わない（載っていても無視する）。
type AgentStep struct {
	// No はタスク内のステップ番号（1 始まり）。0 以下なら step_no を記録しない。
	No int
	// Route はステップの経路: AgentRouteFast か AgentRouteOpenAI。空なら route を記録しない。
	Route string
	// ExtraDetail は detail_json に足すキー（高速パスの candidate_count・dropped_count・chosen_id・confidence など。docs/architecture/er.md）。
	// 入力した文字列の本文・要素の値・秘密を入れてはならない。route・judge・judge_reason・approval と、アクションの引数のキーは上書きできない（Control の値が勝つ）。
	ExtraDetail map[string]any
}

// ステップの経路（agent.step の detail.route。TaskStep.Route と同じ値）。
const (
	AgentRouteFast   = "fast"
	AgentRouteOpenAI = "openai"
)

type agentStepKey struct{}

// WithAgentStep は step を載せた ctx を返す。Control.Do を呼ぶ直前に、アクションごとに載せ直す。
func WithAgentStep(ctx context.Context, step AgentStep) context.Context {
	return context.WithValue(ctx, agentStepKey{}, step)
}

// AgentStepFrom は ctx に載せた AgentStep を返す。載っていなければ false。
func AgentStepFrom(ctx context.Context) (AgentStep, bool) {
	s, ok := ctx.Value(agentStepKey{}).(AgentStep)
	return s, ok
}
