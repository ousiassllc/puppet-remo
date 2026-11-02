package mcp

import (
	"context"
	"math"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

const (
	stateIdle            = "idle"
	statePendingApproval = "pending_approval"
	stateHeld            = "held"
)

// holderBody はリースの保持者。task_id は保持者が agent のときだけ（agent の Actor.SessionID は task_id。domain.Actor）。
type holderBody struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	TaskID string `json:"task_id,omitempty"`
}

func newHolderBody(a domain.Actor) *holderBody {
	b := &holderBody{Kind: string(a.Kind), Label: a.Label}
	if a.Kind == domain.ActorAgent {
		b.TaskID = a.SessionID
	}
	return b
}

// statusBody は get_control_status の出力（API 仕様 §3.3）。
// holder は held のときだけ（pending_approval の要求者は出さない）。mine は held なら保持者が自分、pending_approval なら
// 承認待ちの要求者が自分か。holder.kind は mcp / web / agent（run_task のタスクの実行中）。保持者の種別も照合するので、
// 自分が依頼したタスクの実行中は agent が保持者であり mine は false になる。
type statusBody struct {
	State            string      `json:"state"`
	Holder           *holderBody `json:"holder,omitempty"`
	Mine             bool        `json:"mine"`
	IdleRemainingSec *int        `json:"idle_remaining_sec,omitempty"`
}

// addLeaseTools はリースのツールを登録する: FR-M15。いずれもリースは不要。
func (s *Server) addLeaseTools() {
	addAuditedTool(s, "acquire_control",
		"Acquire the control lease explicitly and return the control status. "+
			"Fails with lease_held_by_other when another session holds it or is waiting for the host's approval. "+
			"When the host's approval is required, waits for it and fails with approval_denied (the host denied) or approval_timeout (no answer).",
		false, noArgsSummary, func(ctx context.Context, who domain.Actor, _ noArgs) (toolResult, error) {
			if _, err := s.leases.Acquire(ctx, who); err != nil {
				return toolResult{}, err
			}
			return toolResult{Structured: s.status(who)}, nil
		})

	addAuditedTool(s, "release_control", "Release the control lease. Does nothing when this session does not hold it.",
		false, noArgsSummary, func(_ context.Context, who domain.Actor, _ noArgs) (toolResult, error) {
			s.leases.Release(who)
			return okResult, nil
		})

	addAuditedTool(s, "get_control_status",
		"Get the control lease state: idle, pending_approval (waiting for the host's approval) or held, who holds it, whether this session holds it, and the idle time left.",
		true, noArgsSummary, func(_ context.Context, who domain.Actor, _ noArgs) (toolResult, error) {
			return toolResult{Structured: s.status(who)}, nil
		})
}

// status は現在のリース状態を who の視点で返す。
func (s *Server) status(who domain.Actor) statusBody {
	h, ok := s.leases.Holder()
	if !ok {
		if p, pending := s.leases.Pending(); pending {
			return statusBody{State: statePendingApproval, Mine: p.Actor.SameSession(who)}
		}
		return statusBody{State: stateIdle}
	}
	return statusBody{
		State:            stateHeld,
		Holder:           newHolderBody(h.Actor),
		Mine:             h.Actor.SameSession(who),
		IdleRemainingSec: idleSeconds(h),
	}
}

// idleSeconds は残り時間を秒（切り上げ。負なら 0）にする。
func idleSeconds(h service.Holder) *int {
	sec := max(0, int(math.Ceil(h.IdleRemaining.Seconds())))
	return &sec
}
