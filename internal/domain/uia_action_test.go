package domain_test

import (
	"errors"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

const actionTestID = "uie:197382:4812:2a.1f4.3"

func TestUIActionValidate(t *testing.T) {
	tests := []struct {
		name string
		a    domain.UIAction
		ok   bool
	}{
		{"invoke", domain.UIAction{ElementID: actionTestID, Kind: domain.UIActionInvoke}, true},
		{"set_value", domain.UIAction{ElementID: actionTestID, Kind: domain.UIActionSetValue, Value: new("x")}, true},
		{"set_value empty", domain.UIAction{ElementID: actionTestID, Kind: domain.UIActionSetValue, Value: new("")}, true},
		{"toggle", domain.UIAction{ElementID: actionTestID, Kind: domain.UIActionToggle}, true},
		{"toggle on", domain.UIAction{ElementID: actionTestID, Kind: domain.UIActionToggle, State: "on"}, true},
		{"toggle off", domain.UIAction{ElementID: actionTestID, Kind: domain.UIActionToggle, State: "off"}, true},
		{"select", domain.UIAction{ElementID: actionTestID, Kind: domain.UIActionSelect}, true},
		{"expand", domain.UIAction{ElementID: actionTestID, Kind: domain.UIActionExpand}, true},
		{"collapse", domain.UIAction{ElementID: actionTestID, Kind: domain.UIActionCollapse}, true},
		{"bad id", domain.UIAction{ElementID: "1234", Kind: domain.UIActionInvoke}, false},
		{"empty id", domain.UIAction{Kind: domain.UIActionInvoke}, false},
		{"unknown action", domain.UIAction{ElementID: actionTestID, Kind: "click"}, false},
		{"empty action", domain.UIAction{ElementID: actionTestID}, false},
		{"set_value without value", domain.UIAction{ElementID: actionTestID, Kind: domain.UIActionSetValue}, false},
		{"value with invoke", domain.UIAction{ElementID: actionTestID, Kind: domain.UIActionInvoke, Value: new("x")}, false},
		{"state with select", domain.UIAction{ElementID: actionTestID, Kind: domain.UIActionSelect, State: "on"}, false},
		{"bad toggle state", domain.UIAction{ElementID: actionTestID, Kind: domain.UIActionToggle, State: "indeterminate"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, err := tt.a.Validate()
			if tt.ok {
				if err != nil || ref.WindowID() != "197382" || ref.PID != 4812 {
					t.Errorf("Validate = %+v, %v", ref, err)
				}
				return
			}
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Errorf("err = %v, want invalid_argument", err)
			}
		})
	}
}

func TestUIActionKindPatterns(t *testing.T) {
	want := map[domain.UIActionKind]string{
		domain.UIActionInvoke: domain.PatternInvoke, domain.UIActionSetValue: domain.PatternValue, domain.UIActionToggle: domain.PatternToggle,
		domain.UIActionSelect: domain.PatternSelectionItem, domain.UIActionExpand: domain.PatternExpandCollapse, domain.UIActionCollapse: domain.PatternExpandCollapse,
	}
	if len(want) != len(domain.UIActionKinds) {
		t.Fatalf("UIActionKinds has %d kinds, the test knows %d", len(domain.UIActionKinds), len(want))
	}
	for k, p := range want {
		if !k.Valid() || k.Pattern() != p {
			t.Errorf("%s: valid %v, pattern %q, want %q", k, k.Valid(), k.Pattern(), p)
		}
	}
	if domain.UIActionKind("click").Valid() || domain.UIActionKind("click").Pattern() != "" {
		t.Error("an unknown action must be invalid with no pattern")
	}
}

// ui_action はカーソルもフォーカスも動かさないので、ホストの実入力による一時停止（#270）の対象ではない。
func TestUIActionIsNotPausedByHostInput(t *testing.T) {
	if domain.OpPausedByHostInput(domain.UIAction{}) {
		t.Error("UIAction must not be paused by host input")
	}
}

func TestCheckUIActionTarget(t *testing.T) {
	tests := []struct {
		name   string
		w      domain.WindowInfo
		reason string
	}{
		{"normal", domain.WindowInfo{ID: "1"}, ""},
		{"own", domain.WindowInfo{ID: "2", Own: true}, domain.ReasonOwnWindow},
		{"own click-through overlay", domain.WindowInfo{ID: "3", Own: true, Overlay: true}, domain.ReasonOwnWindow},
		{"elevated", domain.WindowInfo{ID: "4", Elevated: true}, domain.ReasonElevatedWindow},
	}
	for _, tt := range tests {
		err := domain.CheckUIActionTarget(tt.w)
		if tt.reason == "" {
			if err != nil {
				t.Errorf("%s: err = %v", tt.name, err)
			}
			continue
		}
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeInputBlocked || de.Detail["reason"] != tt.reason {
			t.Errorf("%s: err = %v, want input_blocked %s", tt.name, err, tt.reason)
		}
	}
}
