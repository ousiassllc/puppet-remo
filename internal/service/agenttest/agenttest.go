// Package agenttest は、内蔵エージェントの OpenAI を台本で差し替えるテスト・オフラインのデモ用の部品。
//
// app は openai を import できない（scripts/check-imports.sh）ので、台本を書くのに要る openai の型の別名と、
// computer_call・最終回答の応答を作る関数をここに置く。実際の OpenAI は呼ばない。
package agenttest

import (
	"encoding/json"
	"fmt"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/openai"
)

// openai の型の別名（台本の応答・履歴を書くため）。
type (
	StepInput   = openai.StepInput
	StepOutput  = openai.StepOutput
	OutputItem  = openai.OutputItem
	Usage       = openai.Usage
	SafetyCheck = openai.SafetyCheck
	History     = openai.History
)

// NewHistory は空の履歴を返す（台本の進み具合をタスクごとに数え直す目印にもなる）。
func NewHistory() *History { return openai.NewHistory("p", "", nil, 1) }

// Call はアクション ops を返す computer_call の応答（callID は空にしない）。トークンは見かけの値。
func Call(callID string, ops ...domain.Op) StepOutput {
	return StepOutput{
		CallID: callID, Actions: ops, Usage: Usage{InputTokens: 1200, OutputTokens: 40},
		Items: []OutputItem{{Raw: json.RawMessage(fmt.Sprintf(`{"type":"computer_call","call_id":%q,"status":"completed"}`, callID))}},
	}
}

// Final は最終回答の応答（CallID が空）。
func Final(text string) StepOutput {
	return StepOutput{FinalText: text, Usage: Usage{InputTokens: 1300, OutputTokens: 20}}
}
