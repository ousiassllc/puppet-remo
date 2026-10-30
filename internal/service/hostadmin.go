package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// ConnRequest は接続要求（connection_requests の 1 行）。web 層は repository を import できないため別名で公開する。
type ConnRequest = repository.ConnRequest

// 接続要求の status（connection_requests.status）。
const (
	ConnPending  = repository.ConnPending
	ConnApproved = repository.ConnApproved
	ConnDenied   = repository.ConnDenied
)

// ApproveInput は接続要求を許可するときの入力（FR-H03）。
// M1 の承認モードは常に always（DB の既定値）で、M3 まで参照しない。
type ApproveInput struct {
	DisplayName  string // 空なら要求の端末名
	WebEnabled   bool
	MCPEnabled   bool
	AgentEnabled bool
}

// ClientUpdate はクライアントの部分更新（FR-H04）。nil の項目は変えない。承認モードは M3 から。
type ClientUpdate struct {
	WebEnabled   *bool
	MCPEnabled   *bool
	AgentEnabled *bool
}

// SessionCloser は、クライアントの無効化・削除・トークン失効のときに、該当するセッションを切断する窓口
// （MCP サーバーのセッション表が実装する。M2 から Web も）。
type SessionCloser interface {
	// CloseClient は clientID の kind のセッションを閉じる。
	CloseClient(clientID string, kind domain.ActorKind)
	// CloseToken は tokenID の Bearer で確立した MCP セッションを閉じる。
	CloseToken(tokenID string)
}

// HostAdmin はホスト所有者が GUI で行う許可リストの管理（FR-H03、FR-H04）。
// すべて操作主体 host として監査ログに記録する。トークンの平文は監査・ログに出さない。
//
// 対象（クライアント・トークン）が無いときは ErrNotFound、状態が合わない（pending でない、重複登録、ラベル重複）は
// domain.CodeConflict、入力の不備は domain.CodeInvalidArgument で返す。
type HostAdmin interface {
	PendingRequests(ctx context.Context) ([]ConnRequest, error)
	// ApproveRequest は要求を許可してクライアントを登録する。同じデバイスの登録済みクライアント
	// （ログイン名の変更など）があれば、そのトークン・セッション・リースごと置き換える。
	ApproveRequest(ctx context.Context, requestID string, in ApproveInput) (domain.Client, error)
	// DenyRequest は要求を拒否する（24 時間は同デバイスの再要求を作らない）。
	DenyRequest(ctx context.Context, requestID string) error

	Clients(ctx context.Context) ([]domain.Client, error)
	Client(ctx context.Context, id string) (domain.Client, error)
	// UpdateClient は有効化の切り替えを保存する。無効にした機能のセッションは切断し、リースは解放する。
	UpdateClient(ctx context.Context, id string, u ClientUpdate) (domain.Client, error)
	// DeleteClient はクライアントを許可リストから外す（トークンは CASCADE で消える）。セッションを切断し、リースを解放する。
	DeleteClient(ctx context.Context, id string) error

	Tokens(ctx context.Context, clientID string) ([]TokenRecord, error)
	// IssueToken はトークンを発行して平文を返す（平文を得られるのはこのときだけ）。ttl の意味は Authz.IssueToken と同じ。
	IssueToken(ctx context.Context, clientID, label string, ttl *time.Duration) (plaintext string, err error)
	// RevokeToken は clientID のトークン tokenID を失効し、そのセッションを切断する。
	RevokeToken(ctx context.Context, clientID, tokenID string) error
}

// HostAdminConfig は NewHostAdmin の設定。Clients・Tokens・ConnRequests・Authz・Clock・IDs は必須。
type HostAdminConfig struct {
	Clients      repository.Clients
	Tokens       repository.Tokens
	ConnRequests repository.ConnRequests
	Authz        Authz
	Leases       LeaseManager  // nil なら無効化・削除でリースを解放しない
	Sessions     SessionCloser // nil なら切断しない
	Audit        AuditRecorder // nil なら記録しない
	Bus          *Bus          // nil なら通知しない
	Clock        domain.Clock
	IDs          domain.IDGen
	Logger       *slog.Logger // nil なら slog.Default()
}

type hostAdmin struct {
	cfg HostAdminConfig
	// approveMu は ApproveRequest を直列にする（既存クライアントの置き換えと要求の確定を並行させない）。
	approveMu sync.Mutex
}

// NewHostAdmin は HostAdmin を作る。
func NewHostAdmin(cfg HostAdminConfig) (HostAdmin, error) {
	switch {
	case cfg.Clients == nil, cfg.Tokens == nil, cfg.ConnRequests == nil, cfg.Authz == nil:
		return nil, errors.New("service: host admin requires Clients, Tokens, ConnRequests and Authz")
	case cfg.Clock == nil, cfg.IDs == nil:
		return nil, errors.New("service: host admin requires Clock and IDs")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &hostAdmin{cfg: cfg}, nil
}

// ErrNotFound は HostAdmin の対象（クライアント・トークン）が無いときのエラー（errors.Is で判定する）。
// GUI は 404 にする。接続要求は pending でなければ conflict（API 仕様 §7.2）。
var ErrNotFound = errors.New("service: not found")

func errNotFound(what string) error { return fmt.Errorf("%s: %w", what, ErrNotFound) }

// mapRepoErr は repository の「無い」を ErrNotFound に、「衝突」を API 仕様 §2 の conflict に写す。
func mapRepoErr(err error, what string) error {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return errNotFound(what)
	case errors.Is(err, repository.ErrConflict):
		return domain.NewError(domain.CodeConflict, what+" conflicts with the current state", nil)
	}
	return err
}

func (h *hostAdmin) PendingRequests(ctx context.Context) ([]ConnRequest, error) {
	return h.cfg.ConnRequests.ListPending(ctx)
}

func (h *hostAdmin) ApproveRequest(ctx context.Context, requestID string, in ApproveInput) (domain.Client, error) {
	// 置き換えた古いクライアントの切断は approveMu の外で行う。CloseClient は進行中のツール呼び出し（最長でツールの
	// タイムアウト）を待つので、ロックを持ったままだと他の許可や GUI の操作が止まる。defer は後入れ先出しなので、
	// Unlock のあとに走る。置き換えは済んでいる（トークンは消えている）ので、確定に失敗しても切断は行う。
	var replacedID string
	defer func() {
		if replacedID != "" {
			h.disconnect(replacedID, domain.ActorMCP)
			h.disconnect(replacedID, domain.ActorWeb)
		}
	}()
	h.approveMu.Lock()
	defer h.approveMu.Unlock()
	req, err := h.pending(ctx, requestID)
	if err != nil {
		return domain.Client{}, err
	}
	name := strings.TrimSpace(in.DisplayName)
	if name == "" {
		name = req.DisplayName
	}
	c := domain.Client{
		ID:           h.cfg.IDs.NewID(),
		StableNodeID: req.StableNodeID,
		LoginName:    req.LoginName,
		DisplayName:  name,
		WebEnabled:   in.WebEnabled,
		MCPEnabled:   in.MCPEnabled,
		AgentEnabled: in.AgentEnabled,
		ApprovalMode: domain.ApprovalAlways,
	}
	// 同じデバイス（stable_node_id）の登録済みクライアントがあれば置き換える（ログイン名の変更後の再許可。FR-A06）。
	// 古いクライアントのトークンは CASCADE で消え、セッションとリースは切断・解放するので、権限は引き継がない。
	old, err := h.cfg.Clients.ByNode(ctx, req.StableNodeID)
	switch {
	case err == nil:
		if err := h.cfg.Clients.Replace(ctx, old.ID, c); err != nil {
			return domain.Client{}, mapRepoErr(err, "client")
		}
		replacedID = old.ID
	case errors.Is(err, repository.ErrNotFound):
		if err := h.cfg.Clients.Create(ctx, c); err != nil {
			return domain.Client{}, mapRepoErr(err, "client")
		}
	default:
		return domain.Client{}, err
	}
	if err := h.cfg.ConnRequests.Decide(ctx, requestID, repository.ConnApproved, &c.ID); err != nil {
		// 登録だけが残らないように戻す。戻せなかったら許可リストに残るのでログに出す。
		// 置き換えた古いクライアントは戻さない（要求は pending のままなので、許可し直せる）。
		if derr := h.cfg.Clients.Delete(ctx, c.ID); derr != nil {
			h.cfg.Logger.Error("roll back client after failed approval", "client_id", c.ID, "error", derr)
		}
		return domain.Client{}, mapRepoErr(err, "connection request")
	}
	if old.ID != "" {
		h.audit(ctx, "client", "client.replace", c, map[string]any{
			"request_id": requestID, "stable_node_id": c.StableNodeID,
			"old_client_id": old.ID, "old_login_name": old.LoginName, "login_name": c.LoginName,
		})
	}
	h.audit(ctx, "auth", "auth.connreq_approve", c, map[string]any{
		"request_id": requestID, "stable_node_id": c.StableNodeID, "login_name": c.LoginName,
		"web_enabled": c.WebEnabled, "mcp_enabled": c.MCPEnabled, "agent_enabled": c.AgentEnabled,
	})
	return c, nil
}

func (h *hostAdmin) DenyRequest(ctx context.Context, requestID string) error {
	req, err := h.pending(ctx, requestID)
	if err != nil {
		return err
	}
	if err := h.cfg.ConnRequests.Decide(ctx, requestID, repository.ConnDenied, nil); err != nil {
		return mapRepoErr(err, "connection request")
	}
	h.audit(ctx, "auth", "auth.connreq_deny", domain.Client{DisplayName: req.DisplayName}, map[string]any{
		"request_id": requestID, "stable_node_id": req.StableNodeID, "login_name": req.LoginName,
	})
	return nil
}

// pending は pending の接続要求を返す。無い・pending でないなら conflict。
func (h *hostAdmin) pending(ctx context.Context, id string) (ConnRequest, error) {
	rs, err := h.cfg.ConnRequests.ListPending(ctx)
	if err != nil {
		return ConnRequest{}, err
	}
	for _, r := range rs {
		if r.ID == id {
			return r, nil
		}
	}
	return ConnRequest{}, domain.NewError(domain.CodeConflict, "connection request is not pending", nil)
}

func (h *hostAdmin) Clients(ctx context.Context) ([]domain.Client, error) {
	return h.cfg.Clients.List(ctx)
}

func (h *hostAdmin) Client(ctx context.Context, id string) (domain.Client, error) {
	c, err := h.cfg.Clients.Get(ctx, id)
	return c, mapRepoErr(err, "client")
}

func (h *hostAdmin) UpdateClient(ctx context.Context, id string, u ClientUpdate) (domain.Client, error) {
	before, err := h.Client(ctx, id)
	if err != nil {
		return domain.Client{}, err
	}
	err = h.cfg.Clients.Update(ctx, id, repository.ClientPatch{
		WebEnabled: u.WebEnabled, MCPEnabled: u.MCPEnabled, AgentEnabled: u.AgentEnabled,
	})
	if err != nil {
		return domain.Client{}, mapRepoErr(err, "client")
	}
	after, err := h.Client(ctx, id)
	if err != nil {
		return domain.Client{}, err
	}
	if before.MCPEnabled && !after.MCPEnabled {
		h.disconnect(id, domain.ActorMCP)
	}
	if before.WebEnabled && !after.WebEnabled {
		h.disconnect(id, domain.ActorWeb)
	}
	h.audit(ctx, "client", "client.update", after, map[string]any{
		"web_enabled": after.WebEnabled, "mcp_enabled": after.MCPEnabled, "agent_enabled": after.AgentEnabled,
	})
	return after, nil
}

func (h *hostAdmin) DeleteClient(ctx context.Context, id string) error {
	c, err := h.Client(ctx, id)
	if err != nil {
		return err
	}
	if err := h.cfg.Clients.Delete(ctx, id); err != nil {
		return mapRepoErr(err, "client")
	}
	h.disconnect(id, domain.ActorMCP)
	h.disconnect(id, domain.ActorWeb)
	h.audit(ctx, "client", "client.delete", c, map[string]any{"stable_node_id": c.StableNodeID, "login_name": c.LoginName})
	return nil
}

// disconnect は clientID の kind のセッションを切断し、そのリースを解放する。
func (h *hostAdmin) disconnect(clientID string, kind domain.ActorKind) {
	if h.cfg.Sessions != nil {
		h.cfg.Sessions.CloseClient(clientID, kind)
	}
	if h.cfg.Leases != nil {
		if holder, ok := h.cfg.Leases.Holder(); ok && holder.Actor.ClientID == clientID && holder.Actor.Kind == kind {
			h.cfg.Leases.ReleaseSession(holder.Actor)
		}
	}
	if h.cfg.Bus != nil {
		h.cfg.Bus.Publish(Event{Topic: TopicSessions})
		h.cfg.Bus.Publish(Event{Topic: TopicLease})
	}
}

func (h *hostAdmin) Tokens(ctx context.Context, clientID string) ([]TokenRecord, error) {
	return h.cfg.Tokens.ListByClient(ctx, clientID)
}

func (h *hostAdmin) IssueToken(ctx context.Context, clientID, label string, ttl *time.Duration) (string, error) {
	c, err := h.Client(ctx, clientID)
	if err != nil {
		return "", err
	}
	plaintext, err := h.cfg.Authz.IssueToken(ctx, clientID, label, ttl)
	if err != nil {
		return "", err
	}
	detail := map[string]any{"label": strings.TrimSpace(label)}
	switch {
	case ttl == nil:
		detail["expires_in_sec"] = int64(DefaultTokenTTL / time.Second)
	case *ttl == 0:
		detail["expires_in_sec"] = nil
	default:
		detail["expires_in_sec"] = int64(*ttl / time.Second)
	}
	h.audit(ctx, "token", "token.issue", c, detail)
	return plaintext, nil
}

func (h *hostAdmin) RevokeToken(ctx context.Context, clientID, tokenID string) error {
	c, err := h.Client(ctx, clientID)
	if err != nil {
		return err
	}
	tokens, err := h.cfg.Tokens.ListByClient(ctx, clientID)
	if err != nil {
		return err
	}
	var tok *TokenRecord
	for i := range tokens {
		if tokens[i].ID == tokenID {
			tok = &tokens[i]
		}
	}
	if tok == nil {
		return errNotFound("token")
	}
	if err := h.cfg.Authz.RevokeToken(ctx, tokenID); err != nil {
		return mapRepoErr(err, "token")
	}
	if h.cfg.Sessions != nil {
		h.cfg.Sessions.CloseToken(tokenID)
	}
	if h.cfg.Bus != nil {
		h.cfg.Bus.Publish(Event{Topic: TopicSessions})
	}
	h.audit(ctx, "token", "token.revoke", c, map[string]any{"token_id": tokenID, "label": tok.Label})
	return nil
}

// audit はホスト所有者の操作を actor_type = host で記録する。detail にトークンやハッシュを入れてはならない。
func (h *hostAdmin) audit(ctx context.Context, kind, event string, c domain.Client, detail map[string]any) {
	if h.cfg.Audit == nil {
		return
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		h.cfg.Logger.Warn("marshal audit detail failed", "event", event, "error", err)
		raw = nil
	}
	h.cfg.Audit.Record(ctx, repository.AuditEntry{
		TS:          h.cfg.Clock.Now(),
		Kind:        kind,
		Event:       event,
		ActorType:   domain.ActorHost,
		ClientID:    c.ID,
		ClientLabel: c.DisplayName,
		Result:      auditOK,
		DetailJSON:  raw,
	})
}
