package mcp

import (
	"cmp"
	"context"
	"slices"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// このファイルは接続中の MCP セッションの表を持つ。GUI のダッシュボード（service.SessionSource）と、
// クライアントの無効化・削除・トークン失効によるセッションの切断（service.SessionCloser）の実体になる。
// セッションは、連鎖が認証した最初のリクエスト（initialize）で表に載り、SDK がセッションを終えたら外れる。

// sessionRecord は表の 1 件。
type sessionRecord struct {
	info    service.SessionInfo
	tokenID string
	ss      *mcpsdk.ServerSession
}

var (
	_ service.SessionSource = (*Server)(nil)
	_ service.SessionCloser = (*Server)(nil)
)

// trackSessions は受信したリクエストのセッションを表に載せる受信ミドルウェア。
func (s *Server) trackSessions(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		s.track(req)
		return next(ctx, method, req)
	}
}

func (s *Server) track(req mcpsdk.Request) {
	extra := req.GetExtra()
	ss, ok := req.GetSession().(*mcpsdk.ServerSession)
	if extra == nil || extra.TokenInfo == nil || !ok {
		return
	}
	p, ok := extra.TokenInfo.Extra[principalKey].(service.Principal)
	id := ss.ID()
	if !ok || id == "" {
		return
	}

	s.mu.Lock()
	if _, known := s.sessions[id]; known {
		s.mu.Unlock()
		return
	}
	rec := &sessionRecord{
		info: service.SessionInfo{
			ID:       id,
			ClientID: p.Client.ID,
			Label:    cmp.Or(p.Client.DisplayName, p.Client.LoginName),
			Kind:     domain.ActorMCP,
			Since:    time.Now(),
		},
		ss: ss,
	}
	if p.Token != nil {
		rec.tokenID = p.Token.ID
	}
	s.sessions[id] = rec
	s.mu.Unlock()
	s.sessionsChanged()

	go func() {
		_ = ss.Wait()
		s.mu.Lock()
		delete(s.sessions, id)
		s.mu.Unlock()
		s.sessionsChanged()
	}()
}

func (s *Server) sessionsChanged() {
	if s.onSessions != nil {
		s.onSessions()
	}
}

// Sessions は接続中のセッションを、接続の古い順に返す。
func (s *Server) Sessions() []service.SessionInfo {
	s.mu.Lock()
	out := make([]service.SessionInfo, 0, len(s.sessions))
	for _, rec := range s.sessions {
		out = append(out, rec.info)
	}
	s.mu.Unlock()
	slices.SortFunc(out, func(a, b service.SessionInfo) int {
		if c := a.Since.Compare(b.Since); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}

// CloseClient は clientID の MCP セッションをすべて閉じる。リースの解放は切断の検知（watch）が行う。
// kind が mcp 以外（M2 の web など）のセッションは、この表にない。
func (s *Server) CloseClient(clientID string, kind domain.ActorKind) {
	if kind != domain.ActorMCP {
		return
	}
	s.closeWhere(func(r *sessionRecord) bool { return r.info.ClientID == clientID })
}

// CloseToken は tokenID の Bearer で確立した MCP セッションをすべて閉じる。
func (s *Server) CloseToken(tokenID string) {
	if tokenID == "" {
		return
	}
	s.closeWhere(func(r *sessionRecord) bool { return r.tokenID == tokenID })
}

// closeWhere は match するセッションを、ロックの外で閉じる。
func (s *Server) closeWhere(match func(*sessionRecord) bool) {
	s.mu.Lock()
	var targets []*sessionRecord
	for _, rec := range s.sessions {
		if match(rec) {
			targets = append(targets, rec)
		}
	}
	s.mu.Unlock()
	for _, rec := range targets {
		if err := rec.ss.Close(); err != nil {
			s.log.Debug("close mcp session", "session", rec.info.ID, "error", err)
		}
	}
}
