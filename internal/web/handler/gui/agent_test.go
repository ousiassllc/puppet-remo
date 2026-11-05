package gui_test

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
)

// canaryKey は漏えいの検査に使う、実在しない API キーの形の文字列。応答・ヘッダー・ログのどこにも現れてはならない。
const canaryKey = "sk-canary-0123456789abcdefABCDEF"

func validAgentForm() url.Values {
	return url.Values{
		"enabled": {"on"}, "consent": {"on"}, "model": {"gpt-6.1-sol"}, "max_steps": {"50"}, "max_duration_sec": {"600"},
		"keep_screenshots": {"3"}, "deny_processes": {"A.exe\n\n  B.exe \r\n"}, "approve_processes": {""},
	}
}

func validFastpathForm() url.Values {
	return url.Values{
		"enabled": {"on"}, "consent": {"on"}, "model": {"jev-1.13"}, "min_confidence": {"0.7"}, "max_steps": {"15"},
		"excluded_labels": {"delete\n送信\n"},
	}
}

func registered(openAI, typeSafe bool) func(*guitest.Fakes) {
	return func(f *guitest.Fakes) { f.AgentSettings.Registered(openAI, typeSafe) }
}

func TestAgentPageDefaults(t *testing.T) {
	res := newRig(t).get("/agent")
	status(t, res, http.StatusOK)
	contains(t, res, "<html", `aria-current="page"`, `href="/agent"`, "エージェント",
		// 既定は無効・キー未登録（FR-G14）。
		`data-notice="agent-default-off"`, "既定では無効", `data-key-state="unregistered"`, "未登録",
		`id="agent-form"`, `hx-put="/agent"`, `hx-put="/agent/api-key"`, `hx-put="/agent/fastpath"`, `hx-put="/agent/fastpath/api-key"`,
		` novalidate`, `hx-target-422=`,
		`name="enabled"`, `name="consent"`, `name="model"`, `value="gpt-6.1-sol"`,
		`name="max_steps"`, `value="50"`, `name="max_duration_sec"`, `value="600"`, `name="keep_screenshots"`, `value="3"`,
		`name="deny_processes"`, "KeePass.exe", `name="approve_processes"`,
		`name="min_confidence"`, `value="0.7"`, `value="jev-1.13"`, `value="15"`, `name="excluded_labels"`, "アンインストール",
		// 同意の文言: OpenAI へスクリーンショットが送られる / TypeSafe へ何が送られるか（FR-G14・G18・G22、NFR-PR04）。
		`data-consent="openai"`, "スクリーンショット", "OpenAI に送信される",
		`data-consent="typesafe"`, "TypeSafe", "ウィンドウのタイトル", "直近 5 件", "スクリーンショット、要素の値",
		// キーは資格情報マネージャーだけに保存し、再表示しない（NFR-S14）。ターゲット名は資格情報マネージャー上の実名（go-keyring の service:user）。
		`data-notice="key-storage"`, "（puppet-remo/openai:puppet-remo）", "（puppet-remo/typesafe:puppet-remo）", "資格情報マネージャー", "表示しません")
	// 既定では何も有効・同意済みではない。
	lacks(t, res, "checked", "同意済み", `data-key-delete=`, "data-field-error", `role="alert"`)
	if got := res.Header.Get("Cache-Control"); got != "no-store" && got != "" {
		t.Errorf("Cache-Control = %q", got)
	}
}

// キーの入力欄は伏せ字で、value を持たない（登録済みでも、失敗の再描画でも）。NFR-S14。
func TestAgentPageKeyInputsAreMasked(t *testing.T) {
	for name, mutate := range map[string]func(*guitest.Fakes){"unregistered": nil, "registered": registered(true, true)} {
		t.Run(name, func(t *testing.T) {
			var res resp
			if mutate == nil {
				res = newRig(t).get("/agent")
			} else {
				res = newRig(t, mutate).get("/agent")
			}
			inputs := regexp.MustCompile(`<input[^>]*name="api_key"[^>]*>`).FindAllString(res.Body, -1)
			if len(inputs) != 2 {
				t.Fatalf("api_key inputs = %d, want 2 (OpenAI, TypeSafe)\n%s", len(inputs), res.Body)
			}
			for _, in := range inputs {
				for _, want := range []string{`type="password"`, `autocomplete="off"`} {
					if !strings.Contains(in, want) {
						t.Errorf("input %s lacks %s", in, want)
					}
				}
				if strings.Contains(in, "value=") {
					t.Errorf("input must never carry a value: %s", in)
				}
			}
		})
	}
}

func TestAgentPageRegisteredAndEnabled(t *testing.T) {
	r := newRig(t, registered(true, true))
	res := r.get("/agent")
	contains(t, res, `data-key-state="registered"`, "登録済み", `data-key-delete="OpenAI"`, `data-key-delete="TypeSafe"`,
		`hx-get="/agent/api-key/delete/confirm"`, `hx-get="/agent/fastpath/api-key/delete/confirm"`, `hx-target="#modal-root"`, "置き換える")
	lacks(t, res, `data-key-state="unregistered"`)

	// 有効化に成功した後は、有効・同意済み（日時つき）で描画される。
	res = r.do(http.MethodPut, "/agent", validAgentForm(), hx...)
	status(t, res, http.StatusOK)
	res = r.get("/agent")
	contains(t, res, `name="enabled" type="checkbox" checked`, `name="consent" type="checkbox" checked`, `data-consent-at="openai"`, "2026-10-30 12:00")
	lacks(t, res, `data-consent-at="typesafe"`)
}

func TestAgentPageBoostedFragmentAndLoadFailure(t *testing.T) {
	r := newRig(t)
	res := r.get("/agent", "HX-Request", "true", "HX-Boosted", "true")
	status(t, res, http.StatusOK)
	contains(t, res, `id="agent-form"`, `id="fastpath-form"`)
	lacks(t, res, "<html")

	r = newRig(t, func(f *guitest.Fakes) { f.AgentSettings.LoadErr = errors.New("db down") })
	res = r.get("/agent")
	status(t, res, http.StatusInternalServerError)
	contains(t, res, "<html", "内部エラー")
	lacks(t, res, `id="agent-form"`, "db down")
}

func TestSaveAgentSuccess(t *testing.T) {
	r := newRig(t, registered(true, false))
	res := r.do(http.MethodPut, "/agent", validAgentForm(), hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="agent-form"`, `hx-swap-oob="beforeend:#toast-root"`, "エージェントの設定を保存しました", `data-consent-at="openai"`)
	lacks(t, res, "<html", "data-field-error")
	// 改行区切りのリストは、前後の空白を除き、空の行を捨てて分割する。
	in := r.f.AgentSettings.LastAgent
	if !in.Enabled || !in.Consent || in.MaxSteps != 50 || in.MaxDurationSec != 600 || in.KeepScreenshots != 3 {
		t.Errorf("input = %+v", in)
	}
	if got := strings.Join(in.DenyProcesses, "|"); got != "A.exe|B.exe" {
		t.Errorf("deny_processes = %q", got)
	}
	if len(in.ApproveProcesses) != 0 {
		t.Errorf("approve_processes = %q", in.ApproveProcesses)
	}
	if st := r.f.AgentSettings.State(); !st.Enabled || st.ConsentAt == nil {
		t.Errorf("state = %+v", st)
	}
}

func TestSaveAgentValidationErrors(t *testing.T) {
	r := newRig(t, registered(true, false))
	form := validAgentForm()
	form.Set("max_steps", "501")
	form.Set("max_duration_sec", "29")
	form.Set("keep_screenshots", "11")
	form.Set("model", " ")
	form.Del("consent")
	form.Set("deny_processes", "typed-but-kept.exe")
	res := r.do(http.MethodPut, "/agent", form, hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `id="agent-form"`, `data-field-error="agent-max-steps"`, `data-field-error="agent-max-duration"`,
		`data-field-error="agent-keep-screenshots"`, `data-field-error="agent-model"`, `data-field-error="agent-consent"`,
		"1〜500", "30〜7200", "同意が必要", `value="501"`, `value="29"`, "typed-but-kept.exe", `aria-invalid`)
	lacks(t, res, "<html", "toast-root")
	if st := r.f.AgentSettings.State(); st.Enabled || st.MaxSteps != 50 {
		t.Errorf("an invalid save must not change anything: %+v", st)
	}
}

// 数値に読めない入力は、サービスを呼ばずに 422（入力はそのまま戻す）。
func TestSaveAgentUnparsableNumbers(t *testing.T) {
	r := newRig(t, registered(true, false))
	form := validAgentForm()
	form.Set("max_steps", "abc")
	form.Set("keep_screenshots", "")
	res := r.do(http.MethodPut, "/agent", form, hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `data-field-error="agent-max-steps"`, `data-field-error="agent-keep-screenshots"`, "整数を指定してください", `value="abc"`)
	lacks(t, res, `data-field-error="agent-max-duration"`)
	if st := r.f.AgentSettings.State(); st.Enabled || st.MaxSteps != 50 || st.KeepScreenshots != 3 {
		t.Errorf("nothing may be saved: %+v", st)
	}

	// 読めない項目があっても、ほかの項目の範囲・同意の不備は同時に示す。
	form.Set("max_duration_sec", "10")
	form.Del("consent")
	res = r.do(http.MethodPut, "/agent", form, hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `data-field-error="agent-max-duration"`, "30〜7200", `data-field-error="agent-consent"`, "整数を指定してください")
}

// 同意なしの有効化は 422（consent の項目）。無効のままの保存は同意なしでよい。
func TestSaveAgentConsentRequiredOnlyWhenEnabling(t *testing.T) {
	r := newRig(t, registered(true, false))
	form := validAgentForm()
	form.Del("consent")
	res := r.do(http.MethodPut, "/agent", form, hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `data-field-error="agent-consent"`)

	form.Del("enabled")
	status(t, r.do(http.MethodPut, "/agent", form, hx...), http.StatusOK)
}

// 有効（同意済み）の状態から無効にする保存は、無関係な欄が読めない・範囲外でも 200 で、無効化だけが行われる（他の欄は保存しない）。
// 無効のままなら不備は通常どおり 422。
func TestSaveAgentDisableIsNotBlockedByInvalidFields(t *testing.T) {
	r := newRig(t, registered(true, false))
	status(t, r.do(http.MethodPut, "/agent", validAgentForm(), hx...), http.StatusOK)
	if st := r.f.AgentSettings.State(); !st.Enabled || st.ConsentAt == nil {
		t.Fatalf("setup: %+v", st)
	}
	form := validAgentForm()
	form.Del("enabled")
	form.Del("consent")
	form.Set("max_steps", "abc")
	form.Set("model", " ")
	res := r.do(http.MethodPut, "/agent", form, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="agent-form"`, "エージェントの設定を保存しました")
	lacks(t, res, "data-field-error")
	if st := r.f.AgentSettings.State(); st.Enabled || st.ConsentAt != nil || st.MaxSteps != 50 || st.Model == " " {
		t.Errorf("state = %+v, want disabled without consent and the other fields untouched", st)
	}
	// すでに無効で同意も無いときの不備は 422。
	res = r.do(http.MethodPut, "/agent", form, hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `data-field-error="agent-max-steps"`, `data-field-error="agent-model"`)
}

// API キー未登録で有効にしようとすると 409 agent_disabled（API 仕様 §7.3 のルートの記述）。
func TestSaveAgentWithoutKeyIsConflict(t *testing.T) {
	r := newRig(t)
	res := r.do(http.MethodPut, "/agent", validAgentForm(), hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, `data-error-code="agent_disabled"`, "API キー")
	if got := res.Header.Get("HX-Retarget"); got != "#alert-root" {
		t.Errorf("HX-Retarget = %q", got)
	}
	lacks(t, res, "<html")
	if r.f.AgentSettings.State().Enabled {
		t.Error("the agent was enabled without a key")
	}
}

func TestSaveAgentUnexpectedErrorIsGenericToast(t *testing.T) {
	r := newRig(t, registered(true, false), func(f *guitest.Fakes) {
		f.AgentSettings.SaveErr = errors.New("sqlite: disk I/O error at C:/secret/path")
	})
	res := r.do(http.MethodPut, "/agent", validAgentForm(), hx...)
	status(t, res, http.StatusInternalServerError)
	contains(t, res, `data-error-code="internal"`)
	lacks(t, res, "sqlite", "secret")
}

func TestSaveFastpath(t *testing.T) {
	r := newRig(t, registered(false, true))
	res := r.do(http.MethodPut, "/agent/fastpath", validFastpathForm(), hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="fastpath-form"`, "高速パスの設定を保存しました", `data-consent-at="typesafe"`, `value="0.7"`)
	lacks(t, res, "<html", `data-consent-at="openai"`)
	in := r.f.AgentSettings.LastFastpath
	if !in.Enabled || !in.Consent || in.MinConfidence != 0.7 || in.MaxSteps != 15 || strings.Join(in.ExcludedLabels, "|") != "delete|送信" {
		t.Errorf("input = %+v", in)
	}
	// エージェントの有効・同意とは独立。
	if st := r.f.AgentSettings.State(); st.Enabled || st.ConsentAt != nil || !st.Fastpath.Enabled || st.Fastpath.ConsentAt == nil {
		t.Errorf("state = %+v", st)
	}
	// 無効にすると同意の日時を消す。
	form := validFastpathForm()
	form.Del("enabled")
	form.Del("consent")
	res = r.do(http.MethodPut, "/agent/fastpath", form, hx...)
	status(t, res, http.StatusOK)
	lacks(t, res, `data-consent-at="typesafe"`, "checked")
}

func TestSaveFastpathErrors(t *testing.T) {
	r := newRig(t, registered(false, true))
	form := validFastpathForm()
	form.Del("consent")
	form.Set("min_confidence", "1.5")
	form.Set("max_steps", "0")
	res := r.do(http.MethodPut, "/agent/fastpath", form, hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `data-field-error="fastpath-consent"`, "TypeSafe", `data-field-error="fastpath-min-confidence"`, `data-field-error="fastpath-max-steps"`, `value="1.5"`)

	form = validFastpathForm()
	form.Set("min_confidence", "NaN")
	res = r.do(http.MethodPut, "/agent/fastpath", form, hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `data-field-error="fastpath-min-confidence"`, "数を指定してください")

	// 読めない確信度は 0（有効な値）として保存されない。ほかの不備も同時に示す。
	form = validFastpathForm()
	form.Set("min_confidence", "abc")
	form.Set("max_steps", "501")
	res = r.do(http.MethodPut, "/agent/fastpath", form, hx...)
	status(t, res, http.StatusUnprocessableEntity)
	contains(t, res, `data-field-error="fastpath-min-confidence"`, "数を指定してください", `data-field-error="fastpath-max-steps"`, "1〜500")
	if st := r.f.AgentSettings.State(); st.Fastpath.Enabled || st.Fastpath.MinConfidence != 0.7 {
		t.Errorf("nothing may be saved: %+v", st.Fastpath)
	}

	// TypeSafe キー未登録での有効化は 409 conflict。
	r = newRig(t)
	res = r.do(http.MethodPut, "/agent/fastpath", validFastpathForm(), hx...)
	status(t, res, http.StatusConflict)
	contains(t, res, `data-error-code="conflict"`, "TypeSafe API キー")
	if r.f.AgentSettings.State().Fastpath.Enabled {
		t.Error("fast path was enabled without a key")
	}
}

// ---- API キー ----

// leakRig は運用ログを捕まえる rig（キーがログに出ないことの検査用）。
func leakRig(t *testing.T, f *guitest.Fakes) (http.Handler, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	d := f.Deps()
	d.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h, err := gui.New(d)
	if err != nil {
		t.Fatal(err)
	}
	e := gin.New()
	h.Register(e)
	e.NoRoute(h.NotFound)
	return middleware.CSP()(e), &logs
}

func send(h http.Handler, method, target string, form url.Values) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// assertNoLeak は応答（本文・ヘッダー）とログにキーが無いこと。
func assertNoLeak(t *testing.T, rec *httptest.ResponseRecorder, logs *bytes.Buffer, secrets ...string) {
	t.Helper()
	var all strings.Builder
	all.WriteString(rec.Body.String())
	for k, vs := range rec.Header() {
		all.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
	}
	all.WriteString(logs.String())
	for _, s := range secrets {
		if strings.Contains(all.String(), s) {
			t.Errorf("secret %q leaked:\n%s", s, all.String())
		}
	}
}

func TestSetKeysStoreOnlyPresenceAndNeverEcho(t *testing.T) {
	for _, tc := range []struct {
		name, path, panel, label string
		isSet                    func(service.AgentSettingsView) bool
	}{
		{"openai", "/agent/api-key", `id="agent-key"`, "OpenAI", func(v service.AgentSettingsView) bool { return v.OpenAIKeyRegistered }},
		{"typesafe", "/agent/fastpath/api-key", `id="fastpath-key"`, "TypeSafe", func(v service.AgentSettingsView) bool { return v.TypeSafeKeyRegistered }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := guitest.New()
			h, logs := leakRig(t, f)
			rec := send(h, http.MethodPut, tc.path, url.Values{"api_key": {"  " + canaryKey + "\n"}})
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d\n%s", rec.Code, rec.Body)
			}
			body := rec.Body.String()
			for _, want := range []string{tc.panel, `data-key-state="registered"`, "登録済み", `hx-swap-oob="beforeend:#toast-root"`, "API キーを登録しました", `data-key-delete="` + tc.label + `"`} {
				if !strings.Contains(body, want) {
					t.Errorf("body lacks %q\n%s", want, body)
				}
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			assertNoLeak(t, rec, logs, canaryKey)
			if !tc.isSet(f.AgentSettings.State()) {
				t.Error("registered state not updated")
			}
			// 登録後のページにも、キーは出ない。
			page := send(h, http.MethodGet, "/agent", nil)
			assertNoLeak(t, page, logs, canaryKey)
		})
	}
}

// キーは PUT の本文だけで受ける。URL のクエリ・パスにあっても読まない。
func TestKeyIsReadFromBodyOnly(t *testing.T) {
	f := guitest.New()
	h, logs := leakRig(t, f)
	for _, path := range []string{"/agent/api-key", "/agent/fastpath/api-key"} {
		rec := send(h, http.MethodPut, path+"?api_key="+canaryKey, nil)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", path, rec.Code)
		}
		assertNoLeak(t, rec, logs, canaryKey)
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q", path, got)
		}
	}
	if st := f.AgentSettings.State(); st.OpenAIKeyRegistered || st.TypeSafeKeyRegistered || f.AgentSettings.SetKeyCalls != 0 {
		t.Errorf("a key from the URL must not be registered: %+v calls=%d", st, f.AgentSettings.SetKeyCalls)
	}
	// ページ・フォームの送信先の URL にもキーが含まれない（PUT の本文で送る設計）。
	page := send(h, http.MethodGet, "/agent", nil)
	if strings.Contains(page.Body.String(), "api_key=") {
		t.Error("the page must not put api_key in a URL")
	}
}

func TestSetKeyRejectsEmptyOverlongAndOddKeysWithoutEcho(t *testing.T) {
	long := strings.Repeat("k", 4097)
	for name, tc := range map[string]struct {
		key  string
		want string
	}{
		"empty":     {"", "入力してください"},
		"blank":     {"   \n", "入力してください"},
		"too long":  {long, "長すぎます"},
		"inner sp":  {"sk-abc def", "使用できない文字"},
		"fullwidth": {"sk-ａｂｃ", "使用できない文字"},
		"control":   {"sk-abc\x00def", "使用できない文字"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, path := range []string{"/agent/api-key", "/agent/fastpath/api-key"} {
				f := guitest.New()
				h, logs := leakRig(t, f)
				rec := send(h, http.MethodPut, path, url.Values{"api_key": {tc.key}})
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("%s: status %d, want 422\n%s", path, rec.Code, rec.Body)
				}
				body := rec.Body.String()
				if !strings.Contains(body, tc.want) || !strings.Contains(body, `data-field-error=`) || !strings.Contains(body, `data-key-state="unregistered"`) {
					t.Errorf("%s: body lacks field error %q\n%s", path, tc.want, body)
				}
				if probe := strings.TrimSpace(tc.key); probe != "" {
					assertNoLeak(t, rec, logs, probe[:min(len(probe), 6)])
				}
				if got := rec.Header().Get("Cache-Control"); got != "no-store" {
					t.Errorf("Cache-Control = %q", got)
				}
				// 入力欄は空に戻す（value を描画しない）。
				if regexp.MustCompile(`<input[^>]*name="api_key"[^>]*value=`).MatchString(body) {
					t.Errorf("api_key input carries a value\n%s", body)
				}
				if f.AgentSettings.SetKeyCalls != 0 {
					t.Error("the service must not be called with an invalid key")
				}
			}
		})
	}
}

// 保存の失敗（実体のエラー文にキーが入っていても）は、トーストにもログにも出さない。
func TestSetKeyFailureNeverLeaksKey(t *testing.T) {
	f := guitest.New()
	f.AgentSettings.SetKeyErr = errors.New("credential manager failed for " + canaryKey)
	h, logs := leakRig(t, f)
	rec := send(h, http.MethodPut, "/agent/api-key", url.Values{"api_key": {canaryKey}})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`data-error-code="internal"`, "内部エラー"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	assertNoLeak(t, rec, logs, canaryKey)
	if !strings.Contains(logs.String(), "gui api key action failed") || !strings.Contains(logs.String(), "[redacted]") {
		t.Errorf("the failure must be logged with the key redacted:\n%s", logs.String())
	}
	if f.AgentSettings.State().OpenAIKeyRegistered {
		t.Error("registered although the store failed")
	}
}

// 実体が項目の不備を返したとき（形式の不正など）も、422 でキーは出ない。
func TestSetKeyServiceRejectionHasNoEcho(t *testing.T) {
	f := guitest.New()
	f.AgentSettings.SetKeyErr = &service.ValidationError{Fields: map[string]string{"api_key": "bad " + canaryKey}}
	h, logs := leakRig(t, f)
	rec := send(h, http.MethodPut, "/agent/api-key", url.Values{"api_key": {canaryKey}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", rec.Code)
	}
	assertNoLeak(t, rec, logs, canaryKey)
}

func TestKeyDeleteConfirmThenDeleteDisablesAgent(t *testing.T) {
	r := newRig(t, registered(true, true))
	status(t, r.do(http.MethodPut, "/agent", validAgentForm(), hx...), http.StatusOK)

	res := r.get("/agent/api-key/delete/confirm", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `role="dialog"`, "OpenAI API キーを削除しますか？", "無効", "中断", `hx-delete="/agent/api-key"`, `hx-target="#agent-sections"`, `data-clear="#modal-root"`)
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	// 確認だけでは何も消えない。
	if !r.f.AgentSettings.State().OpenAIKeyRegistered {
		t.Fatal("the confirm dialog must not delete")
	}

	res = r.do(http.MethodDelete, "/agent/api-key", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="agent-sections"`, `id="modal-root" hx-swap-oob="true"`, "OpenAI API キーを削除しました", `hx-swap-oob="beforeend:#toast-root"`)
	lacks(t, res, "エージェントは無効です。") // トーストは結果だけの 1 文（状態は #agent-sections が示す）
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	st := r.f.AgentSettings.State()
	if st.OpenAIKeyRegistered || st.Enabled || st.ConsentAt != nil || !st.TypeSafeKeyRegistered {
		t.Errorf("state = %+v", st)
	}
	// 再描画では OpenAI は未登録・無効、TypeSafe は登録済みのまま。
	if !regexp.MustCompile(`id="agent-key"[^>]*data-key-state="unregistered"`).MatchString(res.Body) || !regexp.MustCompile(`id="fastpath-key"[^>]*data-key-state="registered"`).MatchString(res.Body) {
		t.Errorf("sections not re-rendered with the new key state\n%s", res.Body)
	}
	lacks(t, res, "<html", `name="enabled" type="checkbox" checked`)

	// 登録が無ければ確認は 409。
	status(t, r.get("/agent/api-key/delete/confirm", hx...), http.StatusConflict)
}

func TestFastpathKeyDeleteClearsConsentAndKeepsAgent(t *testing.T) {
	r := newRig(t, registered(true, true))
	status(t, r.do(http.MethodPut, "/agent", validAgentForm(), hx...), http.StatusOK)
	status(t, r.do(http.MethodPut, "/agent/fastpath", validFastpathForm(), hx...), http.StatusOK)

	res := r.get("/agent/fastpath/api-key/delete/confirm", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, "TypeSafe API キーを削除しますか？", "同意も取り消されます", "OpenAI だけで続けます", `hx-delete="/agent/fastpath/api-key"`)

	res = r.do(http.MethodDelete, "/agent/fastpath/api-key", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="agent-sections"`, "TypeSafe API キーを削除しました")
	lacks(t, res, "高速パスは無効です。")
	lacks(t, res, `data-consent-at="typesafe"`)
	st := r.f.AgentSettings.State()
	if st.TypeSafeKeyRegistered || st.Fastpath.Enabled || st.Fastpath.ConsentAt != nil || !st.Enabled || !st.OpenAIKeyRegistered {
		t.Errorf("state = %+v", st)
	}
	status(t, r.get("/agent/fastpath/api-key/delete/confirm", hx...), http.StatusConflict)
}

func TestDeleteKeyFailureIsGenericToast(t *testing.T) {
	f := guitest.New()
	f.AgentSettings.LoadErr = errors.New("sqlite: disk I/O error at C:/secret/path")
	h, logs := leakRig(t, f)
	rec := send(h, http.MethodDelete, "/agent/api-key", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", rec.Code)
	}
	assertNoLeak(t, rec, &bytes.Buffer{}, "sqlite", "secret")
	if !strings.Contains(logs.String(), "sqlite") {
		t.Error("the failure must be logged")
	}
}

// すべての応答が CSP 準拠の HTML（rig が応答ごとに検査する）。
func TestAgentRoutesRenderCSPCompliantHTML(t *testing.T) {
	r := newRig(t, registered(true, true))
	for _, target := range []string{"/agent", "/agent/api-key/delete/confirm", "/agent/fastpath/api-key/delete/confirm"} {
		status(t, r.get(target), http.StatusOK)
	}
	r.do(http.MethodPut, "/agent/api-key", url.Values{"api_key": {canaryKey}}, hx...)
	r.do(http.MethodPut, "/agent/api-key", url.Values{"api_key": {""}}, hx...)
}
