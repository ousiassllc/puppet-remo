package windows

import (
	"context"
	"fmt"
	"slices"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルは UI Automation のパターン操作（platform.UIAutomation.Act。#268）の純粋なロジック。
// COM には触れないので、Windows 以外でもテストできる。COM 側（uia_windows.go）が uiaActor を実装する。
// カーソルもキーボードフォーカスも動かさず、SendInput にも切り替えない。

// UI Automation の ToggleState・ExpandCollapseState の値（UIAutomationCore.idl の enum）。
const (
	uiaToggleOff           = 0
	uiaToggleOn            = 1
	uiaToggleIndeterminate = 2

	uiaExpandCollapsed         = 0
	uiaExpandExpanded          = 1
	uiaExpandPartiallyExpanded = 2
	uiaExpandLeafNode          = 3
)

// uiaMaxToggles は toggle で状態を合わせるときに Toggle を呼ぶ最大の回数。オフ → オン → 不定 → オフ と巡る 3 状態のチェックボックスで、
// 不定からオンへ（オフを経て）2 回、で足りる。
const uiaMaxToggles = 3

// uiaActor は要素 1 つのパターン操作。専用スレッドの上でだけ呼ぶ。パターンを持たない要素は、呼び出しが domain.ErrPatternUnsupported を返す。
// UI Automation が拒否した失敗（アクセス拒否・要素が消えた・操作できない状態）は、実装が domain のエラーにして返す（uiaActionError）。
type uiaActor interface {
	Invoke() error
	ValueIsReadOnly() (bool, error)
	SetValue(text string) error
	ToggleState() (int, error)
	Toggle() error
	Select() error
	IsSelected() (bool, error)
	ExpandState() (int, error)
	Expand() error
	Collapse() error
}

// uiaPerform は props の要素（actor）に act を行う。検査の順序は platform.UIAutomation.Act の契約どおり: 無効 → パターン無し → 読み取り専用 → 操作。
// 結果の Window は呼び出し側が入れる。ID は要素の ID（ref）を入れる。
func uiaPerform(actor uiaActor, props uiaProps, ref domain.ElementRef, act domain.UIAction) (domain.UIActionResult, error) {
	info := uiaElementInfo(props)
	info.ID = ref.String()
	res := domain.UIActionResult{Element: info, Password: props.IsPassword}
	if !props.Enabled {
		return res, domain.ErrElementDisabled
	}
	need := act.Kind.Pattern()
	if !slices.Contains(props.Patterns, need) {
		return res, domain.ErrPatternUnsupported.
			WithMsg("the element does not support the "+need+" pattern").
			WithDetail("action", string(act.Kind)).
			WithDetail("pattern", need).
			WithDetail("supported", slices.Clone(props.Patterns))
	}
	var err error
	switch act.Kind {
	case domain.UIActionInvoke:
		err = actor.Invoke()
		res.Changed = err == nil
	case domain.UIActionSetValue:
		err = uiaSetValue(actor, *act.Value, &res)
	case domain.UIActionToggle:
		err = uiaToggle(actor, act.State, &res)
	case domain.UIActionSelect:
		err = actor.Select()
		if err == nil {
			res.Changed = true
			res.State = domain.SelectStateNotSelected
			if sel, serr := actor.IsSelected(); serr == nil && sel {
				res.State = domain.SelectStateSelected
			}
		}
	case domain.UIActionExpand:
		err = uiaExpandCollapse(actor, true, &res)
	case domain.UIActionCollapse:
		err = uiaExpandCollapse(actor, false, &res)
	}
	return res, err
}

func uiaSetValue(actor uiaActor, text string, res *domain.UIActionResult) error {
	readOnly, err := actor.ValueIsReadOnly()
	if err != nil {
		return err
	}
	if readOnly {
		return domain.ErrElementReadOnly
	}
	if err := actor.SetValue(text); err != nil {
		return err
	}
	res.Changed = true
	return nil
}

// uiaToggle は state（"on" / "off"。空なら 1 回 Toggle）にする。すでにその状態なら Toggle を呼ばない。
func uiaToggle(actor uiaActor, state string, res *domain.UIActionResult) error {
	cur, err := actor.ToggleState()
	if err != nil {
		return err
	}
	if state == "" {
		if err := actor.Toggle(); err != nil {
			return err
		}
		res.Changed = true
		return uiaReadToggle(actor, res)
	}
	want := uiaToggleOn
	if state == domain.ToggleStateOff {
		want = uiaToggleOff
	}
	for n := 0; cur != want && n < uiaMaxToggles; n++ {
		if err := actor.Toggle(); err != nil {
			return err
		}
		res.Changed = true
		if cur, err = actor.ToggleState(); err != nil {
			return err
		}
	}
	res.State = uiaToggleName(cur)
	if cur != want {
		return domain.ErrUIActionFailed.WithMsg("the element did not reach the requested state").
			WithDetail("reason", "state_not_reached").WithDetail("state", res.State)
	}
	return nil
}

func uiaReadToggle(actor uiaActor, res *domain.UIActionResult) error {
	cur, err := actor.ToggleState()
	if err != nil {
		return err
	}
	res.State = uiaToggleName(cur)
	return nil
}

func uiaToggleName(s int) string {
	switch s {
	case uiaToggleOn:
		return domain.ToggleStateOn
	case uiaToggleOff:
		return domain.ToggleStateOff
	}
	return domain.ToggleStateIndeterminate
}

func uiaExpandName(s int) string {
	switch s {
	case uiaExpandExpanded:
		return domain.ExpandStateExpanded
	case uiaExpandCollapsed:
		return domain.ExpandStateCollapsed
	case uiaExpandPartiallyExpanded:
		return domain.ExpandStatePartiallyExpanded
	}
	return domain.ExpandStateLeaf
}

// uiaExpandCollapse は展開（expand が true）または折りたたむ。すでにその状態なら呼ばない。葉（LeafNode）は操作できない。
func uiaExpandCollapse(actor uiaActor, expand bool, res *domain.UIActionResult) error {
	cur, err := actor.ExpandState()
	if err != nil {
		return err
	}
	res.State = uiaExpandName(cur)
	switch {
	case cur == uiaExpandLeafNode:
		return domain.ErrUIActionFailed.WithMsg("the element is a leaf node and cannot expand or collapse").WithDetail("reason", "leaf_node")
	case expand && cur == uiaExpandExpanded, !expand && cur == uiaExpandCollapsed:
		return nil
	}
	if expand {
		err = actor.Expand()
	} else {
		err = actor.Collapse()
	}
	if err != nil {
		return err
	}
	res.Changed = true
	if cur, err = actor.ExpandState(); err != nil {
		return err
	}
	res.State = uiaExpandName(cur)
	return nil
}

// uiaFindLive は root（所有権を引き取る）の下を幅優先で走査し、RuntimeId が runtimeID の要素を、解放せずに返す（呼び出し側が Release する）。
// ほかの要素はすべて解放する。maxNodes 個の節点を見ても無ければ domain.ErrStaleElement。
func uiaFindLive(ctx context.Context, root uiaNode, runtimeID []int32, maxNodes int) (uiaNode, error) {
	queue := []uiaNode{root}
	head, seen := 0, 0
	defer func() {
		for _, n := range queue[head:] {
			n.Release()
		}
	}()
	for head < len(queue) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := queue[head]
		head++
		if slices.Equal(n.Props().RuntimeID, runtimeID) {
			return n, nil // head は n の次を指す。defer が解放するのは queue[head:]（n を含まない）
		}
		seen++
		if seen >= maxNodes {
			n.Release()
			continue
		}
		kids, err := n.Children(ctx)
		n.Release()
		if err != nil {
			return nil, err
		}
		queue = append(queue, kids...)
	}
	return nil, domain.ErrStaleElement
}

// パターン操作の失敗の HRESULT（UIAutomationCoreApi.h・winerror.h）。
const (
	uiaHRAccessDenied        = 0x80070005 // E_ACCESSDENIED（UIPI・昇格したウィンドウ）
	uiaHRNoInterface         = 0x80004002 // E_NOINTERFACE（パターンを持たない）
	uiaHRElementNotEnabled   = 0x80040200 // UIA_E_ELEMENTNOTENABLED
	uiaHRElementNotAvailable = 0x80040201 // UIA_E_ELEMENTNOTAVAILABLE
	uiaHRNotSupported        = 0x80040204 // UIA_E_NOTSUPPORTED
	uiaHRInvalidOperation    = 0x80131509 // UIA_E_INVALIDOPERATION
	uiaHRTimeout             = 0x80131505 // UIA_E_TIMEOUT
	uiaHRInvalidWindow       = 0x80070578 // HRESULT_FROM_WIN32(ERROR_INVALID_WINDOW_HANDLE)
	uiaHRRPCDisconnected     = 0x80010108 // RPC_E_DISCONNECTED（アプリが終了した）
	uiaHRRPCServerGone       = 0x800706BA // HRESULT_FROM_WIN32(RPC_S_SERVER_UNAVAILABLE)
)

// uiaActionError はパターン操作 what（"Invoke" など）の失敗の HRESULT を domain のエラーにする。
// 要素が消えた → stale_element、アクセス拒否 → input_blocked（reason access_denied。昇格したアプリへは UIPI で届かない）、
// 無効 → element_disabled、パターン無し → pattern_unsupported、それ以外 → ui_action_failed（detail.reason と hresult）。
func uiaActionError(code uint32, what string) error {
	switch code {
	case uiaHRElementNotAvailable, uiaHRInvalidWindow, uiaHRRPCDisconnected, uiaHRRPCServerGone:
		return domain.ErrStaleElement
	case uiaHRAccessDenied:
		return domain.ErrInputBlocked.WithMsg("UI Automation refused access to the element (the app may be elevated)").WithDetail("reason", "access_denied")
	case uiaHRElementNotEnabled:
		return domain.ErrElementDisabled
	case uiaHRNoInterface, uiaHRNotSupported:
		return domain.ErrPatternUnsupported.WithMsg("the element does not support " + what)
	}
	reason := "failed"
	switch code {
	case uiaHRInvalidOperation:
		reason = "invalid_operation"
	case uiaHRTimeout:
		reason = "timeout"
	}
	return domain.ErrUIActionFailed.WithMsg("UI Automation could not perform "+what).
		WithDetail("reason", reason).WithDetail("hresult", fmt.Sprintf("0x%08X", code))
}
