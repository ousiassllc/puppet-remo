package domain

import (
	"slices"
	"strings"
)

// 入力スコープ（リースに束縛した 1 つのウィンドウ T と、その対象グループ G だけに入力を限る仕組み。#156）。
// 対象制限は誤入力を止めるガードレールで、セキュリティ境界ではない。

// ScopeState はスコープ付きリースの状態。停止（halted）から active へは、保持者の明示的な再開（LeaseMachine.Resume）だけで戻る。
type ScopeState string

const (
	ScopeActive ScopeState = "active"
	ScopeHalted ScopeState = "halted"
)

// スコープの停止・拒否の reason（ErrInputBlocked の Detail["reason"]。GroupReason と同じ空間で、API 仕様 §3.4 の表にある）。
const (
	// 入力の検証の失敗で、T が最小化されている（スコープ付きリースの T の状態）。
	ReasonTargetMinimized GroupReason = "target_minimized"

	// 停止の 2 つ目の原因: T または検証できたメンバーを撮れない（表示不可）。モニター撮影・画面コピーには切り替えない。
	ReasonCaptureUnavailable       GroupReason = "capture_unavailable"        // T が撮れない・GroupCapturer が無い・撮影そのものの失敗
	ReasonCaptureMemberUnavailable GroupReason = "capture_member_unavailable" // T 以外の検証できたメンバーが撮れない

	// 停止にはしない、その操作だけの拒否（スコープの規則。保持者の状態は変えない）。
	ReasonKeyOutOfScope       GroupReason = "key_out_of_scope"       // win 修飾・Alt+Tab・Alt+Esc・Ctrl+Esc・Ctrl+Shift+Esc
	ReasonClipboardOutOfScope GroupReason = "clipboard_out_of_scope" // ClipboardRead（スコープ付きは書き込みだけ）

	// 対象の移動・リサイズ（エージェントの基準 S と比べて T または既存メンバーの矩形が違う。#158。入力の注入の前に検出する）。
	ReasonTargetMoved GroupReason = "target_moved"
	// run_task の対象が agent.deny_processes に当たる（タスクを開始しない。#158）。
	ReasonDeniedProcess GroupReason = "denied_process"

	// 停止中（halted）に入力・クリップボード書き込みを拒否するときの reason。detail.halt_reason に停止の理由が入る。
	ReasonTargetHalted GroupReason = "target_halted"
)

// NoticeCapturePartial は、所有関係を確かめられないウィンドウ（unverifiable）を対象の表示から除外した通知（停止ではない）。
const NoticeCapturePartial = "capture_partial"

// ReselectOnly は、停止の理由が「T を選び直すしかない」ものか（再開できない）を返す。
// T が閉じた・別のウィンドウに置き換わった・UWP などで同一性を確かめられない場合は、リースを解放して選び直す。
func (r GroupReason) ReselectOnly() bool {
	switch r {
	case ReasonTargetClosed, ReasonTargetReplaced, ReasonTargetUnsupported:
		return true
	}
	return false
}

// LeaseScope はリースに束縛した対象。Target は取得したあとに変えない（変えるにはリースを解放して取り直す）。
type LeaseScope struct {
	Target WindowRef
	// Process・Members は束縛した時点の T のプロセス名とメンバー数（T を含む）。監査（lease.target_set）にだけ使う。
	Process string
	Members int

	State ScopeState
	// HaltReason は State が ScopeHalted のときの停止の理由。
	HaltReason GroupReason
}

// Halted は停止中かを返す。
func (s LeaseScope) Halted() bool { return s.State == ScopeHalted }

// スコープ付きリースで拒否するキー。Alt+Tab・Alt+Esc・Ctrl+Esc（Ctrl+Shift+Esc を含む）と、win 修飾・win キー自体。
// key は ValidKeyName の名前（小文字）か、KeyEvent の Code・Key（KeyboardEvent の値。大文字小文字を区別しない）。

// ScopedKeyForbidden は、スコープ付きリースで mods と key の組み合わせを拒否するかを返す。
// 対象のウィンドウの外へ出る・OS を操作するキー（win 修飾・Alt+Tab・Alt+Esc・Ctrl+Esc・Ctrl+Shift+Esc）が対象。
func ScopedKeyForbidden(mods []Modifier, key string) bool {
	if slices.Contains(mods, ModWin) {
		return true
	}
	switch strings.ToLower(key) {
	case "meta", "metaleft", "metaright", "os", "osleft", "osright", "win", "lwin", "rwin":
		return true // win キー自体
	case "tab":
		return slices.Contains(mods, ModAlt)
	case "escape", "esc":
		return slices.Contains(mods, ModAlt) || slices.Contains(mods, ModCtrl)
	}
	return false
}
