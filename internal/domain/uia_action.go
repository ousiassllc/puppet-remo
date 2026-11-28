package domain

import (
	"fmt"
	"strings"
)

// このファイルは UI Automation のパターンで UI 要素を操作する ui_action（#268。FR-M31）の値。
// カーソルもキーボードフォーカスも動かさず、SendInput にも切り替えない（要素がパターンを持たなければ pattern_unsupported）。
// 純粋な値と関数だけで、外部 I/O をしない。

// UIActionKind は ui_action の操作の種類。
type UIActionKind string

const (
	// UIActionInvoke は Invoke パターンの Invoke（ボタン・メニュー項目・リンクのクリック相当）。
	UIActionInvoke UIActionKind = "invoke"
	// UIActionSetValue は Value パターンの SetValue（編集欄の内容を text に置き換える。キー入力ではない）。
	UIActionSetValue UIActionKind = "set_value"
	// UIActionToggle は Toggle パターンの Toggle（チェックボックス・トグルボタン）。State があればその状態にする。
	UIActionToggle UIActionKind = "toggle"
	// UIActionSelect は SelectionItem パターンの Select（リスト項目・タブ・ラジオボタンを選ぶ）。
	UIActionSelect UIActionKind = "select"
	// UIActionExpand は ExpandCollapse パターンの Expand。
	UIActionExpand UIActionKind = "expand"
	// UIActionCollapse は ExpandCollapse パターンの Collapse。
	UIActionCollapse UIActionKind = "collapse"
)

// UIActionKinds は指定できる操作の種類（ツール説明・エラーメッセージの並び順）。
var UIActionKinds = []UIActionKind{UIActionInvoke, UIActionSetValue, UIActionToggle, UIActionSelect, UIActionExpand, UIActionCollapse}

// Valid は k が既知の操作かを返す。
func (k UIActionKind) Valid() bool {
	for _, v := range UIActionKinds {
		if k == v {
			return true
		}
	}
	return false
}

// Pattern は k が必要とする UI Automation のパターン名（Pattern* の定数）。未知の操作は空。
func (k UIActionKind) Pattern() string {
	switch k {
	case UIActionInvoke:
		return PatternInvoke
	case UIActionSetValue:
		return PatternValue
	case UIActionToggle:
		return PatternToggle
	case UIActionSelect:
		return PatternSelectionItem
	case UIActionExpand, UIActionCollapse:
		return PatternExpandCollapse
	}
	return ""
}

// UIAction は要素 ID の指す UI 要素を、UI Automation のパターンで操作する（#268。FR-M31）。入力系の操作（アプリの状態を変える）なので、
// Control.Do ではキー・クリックと同じ経路（リース・InputGuard・入力スコープ・監査）を通る。カーソル・フォーカスは動かさない。
//
//   - ElementID: find_ui_element・get_ui_tree が返す要素 ID（ElementRef の文字列）
//   - Value: set_value のときだけ必須（空文字列も有効で、内容を空にする）。ほかの操作では nil
//   - State: toggle のときだけ任意（"on" / "off"。空なら今の状態から 1 つ進める）。ほかの操作では空
type UIAction struct {
	ElementID string
	Kind      UIActionKind
	Value     *string
	State     string
}

func (UIAction) isOp() {}

// PausedByHostInput は HostInputGated の答え。UIAction はカーソルもキーボードフォーカスも動かさないので、ホストの実入力による一時停止（#270）の対象にしない。
// 包含へ変えるときはこの 1 行を true にする。
func (UIAction) PausedByHostInput() bool { return false }

// Toggle の State の値。
const (
	ToggleStateOn            = "on"
	ToggleStateOff           = "off"
	ToggleStateIndeterminate = "indeterminate"
)

// ExpandCollapse の状態の値（UIActionResult.State）。
const (
	ExpandStateExpanded          = "expanded"
	ExpandStateCollapsed         = "collapsed"
	ExpandStatePartiallyExpanded = "partially_expanded"
	ExpandStateLeaf              = "leaf"
)

// SelectionItem の状態の値（UIActionResult.State）。
const (
	SelectStateSelected    = "selected"
	SelectStateNotSelected = "not_selected"
)

// Validate は a の引数が操作と合っていることを確かめる（ErrInvalidArgument）。要素 ID の形も確かめ、ElementRef を返す。
// 余計な項目（set_value 以外の value、toggle 以外の state）は黙って捨てずに拒否する。
func (a UIAction) Validate() (ElementRef, error) {
	ref, err := ParseElementRef(a.ElementID)
	if err != nil {
		return ElementRef{}, err
	}
	if !a.Kind.Valid() {
		names := make([]string, len(UIActionKinds))
		for i, k := range UIActionKinds {
			names[i] = string(k)
		}
		return ElementRef{}, ErrInvalidArgument.WithMsg(fmt.Sprintf("action must be one of %s, got %q", strings.Join(names, ", "), a.Kind))
	}
	if a.Kind == UIActionSetValue && a.Value == nil {
		return ElementRef{}, ErrInvalidArgument.WithMsg("value is required for set_value")
	}
	if a.Kind != UIActionSetValue && a.Value != nil {
		return ElementRef{}, ErrInvalidArgument.WithMsg("value is only used by set_value")
	}
	switch {
	case a.Kind != UIActionToggle && a.State != "":
		return ElementRef{}, ErrInvalidArgument.WithMsg("state is only used by toggle")
	case a.State != "" && a.State != ToggleStateOn && a.State != ToggleStateOff:
		return ElementRef{}, ErrInvalidArgument.WithMsg(fmt.Sprintf(`state must be "on" or "off", got %q`, a.State))
	}
	return ref, nil
}

// UIActionResult は platform.UIAutomation.Act の結果。値（テキスト本文）は持たない。
//
// Act が操作の途中で失敗したときも、要素を特定できていれば Window・Element・Password を入れて返す（監査の行に ControlType・AutomationId を残すため）。
type UIActionResult struct {
	Window  WindowInfo    // 要素のあるウィンドウ
	Element UIElementInfo // 操作した要素（操作前に観測したもの）
	// Password は要素がパスワード欄か（set_value の本文を audit.record_text でも監査に残さない）。
	Password bool
	// Changed は、パターンの操作を実際に呼んだか。false は、すでにその状態だった（toggle の state 指定・expand・collapse）。
	Changed bool
	// State は操作後の状態。toggle は ToggleState*、expand・collapse は ExpandState*、select は SelectState*。invoke・set_value は空。
	State string
}
