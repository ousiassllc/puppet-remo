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

// SchemaRejectedAuditEntry は、入力スキーマに合わず、MCP の SDK が実行前に拒否したツール呼び出しの監査エントリを作る
// （FR-AU06。ツールの本体にも Control にも届かないため、受信側が記録する）。result は denied、error_code は invalid_argument。
// 引数は SDK に拒否された未検証の値なので、detail には何も入れない（パス・引数が運用ログ・監査に漏れない。NFR-S13）。
func SchemaRejectedAuditEntry(who domain.Actor, tool string, took time.Duration) repository.AuditEntry {
	e := ToolAuditEntry(who, tool, nil, "", domain.ErrInvalidArgument, took)
	e.Result = auditDenied
	return e
}

// auditResult はエラーを監査の result に写す。
func auditResult(err error) string {
	switch domain.CodeOf(err) {
	case "":
		return auditOK
	case domain.CodeKillSwitchActive, domain.CodeLeaseRequired, domain.CodeLeaseHeldByOther, domain.CodeLeaseRevoked,
		domain.CodeInputBlocked, domain.CodeApprovalDenied, domain.CodeApprovalTimeout, domain.CodeAppNotFound:
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

// 承認の監査のイベント名（kind = lease。docs/architecture/er.md のカタログ）。approval.reset は HostAdmin.ResetApproval が記録する。
const (
	approvalRequestedAuditEvent = "approval.requested"
	approvalGrantedAuditEvent   = "approval.granted"
	approvalDeniedAuditEvent    = "approval.denied"
	approvalTimeoutAuditEvent   = "approval.timeout"
	approvalWithdrawnAuditEvent = "approval.withdrawn"
)

// leaseAuditEntries はリースの遷移を監査エントリにする（kind = lease）。ほとんどは 1 件で、承認の遷移は approval.* を足す:
//
//	idle → pending_approval              approval.requested
//	pending_approval → held              approval.granted と lease.acquire（この順）
//	pending_approval → idle（理由別）    approval.denied / approval.timeout / approval.withdrawn（取り下げ。detail.reason に理由）
//
// 強制剥奪（lease.revoke）の操作主体は剥奪した側（ホスト GUI なら host）で、剥奪された保持者は detail に残す。
func leaseAuditEntries(ev domain.LeaseEvent) []repository.AuditEntry {
	switch {
	case ev.To == domain.LeasePendingApproval:
		return []repository.AuditEntry{approvalAuditEntry(ev, approvalRequestedAuditEvent, auditOK, nil)}
	case ev.From == domain.LeasePendingApproval && ev.To == domain.LeaseHeld:
		return []repository.AuditEntry{approvalAuditEntry(ev, approvalGrantedAuditEvent, auditOK, nil), leaseAuditEntry(ev)}
	case ev.From == domain.LeasePendingApproval && ev.To == domain.LeaseIdle:
		return []repository.AuditEntry{approvalEndAuditEntry(ev)}
	}
	return []repository.AuditEntry{leaseAuditEntry(ev)}
}

// approvalEndAuditEntry は承認待ちが held にならずに終わった遷移（pending_approval → idle）を、理由で
// approval.denied（ホストの拒否）・approval.timeout（60 秒の失効）・approval.withdrawn（それ以外の取り下げ）にする。
func approvalEndAuditEntry(ev domain.LeaseEvent) repository.AuditEntry {
	switch ev.Reason {
	case domain.ReleaseApprovalDenied:
		return approvalAuditEntry(ev, approvalDeniedAuditEvent, auditDenied, domain.ErrApprovalDenied)
	case domain.ReleaseApprovalTimeout:
		return approvalAuditEntry(ev, approvalTimeoutAuditEvent, auditDenied, domain.ErrApprovalTimeout)
	}
	return approvalAuditEntry(ev, approvalWithdrawnAuditEvent, auditOK, nil)
}

// approvalAuditEntry は承認の遷移の監査エントリ。操作主体は承認を求めたセッション。detail は client_id・lease_id と、
// 求めた側の種別・表示名だけ（資格情報・パス・引数は入れない）。理由があれば reason を足す。
func approvalAuditEntry(ev domain.LeaseEvent, event, result string, err error) repository.AuditEntry {
	h := ev.Lease.Holder
	detail := map[string]any{
		"client_id": h.ClientID, "lease_id": ev.Lease.ID, "holder_kind": string(h.Kind), "holder_label": h.Label,
	}
	if ev.Reason != "" {
		detail["reason"] = string(ev.Reason)
	}
	e := repository.AuditEntry{
		TS:          ev.At,
		Kind:        "lease",
		Event:       event,
		ActorType:   h.Kind,
		ClientID:    h.ClientID,
		ClientLabel: h.Label,
		SessionID:   h.SessionID,
		LeaseID:     ev.Lease.ID,
		Result:      result,
		DetailJSON:  marshalDetail(detail),
	}
	if err != nil {
		e.ErrorCode = string(domain.CodeOf(err))
	}
	return e
}

// leaseAuditEntry はリースの取得・解放・剥奪の遷移を監査エントリにする（kind = lease）。
func leaseAuditEntry(ev domain.LeaseEvent) repository.AuditEntry {
	if ev.Reason == domain.ReleaseRevoked {
		return leaseRevokeAuditEntry(ev)
	}
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

func leaseRevokeAuditEntry(ev domain.LeaseEvent) repository.AuditEntry {
	by, h := ev.By, ev.Lease.Holder
	return repository.AuditEntry{
		TS:          ev.At,
		Kind:        "lease",
		Event:       "lease.revoke",
		ActorType:   by.Kind,
		ClientID:    by.ClientID,
		ClientLabel: by.Label,
		SessionID:   by.SessionID,
		LeaseID:     ev.Lease.ID,
		Result:      auditOK,
		DetailJSON: marshalDetail(map[string]any{
			"holder_kind": string(h.Kind), "holder_client_id": h.ClientID, "holder_label": h.Label,
		}),
	}
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
