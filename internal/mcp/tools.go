package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// toolResult はツール 1 回の成功結果。Content が nil なら、Structured の JSON をテキストにする。
// AuditDetail は addAuditedTool の監査の detail（引数の要約）に足す、結果から決まる値（例: 件数）。
type toolResult struct {
	Structured  any
	Content     []mcpsdk.Content
	AuditDetail map[string]any
}

// okBody は入力系ツールの成功時の出力（API 仕様 §3.3）。
type okBody struct {
	OK bool `json:"ok"`
}

var okResult = toolResult{Structured: okBody{OK: true}}

// toolFunc はツールの本体。who はこの呼び出しの操作主体（MCP セッション）。
type toolFunc[In any] func(ctx context.Context, who domain.Actor, in In) (toolResult, error)

// addTool は共通の前処理（主体の解決・セッションの見張り・タイムアウト）と後処理（結果・エラーの変換）つきでツールを登録する。
// 出力の型は any にして SDK の出力スキーマ検査を使わず、structuredContent を自分で組み立てる
// （失敗時の structuredContent が {code, message} になるため）。
// 監査は Control.Do が記録する入力系・クリップボード・UI ツリー・wait・focus_window 用。Do を通らないツールは addAuditedTool を使う。
func addTool[In any](s *Server, name, description string, readOnly bool, fn toolFunc[In]) {
	registerTool(s, name, description, readOnly, nil, fn)
}

// argsFunc は監査に残す引数の要約を返す。本文・トークンなど機微な値を入れてはならない（FR-AU03、NFR-S13）。
type argsFunc[In any] func(in In) map[string]any

// noArgsSummary は引数のないツールの監査用の要約（なし）。
func noArgsSummary(noArgs) map[string]any { return nil }

// addAuditedTool は addTool に、ツール呼び出しの監査（kind = tool。ツール名・引数の要約・結果・所要時間。FR-AU01）を足す。
// Control.Do を通らない閲覧系・リースのツール用。Do を通るツールに使うと二重に記録される。
func addAuditedTool[In any](s *Server, name, description string, readOnly bool, args argsFunc[In], fn toolFunc[In]) {
	registerTool(s, name, description, readOnly, args, fn)
}

// registerTool は addTool・addAuditedTool の実体。auditArgs が nil なら、この層では監査しない。
func registerTool[In any](s *Server, name, description string, readOnly bool, auditArgs argsFunc[In], fn toolFunc[In]) {
	tool := &mcpsdk.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: readOnly},
	}
	mcpsdk.AddTool(s.sdk, tool, func(ctx context.Context, req *mcpsdk.CallToolRequest, in In) (*mcpsdk.CallToolResult, any, error) {
		markReached(ctx)
		who, err := s.actor(req)
		if err != nil {
			return s.toolError(name, err), nil, nil
		}
		s.watch(req.Session, who)

		ctx, cancel := s.toolContext(ctx, name)
		defer cancel()
		start := time.Now()
		res, err := fn(ctx, who, in)
		s.logToolCall(ctx, name, who, readOnly, time.Since(start), err)
		if auditArgs != nil && s.audit != nil {
			detail := auditArgs(in)
			if len(res.AuditDetail) > 0 {
				detail = maps.Clone(detail)
				if detail == nil {
					detail = map[string]any{}
				}
				maps.Copy(detail, res.AuditDetail)
			}
			s.audit.Record(ctx, service.ToolAuditEntry(who, name, detail, "", err, time.Since(start)))
		}
		if err != nil {
			return s.toolError(name, err), nil, nil
		}
		return res.build(name)
	})
}

// build は成功結果を CallToolResult にする。
func (r toolResult) build(tool string) (*mcpsdk.CallToolResult, any, error) {
	content := r.Content
	if content == nil {
		text, err := json.Marshal(r.Structured)
		if err != nil {
			return nil, nil, fmt.Errorf("mcp: encode %s result: %w", tool, err)
		}
		content = []mcpsdk.Content{&mcpsdk.TextContent{Text: string(text)}}
	}
	return &mcpsdk.CallToolResult{Content: content, StructuredContent: r.Structured}, nil, nil
}

// actor は呼び出しの操作主体を返す。主体は連鎖が認証した主体（verify が TokenInfo に載せたもの）と MCP セッション ID。
func (s *Server) actor(req *mcpsdk.CallToolRequest) (domain.Actor, error) {
	var p service.Principal
	var ok bool
	if req.Extra != nil && req.Extra.TokenInfo != nil {
		p, ok = req.Extra.TokenInfo.Extra[principalKey].(service.Principal)
	}
	if !ok {
		return domain.Actor{}, service.ErrUnauthorized
	}
	if req.Session == nil || req.Session.ID() == "" {
		return domain.Actor{}, domain.NewError(domain.CodeInternal, "the call has no MCP session", nil)
	}
	label := p.Client.DisplayName
	if label == "" {
		label = p.Client.LoginName
	}
	return domain.Actor{
		Kind:      domain.ActorMCP,
		ClientID:  p.Client.ID,
		Label:     label,
		SessionID: req.Session.ID(),
	}, nil
}

// leaseAcquiringTools は、リースを取得しうるツール（acquire_control と、説明に「acquired implicitly」とある暗黙に取得するツール）。
// 承認が要るクライアントでは承認待ち（最大 60 秒）の間ブロックするので、通常より長いタイムアウト（Config.LeaseToolTimeout）にして、
// クライアントが approval_timeout を受け取れるようにする。launch_app への承認は取得時の 1 回だけで、保持中の呼び出しごとの承認は無い。
var leaseAcquiringTools = map[string]bool{
	"acquire_control": true,
	"mouse_move":      true, "left_click": true, "right_click": true, "middle_click": true, "double_click": true, "triple_click": true,
	"left_click_drag": true, "scroll": true, "key": true, "type": true,
	"focus_window": true, "clipboard_read": true, "clipboard_write": true, "get_ui_tree": true,
	"launch_app": true,
}

// addTools は FR-M01〜M15・M18・M19 の全ツールを登録する。run_task / get_task / cancel_task は M4 のため登録しない。
func (s *Server) addTools() {
	s.addViewTools()
	s.addInputTools()
	s.addWindowTools()
	s.addLeaseTools()
	s.addAppTools()
}
