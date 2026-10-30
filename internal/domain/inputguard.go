package domain

import "slices"

// CheckInputTarget は、入力の宛先ウィンドウに対する InputGuard の判定規則（NFR-S06）。
// 自プロセスのウィンドウ（ホスト GUI・承認ダイアログ・オーバーレイ）と、昇格ウィンドウ（UIPI で入力できない）を
// ErrInputBlocked で拒否する。ウィンドウの事実（Own・Elevated）は platform が返す。
func CheckInputTarget(w WindowInfo) error {
	switch {
	case w.Own:
		return ErrInputBlocked.
			WithMsg("the target is a puppet-remo window").
			WithDetail("reason", "own_window")
	case w.Elevated:
		return ErrInputBlocked.
			WithMsg("the target window is elevated (UIPI)").
			WithDetail("reason", "elevated_window").
			WithDetail("window_id", w.ID)
	}
	return nil
}

// CheckObserveTarget は、ウィンドウの内容を読む操作（UI ツリー）に対する規則。
// 自ウィンドウは、設定値・API キーの入力欄を AI に見せないために拒否する（NFR-S06）。昇格ウィンドウは読み取りのみで入力しないため拒否しない。
func CheckObserveTarget(w WindowInfo) error {
	if w.Own {
		return ErrInputBlocked.
			WithMsg("the target is a puppet-remo window").
			WithDetail("reason", "own_window")
	}
	return nil
}

// CheckKeyCombo は、送ってはならないキーの組み合わせを拒否する。
// Ctrl+Alt+Delete（SAS）は注入できず、セキュアデスクトップへ遷移させるため ErrInputBlocked とする（API 仕様 §3.3）。
func CheckKeyCombo(k KeyCombo) error {
	if k.Key == "delete" && slices.Contains(k.Mods, ModCtrl) && slices.Contains(k.Mods, ModAlt) {
		return ErrInputBlocked.
			WithMsg("ctrl+alt+delete (secure attention sequence) cannot be sent").
			WithDetail("reason", "secure_attention_sequence")
	}
	return nil
}
