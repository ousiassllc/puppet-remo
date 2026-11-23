package main

import "github.com/ousiassllc/puppet-remo/internal/domain"

// captureAllowed は撮影してよい窓かの判定（撮影の安全規則。純粋関数）。
//
//   - pid が照合済みの fixture（verified: PID → 役）でなければ撮らない。
//   - READY で名乗って OS の事実と照合した窓（named: HWND → 名前）はそのプロセスの窓として撮る。
//   - 名乗っていない窓は、main fixture の PID のメニュー（#32768）・ダイアログ（#32770）だけ撮る（開閉で現れるため）。
//     interferer・foreign のプロセスの同じクラスの窓は撮らない。
func captureAllowed(verified map[uint32]string, named map[uintptr]string, pid uint32, hwnd uintptr, class string) (role string, ok bool) {
	role, ok = verified[pid]
	if !ok {
		return "", false
	}
	if _, isNamed := named[hwnd]; isNamed {
		return role, true
	}
	if role == "main" && (class == domain.ClassMenu || class == "#32770") {
		return role, true
	}
	return "", false
}
