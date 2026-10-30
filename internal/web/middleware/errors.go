package middleware

import (
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// reject は連鎖が拒否したときの応答の内容。
type reject struct {
	code       domain.Code
	retryAfter time.Duration // rate_limited の Retry-After
}

// 連鎖が返すコードの題名（problem+json の title）と、利用者向けの説明。
// detail には内部情報（WhoIs の失敗理由・DB エラー）を出さない固定文だけを使う。
var rejectText = map[domain.Code]struct{ title, detail, htmlMsg string }{
	domain.CodeInvalidHost:         {"Invalid Host header", "The Host header is not an address of this server.", "このサーバーのアドレスではありません。"},
	domain.CodeInvalidOrigin:       {"Invalid Origin header", "The Origin header is not the same origin.", "同一オリジンからのリクエストではありません。"},
	domain.CodeMissingHeader:       {"Missing required header", "The X-Puppet-Remo: 1 header is required for state-changing requests.", "必須ヘッダーがありません。"},
	domain.CodeKillSwitchActive:    {"Kill switch active", "The host has stopped accepting requests.", "ホストが受付を停止しています。"},
	domain.CodeWhoisFailed:         {"Device identification failed", "The source address could not be identified in the tailnet.", "接続元を tailnet で特定できませんでした。"},
	domain.CodeDeviceNotRegistered: {"Device not registered", "This device is not registered. A connection request was sent to the host.", "このデバイスは未登録です。接続要求をホストに送信しました。ホストが許可するまでお待ちください。"},
	domain.CodeClientDisabled:      {"Client disabled", "This feature is disabled for this client.", "このクライアントでは、この機能が無効です。"},
	domain.CodeUnauthorized:        {"Unauthorized", "A valid bearer token is required.", "認証が必要です。"},
	domain.CodeRateLimited:         {"Too many failures", "Too many authentication failures. Retry later.", "認証の失敗が多すぎます。しばらくしてから再試行してください。"},
	domain.CodeInternal:            {"Internal error", "An internal error occurred.", "内部エラーが発生しました。"},
}

// writeReject は route の形式で拒否応答を書く。
func writeReject(w http.ResponseWriter, r *http.Request, kind routeKind, rj reject) {
	status := rj.code.HTTPStatus()
	text, ok := rejectText[rj.code]
	if !ok {
		rj.code, text = domain.CodeInternal, rejectText[domain.CodeInternal]
		status = rj.code.HTTPStatus()
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if rj.code == domain.CodeUnauthorized {
		h.Set("WWW-Authenticate", "Bearer")
	}
	if rj.code == domain.CodeRateLimited {
		sec := int((rj.retryAfter + time.Second - 1) / time.Second)
		h.Set("Retry-After", strconv.Itoa(max(sec, 1)))
	}
	if kind.problem() {
		writeProblem(w, status, rj.code, text.title, text.detail)
		return
	}
	writeHTML(w, r, status, rj.code, text.htmlMsg)
}

// problemBody は RFC 9457 の problem+json（API 仕様 §2）。
type problemBody struct {
	Type   string      `json:"type"`
	Title  string      `json:"title"`
	Status int         `json:"status"`
	Detail string      `json:"detail"`
	Code   domain.Code `json:"code"`
}

func writeProblem(w http.ResponseWriter, status int, code domain.Code, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(problemBody{
		Type: "urn:puppet-remo:error:" + string(code), Title: title, Status: status, Detail: detail, Code: code,
	}); err != nil {
		slog.Debug("write problem response failed", "error", err)
	}
}

// HTML の応答。インラインのスクリプト・スタイル・イベント属性は使わない（CSP。NFR-S19）。
var (
	errorPageTmpl = template.Must(template.New("error").Parse(
		`<!doctype html>
<html lang="ja">
<head><meta charset="utf-8"><title>{{.Title}}</title></head>
<body><main data-error-code="{{.Code}}"><h1>{{.Title}}</h1><p>{{.Message}}</p></main></body>
</html>
`))
	errorFragmentTmpl = template.Must(template.New("fragment").Parse(
		`<div role="alert" data-error-code="{{.Code}}">{{.Message}}</div>
`))
)

type htmlBody struct {
	Code    domain.Code
	Title   string
	Message string
}

// writeHTML は Web UI の拒否を HTML で返す。状態変更のリクエスト（アクションルート）はフラグメント、
// それ以外はエラーページ。未登録デバイスは待機ページ（FR-W02。M2 の Web UI が差し替えるまでは簡易ページ）。
func writeHTML(w http.ResponseWriter, r *http.Request, status int, code domain.Code, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	body := htmlBody{Code: code, Message: msg, Title: rejectText[code].title}
	if code == domain.CodeDeviceNotRegistered {
		body.Title = "接続要求を送信しました"
	}
	tmpl := errorPageTmpl
	if stateChanging(r) && code != domain.CodeDeviceNotRegistered {
		tmpl = errorFragmentTmpl
	}
	if err := tmpl.Execute(w, body); err != nil {
		slog.Debug("write error response failed", "error", err)
	}
}
