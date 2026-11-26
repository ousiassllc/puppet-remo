package openai

import (
	"fmt"
	"testing"
)

// pairingProblem は Responses API の input の項目（JSON を map にしたもの）で、すべての computer_call / function_call に、
// call_id が一致する出力（computer_call_output / function_call_output）がちょうど 1 件あるかを調べる。
// 問題があれば説明を返す（欠落・重複）。ChatGPT のプラン経路（関数ツール。未実装）の実装のテストも再利用する共通の検査関数。
func pairingProblem(items []map[string]any) error {
	calls := map[string]string{} // call_id → call の種別
	outputs := map[string]int{}
	for _, it := range items {
		id, _ := it["call_id"].(string)
		switch it["type"] {
		case "computer_call", "function_call":
			calls[id] = it["type"].(string)
		case "computer_call_output", "function_call_output":
			outputs[id]++
		}
	}
	for id, kind := range calls {
		if outputs[id] != 1 {
			return fmt.Errorf("%s %q has %d outputs, want exactly 1", kind, id, outputs[id])
		}
	}
	for id, n := range outputs {
		if n > 1 {
			return fmt.Errorf("call_id %q has %d outputs", id, n)
		}
	}
	return nil
}

// checkCallPairing は pairingProblem をテストの失敗にする。
func checkCallPairing(t *testing.T, items []map[string]any) {
	t.Helper()
	if err := pairingProblem(items); err != nil {
		t.Errorf("call/output pairing: %v", err)
	}
}
