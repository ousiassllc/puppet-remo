package domain_test

import (
	"errors"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestCheckInputTarget(t *testing.T) {
	tests := []struct {
		name       string
		w          domain.WindowInfo
		wantReason string // 空なら許可
	}{
		{name: "通常のウィンドウ", w: domain.WindowInfo{ID: "1"}},
		{name: "前面・最小化は無関係", w: domain.WindowInfo{ID: "1", Foreground: true, Minimized: true}},
		{name: "自ウィンドウ", w: domain.WindowInfo{ID: "2", Own: true}, wantReason: "own_window"},
		{name: "昇格ウィンドウ", w: domain.WindowInfo{ID: "3", Elevated: true}, wantReason: "elevated_window"},
		{name: "自ウィンドウかつ昇格は自ウィンドウを優先", w: domain.WindowInfo{ID: "4", Own: true, Elevated: true}, wantReason: "own_window"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertBlocked(t, domain.CheckInputTarget(tt.w), tt.wantReason)
		})
	}
}

func TestCheckObserveTarget(t *testing.T) {
	tests := []struct {
		name       string
		w          domain.WindowInfo
		wantReason string
	}{
		{name: "通常のウィンドウ", w: domain.WindowInfo{ID: "1"}},
		{name: "昇格ウィンドウは読み取りのみなので許可", w: domain.WindowInfo{ID: "3", Elevated: true}},
		{name: "自ウィンドウ", w: domain.WindowInfo{ID: "2", Own: true}, wantReason: "own_window"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertBlocked(t, domain.CheckObserveTarget(tt.w), tt.wantReason)
		})
	}
}

func TestCheckKeyCombo(t *testing.T) {
	tests := []struct {
		keys       string
		wantReason string
	}{
		{keys: "enter"},
		{keys: "delete"},
		{keys: "ctrl+delete"},
		{keys: "alt+delete"},
		{keys: "ctrl+alt+a"},
		{keys: "ctrl+alt+delete", wantReason: "secure_attention_sequence"},
		{keys: "alt+ctrl+DELETE", wantReason: "secure_attention_sequence"},
		{keys: "ctrl+alt+shift+delete", wantReason: "secure_attention_sequence"},
	}
	for _, tt := range tests {
		t.Run(tt.keys, func(t *testing.T) {
			k, err := domain.ParseKeyCombo(tt.keys)
			if err != nil {
				t.Fatal(err)
			}
			assertBlocked(t, domain.CheckKeyCombo(k), tt.wantReason)
		})
	}
}

func assertBlocked(t *testing.T, err error, wantReason string) {
	t.Helper()
	if wantReason == "" {
		if err != nil {
			t.Fatalf("want allowed, got %v", err)
		}
		return
	}
	if !errors.Is(err, domain.ErrInputBlocked) {
		t.Fatalf("want ErrInputBlocked, got %v", err)
	}
	var de *domain.Error
	if !errors.As(err, &de) || de.Detail["reason"] != wantReason {
		t.Fatalf("want reason %q, got %+v", wantReason, err)
	}
}

func TestCheckKeyEvent(t *testing.T) {
	ctrlAlt := []domain.Modifier{domain.ModCtrl, domain.ModAlt}
	tests := []struct {
		name       string
		e          domain.KeyEvent
		wantReason string
	}{
		{name: "通常のキー", e: domain.KeyEvent{Action: domain.KeyDown, Code: "KeyA", Key: "a", Mods: ctrlAlt}},
		{name: "Delete 単独", e: domain.KeyEvent{Action: domain.KeyDown, Code: "Delete", Key: "Delete"}},
		{name: "Ctrl+Delete", e: domain.KeyEvent{Action: domain.KeyDown, Code: "Delete", Key: "Delete", Mods: []domain.Modifier{domain.ModCtrl}}},
		{name: "Alt+Delete", e: domain.KeyEvent{Action: domain.KeyDown, Code: "Delete", Key: "Delete", Mods: []domain.Modifier{domain.ModAlt}}},
		{name: "Ctrl+Alt+Delete の down", e: domain.KeyEvent{Action: domain.KeyDown, Code: "Delete", Key: "Delete", Mods: ctrlAlt}, wantReason: "secure_attention_sequence"},
		{name: "Ctrl+Alt+Delete の up", e: domain.KeyEvent{Action: domain.KeyUp, Code: "Delete", Key: "Delete", Mods: ctrlAlt}, wantReason: "secure_attention_sequence"},
		{name: "code だけでも判定する", e: domain.KeyEvent{Action: domain.KeyDown, Code: "Delete", Mods: ctrlAlt}, wantReason: "secure_attention_sequence"},
		{name: "key だけでも判定する（大文字小文字を区別しない）", e: domain.KeyEvent{Action: domain.KeyDown, Key: "DELETE", Mods: ctrlAlt}, wantReason: "secure_attention_sequence"},
		{name: "NumpadDecimal の Del は code が別（key が Delete）", e: domain.KeyEvent{Action: domain.KeyDown, Code: "NumpadDecimal", Key: "Delete", Mods: ctrlAlt}, wantReason: "secure_attention_sequence"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertBlocked(t, domain.CheckKeyEvent(tt.e), tt.wantReason)
		})
	}
}
