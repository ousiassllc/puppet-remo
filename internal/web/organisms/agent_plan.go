package organisms

import (
	"encoding/json"
	"strconv"

	"github.com/ousiassllc/puppet-remo/internal/web/atoms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// プランの状態（service.PlanState の値）。view は service を import しないため、文字列で比べる。
const (
	planSignedOut      = "signed_out"
	planSigningIn      = "signing_in"
	planConnected      = "connected"
	planReauthRequired = "reauth_required"
	planUsageLimited   = "usage_limited"
)

// Codex のエンジンの状態（service.CodexEngineState の値）。
const (
	engineUnsupported = "unsupported"
	engineMissing     = "missing"
	engineInstalling  = "installing"
	engineInstalled   = "installed"
	engineOutdated    = "outdated"
	engineFailed      = "failed"
)

// planBadge は状態のバッジ。signIn はサインイン（公式）かログイン（Codex）かの言い回し。
func planBadge(state, signIn string) (atoms.BadgeTone, string) {
	switch state {
	case planSigningIn:
		return atoms.BadgeNeutral, signIn + "中"
	case planConnected:
		return atoms.BadgeOK, "接続済み"
	case planReauthRequired:
		return atoms.BadgeWarn, "再" + signIn + "が必要"
	case planUsageLimited:
		return atoms.BadgeWarn, "使用量の上限"
	}
	return atoms.BadgeNeutral, "未" + signIn
}

func engineBadge(state string) (atoms.BadgeTone, string) {
	switch state {
	case engineInstalled:
		return atoms.BadgeOK, "エンジン取得済み"
	case engineInstalling:
		return atoms.BadgeNeutral, "エンジン取得中"
	case engineOutdated:
		return atoms.BadgeWarn, "エンジンの版が違います"
	case engineFailed:
		return atoms.BadgeDanger, "エンジンの取得に失敗"
	case engineUnsupported:
		return atoms.BadgeNeutral, "この PC では使えません"
	}
	return atoms.BadgeNeutral, "エンジン未取得"
}

// engineReady はアカウントの操作（ログイン・モデル・ログアウト）を見せるか。取得済みのエンジンがあるとき。
func engineReady(engine string) bool {
	return engine == engineInstalled || engine == engineOutdated
}

// jsonVals は hx-vals の JSON（js: 接頭辞は使わない。CSP: §7.1）。
func jsonVals(m map[string]string) string {
	b, err := json.Marshal(m)
	if err != nil { // map[string]string は常に変換できる
		panic(err)
	}
	return string(b)
}

// cardPost はカード（outerHTML）を差し替える POST ボタンの hx-* 属性。
func cardPost(url, cardID string) atoms.Attrs {
	return atoms.Attrs{"hx-post": url, "hx-target": "#" + cardID, "hx-swap": "outerHTML", "hx-disabled-elt": "this"}
}

// modalGet は確認モーダル（#modal-root）を開く GET ボタンの hx-* 属性。
func modalGet(url string) atoms.Attrs {
	return atoms.Attrs{"hx-get": url, "hx-target": "#modal-root", "hx-swap": "innerHTML"}
}

func connectionButtonAttrs(o view.ConnectionOption) atoms.Attrs {
	if o.ConfirmURL != "" {
		a := modalGet(o.ConfirmURL)
		a["data-connection-select"] = o.Value
		return a
	}
	return atoms.Attrs{
		"hx-put": "/agent/connection", "hx-vals": jsonVals(map[string]string{"connection": o.Value}),
		"hx-target": "#agent-sections", "hx-swap": "outerHTML", "hx-disabled-elt": "this", "data-connection-select": o.Value,
	}
}

func progressText(p int) string { return strconv.Itoa(p) + "%" }

// planConnection は接続方式が ChatGPT のプラン（chatgpt・chatgpt-codex）か。
func planConnection(conn string) bool { return conn == "chatgpt" || conn == "chatgpt-codex" }

func planConnectionLabel(conn string) string {
	if conn == "chatgpt-codex" {
		return "Codex 経由の ChatGPT のプラン"
	}
	return "ChatGPT のプラン"
}

// agentEnableHint は有効化の前提の説明（接続方式ごと。FR-G01・G14）。
func agentEnableHint(conn string) string {
	switch conn {
	case "chatgpt":
		return "既定は無効です。有効にするには、ChatGPT へのサインイン（上のカード）と、下の同意が必要です。"
	case "chatgpt-codex":
		return "既定は無効です。有効にするには、Codex のエンジンの取得とログイン（上のカード）と、下の同意が必要です。"
	}
	return "既定は無効です。有効にするには、OpenAI API キーの登録と、下の同意が必要です。"
}
