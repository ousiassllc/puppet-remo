//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	ole "github.com/go-ole/go-ole"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// IUIAutomation の COM バインディング（cgo なし）。go-ole は COM の初期化・インスタンス生成・VARIANT・BSTR だけに使い、
// vtable のスロット番号は自前で持つ。番号は UIAutomationClient.h の IUIAutomation / IUIAutomationElement /
// IUIAutomationCacheRequest / IUIAutomationElementArray の宣言順（IUnknown の 3 メソッドに続く）。
const (
	uiaSlotRelease = 2

	// IUIAutomation
	uiaSlotElementFromHandleBuildCache = 10
	uiaSlotControlViewCondition        = 18
	uiaSlotCreateCacheRequest          = 20

	// IUIAutomationElement
	uiaSlotFindAllBuildCache      = 8
	uiaSlotGetCachedPropertyValue = 12
	uiaSlotCachedControlType      = 53
	uiaSlotCachedName             = 55
	uiaSlotCachedIsEnabled        = 60
	uiaSlotCachedAutomationID     = 61
	uiaSlotCachedIsPassword       = 67
	uiaSlotCachedIsOffscreen      = 70
	uiaSlotCachedBoundingRect     = 75

	// IUIAutomationCacheRequest
	uiaSlotAddProperty  = 3
	uiaSlotPutTreeScope = 7

	// IUIAutomationElementArray
	uiaSlotArrayLength     = 3
	uiaSlotArrayGetElement = 4
)

// TreeScope。
const (
	uiaTreeScopeElement  = 0x1
	uiaTreeScopeChildren = 0x2
)

// キャッシュするプロパティの ID（UIAutomationClient.h の UIA_*PropertyId）。
const (
	uiaPropRuntimeID         = 30000
	uiaPropBoundingRectangle = 30001
	uiaPropControlType       = 30003
	uiaPropName              = 30005
	uiaPropIsEnabled         = 30010
	uiaPropAutomationID      = 30011
	uiaPropIsPassword        = 30019
	uiaPropIsOffscreen       = 30022
	uiaPropValueValue        = 30045
	uiaPropSelectionIsSel    = 30079
	uiaPropToggleState       = 30086
)

var (
	uiaCLSIDAutomation = ole.NewGUID("{ff48dba4-60ef-4201-aa87-54103eef594e}") // CLSID_CUIAutomation
	uiaIIDAutomation   = ole.NewGUID("{30cbe57d-d9d0-452a-ab13-7ac5ac4825ee}") // IID_IUIAutomation
)

// 要素が消えた・ウィンドウが無い、を表す HRESULT。
const (
	uiaEElementNotAvailable = 0x80040201 // UIA_E_ELEMENTNOTAVAILABLE
	uiaEInvalidArg          = 0x80070057 // E_INVALIDARG
	uiaEInvalidWindow       = 0x80070578 // HRESULT_FROM_WIN32(ERROR_INVALID_WINDOW_HANDLE)
)

// uiaHRESULT は失敗した COM 呼び出しの HRESULT。
type uiaHRESULT struct {
	what string
	code uint32
}

func (e uiaHRESULT) Error() string { return fmt.Sprintf("uia: %s: HRESULT 0x%08X", e.what, e.code) }

func (e uiaHRESULT) elementGone() bool {
	return e.code == uiaEElementNotAvailable || e.code == uiaEInvalidArg || e.code == uiaEInvalidWindow
}

func uiaCheck(r uintptr, what string) error {
	if int32(r) < 0 {
		return uiaHRESULT{what: what, code: uint32(r)}
	}
	return nil
}

// --- vtable 呼び出し。ポインタの引数は unsafe.Pointer のまま SyscallN の呼び出し式の中で uintptr にする（GC・スタック移動の規則）---

func uiaSlot(obj unsafe.Pointer, slot uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	return *(*uintptr)(unsafe.Add(vtbl, slot*unsafe.Sizeof(uintptr(0))))
}

func uiaCall0(obj unsafe.Pointer, slot uintptr) uintptr {
	r, _, _ := syscall.SyscallN(uiaSlot(obj, slot), uintptr(obj))
	return r
}

func uiaCallI(obj unsafe.Pointer, slot, a uintptr) uintptr {
	r, _, _ := syscall.SyscallN(uiaSlot(obj, slot), uintptr(obj), a)
	return r
}

func uiaCallP(obj unsafe.Pointer, slot uintptr, p unsafe.Pointer) uintptr {
	r, _, _ := syscall.SyscallN(uiaSlot(obj, slot), uintptr(obj), uintptr(p))
	return r
}

func uiaCallIP(obj unsafe.Pointer, slot, a uintptr, p unsafe.Pointer) uintptr {
	r, _, _ := syscall.SyscallN(uiaSlot(obj, slot), uintptr(obj), a, uintptr(p))
	return r
}

func uiaCallIPP(obj unsafe.Pointer, slot, a uintptr, p, q unsafe.Pointer) uintptr {
	r, _, _ := syscall.SyscallN(uiaSlot(obj, slot), uintptr(obj), a, uintptr(p), uintptr(q))
	return r
}

func uiaCallIPPP(obj unsafe.Pointer, slot, a uintptr, p, q, s unsafe.Pointer) uintptr {
	r, _, _ := syscall.SyscallN(uiaSlot(obj, slot), uintptr(obj), a, uintptr(p), uintptr(q), uintptr(s))
	return r
}

func uiaRelease(obj unsafe.Pointer) {
	if obj != nil {
		uiaCall0(obj, uiaSlotRelease)
	}
}

// NewUIAutomation は専用の COM スレッドを起動して IUIAutomation を作る。使い終えたら Close を呼ぶ。
// windows はウィンドウ ID（HWND の 10 進文字列）の解決と、WindowInfo の取得に使う。
func NewUIAutomation(windows platform.WindowManager) (*UIAutomation, error) {
	return newUIAutomation(windows, openUIACOM)
}

// uiaCOM は専用スレッドの上でだけ使う（MTA に参加したスレッド）。
type uiaCOM struct {
	automation unsafe.Pointer // IUIAutomation
	cache      unsafe.Pointer // IUIAutomationCacheRequest（TreeScope_Element。使い回す）
	condition  unsafe.Pointer // コントロールビューの条件
}

func openUIACOM() (uiaBackend, func(), error) {
	// 専用スレッドは MTA で動かす（メッセージポンプを持たないので STA にしない）。
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		return nil, nil, fmt.Errorf("uia: CoInitializeEx: %w", err)
	}
	unk, err := ole.CreateInstance(uiaCLSIDAutomation, uiaIIDAutomation)
	if err != nil {
		ole.CoUninitialize()
		return nil, nil, fmt.Errorf("uia: create IUIAutomation: %w", err)
	}
	c := &uiaCOM{automation: unsafe.Pointer(unk)}
	cleanup := func() {
		uiaRelease(c.condition)
		uiaRelease(c.cache)
		uiaRelease(c.automation)
		ole.CoUninitialize()
	}
	if err := c.init(); err != nil {
		cleanup()
		return nil, nil, err
	}
	return c, cleanup, nil
}

func (c *uiaCOM) init() error {
	if err := uiaCheck(uiaCallP(c.automation, uiaSlotCreateCacheRequest, unsafe.Pointer(&c.cache)), "CreateCacheRequest"); err != nil {
		return err
	}
	for _, id := range []uintptr{
		uiaPropRuntimeID, uiaPropBoundingRectangle, uiaPropControlType, uiaPropName, uiaPropIsEnabled, uiaPropAutomationID,
		uiaPropIsPassword, uiaPropIsOffscreen, uiaPropValueValue, uiaPropSelectionIsSel, uiaPropToggleState,
	} {
		if err := uiaCheck(uiaCallI(c.cache, uiaSlotAddProperty, id), "CacheRequest.AddProperty"); err != nil {
			return err
		}
	}
	if err := uiaCheck(uiaCallI(c.cache, uiaSlotPutTreeScope, uiaTreeScopeElement), "CacheRequest.put_TreeScope"); err != nil {
		return err
	}
	return uiaCheck(uiaCallP(c.automation, uiaSlotControlViewCondition, unsafe.Pointer(&c.condition)), "get_ControlViewCondition")
}

func (c *uiaCOM) Root(hwnd uintptr) (uiaNode, error) {
	var elem unsafe.Pointer
	err := uiaCheck(uiaCallIPP(c.automation, uiaSlotElementFromHandleBuildCache, hwnd, c.cache, unsafe.Pointer(&elem)), "ElementFromHandleBuildCache")
	if err != nil {
		var hr uiaHRESULT
		if errors.As(err, &hr) && hr.elementGone() {
			return nil, domain.ErrWindowNotFound.WithMsg("the window is gone")
		}
		return nil, err
	}
	if elem == nil {
		return nil, domain.ErrWindowNotFound.WithMsg("the window has no UI Automation element")
	}
	return &uiaCOMNode{c: c, elem: elem}, nil
}

// uiaCOMNode は IUIAutomationElement 1 つ。プロパティはキャッシュ済み（取得時の CacheRequest）を読む。
type uiaCOMNode struct {
	c    *uiaCOM
	elem unsafe.Pointer
}

func (n *uiaCOMNode) Release() { uiaRelease(n.elem) }

// Children はコントロールビューの直接の子を、プロパティつきで 1 回の FindAllBuildCache で取る。
func (n *uiaCOMNode) Children(ctx context.Context) ([]uiaNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var arr unsafe.Pointer
	err := uiaCheck(uiaCallIPPP(n.elem, uiaSlotFindAllBuildCache, uiaTreeScopeChildren, n.c.condition, n.c.cache, unsafe.Pointer(&arr)), "FindAllBuildCache")
	if err != nil {
		var hr uiaHRESULT
		if errors.As(err, &hr) && hr.code == uiaEElementNotAvailable {
			return nil, nil // 走査中に消えた要素は子なしとして扱う
		}
		return nil, err
	}
	if arr == nil {
		return nil, nil
	}
	defer uiaRelease(arr)
	var length int32
	if err := uiaCheck(uiaCallP(arr, uiaSlotArrayLength, unsafe.Pointer(&length)), "ElementArray.get_Length"); err != nil {
		return nil, err
	}
	kids := make([]uiaNode, 0, length)
	for i := int32(0); i < length; i++ {
		var e unsafe.Pointer
		if err := uiaCheck(uiaCallIP(arr, uiaSlotArrayGetElement, uintptr(i), unsafe.Pointer(&e)), "ElementArray.GetElement"); err != nil {
			uiaReleaseAll(kids)
			return nil, err
		}
		if e != nil {
			kids = append(kids, &uiaCOMNode{c: n.c, elem: e})
		}
	}
	return kids, nil
}

// Props はキャッシュ済みのプロパティを読む。個々の取得に失敗した項目は零値のままにする。
// パスワード欄の値は読まない。
func (n *uiaCOMNode) Props() uiaProps {
	p := uiaProps{
		RuntimeID:    n.runtimeID(),
		ControlType:  int(n.int32Prop(uiaSlotCachedControlType)),
		Name:         n.stringProp(uiaSlotCachedName),
		AutomationID: n.stringProp(uiaSlotCachedAutomationID),
		IsPassword:   n.boolProp(uiaSlotCachedIsPassword),
		Enabled:      n.boolProp(uiaSlotCachedIsEnabled),
		Offscreen:    n.boolProp(uiaSlotCachedIsOffscreen),
		Rect:         n.rect(),
	}
	if !p.IsPassword {
		p.Value = n.valueProp()
	}
	if v, ok := n.variantProp(uiaPropToggleState); ok {
		if v.VT == ole.VT_I4 {
			p.ToggleState = int(int32(v.Val))
		}
		v.Clear()
	}
	if v, ok := n.variantProp(uiaPropSelectionIsSel); ok {
		if v.VT == ole.VT_BOOL {
			p.Selected = v.Val&0xFFFF != 0
		}
		v.Clear()
	}
	return p
}

// runtimeID はキャッシュ済みの RuntimeId（VT_ARRAY | VT_I4）を読む。SAFEARRAY は VARIANT の Clear が解放する。
func (n *uiaCOMNode) runtimeID() []int32 {
	v, ok := n.variantProp(uiaPropRuntimeID)
	if !ok {
		return nil
	}
	defer v.Clear()
	if v.VT != ole.VT_ARRAY|ole.VT_I4 {
		return nil
	}
	sa := *(**ole.SafeArray)(unsafe.Pointer(&v.Val))
	if sa == nil {
		return nil
	}
	conv := &ole.SafeArrayConversion{Array: sa}
	var id []int32
	for _, e := range conv.ToValueArray() {
		if x, ok := e.(int32); ok {
			id = append(id, x)
		}
	}
	return id
}

func (n *uiaCOMNode) int32Prop(slot uintptr) int32 {
	var v int32
	if uiaCheck(uiaCallP(n.elem, slot, unsafe.Pointer(&v)), "get_Cached") != nil {
		return 0
	}
	return v
}

func (n *uiaCOMNode) boolProp(slot uintptr) bool { return n.int32Prop(slot) != 0 }

func (n *uiaCOMNode) stringProp(slot uintptr) string {
	var b *uint16
	if uiaCheck(uiaCallP(n.elem, slot, unsafe.Pointer(&b)), "get_Cached") != nil || b == nil {
		return ""
	}
	s := ole.BstrToString(b)
	_ = ole.SysFreeString((*int16)(unsafe.Pointer(b)))
	return s
}

func (n *uiaCOMNode) rect() domain.Rect {
	var r struct{ left, top, right, bottom int32 }
	if uiaCheck(uiaCallP(n.elem, uiaSlotCachedBoundingRect, unsafe.Pointer(&r)), "get_CachedBoundingRectangle") != nil {
		return domain.Rect{}
	}
	return domain.Rect{X: int(r.left), Y: int(r.top), W: int(r.right - r.left), H: int(r.bottom - r.top)}
}

// variantProp はキャッシュ済みのプロパティを VARIANT で返す。ok のとき、呼び出し側が Clear する。
func (n *uiaCOMNode) variantProp(id uintptr) (v ole.VARIANT, ok bool) {
	if uiaCheck(uiaCallIP(n.elem, uiaSlotGetCachedPropertyValue, id, unsafe.Pointer(&v)), "GetCachedPropertyValue") != nil {
		return ole.VARIANT{}, false
	}
	return v, true
}

// valueProp は ValuePattern の値を返す。非対応の要素は空文字。
func (n *uiaCOMNode) valueProp() string {
	v, ok := n.variantProp(uiaPropValueValue)
	if !ok {
		return ""
	}
	defer v.Clear()
	return v.ToString()
}
