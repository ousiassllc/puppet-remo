package service

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// RecordConnectionRequest は未登録デバイスの接続要求を 1 デバイス 1 件に集約して作る（FR-A06、FR-A07）。
//
//   - 拒否してから 24 時間は同デバイスから新しい要求を作らない（nil を返す）。
//   - 新しい要求の作成は全体で 1 分あたり 10 件まで。超過分は要求を作らず（GUI に出さず）、
//     窓ごとに 1 回だけ監査に記録して nil を返す。既存の pending への集約は数えない。
//
// キルスイッチ中は呼び出し側（ミドルウェア）が先に 503 を返すので、ここには来ない。
func (a *authz) RecordConnectionRequest(ctx context.Context, id Identity, ip string) error {
	now := a.cfg.Clock.Now()
	denied, err := a.cfg.ConnRequests.DeniedActive(ctx, id.StableNodeID, now)
	if err != nil || denied {
		return err
	}
	pending, err := a.cfg.ConnRequests.ListPending(ctx)
	if err != nil {
		return err
	}
	if !hasPending(pending, id.StableNodeID) {
		if ok, first, _ := a.connRequests.check("", now, true); !ok {
			if first {
				a.audit(ctx, repository.AuditEntry{
					Event: "auth.connreq_rate_limited", ActorType: domain.ActorSystem, ClientLabel: id.NodeName,
				}, map[string]any{"stable_node_id": id.StableNodeID, "remote_ip": ip})
			}
			return nil
		}
	}
	_, _, err = a.cfg.ConnRequests.UpsertPending(ctx, repository.ConnRequest{
		ID:           a.cfg.IDs.NewID(),
		StableNodeID: id.StableNodeID,
		LoginName:    id.LoginName,
		DisplayName:  id.NodeName,
		RemoteIP:     ip,
		FirstSeenAt:  now,
		LastSeenAt:   now,
		ExpiresAt:    now.Add(connRequestPendingTTL),
	})
	return err
}

func hasPending(pending []repository.ConnRequest, stableNodeID string) bool {
	for _, r := range pending {
		if r.StableNodeID == stableNodeID {
			return true
		}
	}
	return false
}
