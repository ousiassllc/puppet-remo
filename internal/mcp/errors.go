package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// errorBody はツール失敗の structuredContent（API 仕様 §3.4）。
type errorBody struct {
	Code    domain.Code    `json:"code"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
	Holder  *holderBody    `json:"holder,omitempty"` // lease_held_by_other のとき
	Reason  string         `json:"reason,omitempty"` // launch_failed のとき（file_missing / access_denied / elevation_required / other）
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
		body.Detail = errorDetail(de)
		if de.Code == domain.CodeLaunchFailed {
			body.Detail, body.Reason = nil, launchReason(de)
		}
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
			body.Holder = &holderBody{Kind: string(h.Actor.Kind), Label: h.Actor.Label} // lease_held_by_other には task_id を載せない
		}
	}
	return &mcpsdk.CallToolResult{
		IsError:           true,
		Content:           []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf("%s: %s", body.Code, body.Message)}},
		StructuredContent: body,
	}
}

// errorDetail は de.Detail を出力の形にする。own_windows（[]domain.OwnRegion）は rect の JSON 名（x, y, width, height）に直す。
func errorDetail(de *domain.Error) map[string]any {
	regions, ok := de.Detail[service.DetailOwnWindows].([]domain.OwnRegion)
	if !ok {
		return de.Detail
	}
	detail := maps.Clone(de.Detail)
	detail[service.DetailOwnWindows] = ownWindowBodies(regions)
	return detail
}

// logToolCall はツール実行の結果を運用ログに出す（監査とは別。引数・結果の中身は出さない。FR-AU03、NFR-S13）。
// 成功は、状態を変える操作を Info、読み取りだけのツール（スクリーンショットの取得など。頻繁に呼ばれる）を Debug にする。
// 失敗のうち、拒否・不正な引数などのドメインエラーは Warn。想定外の失敗とタイムアウトは toolError が Error・Warn で出す。
func (s *Server) logToolCall(ctx context.Context, tool string, who domain.Actor, readOnly bool, took time.Duration, err error) {
	attrs := []any{"tool", tool, "client", who.Label, "took_ms", took.Milliseconds()}
	var de *domain.Error
	switch {
	case err == nil && readOnly:
		s.log.Log(ctx, slog.LevelDebug, "mcp tool succeeded", attrs...)
	case err == nil:
		s.log.Log(ctx, slog.LevelInfo, "mcp tool succeeded", attrs...)
	case errors.As(err, &de) && de.Code != domain.CodeInternal:
		s.log.Log(ctx, slog.LevelWarn, "mcp tool was rejected", append(attrs, "code", string(de.Code))...)
	}
}

// launchReason は launch_failed の reason を返す。定められた 4 値以外（パスや引数になりうる値）は other にする。
func launchReason(de *domain.Error) string {
	reason, _ := de.Detail["reason"].(domain.LaunchFailure)
	if !reason.Valid() {
		reason = domain.LaunchOther
	}
	return string(reason)
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
