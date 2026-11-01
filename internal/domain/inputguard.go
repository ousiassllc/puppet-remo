package domain

import "slices"

// 入力を拒否する宛先の reason（ErrInputBlocked の Detail["reason"]。API 仕様 §3.4）。
const (
	ReasonOwnWindow      = "own_window"
	ReasonElevatedWindow = "elevated_window"
)

// InputBlockReason は w への入力を拒否する理由（ReasonOwnWindow / ReasonElevatedWindow）を返す。拒否しないウィンドウは空文字。
// 自ウィンドウかつ昇格のときは自ウィンドウを優先する。CheckInputTarget と list_windows の blocked の印が同じ規則を使う。
func InputBlockReason(w WindowInfo) string {
	switch {
	case w.Own:
		return ReasonOwnWindow
	case w.Elevated:
		return ReasonElevatedWindow
	}
	return ""
}

// CheckInputTarget は、入力の宛先ウィンドウに対する InputGuard の判定規則（NFR-S06）。
// 自プロセスのウィンドウ（ホスト GUI・承認ダイアログ・オーバーレイ）と、昇格ウィンドウ（UIPI で入力できない）を
// ErrInputBlocked で拒否する。ウィンドウの事実（Own・Elevated）は platform が返す。
func CheckInputTarget(w WindowInfo) error {
	switch InputBlockReason(w) {
	case ReasonOwnWindow:
		return ErrInputBlocked.
			WithMsg("the target is a puppet-remo window").
			WithDetail("reason", ReasonOwnWindow)
	case ReasonElevatedWindow:
		return ErrInputBlocked.
			WithMsg("the target window is elevated (UIPI)").
			WithDetail("reason", ReasonElevatedWindow).
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
			WithDetail("reason", ReasonOwnWindow)
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
