package mcp

import (
	"context"
	"sync/atomic"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/service"
)

// launch_app の入力スキーマ違反の監査（FR-AU06）。
//
// 入力スキーマに合わない呼び出し（app_id の欠落・型違い・追加のプロパティ）は、SDK が実行前に拒否する（IsError の結果を返す）ため、
// ツールの本体にも Control.Launch にも届かず、そのままでは監査に残らない。受信ミドルウェアが、本体に届かなかった launch_app の
// 呼び出しを tool.launch_app（denied・invalid_argument）として 1 件記録する。本体に届いた呼び出しは Control.Launch が記録する。
//
// 記録するのは launch_app だけ。他のツール（mouse_move など）のスキーマ違反は何も実行せず状態を変えないため、FR-AU01 の範囲外とする
// （決定事項。#38。docs/api/endpoints.md §3.4）。

// auditedWhenRejected は、スキーマ違反の拒否を受信側で記録するツール。
const auditedWhenRejected = "launch_app"

// reachedKey は、本体に届いたかを registerTool のハンドラが受信ミドルウェアへ知らせるための ctx のキー。
type reachedKey struct{}

// markReached は、ツールの本体に届いたことを記録する（受信ミドルウェアが付けた印があるときだけ）。
func markReached(ctx context.Context) {
	if reached, ok := ctx.Value(reachedKey{}).(*atomic.Bool); ok {
		reached.Store(true)
	}
}

// auditRejectedLaunch は、SDK がスキーマ違反で拒否した launch_app の呼び出しを監査へ記録する受信ミドルウェア。
func (s *Server) auditRejectedLaunch(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		call, ok := req.(*mcpsdk.CallToolRequest)
		if method != "tools/call" || s.audit == nil || !ok || call.Params == nil || call.Params.Name != auditedWhenRejected {
			return next(ctx, method, req)
		}
		var reached atomic.Bool
		start := time.Now()
		res, err := next(context.WithValue(ctx, reachedKey{}, &reached), method, req)
		if reached.Load() {
			return res, err
		}
		// 本体に届かなかった。認証済みの主体を解決できない呼び出しは、連鎖が先に拒否しているのでここでは記録しない。
		if who, aerr := s.actor(call); aerr == nil {
			s.audit.Record(ctx, service.SchemaRejectedAuditEntry(who, call.Params.Name, time.Since(start)))
		}
		return res, err
	}
}
