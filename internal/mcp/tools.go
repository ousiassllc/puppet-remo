package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// toolResult はツール 1 回の成功結果。Content が nil なら、Structured の JSON をテキストにする。
type toolResult struct {
	Structured any
	Content    []mcpsdk.Content
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
func addTool[In any](s *Server, name, description string, readOnly bool, fn toolFunc[In]) {
	tool := &mcpsdk.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: readOnly},
	}
	mcpsdk.AddTool(s.sdk, tool, func(ctx context.Context, req *mcpsdk.CallToolRequest, in In) (*mcpsdk.CallToolResult, any, error) {
		who, err := s.actor(req)
		if err != nil {
			return s.toolError(name, err), nil, nil
		}
		s.watch(req.Session, who)

		ctx, cancel := context.WithTimeout(ctx, s.toolTimeout)
		defer cancel()
		res, err := fn(ctx, who, in)
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

// addTools は FR-M01〜M15 の全ツールを登録する。run_task / get_task / cancel_task は M4 のため登録しない。
func (s *Server) addTools() {
	s.addViewTools()
	s.addInputTools()
	s.addWindowTools()
	s.addLeaseTools()
}
