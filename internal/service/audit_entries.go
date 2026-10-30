package service

import (
	"encoding/json"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// 監査ログの result（audit_logs の CHECK 制約）。
const (
	auditOK     = "ok"
	auditError  = "error"
	auditDenied = "denied"
)

// ToolAuditEntry はツール実行の監査エントリを作る（FR-AU01）。mcp 以外の保持者（M2 以降の web）は kind = input。
// args は引数の要約で、`type` の本文・クリップボードの内容・トークンを入れてはならない（FR-AU03、NFR-S13。文字数だけを入れる）。
// err が nil なら ok、拒否（リース・InputGuard・キルスイッチ・判定）は denied、それ以外は error。
func ToolAuditEntry(who domain.Actor, tool string, args map[string]any, leaseID string, err error, took time.Duration) repository.AuditEntry {
	kind := "tool"
	if who.Kind == domain.ActorWeb {
		kind = "input"
	}
	ms := took.Milliseconds()
	e := repository.AuditEntry{
		Kind:        kind,
		Event:       "tool." + tool,
		ActorType:   who.Kind,
		ClientID:    who.ClientID,
		ClientLabel: who.Label,
		SessionID:   who.SessionID,
		LeaseID:     leaseID,
		Tool:        tool,
		Result:      auditResult(err),
		DurationMS:  &ms,
		DetailJSON:  marshalDetail(args),
	}
	if err != nil {
		e.ErrorCode = string(domain.CodeOf(err))
	}
	return e
}

// auditResult はエラーを監査の result に写す。
func auditResult(err error) string {
	switch domain.CodeOf(err) {
	case "":
		return auditOK
	case domain.CodeKillSwitchActive, domain.CodeLeaseRequired, domain.CodeLeaseHeldByOther, domain.CodeLeaseRevoked,
		domain.CodeInputBlocked, domain.CodeApprovalDenied, domain.CodeApprovalTimeout:
		return auditDenied
	}
	return auditError
}

// marshalDetail は detail を JSON にする。nil・空・変換できない場合は nil（NULL で保存される）。
func marshalDetail(detail map[string]any) json.RawMessage {
	if len(detail) == 0 {
		return nil
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return nil
	}
	return b
}

// leaseAuditEntry はリースの遷移を監査エントリにする（kind = lease）。
func leaseAuditEntry(ev domain.LeaseEvent) repository.AuditEntry {
	h := ev.Lease.Holder
	e := repository.AuditEntry{
		TS:          ev.At,
		Kind:        "lease",
		ActorType:   h.Kind,
		ClientID:    h.ClientID,
		ClientLabel: h.Label,
		SessionID:   h.SessionID,
		LeaseID:     ev.Lease.ID,
		Result:      auditOK,
	}
	if ev.To == domain.LeaseHeld {
		e.Event = "lease.acquire"
		return e
	}
	e.Event = "lease.release"
	e.DetailJSON = marshalDetail(map[string]any{"reason": string(ev.Reason)})
	return e
}

// leaseDeniedAuditEntry は取得の失敗（他者が保持中など）を監査エントリにする。
func leaseDeniedAuditEntry(who domain.Actor, at time.Time, err error) repository.AuditEntry {
	return repository.AuditEntry{
		TS:          at,
		Kind:        "lease",
		Event:       "lease.acquire",
		ActorType:   who.Kind,
		ClientID:    who.ClientID,
		ClientLabel: who.Label,
		SessionID:   who.SessionID,
		Result:      auditResult(err),
		ErrorCode:   string(domain.CodeOf(err)),
	}
}
