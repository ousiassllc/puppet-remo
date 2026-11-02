package domain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// UIA の ControlType ID（FR-G17 の役割クラスの写像を、実装とは別に固定する）。
const (
	ctButton   = 50000
	ctCheckBox = 50002
	ctComboBox = 50003
	ctEdit     = 50004
	ctLink     = 50005
	ctListItem = 50007
	ctMenuItem = 50011
	ctRadio    = 50013
	ctTabItem  = 50019
	ctTreeItem = 50024
	ctWindow   = 50032
	ctPane     = 50033
)

func btn(name string, x, y, w, h int, path ...string) domain.UIElement {
	return domain.UIElement{ControlType: ctButton, Name: name, Enabled: true, Rect: domain.Rect{X: x, Y: y, W: w, H: h}, Path: path}
}

func snapshotOf(els ...domain.UIElement) domain.UISnapshot {
	return domain.UISnapshot{Window: domain.WindowInfo{ID: "100", Title: "app", Foreground: true, Monitor: 2}, Elements: els}
}

func buildClicks(els ...domain.UIElement) domain.CandidateTable {
	return domain.BuildCandidateTable(domain.CandidateSource{Snapshot: snapshotOf(els...)}, domain.CandidateOptions{})
}

func labelsOf(t domain.CandidateTable) []string {
	var out []string
	for _, c := range t.Candidates() {
		out = append(out, c.Label)
	}
	return out
}

func TestRoleClassOf(t *testing.T) {
	tests := []struct {
		controlType int
		want        domain.RoleClass
		ok          bool
	}{
		{ctButton, domain.RoleButton, true},
		{ctCheckBox, domain.RoleCheckBox, true},
		{ctComboBox, domain.RoleComboBox, true},
		{ctLink, domain.RoleHyperlink, true},
		{ctListItem, domain.RoleListItem, true},
		{ctMenuItem, domain.RoleMenuItem, true},
		{ctRadio, domain.RoleRadioButton, true},
		{ctTabItem, domain.RoleTabItem, true},
		{ctTreeItem, domain.RoleTreeItem, true},
		{ctWindow, domain.RoleWindow, true},
		{ctEdit, "", false},
		{50030 /* Document */, "", false},
		{ctPane, "", false},
		{50020 /* Text */, "", false},
		{0, "", false},
		{-1, "", false},
	}
	for _, tt := range tests {
		got, ok := domain.RoleClassOf(tt.controlType)
		if got != tt.want || ok != tt.ok {
			t.Errorf("RoleClassOf(%d) = %q, %v; want %q, %v", tt.controlType, got, ok, tt.want, tt.ok)
		}
	}
}

func TestNormalizeLabel(t *testing.T) {
	tests := map[string]string{
		"OK":               "ok",
		"  Save  As \t…\n": "save as …",
		"":                 "",
		"   ":              "",
		"名前を付けて\u3000保存":   "名前を付けて 保存",
		"ÄB":               "äb",
	}
	for in, want := range tests {
		if got := domain.NormalizeLabel(in); got != want {
			t.Errorf("NormalizeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLabelExcluded(t *testing.T) {
	defaults := []string{"delete", "remove", "send", "submit", "purchase", "buy", "pay", "close", "quit", "exit",
		"uninstall", "format", "erase", "削除", "送信", "購入", "支払い", "閉じる", "終了", "アンインストール", "初期化"}
	tests := []struct {
		label    string
		excluded []string
		want     bool
	}{
		{"Delete", defaults, true},
		{"Delete all files", defaults, true},
		{"&Send message", defaults, true},
		{"UNINSTALL", defaults, true},
		{"アカウントを削除", defaults, true},
		{"Open", defaults, false},
		{"保存", defaults, false},
		{"Reformat  disk", []string{"FORMAT"}, true},
		{"Pay  now", []string{"pay now"}, true},
		{"Open", nil, false},
		{"Open", []string{""}, false}, // 空の除外語は何にも一致させない（全除外を防ぐ）
		{"Open", []string{"  "}, false},
		{"", defaults, false},
	}
	for _, tt := range tests {
		if got := domain.LabelExcluded(tt.label, tt.excluded); got != tt.want {
			t.Errorf("LabelExcluded(%q, %v) = %v, want %v", tt.label, tt.excluded, got, tt.want)
		}
	}
}

func TestCandidateID(t *testing.T) {
	t.Run("形式: c_ + SHA-256(role, 0x00, 正規化ラベル, 0x00, パスを / で連結) の先頭 12 桁", func(t *testing.T) {
		sum := sha256.Sum256([]byte("button\x00save as\x00window:app/tab_item:file"))
		want := "c_" + hex.EncodeToString(sum[:])[:12]
		if got := domain.CandidateID(domain.RoleButton, "  Save   AS ", []string{"window:app", "tab_item:file"}); got != want {
			t.Errorf("CandidateID = %q, want %q", got, want)
		}
		sum = sha256.Sum256([]byte("window\x00notepad\x00"))
		want = "c_" + hex.EncodeToString(sum[:])[:12]
		if got := domain.CandidateID(domain.RoleWindow, "Notepad", nil); got != want {
			t.Errorf("CandidateID(nil path) = %q, want %q", got, want)
		}
	})

	t.Run("ラベルの空白・大文字小文字の揺れでは変わらず、役割・ラベル・パスが違えば変わる", func(t *testing.T) {
		base := domain.CandidateID(domain.RoleButton, "OK", []string{"a"})
		if got := domain.CandidateID(domain.RoleButton, " ok ", []string{"a"}); got != base {
			t.Errorf("normalization-equivalent label gave %q, want %q", got, base)
		}
		for name, other := range map[string]string{
			"role":   domain.CandidateID(domain.RoleMenuItem, "OK", []string{"a"}),
			"label":  domain.CandidateID(domain.RoleButton, "Cancel", []string{"a"}),
			"path":   domain.CandidateID(domain.RoleButton, "OK", []string{"b"}),
			"nopath": domain.CandidateID(domain.RoleButton, "OK", nil),
		} {
			if other == base {
				t.Errorf("changing the %s did not change the ID", name)
			}
		}
	})
}

// 候補 ID は要素の並び・添字に依存しない（FR-G17 の不変条件）。
func TestCandidateIDIndependentOfOrderAndIndex(t *testing.T) {
	els := []domain.UIElement{
		btn("OK", 10, 10, 20, 20, "window:app"),
		btn("Cancel", 40, 10, 20, 20, "window:app"),
		btn("Help", 10, 50, 20, 20, "window:app", "menu_item:help"),
		{ControlType: ctCheckBox, Name: "Remember", Enabled: true, Rect: domain.Rect{X: 5, Y: 90, W: 10, H: 10}},
		{ControlType: ctTabItem, Name: "General", Enabled: true, Rect: domain.Rect{X: 5, Y: 0, W: 10, H: 5}},
	}
	want := buildClicks(els...).Candidates()

	rng := rand.New(rand.NewPCG(1, 2))
	for range 50 {
		shuffled := slices.Clone(els)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if got := buildClicks(shuffled...).Candidates(); !slices.Equal(got, want) {
			t.Fatalf("shuffled input gave a different table:\n got %+v\nwant %+v", got, want)
		}
	}

	// 先頭に無関係な要素を差し込んでも（添字がずれても）既存の ID は変わらない。
	withExtra := buildClicks(append([]domain.UIElement{btn("Extra", 1, 1, 5, 5)}, els...)...)
	ids := map[string]bool{}
	for _, c := range withExtra.Candidates() {
		ids[c.ID] = true
	}
	for _, c := range want {
		if !ids[c.ID] {
			t.Errorf("ID of %q changed when an unrelated element was inserted", c.Label)
		}
	}
}

func TestBuildCandidateTableAdoptionRules(t *testing.T) {
	ok := btn("OK", 10, 10, 20, 20)
	disabled := ok
	disabled.Enabled = false
	offscreen := ok
	offscreen.Offscreen = true
	unlabeled := btn("   ", 10, 10, 20, 20)
	chrome := btn("Close", 0, 0, 5, 5)
	chrome.Chrome = true
	sameAsValue := btn("hello", 10, 10, 20, 20)
	sameAsValue.SameAsValue = true
	edit := domain.UIElement{ControlType: ctEdit, Name: "Search", Enabled: true, Rect: domain.Rect{X: 1, Y: 1, W: 5, H: 5}}
	empty := btn("Zero", 10, 10, 0, 20)

	tests := []struct {
		name string
		el   domain.UIElement
		want int // 表に残る件数
	}{
		{"有効・画面内・ラベルあり・役割あり", ok, 1},
		{"無効", disabled, 0},
		{"画面外", offscreen, 0},
		{"ラベルが空白だけ", unlabeled, 0},
		{"Chrome", chrome, 0},
		{"ラベルが値と同一", sameAsValue, 0},
		{"役割クラスの表にない型（Edit）", edit, 0},
		{"外接矩形が空（中心が定まらない）", empty, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := buildClicks(tt.el)
			if table.Len() != tt.want || len(table.Candidates()) != tt.want {
				t.Errorf("Len = %d (candidates %d), want %d", table.Len(), len(table.Candidates()), tt.want)
			}
			if table.Dropped() != 0 {
				t.Errorf("Dropped = %d, want 0 (adoption filters are not drops)", table.Dropped())
			}
		})
	}

	t.Run("click 候補の中身: 矩形の中心・モニター・正規化前のラベル", func(t *testing.T) {
		el := btn("  Save   As ", 100, 200, 41, 31, "window:app")
		table := buildClicks(el)
		got := table.Candidates()
		if len(got) != 1 {
			t.Fatalf("candidates = %+v", got)
		}
		c := got[0]
		if c.Kind != domain.CandidateClick || c.Role != domain.RoleButton || c.Label != "Save As" || !c.Enabled ||
			c.At != (domain.Point{X: 120, Y: 215}) || c.Monitor != 2 || c.WindowID != "" ||
			c.ID != domain.CandidateID(domain.RoleButton, "Save As", []string{"window:app"}) {
			t.Errorf("candidate = %+v", c)
		}
	})

	t.Run("各役割クラスが候補になる", func(t *testing.T) {
		var els []domain.UIElement
		for i, ct := range []int{ctButton, ctCheckBox, ctComboBox, ctLink, ctListItem, ctMenuItem, ctRadio, ctTabItem, ctTreeItem, ctWindow} {
			els = append(els, domain.UIElement{ControlType: ct, Name: fmt.Sprint("e", i), Enabled: true, Rect: domain.Rect{X: 0, Y: i * 10, W: 5, H: 5}})
		}
		if table := buildClicks(els...); table.Len() != len(els) {
			t.Errorf("Len = %d, want %d", table.Len(), len(els))
		}
	})
}

func TestBuildCandidateTableExcludedLabels(t *testing.T) {
	src := domain.CandidateSource{
		Snapshot: snapshotOf(btn("Delete file", 0, 0, 5, 5), btn("Open", 0, 10, 5, 5), btn("送信する", 0, 20, 5, 5)),
		Windows: []domain.WindowInfo{
			{ID: "1", Title: "Pay now"},
			{ID: "2", Title: "Notes"},
		},
	}
	table := domain.BuildCandidateTable(src, domain.CandidateOptions{ExcludedLabels: []string{"DELETE", "送信", "pay"}})
	if got, want := labelsOf(table), []string{"Open", "Notes"}; !slices.Equal(got, want) {
		t.Errorf("labels = %v, want %v (excluded labels apply to elements and window titles)", got, want)
	}
	if table.Dropped() != 0 {
		t.Errorf("Dropped = %d, want 0", table.Dropped())
	}
}

func TestBuildCandidateTableOrdering(t *testing.T) {
	src := domain.CandidateSource{
		Snapshot: snapshotOf(
			btn("right-low", 200, 100, 10, 10),
			btn("left-low", 10, 100, 10, 10),
			btn("right-high", 200, 10, 10, 10),
			btn("left-high", 10, 10, 10, 10),
			// 上端が小さい大きな要素: 中心ではなく矩形の（上端, 左端）で並ぶ
			btn("tall", 300, 0, 10, 1000),
		),
		Windows: []domain.WindowInfo{{ID: "7", Title: "Other"}, {ID: "8", Title: "Another"}},
	}
	table := domain.BuildCandidateTable(src, domain.CandidateOptions{})
	want := []string{"tall", "left-high", "right-high", "left-low", "right-low", "Other", "Another"}
	if got := labelsOf(table); !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestBuildCandidateTableFocusWindow(t *testing.T) {
	src := domain.CandidateSource{
		Snapshot: snapshotOf(),
		Windows: []domain.WindowInfo{
			{ID: "1", Title: "Foreground", Foreground: true},
			{ID: "100", Title: "Same as snapshot window"},
			{ID: "2", Title: "Own window", Own: true},
			{ID: "3", Title: "Overlay", Own: true, Overlay: true},
			{ID: "4", Title: "Admin tool", Elevated: true},
			{ID: "5", Title: "   "},
			{ID: "6", Title: "Browser", Process: "browser.exe", Monitor: 1},
			{ID: "9", Title: "Minimized memo", Minimized: true, Monitor: 0},
		},
	}
	table := domain.BuildCandidateTable(src, domain.CandidateOptions{})
	got := table.Candidates()
	if len(got) != 2 {
		t.Fatalf("candidates = %+v, want only Browser and Minimized memo", got)
	}
	want0 := domain.Candidate{
		ID: domain.CandidateID(domain.RoleWindow, "Browser", nil), Kind: domain.CandidateFocusWindow,
		Role: domain.RoleWindow, Label: "Browser", Enabled: true, Monitor: 1, WindowID: "6",
	}
	if got[0] != want0 {
		t.Errorf("candidate[0] = %+v, want %+v", got[0], want0)
	}
	if got[1].WindowID != "9" || got[1].Kind != domain.CandidateFocusWindow {
		t.Errorf("candidate[1] = %+v", got[1])
	}
	if got[0].At != (domain.Point{}) {
		t.Errorf("focus_window has no click point, got %+v", got[0].At)
	}
}

func TestBuildCandidateTableCollisions(t *testing.T) {
	t.Run("ID が衝突する候補はすべて落とし、Dropped に数える", func(t *testing.T) {
		// 同じ役割・ラベル（正規化後）・パス → 同じ ID。
		table := buildClicks(
			btn("OK", 0, 0, 5, 5, "p"),
			btn(" ok ", 0, 20, 5, 5, "p"),
			btn("OK", 0, 40, 5, 5, "p"),
			btn("OK", 0, 60, 5, 5, "q"), // パスが違えば別の ID
			btn("Cancel", 0, 80, 5, 5, "p"),
		)
		if got, want := labelsOf(table), []string{"OK", "Cancel"}; !slices.Equal(got, want) {
			t.Errorf("labels = %v, want %v", got, want)
		}
		if table.Dropped() != 3 {
			t.Errorf("Dropped = %d, want 3", table.Dropped())
		}
	})

	t.Run("衝突した ID は Validate が受理しない", func(t *testing.T) {
		a, b := btn("OK", 0, 0, 5, 5), btn("OK", 0, 20, 5, 5)
		table := buildClicks(a, b)
		if table.Len() != 0 {
			t.Fatalf("Len = %d, want 0", table.Len())
		}
		id := domain.CandidateID(domain.RoleButton, "OK", nil)
		if _, err := table.Validate(id); !errors.Is(err, domain.ErrInvalidChoice) {
			t.Errorf("Validate(collided id) err = %v, want ErrInvalidChoice", err)
		}
	})

	t.Run("click と focus_window の間の衝突も落とす", func(t *testing.T) {
		// 役割 window・パスなしの click 要素と、同名のウィンドウ。
		el := domain.UIElement{ControlType: ctWindow, Name: "Dialog", Enabled: true, Rect: domain.Rect{W: 5, H: 5}}
		src := domain.CandidateSource{Snapshot: snapshotOf(el), Windows: []domain.WindowInfo{{ID: "1", Title: "Dialog"}}}
		table := domain.BuildCandidateTable(src, domain.CandidateOptions{})
		if table.Len() != 0 || table.Dropped() != 2 {
			t.Errorf("Len = %d, Dropped = %d; want 0 and 2", table.Len(), table.Dropped())
		}
	})
}

func TestBuildCandidateTableCap(t *testing.T) {
	manyButtons := func(n int) []domain.UIElement {
		var els []domain.UIElement
		for i := range n {
			els = append(els, btn(fmt.Sprint("b", i), 0, i*10, 5, 5))
		}
		return els
	}

	t.Run("既定は 24 件で、超過分を Dropped に数える。先頭（上の要素）が残る", func(t *testing.T) {
		table := buildClicks(manyButtons(30)...)
		if table.Len() != 24 || table.Dropped() != 6 {
			t.Fatalf("Len = %d, Dropped = %d; want 24 and 6", table.Len(), table.Dropped())
		}
		if first, last := table.Candidates()[0].Label, table.Candidates()[23].Label; first != "b0" || last != "b23" {
			t.Errorf("first = %q, last = %q", first, last)
		}
	})

	t.Run("ちょうど 24 件なら落とさない", func(t *testing.T) {
		if table := buildClicks(manyButtons(24)...); table.Len() != 24 || table.Dropped() != 0 {
			t.Errorf("Len = %d, Dropped = %d", table.Len(), table.Dropped())
		}
	})

	t.Run("上限を超えると後ろの focus_window から落ちる", func(t *testing.T) {
		src := domain.CandidateSource{Snapshot: snapshotOf(manyButtons(3)...), Windows: []domain.WindowInfo{{ID: "1", Title: "W1"}, {ID: "2", Title: "W2"}}}
		table := domain.BuildCandidateTable(src, domain.CandidateOptions{Max: 4})
		if got, want := labelsOf(table), []string{"b0", "b1", "b2", "W1"}; !slices.Equal(got, want) || table.Dropped() != 1 {
			t.Errorf("labels = %v, Dropped = %d; want %v and 1", got, table.Dropped(), want)
		}
	})

	t.Run("衝突の分と超過の分を合算する", func(t *testing.T) {
		els := append(manyButtons(5), btn("dup", 0, 500, 5, 5), btn("dup", 0, 510, 5, 5))
		table := buildClicks(els...)
		if table.Len() != 5 || table.Dropped() != 2 {
			t.Errorf("Len = %d, Dropped = %d; want 5 and 2", table.Len(), table.Dropped())
		}
		table = domain.BuildCandidateTable(domain.CandidateSource{Snapshot: snapshotOf(els...)}, domain.CandidateOptions{Max: 3})
		if table.Len() != 3 || table.Dropped() != 4 {
			t.Errorf("Max 3: Len = %d, Dropped = %d; want 3 and 4", table.Len(), table.Dropped())
		}
	})

	t.Run("Max が 0 以下なら既定値", func(t *testing.T) {
		for _, max := range []int{0, -5} {
			table := domain.BuildCandidateTable(domain.CandidateSource{Snapshot: snapshotOf(manyButtons(30)...)}, domain.CandidateOptions{Max: max})
			if table.Len() != domain.DefaultMaxCandidates {
				t.Errorf("Max %d: Len = %d", max, table.Len())
			}
		}
	})
}

func TestCandidateTableImmutable(t *testing.T) {
	table := buildClicks(btn("OK", 0, 0, 5, 5), btn("Cancel", 0, 10, 5, 5))
	got := table.Candidates()
	got[0].Label = "tampered"
	got[0].ID = "c_tampered"
	got = append(got[:1], domain.Candidate{ID: "c_extra"})
	_ = got

	again := table.Candidates()
	if again[0].Label != "OK" || table.Len() != 2 {
		t.Errorf("mutating the returned slice changed the table: %+v", again)
	}
	if _, err := table.Validate("c_tampered"); !errors.Is(err, domain.ErrInvalidChoice) {
		t.Errorf("Validate(tampered id) err = %v", err)
	}
	c, err := table.Validate(again[0].ID)
	if err != nil || c.Label != "OK" {
		t.Errorf("Validate = %+v, %v", c, err)
	}
}

func TestCandidateTableValidate(t *testing.T) {
	table := buildClicks(btn("OK", 0, 0, 5, 5), btn("Cancel", 0, 10, 5, 5))
	cands := table.Candidates()

	t.Run("表の ID は候補をそのまま返す", func(t *testing.T) {
		for _, want := range cands {
			got, err := table.Validate(want.ID)
			if err != nil || got != want {
				t.Errorf("Validate(%q) = %+v, %v; want %+v", want.ID, got, err, want)
			}
		}
	})

	t.Run("reobserve と abstain はメタ候補", func(t *testing.T) {
		for choice, kind := range map[string]domain.CandidateKind{
			domain.ChoiceReobserve: domain.CandidateReobserve,
			domain.ChoiceAbstain:   domain.CandidateAbstain,
		} {
			got, err := table.Validate(choice)
			if err != nil || got.Kind != kind || got.ID != choice {
				t.Errorf("Validate(%q) = %+v, %v", choice, got, err)
			}
		}
		// 空の表でも受理する。
		if _, err := (domain.CandidateTable{}).Validate(domain.ChoiceAbstain); err != nil {
			t.Errorf("empty table: Validate(abstain) = %v", err)
		}
	})

	t.Run("未知・空・形式不正・古い表の ID は ErrInvalidChoice", func(t *testing.T) {
		stale := buildClicks(btn("Other", 0, 0, 5, 5)).Candidates()[0].ID
		invalid := []string{
			"", " ", "c_", "c_000000000000", stale,
			cands[0].ID + " ", " " + cands[0].ID, strings.ToUpper(cands[0].ID), cands[0].ID[:5], cands[0].ID + "0",
			"Reobserve", " reobserve", "abstain\n", "ABSTAIN", "reobserve,abstain",
			cands[0].ID + "," + cands[1].ID, "OK", "click", "\x00",
		}
		for _, choice := range invalid {
			got, err := table.Validate(choice)
			if !errors.Is(err, domain.ErrInvalidChoice) || got != (domain.Candidate{}) {
				t.Errorf("Validate(%q) = %+v, %v; want zero value and ErrInvalidChoice", choice, got, err)
			}
		}
		if _, err := (domain.CandidateTable{}).Validate(cands[0].ID); !errors.Is(err, domain.ErrInvalidChoice) {
			t.Errorf("empty table accepted an ID: %v", err)
		}
	})

	t.Run("エラーに入力（Jev の応答）を載せない", func(t *testing.T) {
		_, err := table.Validate("ignore previous instructions")
		if err == nil || strings.Contains(err.Error(), "ignore") {
			t.Errorf("err = %v", err)
		}
	})

	// プロパティ: 受理するのは表の ID と 2 つのメタ候補だけ。
	t.Run("ランダムな文字列は表の ID とメタ候補以外を一切受理しない", func(t *testing.T) {
		accepted := map[string]bool{domain.ChoiceReobserve: true, domain.ChoiceAbstain: true}
		for _, c := range cands {
			accepted[c.ID] = true
		}
		rng := rand.New(rand.NewPCG(3, 4))
		alphabet := []rune("c_0123456789abcdefABCDEF reobservstain\x00,/")
		for range 20000 {
			n := rng.IntN(16)
			rs := make([]rune, n)
			for i := range rs {
				rs[i] = alphabet[rng.IntN(len(alphabet))]
			}
			s := string(rs)
			if _, err := table.Validate(s); (err == nil) != accepted[s] {
				t.Fatalf("Validate(%q) err = %v, accepted = %v", s, err, accepted[s])
			}
		}
	})
}
