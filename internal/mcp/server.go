// Package mcp は MCP サーバー（Streamable HTTP）と FR-M01〜M17 のツールを提供する
// （docs/api/endpoints.md §3、docs/components/overview.md §6）。
//
// ツールの引数を domain.Op に変換して service.Control を呼び、結果とエラーを MCP の形式に直す。
// 座標は縮小スクリーンショット座標のまま Control に渡す（変換は Control が行う。FR-M16）。
// リースは MCP セッションに紐づく: セッションが閉じたら保持者のリースを解放する（FR-L05）。
// platform・repository・tailnet は import しない（CI の import 規則）。
//
// Handler は /mcp にマウントする。Host / Origin・キルスイッチ・WhoIs・許可リスト・Bearer の検査は
// 手前の web/middleware の連鎖が済ませてあるものとし、ここでは行わない。
package mcp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// 既定値（API 仕様 §3.1・§8.1、NFR-S09・A04・C02）。
const (
	// ServerName は initialize で返すサーバー名。
	ServerName = "puppet-remo"

	// MaxRequestBodyBytes は HTTP 本文の上限（NFR-S09）。
	MaxRequestBodyBytes = 1 << 20
	// DefaultSessionTimeout はセッションのアイドルタイムアウト。リースのアイドル解放とは独立（NFR-S09）。
	DefaultSessionTimeout = 30 * time.Minute
	// DefaultToolTimeout は同期のツール呼び出しのタイムアウト（NFR-S09）。
	DefaultToolTimeout = 30 * time.Second
	// DefaultKeepAlive は keepalive の ping 間隔（NFR-A04）。
	DefaultKeepAlive = 15 * time.Second
	// DefaultMaxSessions は同時セッションの上限（NFR-C02）。
	DefaultMaxSessions = 5

	// keepAliveFailureThreshold は連続した ping の失敗でセッションを閉じる回数。
	// 15 秒間隔 × 3 回 = 45 秒でタイムアウトとする（NFR-A04）。
	keepAliveFailureThreshold = 3
)

// Instructions は initialize で返す指示文（API 仕様 §3.1）。run_task は M4 で入るため、ここでは触れない。
const Instructions = "puppet-remo lets you operate the host Windows desktop. " +
	"All coordinates are in the scaled screenshot coordinate system: the origin is the top-left of each monitor " +
	"and one unit is one pixel of the screenshot returned by the screenshot tool, not a physical pixel. " +
	"Call get_screen_info for the monitors and their scale; pass monitor to address a monitor other than the primary. " +
	"Viewing tools (get_screen_info, screenshot, cursor_position, wait, list_windows, get_control_status) " +
	"do not need the control lease. " +
	"Every tool that sends input or reads the clipboard or the UI tree needs the control lease; " +
	"it is acquired implicitly on first use (acquire_control and release_control do it explicitly) " +
	"and is released when you release it, when the session ends, after an idle period, or when the host stops all control. " +
	"Only one session holds the lease at a time; others get lease_held_by_other. " +
	"Tool failures are returned with isError and structuredContent {code, message}."

// PrincipalFunc は連鎖（web/middleware）を通ったリクエストの主体を返す。
// S11 が middleware.PrincipalFrom(r.Context()) を包んで渡す（mcp は web を import できない）。
type PrincipalFunc func(r *http.Request) (service.Principal, bool)

// Config は New の設定。Control・Leases・Principal は必須。
type Config struct {
	Control   service.Control
	Leases    service.LeaseManager
	Principal PrincipalFunc
	// Version は initialize で返すアプリの版。
	Version string
	Logger  *slog.Logger // nil なら slog.Default()

	// 以下は 0 なら既定値。テストが短くする。
	SessionTimeout time.Duration
	ToolTimeout    time.Duration
	KeepAlive      time.Duration
	MaxSessions    int
}

// Server は MCP サーバー。
type Server struct {
	control   service.Control
	leases    service.LeaseManager
	principal PrincipalFunc
	log       *slog.Logger

	toolTimeout time.Duration
	maxSessions int

	sdk     *mcpsdk.Server
	handler http.Handler

	mu      sync.Mutex
	opening int                 // セッションを作っている最中のリクエスト数（上限の判定に数える）
	watched map[string]struct{} // 切断を見張っているセッション ID
}

// New は MCP サーバーを作り、全ツールを登録する。
func New(cfg Config) (*Server, error) {
	switch {
	case cfg.Control == nil:
		return nil, errors.New("mcp: Control is required")
	case cfg.Leases == nil:
		return nil, errors.New("mcp: Leases is required")
	case cfg.Principal == nil:
		return nil, errors.New("mcp: Principal is required")
	}
	s := &Server{
		control:     cfg.Control,
		leases:      cfg.Leases,
		principal:   cfg.Principal,
		log:         cfg.Logger,
		toolTimeout: cmp.Or(cfg.ToolTimeout, DefaultToolTimeout),
		maxSessions: cmp.Or(cfg.MaxSessions, DefaultMaxSessions),
		watched:     make(map[string]struct{}),
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.sdk = mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: ServerName, Version: cfg.Version},
		&mcpsdk.ServerOptions{
			Instructions: Instructions,
			Logger:       s.log,
			// keepalive の ping が連続して失敗したら、そのセッションを閉じてリースを解放する（NFR-A04）。
			KeepAlive:                 cmp.Or(cfg.KeepAlive, DefaultKeepAlive),
			KeepAliveFailureThreshold: keepAliveFailureThreshold,
		},
	)
	s.addTools()

	// SDK の設定は API 仕様 §8.1 のとおり: Stateless=false（リース保持者はセッションに束縛）、
	// SessionTimeout と MaxRequestBodyBytes は明示、EventStore は使わない、応答は既定の SSE 形式。
	// DisableLocalhostProtection は loopback からの要求にしか効かないため既定のまま（Host / Origin は連鎖が守る）。
	sdkHandler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return s.sdk },
		&mcpsdk.StreamableHTTPOptions{
			SessionTimeout:      cmp.Or(cfg.SessionTimeout, DefaultSessionTimeout),
			MaxRequestBodyBytes: MaxRequestBodyBytes,
			Logger:              s.log,
		},
	)
	// 連鎖が認証済みの主体を、SDK の TokenInfo としてセッションとツール呼び出しへ伝える。
	// UserID によって、セッションを作ったクライアント以外が同じ Mcp-Session-Id を使えなくなる。
	bearer := auth.RequireBearerToken(s.verify, nil)
	s.handler = bearer(s.limitSessions(sdkHandler))
	return s, nil
}

// Handler は /mcp にマウントする http.Handler を返す。S7 のミドルウェア連鎖の内側に置くこと。
// 連鎖を通っていない（Principal が false の）リクエスト、Authorization ヘッダーの無いリクエストは 401 にする
// （連鎖の外に置いた場合の安全側の既定。Bearer の照合そのものは連鎖が行う）。
func (s *Server) Handler() http.Handler { return s.handler }

// CloseSessions は全 MCP セッションを閉じる。キルスイッチのクローザー（service.CloserFunc）として登録する。
// 各セッションのリースは切断の検知で解放される。
func (s *Server) CloseSessions(ctx context.Context) error {
	var errs []error
	for ss := range s.sdk.Sessions() {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if err := ss.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// principalKey は TokenInfo.Extra に主体を入れるキー。
const principalKey = "puppet-remo.principal"

// verify は連鎖が済ませた認証の結果を TokenInfo にする。Bearer の照合は連鎖（Authz）が行っており、
// ここでは SDK のミドルウェアを通すために形だけの検証をする（token の文字列は見ない）。
func (s *Server) verify(_ context.Context, _ string, r *http.Request) (*auth.TokenInfo, error) {
	p, ok := s.principal(r)
	if !ok || p.Client.ID == "" {
		return nil, fmt.Errorf("%w: no authenticated principal", auth.ErrInvalidToken)
	}
	return &auth.TokenInfo{
		// SDK はリクエストごとに有効期限を見る。連鎖が今回のリクエストを認証済みなので、
		// 期限切れはここでは起こさない。
		Expiration: time.Now().Add(time.Hour),
		UserID:     p.Client.ID,
		Extra:      map[string]any{principalKey: p},
	}, nil
}

// limitSessions は同時セッションの上限（NFR-C02）を超える新規セッションを拒否する。
// Mcp-Session-Id の無い POST が新しいセッションを作る。
func (s *Server) limitSessions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Mcp-Session-Id") != "" {
			next.ServeHTTP(w, r)
			return
		}
		s.mu.Lock()
		if len(slices.Collect(s.sdk.Sessions()))+s.opening >= s.maxSessions {
			s.mu.Unlock()
			writeSessionLimit(w, s.maxSessions)
			return
		}
		s.opening++
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			s.opening--
			s.mu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
}

// watch は ss が閉じたら who のリースを解放する（FR-L05、NFR-A04）。ツールを呼ぶ最初の機会に 1 度だけ張る。
// リースを取れるのはツールの呼び出しだけなので、これより前に見張る必要はない。
func (s *Server) watch(ss *mcpsdk.ServerSession, who domain.Actor) {
	s.mu.Lock()
	if _, ok := s.watched[who.SessionID]; ok {
		s.mu.Unlock()
		return
	}
	s.watched[who.SessionID] = struct{}{}
	s.mu.Unlock()

	go func() {
		_ = ss.Wait()
		s.leases.ReleaseSession(who)
		s.mu.Lock()
		delete(s.watched, who.SessionID)
		s.mu.Unlock()
	}()
}
