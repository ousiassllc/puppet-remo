package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// このファイルは「ウィンドウ・UI 要素の探し方」と要素 ID（#266 wait_for・#267 find_ui_element。操作系の #268 も使う）。
// 純粋な値と関数だけで、外部 I/O をしない。

// MatchMode は title・要素の name の比較方式。
type MatchMode string

const (
	// MatchExact は完全一致（序数比較。大文字小文字を区別する）。既定。
	MatchExact MatchMode = "exact"
	// MatchContains は部分一致（大文字小文字を区別しない）。
	MatchContains MatchMode = "contains"
)

// ParseMatchMode は MCP の match を MatchMode にする。空は MatchExact。未知の値は ErrInvalidArgument。
func ParseMatchMode(s string) (MatchMode, error) {
	switch MatchMode(s) {
	case "", MatchExact:
		return MatchExact, nil
	case MatchContains:
		return MatchContains, nil
	}
	return "", ErrInvalidArgument.WithMsg(fmt.Sprintf(`match must be "exact" or "contains", got %q`, s))
}

// matchText は text が want に合うかを mode で返す。
func matchText(mode MatchMode, text, want string) bool {
	if mode == MatchContains {
		return strings.Contains(strings.ToLower(text), strings.ToLower(want))
	}
	return text == want
}

// WindowLocator はトップレベルウィンドウの探し方。指定した項目はすべて満たす必要がある（AND）。
//   - WindowID: list_windows の id（HWND の 10 進文字列）。完全一致
//   - Title: Match に従う（既定は完全一致）
//   - ProcessName: プロセス名（例 "notepad.exe"）。常に大文字小文字を区別しない完全一致
type WindowLocator struct {
	WindowID    string
	Title       string
	ProcessName string
	Match       MatchMode
}

// IsZero は探し方の項目が 1 つも無いかを返す（Match は項目に数えない）。
func (l WindowLocator) IsZero() bool {
	return l.WindowID == "" && l.Title == "" && l.ProcessName == ""
}

// Validate は Match が正しいことを確かめる。
func (l WindowLocator) Validate() error {
	_, err := ParseMatchMode(string(l.Match))
	return err
}

// Matches は w が指定した項目をすべて満たすかを返す。項目が無ければ true。
func (l WindowLocator) Matches(w WindowInfo) bool {
	if l.WindowID != "" && w.ID != l.WindowID {
		return false
	}
	if l.Title != "" && !matchText(l.Match, w.Title, l.Title) {
		return false
	}
	return l.ProcessName == "" || strings.EqualFold(w.Process, l.ProcessName)
}

// MatchesAny は w が指定した項目のどれか 1 つでも（title は部分一致で）満たすかを返す。タイムアウト時の「近い候補」の並べ替え用。
// 項目が無ければ false。
func (l WindowLocator) MatchesAny(w WindowInfo) bool {
	return (l.WindowID != "" && w.ID == l.WindowID) ||
		(l.Title != "" && matchText(MatchContains, w.Title, l.Title)) ||
		(l.ProcessName != "" && strings.EqualFold(w.Process, l.ProcessName))
}

// ElementLocator はウィンドウの中の UI 要素の探し方。指定した項目はすべて満たす必要がある（AND）。1 項目は必須。
//   - Name: UIA の Name。Match に従う（既定は完全一致）
//   - AutomationID: UIA の AutomationId。常に完全一致（序数比較）
//   - ControlType: UIA の ControlType の名前（"Button"、"Edit" など。大文字小文字は区別しない）
type ElementLocator struct {
	Name         string
	AutomationID string
	ControlType  string
	Match        MatchMode
}

// IsZero は探し方の項目が 1 つも無いかを返す。
func (l ElementLocator) IsZero() bool {
	return l.Name == "" && l.AutomationID == "" && l.ControlType == ""
}

// Validate は項目が 1 つ以上あり、Match と ControlType が正しいことを確かめる（ErrInvalidArgument）。
func (l ElementLocator) Validate() error {
	if l.IsZero() {
		return ErrInvalidArgument.WithMsg("at least one of name, automation_id and control_type is required")
	}
	if _, err := ParseMatchMode(string(l.Match)); err != nil {
		return err
	}
	if l.ControlType != "" {
		if _, ok := CanonicalUIControlType(l.ControlType); !ok {
			return ErrInvalidArgument.WithMsg(fmt.Sprintf("unknown control_type %q (use a UI Automation ControlType name such as Button, Edit, CheckBox)", l.ControlType))
		}
	}
	return nil
}

// Matches は e が指定した項目をすべて満たすかを返す。
func (l ElementLocator) Matches(e UIElementInfo) bool {
	if l.Name != "" && !matchText(l.Match, e.Name, l.Name) {
		return false
	}
	if l.AutomationID != "" && e.AutomationID != l.AutomationID {
		return false
	}
	return l.ControlType == "" || strings.EqualFold(e.ControlType, l.ControlType)
}

// MatchesAny は e が指定した項目のどれか 1 つでも（name・automation_id は部分一致、control_type は一致で）満たすかを返す。
// タイムアウト時の「近い候補」を集めるための緩い判定。項目が無ければ false。
func (l ElementLocator) MatchesAny(e UIElementInfo) bool {
	return (l.Name != "" && matchText(MatchContains, e.Name, l.Name)) ||
		(l.AutomationID != "" && matchText(MatchContains, e.AutomationID, l.AutomationID)) ||
		(l.ControlType != "" && strings.EqualFold(e.ControlType, l.ControlType))
}

// uiControlTypeNames は UIA の ControlType ID → 名前（UIAutomationClient.h の UIA_*ControlTypeId から Control を除いた名前）。
var uiControlTypeNames = map[int]string{
	50000: "Button", 50001: "Calendar", 50002: "CheckBox", 50003: "ComboBox", 50004: "Edit",
	50005: "Hyperlink", 50006: "Image", 50007: "ListItem", 50008: "List", 50009: "Menu",
	50010: "MenuBar", 50011: "MenuItem", 50012: "ProgressBar", 50013: "RadioButton", 50014: "ScrollBar",
	50015: "Slider", 50016: "Spinner", 50017: "StatusBar", 50018: "Tab", 50019: "TabItem",
	50020: "Text", 50021: "ToolBar", 50022: "ToolTip", 50023: "Tree", 50024: "TreeItem",
	50025: "Custom", 50026: "Group", 50027: "Thumb", 50028: "DataGrid", 50029: "DataItem",
	50030: "Document", 50031: "SplitButton", 50032: "Window", 50033: "Pane", 50034: "Header",
	50035: "HeaderItem", 50036: "Table", 50037: "TitleBar", 50038: "Separator", 50039: "SemanticZoom",
	50040: "AppBar",
}

// UIControlTypeName は ControlType ID の名前（"Button"、"Window"）。未知の型は "Unknown"。
func UIControlTypeName(id int) string {
	if n, ok := uiControlTypeNames[id]; ok {
		return n
	}
	return "Unknown"
}

// CanonicalUIControlType は ControlType の名前（大文字小文字を区別しない）を正式な綴りにする。知らない名前は ok=false。
func CanonicalUIControlType(name string) (string, bool) {
	for _, n := range uiControlTypeNames {
		if strings.EqualFold(n, name) {
			return n, true
		}
	}
	return "", false
}

// ElementRef は UI 要素 ID の中身: ウィンドウの HWND・そのプロセスの PID・要素の UIA RuntimeId。
// 文字列は "uie:<HWND の 10 進>:<PID の 10 進>:<RuntimeId の各値の 16 進を . でつなぐ>"（例 "uie:197382:4812:2a.1f4.3"）。
// 不透明な値として扱うこと（get_ui_tree・find_ui_element が返し、他のツールが受け取る）。
// 受け取る側は必ず生きた木で引き直し、なければ ErrStaleElement にする（古い ID が別の要素に当たってはならない）。
type ElementRef struct {
	HWND      uint64
	PID       uint32
	RuntimeID []int32
}

const elementRefPrefix = "uie:"

// String は ref を要素 ID の文字列にする。RuntimeID が無い（引き直せない）ref は空文字列。
func (r ElementRef) String() string {
	if len(r.RuntimeID) == 0 {
		return ""
	}
	parts := make([]string, len(r.RuntimeID))
	for i, v := range r.RuntimeID {
		parts[i] = strconv.FormatUint(uint64(uint32(v)), 16)
	}
	return elementRefPrefix + strconv.FormatUint(r.HWND, 10) + ":" + strconv.FormatUint(uint64(r.PID), 10) + ":" + strings.Join(parts, ".")
}

// WindowID は要素のウィンドウの ID（list_windows の id と同じ形式）。
func (r ElementRef) WindowID() string { return strconv.FormatUint(r.HWND, 10) }

// ParseElementRef は要素 ID の文字列を ElementRef にする。形が違えば ErrInvalidArgument（古いのではなく、そもそも要素 ID でない）。
func ParseElementRef(s string) (ElementRef, error) {
	bad := func() (ElementRef, error) {
		return ElementRef{}, ErrInvalidArgument.WithMsg("element_id is not a valid element id (use an id returned by find_ui_element or get_ui_tree)")
	}
	rest, ok := strings.CutPrefix(s, elementRefPrefix)
	if !ok {
		return bad()
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 {
		return bad()
	}
	hwnd, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || hwnd == 0 {
		return bad()
	}
	pid, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return bad()
	}
	var id []int32
	for _, p := range strings.Split(parts[2], ".") {
		v, err := strconv.ParseUint(p, 16, 32)
		if err != nil {
			return bad()
		}
		id = append(id, int32(uint32(v)))
	}
	ref := ElementRef{HWND: hwnd, PID: uint32(pid), RuntimeID: id}
	if ref.String() != s { // 先頭ゼロ・大文字などの別表記は受け付けない（同じ要素に複数の ID を作らない）
		return bad()
	}
	return ref, nil
}
