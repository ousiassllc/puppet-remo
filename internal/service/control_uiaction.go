package service

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは UI Automation のパターンで要素を操作する UIAction（#268。ui_action）。
// 検査の順序・スコープ付きリースの扱いは Control.Do の説明（control.go）にある。ここは引数の検査と実行だけ。
// platform.UIAutomation.Act がカーソルもフォーカスも動かさず、SendInput にも切り替えない（要素がパターンを持たなければ pattern_unsupported）。

// planUIAction は UIAction の引数を検査する。set_value の本文は type と同じ上限（10,000 文字）。
func planUIAction(o domain.UIAction) (domain.UIAction, error) {
	if _, err := o.Validate(); err != nil {
		return o, err
	}
	if o.Value != nil && utf8.RuneCountInString(*o.Value) > maxTypeTextRunes {
		return o, domain.ErrInvalidArgument.WithMsg(fmt.Sprintf("value is limited to %d characters", maxTypeTextRunes))
	}
	return o, nil
}

// actUI は UIAction の実行。スコープ付きのリース（sr が非 nil）は、操作の直前に要素のウィンドウが G のメンバーかを検証する。
// platform の Act が要素を特定したあとで失敗したときも、特定した要素の情報を Result に入れて返す（監査に ControlType・AutomationId を残す）。
func (c *control) actUI(ctx context.Context, o domain.UIAction, sr *scopeRun) (Result, error) {
	ref, err := o.Validate()
	if err != nil {
		return Result{}, err
	}
	if err := c.verifyInput(ctx, sr, scopeCheck{focusID: ref.WindowID()}); err != nil {
		return Result{}, err
	}
	res, err := c.ui.Act(ctx, ref, o)
	if err != nil && res.Element.ID == "" {
		return Result{}, err
	}
	return Result{Acted: &res}, err
}

// uiActionArgs は ui_action の監査の引数。set_value の本文は入れず文字数（text_len）だけ（FR-AU03。audit.record_text が true のときの text は addRecordedText が足す）。
// 要素の ControlType・AutomationId・操作後の状態は、要素を特定できたときだけ入れる。呼び出し側の文字列は、トークンの形を伏せて切り詰める。
func uiActionArgs(o domain.UIAction, res Result) map[string]any {
	args := map[string]any{"action": auditShort(string(o.Kind)), "element_id": auditShort(o.ElementID)}
	if o.Value != nil {
		args["text_len"] = utf8.RuneCountInString(*o.Value)
	}
	if o.State != "" {
		args["state"] = auditShort(o.State)
	}
	if a := res.Acted; a != nil {
		args["control_type"] = a.Element.ControlType
		if a.Element.AutomationID != "" {
			args["automation_id"] = auditShort(a.Element.AutomationID)
		}
		args["changed"] = a.Changed
		if a.State != "" {
			args["result_state"] = a.State
		}
	}
	return args
}

// auditShort は監査に残す呼び出し側・アプリ由来の文字列を、トークンの形を伏せて locatorMaxRunes 文字までにする。
func auditShort(s string) string {
	s, _ = cutAtRunes(auditRedactString(s), locatorMaxRunes)
	return s
}
