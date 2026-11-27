package gui

import (
	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルは ChatGPT のプランを使う接続方式（公式 = chatgpt、Codex 経由 = chatgpt-codex。#247・FR-G25〜G30・FR-H11）の画面の表示モデルを作る。
// トークン・認可コード・ID トークンは、どの戻り値にも無い。メールは本人の確認用として、接続中のカードにだけ出す（NFR-S13）。

const (
	chatgptCardID = "chatgpt-card"
	codexCardID   = "codex-card"

	chatgptRoute = "/agent/chatgpt"
	codexRoute   = "/agent/codex"

	connectionRoute        = "/agent/connection"
	connectionConfirmRoute = connectionRoute + "/api-key/confirm"

	// 接続方式の切り替えの理由（監査用。FR-G30 (4)）。
	reasonUsageLimit = "usage_limit"
	reasonManual     = "manual"
)

// connectionOf は設定が持つ接続方式（未設定なら既定）。
func connectionOf(s service.AgentSettingsView) domain.AgentConnection {
	if s.Connection == "" {
		return domain.DefaultAgentConnection
	}
	return s.Connection
}

func (h *Handler) connectionPanel(s service.AgentSettingsView) view.ConnectionPanel {
	cur := connectionOf(s)
	opts := []view.ConnectionOption{
		{
			Value: string(domain.ConnectionChatGPT), Label: "ChatGPT のプラン",
			Description: "公式の Sign in with ChatGPT。ChatGPT のアカウントのプランの使用量で動かします（既定）。",
		},
		{
			Value: string(domain.ConnectionChatGPTCodex), Label: "ChatGPT のプラン（Codex 経由）",
			Description: "公式のサインインがワークスペースのアカウントを拒否するときの代わり。Codex のエンジンを使い、ChatGPT のプランの使用量で動かします。",
		},
		{
			Value: string(domain.ConnectionOpenAIAPIKey), Label: "OpenAI API キー",
			Description: "OpenAI API の従量課金で動かします。キーの登録が必要です。切り替えは確認を経ます。",
			ConfirmURL:  connectionConfirmRoute + "?reason=" + reasonManual,
		},
	}
	for i := range opts {
		opts[i].Current = domain.AgentConnection(opts[i].Value) == cur
		if opts[i].Current {
			opts[i].ConfirmURL = ""
		}
	}
	return view.ConnectionPanel{Options: opts, Current: string(cur)}
}

func (h *Handler) chatgptCardView(s service.AgentSettingsView) view.ChatGPTCard {
	st := h.d.ChatGPT.Status()
	v := view.ChatGPTCard{
		Selected: connectionOf(s) == domain.ConnectionChatGPT, State: string(st.State),
		Problem: string(st.Problem), ProblemMessage: chatgptProblemMessage(st.Problem), OfferCodex: st.Problem == service.PlanProblemPolicyDenied,
		Model:               planModelSection("chatgpt-model", s.ChatGPTModel, chatgptRoute, chatgptCardID),
		OpenAIKeyRegistered: s.OpenAIKeyRegistered,
		WelcomePending:      st.WelcomePending && st.State == service.PlanConnected,
	}
	if st.State == service.PlanConnected || st.State == service.PlanUsageLimited {
		v.Email = st.AccountEmail
	}
	v.Rev = revision(v)
	return v
}

func (h *Handler) codexCardView(s service.AgentSettingsView) view.CodexCard {
	st := h.d.Codex.Status()
	v := view.CodexCard{
		Selected: connectionOf(s) == domain.ConnectionChatGPTCodex,
		Engine:   string(st.Engine.State), EngineVersion: st.Engine.Version, Progress: st.Engine.Progress, EngineError: st.Engine.Error,
		State: string(st.State), Problem: string(st.Problem), ProblemMessage: codexProblemMessage(st.Problem),
		Model:               planModelSection("codex-model", s.CodexModel, codexRoute, codexCardID),
		OpenAIKeyRegistered: s.OpenAIKeyRegistered,
	}
	if st.Engine.State != service.CodexEngineInstalling {
		v.Progress = 0
	}
	if st.State == service.PlanConnected || st.State == service.PlanUsageLimited {
		v.Email, v.PlanType = st.AccountEmail, st.PlanType
	}
	v.Rev = revision(v)
	return v
}

func planModelSection(id, current, route, cardID string) view.PlanModelSection {
	return view.PlanModelSection{ID: id, Current: current, ModelsURL: route + "/models", PutURL: route, CardID: cardID}
}

// planModelChoices はモデルの一覧を選択肢にする（サーバーが返した順のまま。表示名が空なら slug）。
func planModelChoices(sec view.PlanModelSection, models []service.PlanModel) view.PlanModelSection {
	sec.Options = make([]view.PlanModelOption, 0, len(models))
	for _, m := range models {
		label := m.DisplayName
		if label == "" {
			label = m.Slug
		}
		sec.Options = append(sec.Options, view.PlanModelOption{Slug: m.Slug, Label: label, Selected: m.Slug == sec.Current})
	}
	return sec
}

// planModelCurrentOnly は一覧を取得できなかったときの、現在の値だけの選択肢。
func planModelCurrentOnly(sec view.PlanModelSection) view.PlanModelSection {
	sec.Options = []view.PlanModelOption{{Slug: sec.Current, Label: sec.Current, Selected: true}}
	return sec
}

// chatgptProblemMessage は公式の経路の直近の失敗の説明（固定文。秘密・本文を含まない。FR-G28〜G30）。
func chatgptProblemMessage(p service.PlanProblem) string {
	switch p {
	case service.PlanProblemAccessDenied:
		return "ChatGPT へのサインインが許可されませんでした。もう一度「Continue with ChatGPT」から進めてください。"
	case service.PlanProblemScopeMissing:
		return "ChatGPT のプランの使用が許可されませんでした（アプリへの許可に「ChatGPT のプランを使う」が含まれていません）。もう一度許可するか、接続方式を OpenAI API キーにしてください。"
	case service.PlanProblemPolicyDenied:
		return "お使いの ChatGPT アカウント（ワークスペース）のポリシーで、このアプリへのサインインが拒否されました。ワークスペースの管理者に確認するか、「Codex 経由で使う」を試してください。"
	case service.PlanProblemNotEligible:
		return "このアカウントでは、ChatGPT のプランをこのアプリから使えません（利用資格がありません）。同じ操作は繰り返しません。ChatGPT のプランの設定を確認してください。"
	case service.PlanProblemAccountMismatch:
		return "前回と別の ChatGPT アカウントでサインインしようとしたため、拒否しました。前回のアカウントでサインインするか、サインインしたあとの「解除」で「別のアカウントでサインインする」を選んでください。"
	case service.PlanProblemTimeout:
		return "サインインが 5 分以内に終わらなかったため、中止しました。もう一度お試しください。"
	case service.PlanProblemRevokeFailed:
		return "ChatGPT 側の失効を確認できませんでした。ChatGPT の設定からアプリの接続を解除できます。"
	case service.PlanProblemFailed:
		return "ChatGPT のサインインに失敗しました。時間をおいて、もう一度お試しください。"
	}
	return ""
}

// codexProblemMessage は Codex の経路の直近の失敗の説明。
func codexProblemMessage(p service.PlanProblem) string {
	switch p {
	case service.PlanProblemAccessDenied:
		return "Codex のログインが許可されませんでした。もう一度ログインしてください。"
	case service.PlanProblemScopeMissing, service.PlanProblemPolicyDenied:
		return "ワークスペースの設定で、Codex の利用が許可されていません。管理者に、Codex Local の許可を確認してください。"
	case service.PlanProblemNotEligible:
		return "このアカウントでは、Codex を使えません（利用資格がありません）。ChatGPT のプランの設定を確認してください。"
	case service.PlanProblemAccountMismatch:
		return "前回と別のアカウントでログインしようとしたため、拒否しました。"
	case service.PlanProblemTimeout:
		return "ログインが時間内に終わらなかったため、中止しました。もう一度お試しください。"
	case service.PlanProblemRevokeFailed:
		return "ログアウトの確認ができませんでした。ChatGPT の設定からアプリの接続を確認できます。"
	case service.PlanProblemFailed:
		return "Codex のログインに失敗しました。時間をおいて、もう一度お試しください。"
	}
	return ""
}

// agentDisabledMessages は agent_disabled の detail.reason ごとの 409 の文言（FR-G01）。
var agentDisabledMessages = map[string]string{
	domain.AgentDisabledOpenAIKeyMissing:      "OpenAI API キーが登録されていないため、エージェントを有効にできません。先に API キーを登録してください。",
	domain.AgentDisabledChatGPTNotSignedIn:    "ChatGPT にサインインしていないため、エージェントを有効にできません。先に ChatGPT のカードの「Continue with ChatGPT」でサインインしてください。",
	domain.AgentDisabledChatGPTSigningIn:      "ChatGPT のサインインが進行中のため、エージェントを有効にできません。サインインが終わってから、もう一度お試しください。",
	domain.AgentDisabledChatGPTReauthRequired: "ChatGPT の再サインインが必要なため、エージェントを有効にできません。ChatGPT のカードからもう一度サインインしてください。",
	domain.AgentDisabledChatGPTUsageLimited:   "ChatGPT のプランの使用量の上限に達しているため、エージェントを有効にできません。ChatGPT のカードで「再開」するか、接続方式を変えてください。",
	domain.AgentDisabledCodexUnsupported:      "この PC の OS・CPU では Codex 経由の接続を使えないため、エージェントを有効にできません。接続方式を変えてください。",
	domain.AgentDisabledCodexEngineMissing:    "Codex のエンジンを取得していないため、エージェントを有効にできません。Codex のカードで「エンジンを取得する」を押してください。",
	domain.AgentDisabledCodexNotSignedIn:      "Codex にログインしていないため、エージェントを有効にできません。Codex のカードの「Codex でログイン」でログインしてください。",
	domain.AgentDisabledCodexSigningIn:        "Codex のログインが進行中のため、エージェントを有効にできません。ログインが終わってから、もう一度お試しください。",
	domain.AgentDisabledCodexReauthRequired:   "Codex の再ログインが必要なため、エージェントを有効にできません。Codex のカードからもう一度ログインしてください。",
	domain.AgentDisabledCodexUsageLimited:     "Codex 経由の ChatGPT のプランの使用量の上限に達しているため、エージェントを有効にできません。Codex のカードで「再開」するか、接続方式を変えてください。",
}

const agentDisabledFallback = "選んだ接続方式が使える状態ではないため、エージェントを有効にできません。接続方式のカードの状態を確認してください。"

// agentDisabledMessage は agent_disabled のエラーの detail.reason から文言を選ぶ（reason が無い・未知なら接続方式の汎用の文言）。
func agentDisabledMessage(err error) string {
	if de, ok := asDomainError(err); ok {
		if reason, _ := de.Detail["reason"].(string); reason != "" {
			if msg, ok := agentDisabledMessages[reason]; ok {
				return msg
			}
		}
	}
	return agentDisabledFallback
}
