package domain

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// 高速パスの候補（FR-G16〜G20）。純粋な値と関数だけで、外部 I/O をしない。
// 候補表はアプリが作り、Jev（TypeSafe）はその表の中から選ぶだけ。Jev の応答は、問い合わせに使った表そのものに対して検証する。

// RoleClass は UI Automation の ControlType を写した役割クラス。
type RoleClass string

const (
	RoleButton      RoleClass = "button"
	RoleMenuItem    RoleClass = "menu_item"
	RoleTabItem     RoleClass = "tab_item"
	RoleHyperlink   RoleClass = "hyperlink"
	RoleCheckBox    RoleClass = "check_box"
	RoleRadioButton RoleClass = "radio_button"
	RoleListItem    RoleClass = "list_item"
	RoleTreeItem    RoleClass = "tree_item"
	RoleComboBox    RoleClass = "combo_box"
	RoleWindow      RoleClass = "window"
)

// UIA の ControlType ID（UIAutomationClient.h の UIA_*ControlTypeId）。
const (
	uiaButton      = 50000
	uiaCheckBox    = 50002
	uiaComboBox    = 50003
	uiaHyperlink   = 50005
	uiaListItem    = 50007
	uiaMenuItem    = 50011
	uiaRadioButton = 50013
	uiaTabItem     = 50019
	uiaTreeItem    = 50024
	uiaWindow      = 50032
)

// RoleClassOf は ControlType から役割クラスを返す。表にない型（Edit・Document・Pane など）は ok=false で、候補にしない。
func RoleClassOf(controlType int) (RoleClass, bool) {
	switch controlType {
	case uiaButton:
		return RoleButton, true
	case uiaCheckBox:
		return RoleCheckBox, true
	case uiaComboBox:
		return RoleComboBox, true
	case uiaHyperlink:
		return RoleHyperlink, true
	case uiaListItem:
		return RoleListItem, true
	case uiaMenuItem:
		return RoleMenuItem, true
	case uiaRadioButton:
		return RoleRadioButton, true
	case uiaTabItem:
		return RoleTabItem, true
	case uiaTreeItem:
		return RoleTreeItem, true
	case uiaWindow:
		return RoleWindow, true
	}
	return "", false
}

// CandidateKind は候補の行動種別。v1 の高速パスに type / key / scroll はない（必要なら OpenAI へ引き継ぐ。FR-G17）。
type CandidateKind string

const (
	CandidateClick       CandidateKind = "click"
	CandidateFocusWindow CandidateKind = "focus_window"
	CandidateReobserve   CandidateKind = "reobserve"
	CandidateAbstain     CandidateKind = "abstain"
)

// メタ候補の選択肢。候補 ID は必ず "c_" で始まるので、これらと衝突しない。
const (
	ChoiceReobserve = "reobserve"
	ChoiceAbstain   = "abstain"
)

// DefaultMaxCandidates は行動候補の上限（FR-G17）。
const DefaultMaxCandidates = 24

// Candidate は候補表の 1 行。
type Candidate struct {
	ID       string // CandidateID の結果。要素の添字・実行時 ID は使わない
	Kind     CandidateKind
	Role     RoleClass
	Label    string
	Enabled  bool
	At       Point // click: 外接矩形の中心（物理座標。アプリが計算する。Jev には送らない）
	Monitor  int
	WindowID string // focus_window の対象
}

// CandidateID は候補 ID を返す: "c_" + hex(SHA-256(role, 0x00, NormalizeLabel(label), 0x00, join(path, "/")))[:12]。
// 同じ入力に同じ出力を返し、要素の並び・添字・実行時 ID に依存しない（画面が変わらなければ ID も変わらない）。
func CandidateID(role RoleClass, label string, path []string) string {
	h := sha256.New()
	h.Write([]byte(role))
	h.Write([]byte{0})
	h.Write([]byte(NormalizeLabel(label)))
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(path, "/")))
	return "c_" + hex.EncodeToString(h.Sum(nil))[:12]
}

// NormalizeLabel はラベルの比較用の形を返す: 前後の空白除去・連続する空白の圧縮・小文字化。
func NormalizeLabel(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// LabelExcluded は label が excluded のいずれかを含むか（大文字小文字・空白の揺れを区別しない部分一致。FR-S18）。
// 空の除外語は無視する（空文字列は何にでも一致してしまうため）。
func LabelExcluded(label string, excluded []string) bool {
	l := NormalizeLabel(label)
	for _, e := range excluded {
		if e = NormalizeLabel(e); e != "" && strings.Contains(l, e) {
			return true
		}
	}
	return false
}

// CandidateSource は候補表の材料。
type CandidateSource struct {
	Snapshot UISnapshot
	Windows  []WindowInfo // list_windows の結果（focus_window 用）
}

// CandidateOptions は候補表の作り方。
type CandidateOptions struct {
	Max            int      // 行動候補の上限。0 以下は DefaultMaxCandidates（24）
	ExcludedLabels []string // agent.fastpath.excluded_labels（FR-S18）
}

// CandidateTable は構築後に変更できない候補表（変更するメソッドを持たない）。reobserve / abstain は表に含まれない。
type CandidateTable struct {
	candidates []Candidate
	byID       map[string]int
	dropped    int
}

// BuildCandidateTable は FR-G17 の規則で候補表を作る。
//
//  1. 採用する要素は、有効・画面内・ラベルが空でない・Chrome でない・SameAsValue でない・外接矩形が空でない、
//     かつ RoleClassOf が ok のものだけ
//  2. LabelExcluded に当たるラベルは除く（focus_window のウィンドウ名にも適用する）
//  3. click 候補は外接矩形の（上端, 左端）の昇順（同位置は ID 順）に並べ、その後ろに list_windows から作る
//     focus_window 候補（前面・自プロセス・昇格・スナップショットのウィンドウ自身は除く）を並べる
//  4. ID が衝突する候補は、決定的にするためすべて表から落とす（Dropped に数える）
//  5. 先頭から Max 件で切り、超過分を Dropped に数える
func BuildCandidateTable(src CandidateSource, opt CandidateOptions) CandidateTable {
	limit := opt.Max
	if limit <= 0 {
		limit = DefaultMaxCandidates
	}

	all := append(clickCandidates(src.Snapshot, opt.ExcludedLabels), focusCandidates(src, opt.ExcludedLabels)...)

	counts := make(map[string]int, len(all))
	for _, c := range all {
		counts[c.ID]++
	}
	kept := make([]Candidate, 0, min(len(all), limit))
	dropped := 0
	for _, c := range all {
		if counts[c.ID] > 1 {
			dropped++
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) > limit {
		dropped += len(kept) - limit
		kept = kept[:limit]
	}

	t := CandidateTable{candidates: kept, byID: make(map[string]int, len(kept)), dropped: dropped}
	for i, c := range kept {
		t.byID[c.ID] = i
	}
	return t
}

// clickCandidates は規則 1・2 を満たす要素から click 候補を作り、外接矩形の（上端, 左端）の昇順（同位置は ID 順）に並べる。
// 並べ替えのキーは At（中心）ではなく矩形の左上。ID で同順位を決めるので、入力の並びに結果が依存しない。
func clickCandidates(snap UISnapshot, excluded []string) []Candidate {
	type keyed struct {
		Candidate
		top, left int
	}
	var ks []keyed
	for _, e := range snap.Elements {
		role, ok := RoleClassOf(e.ControlType)
		if !ok || !e.Enabled || e.Offscreen || e.Chrome || e.SameAsValue || e.Rect.Empty() {
			continue
		}
		label := cleanLabel(e.Name)
		if label == "" || LabelExcluded(label, excluded) {
			continue
		}
		ks = append(ks, keyed{
			Candidate: Candidate{
				ID:      CandidateID(role, label, e.Path),
				Kind:    CandidateClick,
				Role:    role,
				Label:   label,
				Enabled: true,
				At:      Point{X: e.Rect.X + e.Rect.W/2, Y: e.Rect.Y + e.Rect.H/2},
				Monitor: snap.Window.Monitor,
			},
			top:  e.Rect.Y,
			left: e.Rect.X,
		})
	}
	slices.SortStableFunc(ks, func(a, b keyed) int {
		return cmp.Or(cmp.Compare(a.top, b.top), cmp.Compare(a.left, b.left), cmp.Compare(a.ID, b.ID))
	})
	out := make([]Candidate, len(ks))
	for i, k := range ks {
		out[i] = k.Candidate
	}
	return out
}

// focusCandidates は list_windows の結果から focus_window 候補を作る。前面・自プロセス（オーバーレイを含む）・
// 昇格（UIPI で入力できない）のウィンドウと、スナップショットのウィンドウ自身は候補にしない。
func focusCandidates(src CandidateSource, excluded []string) []Candidate {
	var out []Candidate
	for _, w := range src.Windows {
		if w.Foreground || w.Own || w.Elevated || (w.ID != "" && w.ID == src.Snapshot.Window.ID) {
			continue
		}
		label := cleanLabel(w.Title)
		if label == "" || LabelExcluded(label, excluded) {
			continue
		}
		out = append(out, Candidate{
			ID:       CandidateID(RoleWindow, label, nil),
			Kind:     CandidateFocusWindow,
			Role:     RoleWindow,
			Label:    label,
			Enabled:  true,
			Monitor:  w.Monitor,
			WindowID: w.ID,
		})
	}
	return out
}

// cleanLabel は表示用のラベルを返す（前後の空白除去・連続する空白の圧縮。大文字小文字は残す）。
func cleanLabel(s string) string { return strings.Join(strings.Fields(s), " ") }

// Candidates は表の候補（行動候補だけ）の複製を返す。
func (t CandidateTable) Candidates() []Candidate { return slices.Clone(t.candidates) }

// Len は表の行動候補の数。
func (t CandidateTable) Len() int { return len(t.candidates) }

// Dropped は上限超過と ID 衝突で表から落とした数（ログに出す）。
func (t CandidateTable) Dropped() int { return t.dropped }

// Validate は選択を検証する。表に 1 件だけある ID、または ChoiceReobserve / ChoiceAbstain だけを受理する。
// 未知・空・重複（衝突で落とした ID）・形式不正・古い表の ID は ErrInvalidChoice を包んで返す（入力は載せない。Jev の応答は信頼しない）。
// メタ候補は Kind だけを持つ Candidate（ID は選択の文字列）で返す。
func (t CandidateTable) Validate(choice string) (Candidate, error) {
	switch choice {
	case ChoiceReobserve:
		return Candidate{ID: choice, Kind: CandidateReobserve}, nil
	case ChoiceAbstain:
		return Candidate{ID: choice, Kind: CandidateAbstain}, nil
	}
	if i, ok := t.byID[choice]; ok {
		return t.candidates[i], nil
	}
	return Candidate{}, fmt.Errorf("%w: not in the candidate table", ErrInvalidChoice)
}
