package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// errorBody はツール失敗の structuredContent（API 仕様 §3.4）。
type errorBody struct {
	Code    domain.Code    `json:"code"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
	Holder  *holderBody    `json:"holder,omitempty"` // lease_held_by_other のとき
}

// toolError はツールの失敗を isError の結果にする。ツールの失敗は JSON-RPC エラーではなくツール結果で返す（§3.4）。
// コードは domain.CodeOf による。ドメインエラーでないもの（platform の想定外の失敗）は internal にして、
// 内容はログにだけ残し、クライアントへは出さない（API 仕様 §2）。
func (s *Server) toolError(tool string, err error) *mcpsdk.CallToolResult {
	body := errorBody{Code: domain.CodeOf(err)}
	var de *domain.Error
	switch {
	case errors.As(err, &de) && de.Code != domain.CodeInternal:
		body.Message = de.Msg
		if body.Message == "" {
			body.Message = string(de.Code)
		}
		body.Detail = de.Detail
	case errors.Is(err, context.DeadlineExceeded):
		body.Code = domain.CodeInternal
		body.Message = "the tool call timed out"
		s.log.Warn("mcp tool timed out", "tool", tool)
	default:
		body.Code = domain.CodeInternal
		body.Message = "an internal error occurred"
		s.log.Error("mcp tool failed", "tool", tool, "error", err)
	}
	if body.Code == domain.CodeLeaseHeldByOther {
		if h, ok := s.leases.Holder(); ok {
			body.Holder = &holderBody{Kind: string(h.Actor.Kind), Label: h.Actor.Label}
		}
	}
	return &mcpsdk.CallToolResult{
		IsError:           true,
		Content:           []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf("%s: %s", body.Code, body.Message)}},
		StructuredContent: body,
	}
}

// writeSessionLimit は同時セッションの上限を超えた新規セッションを problem+json（API 仕様 §2）で断る。
// 上限は表のコードにないため、「現在の状態では実行できない」の conflict（409）を使う。
func writeSessionLimit(w http.ResponseWriter, limit int) {
	code := domain.CodeConflict
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code.HTTPStatus())
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "urn:puppet-remo:error:" + string(code),
		"title":  "Too many MCP sessions",
		"status": code.HTTPStatus(),
		"detail": fmt.Sprintf("At most %d MCP sessions can be open at the same time.", limit),
		"code":   code,
	})
}
