package windows

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.Power = (*Power)(nil)

// SetThreadExecutionState に渡すフラグ（NFR-A05）。
const (
	esContinuous      uint32 = 0x80000000 // 次に変えるまで有効（スレッドが終わると失効する）
	esSystemRequired  uint32 = 0x00000001 // システムのスリープを抑止する
	esDisplayRequired uint32 = 0x00000002 // 画面オフを抑止する
)

// powerFlags は KeepAwake(on) が SetThreadExecutionState に渡すフラグを返す。
func powerFlags(on bool) uint32 {
	if on {
		return esContinuous | esSystemRequired | esDisplayRequired
	}
	return esContinuous
}

// Power は platform.Power の Windows 実装（SetThreadExecutionState）。
// 実行状態はスレッドごとなので、設定と解除は常に同じ専用の OS スレッドで行う。
type Power struct {
	set    func(flags uint32) // Windows では SetThreadExecutionState。専用スレッドからだけ呼ばれる
	runner *threadRunner
}

func newPower(set func(flags uint32)) *Power {
	return &Power{set: set, runner: newThreadRunner()}
}

// KeepAwake は on なら画面オフとスリープを抑止し、off なら解除する。platform.Power は結果を返さないため、
// 失敗（SetThreadExecutionState が 0 を返す場合）や Close 後の呼び出しは何もしない。
func (p *Power) KeepAwake(on bool) {
	flags := powerFlags(on)
	_ = p.runner.do(context.Background(), func(context.Context) error {
		p.set(flags)
		return nil
	})
}

// Close は抑止を解除して専用スレッドを止める。複数回呼んでも安全。
func (p *Power) Close() error {
	p.KeepAwake(false)
	p.runner.close()
	return nil
}
