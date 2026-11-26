package service

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// WindowPicker はビューア（#157）のウィンドウ一覧の読み取り。選べるのは所有されていない最上位のウィンドウだけで、関連ウィンドウ
// （ダイアログ・メニュー）・自ウィンドウは出さず、昇格・最小化・同一性を確かめられないものは理由つきで選べないものとして返す
// （domain.PickCandidates）。リース・保持者は問わない閲覧系の操作（キルスイッチ中は拒否）。
type WindowPicker interface {
	Candidates(ctx context.Context) ([]domain.PickCandidate, error)
}

type windowPicker struct {
	windows platform.WindowManager
	kill    KillSwitchState
}

// NewWindowPicker は WindowPicker を返す。
func NewWindowPicker(windows platform.WindowManager, kill KillSwitchState) WindowPicker {
	return &windowPicker{windows: windows, kill: kill}
}

func (p *windowPicker) Candidates(ctx context.Context) ([]domain.PickCandidate, error) {
	if p.kill != nil && p.kill.Active() {
		return nil, domain.ErrKillSwitchActive
	}
	list, err := p.windows.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	return domain.PickCandidates(list), nil
}
