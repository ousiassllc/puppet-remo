package mcp

import (
	"context"
	"math"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

const (
	stateIdle = "idle"
	stateHeld = "held"
)

type holderBody struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// statusBody は get_control_status の出力（API 仕様 §3.3）。
// M1 の state は idle / held のみ、holder.kind は mcp のみ（pending_approval は M3、web は M2、agent は M4）。
type statusBody struct {
	State            string      `json:"state"`
	Holder           *holderBody `json:"holder,omitempty"`
	Mine             bool        `json:"mine"`
	IdleRemainingSec *int        `json:"idle_remaining_sec,omitempty"`
}

// addLeaseTools はリースのツールを登録する: FR-M15。いずれもリースは不要。
func (s *Server) addLeaseTools() {
	addTool(s, "acquire_control",
		"Acquire the control lease explicitly and return the control status. "+
			"Fails with lease_held_by_other when another session holds it.",
		false, func(ctx context.Context, who domain.Actor, _ noArgs) (toolResult, error) {
			if _, err := s.leases.Acquire(ctx, who); err != nil {
				return toolResult{}, err
			}
			return toolResult{Structured: s.status(who)}, nil
		})

	addTool(s, "release_control", "Release the control lease. Does nothing when this session does not hold it.",
		false, func(_ context.Context, who domain.Actor, _ noArgs) (toolResult, error) {
			s.leases.Release(who)
			return okResult, nil
		})

	addTool(s, "get_control_status",
		"Get the control lease state: idle or held, who holds it, whether this session holds it, and the idle time left.",
		true, func(_ context.Context, who domain.Actor, _ noArgs) (toolResult, error) {
			return toolResult{Structured: s.status(who)}, nil
		})
}

// status は現在のリース状態を who の視点で返す。
func (s *Server) status(who domain.Actor) statusBody {
	h, ok := s.leases.Holder()
	if !ok {
		return statusBody{State: stateIdle}
	}
	return statusBody{
		State:            stateHeld,
		Holder:           &holderBody{Kind: string(h.Actor.Kind), Label: h.Actor.Label},
		Mine:             h.Actor.SameSession(who),
		IdleRemainingSec: idleSeconds(h),
	}
}

// idleSeconds は残り時間を秒（切り上げ。負なら 0）にする。
func idleSeconds(h service.Holder) *int {
	sec := max(0, int(math.Ceil(h.IdleRemaining.Seconds())))
	return &sec
}
