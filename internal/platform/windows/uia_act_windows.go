//go:build windows

package windows

import (
	"errors"
	"unsafe"

	ole "github.com/go-ole/go-ole"
)

// パターン操作（#268）の COM バインディング。vtable のスロット番号・IID・パターン ID は、Windows SDK の UIAutomationClient.h /
// UIAutomationCore.idl（wine の include/uiautomationclient.idl・uiautomationcore.idl と mingw-w64 の uiautomationclient.h で突き合わせた値）。
// 各パターンのインターフェースは IUnknown の 3 メソッドに続く宣言順（[propget] もメソッドとして 1 つ数える）。
const (
	// IUIAutomationElement
	uiaSlotGetCurrentPatternAs = 14

	// UIA_*PatternId
	uiaPatternIDInvoke         = 10000
	uiaPatternIDValue          = 10002
	uiaPatternIDExpandCollapse = 10005
	uiaPatternIDSelectionItem  = 10010
	uiaPatternIDToggle         = 10015

	// IUIAutomationInvokePattern: Invoke
	uiaSlotInvoke = 3
	// IUIAutomationValuePattern: SetValue, get_CurrentValue, get_CurrentIsReadOnly
	uiaSlotValueSetValue   = 3
	uiaSlotValueIsReadOnly = 5
	// IUIAutomationTogglePattern: Toggle, get_CurrentToggleState
	uiaSlotToggle      = 3
	uiaSlotToggleState = 4
	// IUIAutomationSelectionItemPattern: Select, AddToSelection, RemoveFromSelection, get_CurrentIsSelected
	uiaSlotSelect     = 3
	uiaSlotIsSelected = 6
	// IUIAutomationExpandCollapsePattern: Expand, Collapse, get_CurrentExpandCollapseState
	uiaSlotExpand      = 3
	uiaSlotCollapse    = 4
	uiaSlotExpandState = 5
)

var (
	uiaIIDInvokePattern         = ole.NewGUID("{fb377fbe-8ea6-46d5-9c73-6499642d3059}")
	uiaIIDValuePattern          = ole.NewGUID("{a94cd8b1-0844-4cd6-9d2d-640537ab39e9}")
	uiaIIDTogglePattern         = ole.NewGUID("{94cf8058-9b8d-4ab9-8bfd-4cd0a33c8c70}")
	uiaIIDSelectionItemPattern  = ole.NewGUID("{a8efa66a-0fda-421a-9194-38021f3578ea}")
	uiaIIDExpandCollapsePattern = ole.NewGUID("{619be086-1f4e-4ee4-bafa-210128738730}")
)

var _ uiaActor = (*uiaCOMNode)(nil)

// uiaFail は COM の失敗 err（uiaHRESULT）を domain のエラーにする（uiaActionError）。HRESULT でなければそのまま返す。
func uiaFail(err error, what string) error {
	var hr uiaHRESULT
	if errors.As(err, &hr) {
		return uiaActionError(hr.code, what)
	}
	return err
}

// withPattern は要素の現在のパターン（GetCurrentPatternAs）を取り、fn に渡して Release する。
// パターンを持たない（S_OK で NULL、または E_NOINTERFACE）なら domain.ErrPatternUnsupported。
func (n *uiaCOMNode) withPattern(patternID uintptr, iid *ole.GUID, what string, fn func(p unsafe.Pointer) error) error {
	var p unsafe.Pointer
	if err := uiaCheck(uiaCallIPP(n.elem, uiaSlotGetCurrentPatternAs, patternID, unsafe.Pointer(iid), unsafe.Pointer(&p)), "GetCurrentPatternAs"); err != nil {
		return uiaFail(err, what)
	}
	if p == nil {
		return uiaActionError(uiaHRNoInterface, what)
	}
	defer uiaRelease(p)
	return fn(p)
}

func (n *uiaCOMNode) Invoke() error {
	return n.withPattern(uiaPatternIDInvoke, uiaIIDInvokePattern, "Invoke", func(p unsafe.Pointer) error {
		return uiaFail(uiaCheck(uiaCall0(p, uiaSlotInvoke), "Invoke"), "Invoke")
	})
}

func (n *uiaCOMNode) ValueIsReadOnly() (readOnly bool, err error) {
	err = n.withPattern(uiaPatternIDValue, uiaIIDValuePattern, "Value", func(p unsafe.Pointer) error {
		var v int32
		if err := uiaCheck(uiaCallP(p, uiaSlotValueIsReadOnly, unsafe.Pointer(&v)), "get_CurrentIsReadOnly"); err != nil {
			return uiaFail(err, "Value")
		}
		readOnly = v != 0
		return nil
	})
	return readOnly, err
}

// SetValue は ValuePattern::SetValue。キー入力ではない。BSTR は C 側のメモリなので、呼び出しのあとに解放する。
func (n *uiaCOMNode) SetValue(text string) error {
	return n.withPattern(uiaPatternIDValue, uiaIIDValuePattern, "SetValue", func(p unsafe.Pointer) error {
		bstr := ole.SysAllocString(text)
		defer func() { _ = ole.SysFreeString(bstr) }()
		return uiaFail(uiaCheck(uiaCallI(p, uiaSlotValueSetValue, uintptr(unsafe.Pointer(bstr))), "SetValue"), "SetValue")
	})
}

func (n *uiaCOMNode) ToggleState() (state int, err error) {
	err = n.withPattern(uiaPatternIDToggle, uiaIIDTogglePattern, "Toggle", func(p unsafe.Pointer) error {
		var v int32
		if err := uiaCheck(uiaCallP(p, uiaSlotToggleState, unsafe.Pointer(&v)), "get_CurrentToggleState"); err != nil {
			return uiaFail(err, "Toggle")
		}
		state = int(v)
		return nil
	})
	return state, err
}

func (n *uiaCOMNode) Toggle() error {
	return n.withPattern(uiaPatternIDToggle, uiaIIDTogglePattern, "Toggle", func(p unsafe.Pointer) error {
		return uiaFail(uiaCheck(uiaCall0(p, uiaSlotToggle), "Toggle"), "Toggle")
	})
}

func (n *uiaCOMNode) Select() error {
	return n.withPattern(uiaPatternIDSelectionItem, uiaIIDSelectionItemPattern, "Select", func(p unsafe.Pointer) error {
		return uiaFail(uiaCheck(uiaCall0(p, uiaSlotSelect), "Select"), "Select")
	})
}

func (n *uiaCOMNode) IsSelected() (selected bool, err error) {
	err = n.withPattern(uiaPatternIDSelectionItem, uiaIIDSelectionItemPattern, "SelectionItem", func(p unsafe.Pointer) error {
		var v int32
		if err := uiaCheck(uiaCallP(p, uiaSlotIsSelected, unsafe.Pointer(&v)), "get_CurrentIsSelected"); err != nil {
			return uiaFail(err, "SelectionItem")
		}
		selected = v != 0
		return nil
	})
	return selected, err
}

func (n *uiaCOMNode) ExpandState() (state int, err error) {
	err = n.withPattern(uiaPatternIDExpandCollapse, uiaIIDExpandCollapsePattern, "ExpandCollapse", func(p unsafe.Pointer) error {
		var v int32
		if err := uiaCheck(uiaCallP(p, uiaSlotExpandState, unsafe.Pointer(&v)), "get_CurrentExpandCollapseState"); err != nil {
			return uiaFail(err, "ExpandCollapse")
		}
		state = int(v)
		return nil
	})
	return state, err
}

func (n *uiaCOMNode) Expand() error {
	return n.withPattern(uiaPatternIDExpandCollapse, uiaIIDExpandCollapsePattern, "Expand", func(p unsafe.Pointer) error {
		return uiaFail(uiaCheck(uiaCall0(p, uiaSlotExpand), "Expand"), "Expand")
	})
}

func (n *uiaCOMNode) Collapse() error {
	return n.withPattern(uiaPatternIDExpandCollapse, uiaIIDExpandCollapsePattern, "Collapse", func(p unsafe.Pointer) error {
		return uiaFail(uiaCheck(uiaCall0(p, uiaSlotCollapse), "Collapse"), "Collapse")
	})
}
