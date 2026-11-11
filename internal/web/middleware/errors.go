package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/a-h/templ"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/atoms"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// reject は連鎖が拒否したときの応答の内容。
type reject struct {
	code       domain.Code
	retryAfter time.Duration // rate_limited の Retry-After
	// connState は device_not_registered のときの接続要求の状態（HTML の待機ページの出し分け。空なら待機）。
	connState service.ConnRequestState
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
	writeHTML(w, r, status, rj, text.title, text.htmlMsg)
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
// ページは pages（Templ）、状態変更のリクエスト（アクションルート）のフラグメントは atoms.Alert。

// writeHTML は Web UI の拒否を HTML で返す。状態変更のリクエスト（アクションルート）はフラグメント、
// それ以外はエラーページ。未登録デバイスは、接続要求の状態（rj.connState）に応じた待機・拒否・失効のページ（FR-W02）で、
// アクションルートにもページを返す。
func writeHTML(w http.ResponseWriter, r *http.Request, status int, rj reject, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var comps []templ.Component
	switch {
	case rj.code == domain.CodeDeviceNotRegistered:
		SetNoticeCSP(w.Header())
		comps = []templ.Component{notRegisteredPage(rj.connState)}
	case stateChanging(r):
		// htmx は 4xx / 5xx を既定では差し込まないので、ハンドラの失敗（handler/remote の failMessage）と同じく
		// HX-Retarget で #alert-root へ回す。確認モーダル（強制剥奪）の確定の拒否でもアラートが暗幕の背面に隠れないよう、モーダルを閉じる OOB を添える（stateChanging は GET を含まないので、
		// GET の失敗で開いているモーダルを閉じることはない）。
		w.Header().Set("HX-Retarget", "#alert-root")
		w.Header().Set("HX-Reswap", "innerHTML")
		comps = []templ.Component{
			atoms.Alert(atoms.AlertProps{Tone: atoms.AlertDanger, Code: string(rj.code), Heading: "エラー", Message: msg, Dismissible: true}),
			organisms.ModalClear(),
		}
	default:
		SetNoticeCSP(w.Header())
		comps = []templ.Component{pages.RemoteError(view.RemoteError{Code: string(rj.code), Title: title, Message: msg})}
	}
	w.WriteHeader(status)
	for _, comp := range comps {
		if err := comp.Render(r.Context(), w); err != nil {
			slog.Debug("write error response failed", "error", err)
			return
		}
	}
}

// notRegisteredPage は未登録デバイスのページ。状態が分からないとき（空・pending・読めなかったとき）は待機ページ。
func notRegisteredPage(state service.ConnRequestState) templ.Component {
	switch state {
	case service.ConnRequestDenied:
		return pages.Denied()
	case service.ConnRequestExpired:
		return pages.Expired()
	}
	return pages.Pending()
}
