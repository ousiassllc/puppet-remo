// Package openai は OpenAI Responses API の `computer` ツールを 1 ステップずつ呼ぶ薄い層（docs/components/overview.md §4.3）。
//
// service/agent は Model だけに依存し、テストでは Model を差し替える（NFR-O06）。
// 会話は持たない: Responses API は常に store: false で呼び、previous_response_id は使わず、
// 呼び出しごとに History（指示・これまでの出力アイテム・computer_call_output）を送り直す（FR-G24）。
package openai

import (
	"context"
	"encoding/json"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// Model は service/agent が使うモデルのインターフェース。テストではモックする（NFR-O06）。
type Model interface {
	// Step は 1 ステップ（Responses API 呼び出し 1 回）。Config.Timeout（既定 60 秒）でタイムアウトし（NFR-S16）、再試行しない。
	// 常に store: false。previous_response_id は使わない（FR-G24）。
	//
	// 失敗は次の 3 通りで、API キーはどのエラーにも含まれない:
	//   - 未知・写像できないアクションは errors.Is(err, domain.ErrUnsupportedAction)（agent は unsupported_action にする）。
	//   - HTTP の失敗（401 / 429 / 5xx など）は errors.As で *APIError を取り出せる。401 は errors.Is(err, ErrUnauthorized) でも一致する
	//     （agent は 401 を GUI へ通知し、エージェントを無効に戻す。NFR-S16）。
	//   - それ以外（タイムアウト・通信エラー・キーの取得失敗・不正な応答）は通常のエラー（agent は openai_error にする）。
	Step(ctx context.Context, in StepInput) (StepOutput, error)
}

// StepInput は Step への入力。
type StepInput struct {
	Model   string   // 例: gpt-6.1-sol（設定 agent.model。FR-G04）。空は不可
	History *History // 呼び出し側（run）が所有し、Step は読むだけ
}

// StepOutput は 1 回の応答。
type StepOutput struct {
	ResponseID string // 診断用（監査・ログ）。継続には使わない
	// Items は出力アイテム（原文のまま）。呼び出し側は History.AddOutput へ追記して次回に返す。
	Items []OutputItem
	// Actions は computer_call の actions[] を、順序を保って domain.Op へ写したもの。
	// 座標は縮小スクリーンショットの座標（FR-G06）で、Monitor は domain.MonitorPrimary。
	// screenshot アクションは含めない（ループは各ステップの後に必ず新しい画面を返す）。
	Actions []domain.Op
	// CallID は computer_call の call_id。空でないことと computer_call が返ったことは同値で、空なら最終回答（FinalText）。
	CallID       string
	SafetyChecks []SafetyCheck // pending_safety_checks（FR-G08）
	FinalText    string        // computer_call が返らなかったときの最終回答
	Usage        Usage         // input / output トークン
}

// OutputItem は応答の出力アイテム 1 つ。変更せず、そのまま次の呼び出しへ返す（FR-G24）。
type OutputItem struct{ Raw json.RawMessage }

// SafetyCheck は API の安全チェック 1 件（FR-G08）。pending_safety_checks で届き、
// 所有者が許可したものを acknowledged_safety_checks としてそのまま返す。
type SafetyCheck struct {
	ID      string
	Code    string
	Message string
}

// Usage は 1 回の応答のトークン数（FR-G09。タスクに加算して記録する）。
type Usage struct {
	InputTokens  int64
	OutputTokens int64
}
