package gui

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/pages"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// このファイルはエージェント設定の画面（FR-H11、FR-G14、FR-G22）。保存・検証・監査は service.AgentSettings が行い、ここは描画と受け付けだけ。
//
// API キー（NFR-S14）: キーは PUT の本文（api_key）だけで受け取る（URL・クエリでは受けない）。応答・通知・エラー・ログのどこにも
// 出さず（登録の有無は AgentSettingsView の真偽値だけ）、キーに関わる応答は Cache-Control: no-store にする。
// 入力欄は value を持たない（atoms.SecretInput）。

const (
	// fieldAPIKey はキーの入力欄の name。
	fieldAPIKey = "api_key"
	// maxAPIKeyBytes は受け付けるキーの長さの上限（実在のキーは 200 バイト前後）。
	maxAPIKeyBytes = 4096

	agentKeyRoute    = "/agent/api-key"
	fastpathKeyRoute = "/agent/fastpath/api-key"

	agentKeyPanelID    = "agent-key"
	fastpathKeyPanelID = "fastpath-key"

	messageGeneralInvalid = "入力内容を確認してください。"
)

// AgentSettings は GET /agent。
func (h *Handler) AgentSettings(c *gin.Context) {
	ctx := c.Request.Context()
	s, err := h.d.AgentSettings.Load(ctx)
	if err != nil {
		h.pageFail(c, err)
		return
	}
	v := h.agentSettingsPage(ctx, s)
	if fragmentRequest(c) {
		h.render(c, http.StatusOK, pages.AgentSettingsBoosted(v))
		return
	}
	h.render(c, http.StatusOK, pages.AgentSettings(v))
}

// agentSettingsPage は読み取り結果から画面全体の表示を作る。
func (h *Handler) agentSettingsPage(ctx context.Context, s service.AgentSettingsView) view.AgentSettings {
	return view.AgentSettings{
		Chrome:      h.chrome(ctx, view.NavAgent),
		Agent:       h.agentFormView(agentValues(s), view.AgentErrors{}, s),
		AgentKey:    openAIKeyPanel(s.OpenAIKeyRegistered, ""),
		Fastpath:    h.fastpathFormView(fastpathValues(s), view.FastpathErrors{}, s),
		FastpathKey: typeSafeKeyPanel(s.TypeSafeKeyRegistered, ""),
	}
}

// openAIKeyPanel・typeSafeKeyPanel の Target は、Windows の資格情報マネージャー上の実際のターゲット名
// （go-keyring が `service:user` で作る。platform.SecretOpenAI・SecretTypeSafe + ":puppet-remo"。windows/secret.go）。
// web は platform を import できないため、名前はここに書く。
func openAIKeyPanel(registered bool, errMsg string) view.APIKeyPanel {
	return view.APIKeyPanel{
		ID: agentKeyPanelID, Provider: "OpenAI", Target: "puppet-remo/openai:puppet-remo",
		PutURL: agentKeyRoute, ConfirmURL: agentKeyRoute + "/delete/confirm", Registered: registered, Error: errMsg,
	}
}

func typeSafeKeyPanel(registered bool, errMsg string) view.APIKeyPanel {
	return view.APIKeyPanel{
		ID: fastpathKeyPanelID, Provider: "TypeSafe", Target: "puppet-remo/typesafe:puppet-remo",
		PutURL: fastpathKeyRoute, ConfirmURL: fastpathKeyRoute + "/delete/confirm", Registered: registered, Error: errMsg,
	}
}

// agentFormView は入力値とエラーに、同意の日時（保存済みの値）を添えてフォームの表示にする。
func (h *Handler) agentFormView(v view.AgentValues, errs view.AgentErrors, s service.AgentSettingsView) view.AgentForm {
	return view.AgentForm{Values: v, Errors: errs, ConsentAt: h.formatTimePtr(s.ConsentAt, "")}
}

func (h *Handler) fastpathFormView(v view.FastpathValues, errs view.FastpathErrors, s service.AgentSettingsView) view.FastpathForm {
	return view.FastpathForm{Values: v, Errors: errs, ConsentAt: h.formatTimePtr(s.Fastpath.ConsentAt, "")}
}

// agentValues は保存済みの設定をフォームの入力値にする。同意済み（consent_at がある）ならチェック済みで描画する。
func agentValues(s service.AgentSettingsView) view.AgentValues {
	return view.AgentValues{
		Enabled: s.Enabled, Consent: s.ConsentAt != nil, Model: s.Model,
		MaxSteps: strconv.Itoa(s.MaxSteps), MaxDurationSec: strconv.Itoa(s.MaxDurationSec), KeepScreenshots: strconv.Itoa(s.KeepScreenshots),
		DenyProcesses: joinLines(s.DenyProcesses), ApproveProcesses: joinLines(s.ApproveProcesses),
	}
}

func fastpathValues(s service.AgentSettingsView) view.FastpathValues {
	f := s.Fastpath
	return view.FastpathValues{
		Enabled: f.Enabled, Consent: f.ConsentAt != nil, Model: f.Model,
		MinConfidence: strconv.FormatFloat(f.MinConfidence, 'f', -1, 64), MaxSteps: strconv.Itoa(f.MaxSteps),
		ExcludedLabels: joinLines(f.ExcludedLabels),
	}
}

func joinLines(items []string) string { return strings.Join(items, "\n") }

// splitLines は改行区切りのリストを項目にする。各行の前後の空白を除き、空の行は捨てる。
func splitLines(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// parseFloat は小数のフォーム値を読む。読めない値・NaN・無限大は 0 とメッセージ。
func parseFloat(raw string) (float64, string) {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, "数を指定してください。"
	}
	return f, ""
}

// firstField は verr の項目別のメッセージのうち、keys の最初に見つかったもの（項目名は service の設定キーでもフォームのキーでも受ける）。
func firstField(verr *service.ValidationError, keys ...string) string {
	for _, k := range keys {
		if m := verr.Fields[k]; m != "" {
			return m
		}
	}
	return ""
}

// SaveAgent は PUT /agent。検証エラーは 422 で、入力値と項目別のメッセージ付きでフォームを再描画する。
// API キー未登録などで有効にできないときは 409（agent_disabled。API 仕様 §2 のエラーコード表も 409）。
func (h *Handler) SaveAgent(c *gin.Context) {
	ctx := c.Request.Context()
	in := view.AgentValues{
		Enabled: checked(c, "enabled"), Consent: checked(c, "consent"), Model: strings.TrimSpace(c.PostForm("model")),
		MaxSteps: strings.TrimSpace(c.PostForm("max_steps")), MaxDurationSec: strings.TrimSpace(c.PostForm("max_duration_sec")),
		KeepScreenshots: strings.TrimSpace(c.PostForm("keep_screenshots")),
		DenyProcesses:   c.PostForm("deny_processes"), ApproveProcesses: c.PostForm("approve_processes"),
	}
	req := service.AgentSettingsInput{
		Enabled: in.Enabled, Consent: in.Consent, Model: in.Model,
		DenyProcesses: splitLines(in.DenyProcesses), ApproveProcesses: splitLines(in.ApproveProcesses),
	}
	var errs view.AgentErrors
	req.MaxSteps, errs.MaxSteps = parseInt(in.MaxSteps)
	req.MaxDurationSec, errs.MaxDurationSec = parseInt(in.MaxDurationSec)
	req.KeepScreenshots, errs.KeepScreenshots = parseInt(in.KeepScreenshots)

	// 数値に読めなかった項目は 0 として検証に回し、範囲の不備も同時に示す（0 はどの項目でも範囲外なので、有効にする保存・変化のない保存は
	// 進まない。読めなかった項目のメッセージは上書きしない）。無効にする保存（enabled を外す）は、有効または同意済みの状態からなら
	// 他の欄の不備に妨げられず、無効化だけが行われる（service.AgentSettings）。そのときは成功として保存済みの値を描画し直す。
	{
		err := h.d.AgentSettings.SaveAgent(ctx, req)
		var verr *service.ValidationError
		switch {
		case err == nil && (errs == (view.AgentErrors{}) || !req.Enabled):
			s, err := h.d.AgentSettings.Load(ctx)
			if err != nil {
				h.fail(c, err)
				return
			}
			h.render(c, http.StatusOK,
				organisms.AgentForm(h.agentFormView(agentValues(s), view.AgentErrors{}, s)),
				organisms.ToastOOB(infoToast("エージェントの設定を保存しました。")),
			)
			return
		case errors.As(err, &verr):
			errs = mergeAgentErrors(errs, verr)
		case err == nil:
			// 数値に読めない入力があるのに、有効のまま保存された（検証が 0 を通した）。保存済みの値を描画し直す。
			h.log.Error("agent settings saved despite unparsable input")
		case domain.CodeOf(err) == domain.CodeAgentDisabled:
			h.failMessage(c, http.StatusConflict, string(domain.CodeAgentDisabled), "OpenAI API キーが登録されていないため、エージェントを有効にできません。先に API キーを登録してください。")
			return
		default:
			h.fail(c, err)
			return
		}
	}
	h.render(c, http.StatusUnprocessableEntity, organisms.AgentForm(h.agentFormView(in, errs, h.loadAgentOrZero(ctx))))
}

// loadAgentOrZero は 422 の再描画で同意の日時を添えるための読み取り（失敗しても再描画は続ける）。
func (h *Handler) loadAgentOrZero(ctx context.Context) service.AgentSettingsView {
	s, err := h.d.AgentSettings.Load(ctx)
	if err != nil {
		h.log.Warn("load agent settings failed", "error", err)
	}
	return s
}

// mergeAgentErrors は verr の項目別のメッセージを、まだメッセージの無い項目に足す。どの項目にも当たらなければ全体のメッセージにする。
func mergeAgentErrors(errs view.AgentErrors, verr *service.ValidationError) view.AgentErrors {
	fill := func(dst *string, keys ...string) {
		if *dst == "" {
			*dst = firstField(verr, keys...)
		}
	}
	fill(&errs.Consent, service.SettingAgentConsentAt, "consent")
	fill(&errs.Model, service.SettingAgentModel, "model")
	fill(&errs.MaxSteps, service.SettingAgentMaxSteps, "max_steps")
	fill(&errs.MaxDurationSec, service.SettingAgentMaxDurationSec, "max_duration_sec")
	fill(&errs.KeepScreenshots, service.SettingAgentKeepScreenshots, "keep_screenshots")
	fill(&errs.DenyProcesses, service.SettingAgentDenyProcesses, "deny_processes")
	fill(&errs.ApproveProcesses, service.SettingAgentApproveProcesses, "approve_processes")
	if errs == (view.AgentErrors{}) {
		errs.General = messageGeneralInvalid
	}
	return errs
}

// SaveFastpath は PUT /agent/fastpath。TypeSafe API キー未登録で有効にしようとすると 409 conflict、同意なしなどは 422。
func (h *Handler) SaveFastpath(c *gin.Context) {
	ctx := c.Request.Context()
	in := view.FastpathValues{
		Enabled: checked(c, "enabled"), Consent: checked(c, "consent"), Model: strings.TrimSpace(c.PostForm("model")),
		MinConfidence: strings.TrimSpace(c.PostForm("min_confidence")), MaxSteps: strings.TrimSpace(c.PostForm("max_steps")),
		ExcludedLabels: c.PostForm("excluded_labels"),
	}
	req := service.FastpathSettingsInput{Enabled: in.Enabled, Consent: in.Consent, Model: in.Model, ExcludedLabels: splitLines(in.ExcludedLabels)}
	var errs view.FastpathErrors
	req.MinConfidence, errs.MinConfidence = parseFloat(in.MinConfidence)
	req.MaxSteps, errs.MaxSteps = parseInt(in.MaxSteps)
	if errs.MinConfidence != "" {
		req.MinConfidence = -1 // 0 は有効な値なので、読めなかった値は範囲外の値で検証に回す
	}

	// 数値に読めなかった項目は範囲外の値として検証に回し、範囲の不備も同時に示す（保存には進まない。読めなかった項目のメッセージは上書きしない）。
	{
		err := h.d.AgentSettings.SaveFastpath(ctx, req)
		var verr *service.ValidationError
		switch {
		case err == nil && errs == (view.FastpathErrors{}):
			s, err := h.d.AgentSettings.Load(ctx)
			if err != nil {
				h.fail(c, err)
				return
			}
			h.render(c, http.StatusOK,
				organisms.FastpathForm(h.fastpathFormView(fastpathValues(s), view.FastpathErrors{}, s)),
				organisms.ToastOOB(infoToast("高速パスの設定を保存しました。")),
			)
			return
		case errors.As(err, &verr):
			errs = mergeFastpathErrors(errs, verr)
		case err == nil:
			h.log.Error("fastpath settings saved despite unparsable input")
		case domain.CodeOf(err) == domain.CodeConflict || domain.CodeOf(err) == domain.CodeAgentDisabled:
			h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), "TypeSafe API キーが登録されていないため、高速パスを有効にできません。先に API キーを登録してください。")
			return
		default:
			h.fail(c, err)
			return
		}
	}
	h.render(c, http.StatusUnprocessableEntity, organisms.FastpathForm(h.fastpathFormView(in, errs, h.loadAgentOrZero(ctx))))
}

func mergeFastpathErrors(errs view.FastpathErrors, verr *service.ValidationError) view.FastpathErrors {
	fill := func(dst *string, keys ...string) {
		if *dst == "" {
			*dst = firstField(verr, keys...)
		}
	}
	fill(&errs.Consent, service.SettingFastpathConsentAt, "fastpath.consent", "consent")
	fill(&errs.Model, service.SettingFastpathModel, "fastpath.model", "model")
	fill(&errs.MinConfidence, service.SettingFastpathMinConfidence, "fastpath.min_confidence", "min_confidence")
	fill(&errs.MaxSteps, service.SettingFastpathMaxSteps, "fastpath.max_steps", "max_steps")
	fill(&errs.ExcludedLabels, service.SettingFastpathExcludedLabels, "fastpath.excluded_labels", "excluded_labels")
	if errs == (view.FastpathErrors{}) {
		errs.General = messageGeneralInvalid
	}
	return errs
}

// ---- API キー ----

// SetOpenAIKey は PUT /agent/api-key。
func (h *Handler) SetOpenAIKey(c *gin.Context) {
	h.setKey(c, h.d.AgentSettings.SetOpenAIKey, func(s service.AgentSettingsView, msg string) view.APIKeyPanel {
		return openAIKeyPanel(s.OpenAIKeyRegistered, msg)
	})
}

// SetTypeSafeKey は PUT /agent/fastpath/api-key。登録だけでは高速パスは有効にならない（有効化と同意は別）。
func (h *Handler) SetTypeSafeKey(c *gin.Context) {
	h.setKey(c, h.d.AgentSettings.SetTypeSafeKey, func(s service.AgentSettingsView, msg string) view.APIKeyPanel {
		return typeSafeKeyPanel(s.TypeSafeKeyRegistered, msg)
	})
}

// validateAPIKey はキーの形を検査する。通れば空、通らなければ利用者向けのメッセージ（入力を含めない）。
// キーは Authorization ヘッダーに載るため、印字可能な ASCII 以外（空白・制御文字・全角など）は受けない。
func validateAPIKey(key string) string {
	switch {
	case key == "":
		return "API キーを入力してください。"
	case len(key) > maxAPIKeyBytes:
		return "API キーが長すぎます（4,096 バイト以下）。"
	}
	for i := range len(key) {
		if key[i] <= ' ' || key[i] >= 0x7f {
			return "API キーに使用できない文字（空白・制御文字・全角文字など）が含まれています。"
		}
	}
	return ""
}

// setKey はキーの登録の共通部分。panel は登録の有無（読み取り結果の真偽値）と失敗の理由からパネルを作る。
func (h *Handler) setKey(c *gin.Context, set func(context.Context, string) error, panel func(service.AgentSettingsView, string) view.APIKeyPanel) {
	c.Header("Cache-Control", "no-store")
	ctx := c.Request.Context()
	// 本文のフォームだけを読む（c.PostForm。URL のクエリは読まない）。貼り付けで付く前後の空白・改行は除く。
	key := strings.TrimSpace(c.PostForm(fieldAPIKey))
	reject := func(msg string) {
		h.render(c, http.StatusUnprocessableEntity, organisms.APIKeyPanel(panel(h.loadAgentOrZero(ctx), msg)))
	}
	if msg := validateAPIKey(key); msg != "" {
		reject(msg)
		return
	}
	if err := set(ctx, key); err != nil {
		if errors.Is(err, domain.ErrInvalidArgument) {
			reject("API キーの形式が正しくありません。")
			return
		}
		h.failKey(c, err, key)
		return
	}
	s, err := h.d.AgentSettings.Load(ctx)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK, organisms.APIKeyPanel(panel(s, "")), organisms.ToastOOB(infoToast("API キーを登録しました。")))
}

// failKey はキーの登録の失敗をアラートで返す。ログに残すエラー文からは、念のため送られたキーを伏せる。
func (h *Handler) failKey(c *gin.Context, err error, key string) {
	f := classify(err)
	if f.status >= 500 {
		msg := err.Error()
		if key != "" {
			msg = strings.ReplaceAll(msg, key, "[redacted]")
		}
		h.log.Error("gui api key action failed", "method", c.Request.Method, "path", c.Request.URL.Path, "error", msg)
	}
	h.failMessage(c, f.status, f.code, f.message)
}

// ConfirmDeleteOpenAIKey は GET /agent/api-key/delete/confirm。登録済みのときだけ描画する。
func (h *Handler) ConfirmDeleteOpenAIKey(c *gin.Context) {
	h.confirmDeleteKey(c, func(s service.AgentSettingsView) bool { return s.OpenAIKeyRegistered }, view.ConfirmModal{
		Title:      "OpenAI API キーを削除しますか？",
		Message:    "資格情報マネージャーからキーを削除します。エージェントは無効になり、実行中のタスクは中断されます。もう一度使うには、キーを登録して有効にし直してください。",
		ConfirmURL: agentKeyRoute, Method: "delete", Target: "#agent-sections", Swap: "outerHTML", Label: "削除する",
	})
}

// ConfirmDeleteTypeSafeKey は GET /agent/fastpath/api-key/delete/confirm。
func (h *Handler) ConfirmDeleteTypeSafeKey(c *gin.Context) {
	h.confirmDeleteKey(c, func(s service.AgentSettingsView) bool { return s.TypeSafeKeyRegistered }, view.ConfirmModal{
		Title:      "TypeSafe API キーを削除しますか？",
		Message:    "資格情報マネージャーからキーを削除します。高速パスは無効になり、同意も取り消されます。実行中のタスクは失敗にせず、OpenAI だけで続けます。",
		ConfirmURL: fastpathKeyRoute, Method: "delete", Target: "#agent-sections", Swap: "outerHTML", Label: "削除する",
	})
}

func (h *Handler) confirmDeleteKey(c *gin.Context, registered func(service.AgentSettingsView) bool, m view.ConfirmModal) {
	c.Header("Cache-Control", "no-store")
	s, err := h.d.AgentSettings.Load(c.Request.Context())
	if err != nil {
		h.fail(c, err)
		return
	}
	if !registered(s) {
		h.failMessage(c, http.StatusConflict, string(domain.CodeConflict), "API キーは登録されていません。")
		return
	}
	h.confirm(c, m)
}

// DeleteOpenAIKey は DELETE /agent/api-key。キーを削除してエージェントを無効にし、実行中のタスクを中断する（実体が行う）。
func (h *Handler) DeleteOpenAIKey(c *gin.Context) {
	h.deleteKey(c, h.d.AgentSettings.DeleteOpenAIKey, "OpenAI API キーを削除しました。")
}

// DeleteTypeSafeKey は DELETE /agent/fastpath/api-key。キーを削除して高速パスを無効にし、同意を取り消す（実体が行う）。
func (h *Handler) DeleteTypeSafeKey(c *gin.Context) {
	h.deleteKey(c, h.d.AgentSettings.DeleteTypeSafeKey, "TypeSafe API キーを削除しました。")
}

// deleteKey は削除の確定の共通部分。有効・同意の状態も変わるため、画面の本体（#agent-sections）を読み直して差し替える。
func (h *Handler) deleteKey(c *gin.Context, del func(context.Context) error, toast string) {
	c.Header("Cache-Control", "no-store")
	ctx := c.Request.Context()
	if err := del(ctx); err != nil {
		h.fail(c, err)
		return
	}
	s, err := h.d.AgentSettings.Load(ctx)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.render(c, http.StatusOK, organisms.AgentSections(h.agentSettingsPage(ctx, s)), organisms.ModalClear(), organisms.ToastOOB(infoToast(toast)))
}
