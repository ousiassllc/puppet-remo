package gui_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

var tokenValue = regexp.MustCompile(`value="(prm_[A-Za-z0-9_-]+)"`)

// トークンの平文は、発行の応答に一度だけ出る。no-store で、読み取り専用の入力欄に入り、以後はどこにも出ない。
func TestIssueTokenRevealsPlaintextOnce(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { seedClient(f, "c1", "alice-desktop", true, true, false) })

	res := r.post("/clients/c1/tokens", url.Values{"label": {"claude-desktop"}, "expires_in_days": {"30"}}, hx...)
	status(t, res, http.StatusOK)
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	m := tokenValue.FindStringSubmatch(res.Body)
	if m == nil {
		t.Fatalf("plaintext token is not in the response:\n%s", res.Body)
	}
	plaintext := m[1]
	if n := strings.Count(res.Body, plaintext); n != 1 {
		t.Errorf("plaintext appears %d times in the issuing response, want 1", n)
	}
	// 読み取り専用の入力欄、コピーのボタン、フォールバックの案内。
	input := regexp.MustCompile(`<input[^>]*id="issued-token"[^>]*>`).FindString(res.Body)
	if !strings.Contains(input, " readonly") {
		t.Errorf("token input is not readonly: %q", input)
	}
	contains(t, res, ` novalidate`, `data-copy-target="issued-token"`, `data-fallback="`, "今だけ表示されます", "claude-desktop")
	// 一覧の更新は OOB（接頭辞だけ）で添えられる。
	contains(t, res, `id="token-list" class="overflow-x-auto" hx-swap-oob="true"`, "claude-desktop")
	lacks(t, res, "<html")
	// フォームは空の状態に戻る（OOB）。発行したラベルも古いエラーも残さない。
	contains(t, res, `id="token-form"`, `hx-swap-oob="true"`, `id="token-form-errors"`)
	lacks(t, res, `value="claude-desktop"`, "data-error-code")

	// 期限は発行時刻 + 30 日。
	if len(r.f.Admin.Issued) != 1 || r.f.Admin.Issued[0].TTL == nil || *r.f.Admin.Issued[0].TTL != 30*24*time.Hour {
		t.Errorf("issued = %+v", r.f.Admin.Issued)
	}
	contains(t, res, "2026-11-29 12:00")

	// 以後のどの応答にも平文は出ない。
	for _, again := range []resp{r.get("/clients/c1"), r.get("/clients/c1", hx...), r.get("/clients", hx...)} {
		lacks(t, again, plaintext)
	}
	if got := r.get("/clients/c1"); !strings.Contains(got.Body, "claude-desktop") {
		t.Error("issued token is not listed on the detail page")
	}
}

func TestIssueTokenExpiry(t *testing.T) {
	tests := []struct {
		name    string
		form    url.Values
		wantTTL func(*time.Duration) bool
		expires string
	}{
		{"default", url.Values{"label": {"a"}}, func(d *time.Duration) bool { return d == nil }, "2027-01-28 12:00"},
		{"90 days", url.Values{"label": {"a"}, "expires_in_days": {"90"}}, func(d *time.Duration) bool { return d != nil && *d == 90*24*time.Hour }, "2027-01-28 12:00"},
		{"never", url.Values{"label": {"a"}, "expires_in_days": {"never"}}, func(d *time.Duration) bool { return d != nil && *d == 0 }, "無期限"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, func(f *guitest.Fakes) { seedClient(f, "c1", "alice-desktop", true, true, false) })
			res := r.post("/clients/c1/tokens", tt.form, hx...)
			status(t, res, http.StatusOK)
			if len(r.f.Admin.Issued) != 1 || !tt.wantTTL(r.f.Admin.Issued[0].TTL) {
				t.Fatalf("issued = %+v", r.f.Admin.Issued)
			}
			contains(t, res, "（有効期限: "+tt.expires+"）")
		})
	}
}

func TestIssueTokenErrors(t *testing.T) {
	tests := []struct {
		name   string
		form   url.Values
		status int
		code   string
		msg    string
	}{
		{"empty label", url.Values{"label": {"  "}}, http.StatusUnprocessableEntity, "invalid_argument", "ラベルを入力してください。"},
		{"bad expiry", url.Values{"label": {"a"}, "expires_in_days": {"-1"}}, http.StatusUnprocessableEntity, "invalid_argument", "有効期限の指定が正しくありません。"},
		{"expiry not a number", url.Values{"label": {"a"}, "expires_in_days": {"soon"}}, http.StatusUnprocessableEntity, "invalid_argument", "有効期限の指定が正しくありません。"},
		{"expiry too long", url.Values{"label": {"a"}, "expires_in_days": {"99999"}}, http.StatusUnprocessableEntity, "invalid_argument", "有効期限の指定が正しくありません。"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, func(f *guitest.Fakes) { seedClient(f, "c1", "alice-desktop", true, true, false) })
			res := r.post("/clients/c1/tokens", tt.form, hx...)
			status(t, res, tt.status)
			contains(t, res, `data-error-code="`+tt.code+`"`, tt.msg)
			lacks(t, res, "prm_", "data-token-reveal", "<html")
			must(t, len(r.f.Admin.Issued) == 0, "invalid input must not reach the service")
		})
	}

	t.Run("duplicate label is a 409", func(t *testing.T) {
		r := newRig(t, func(f *guitest.Fakes) { seedClient(f, "c1", "alice-desktop", true, true, false) })
		status(t, r.post("/clients/c1/tokens", url.Values{"label": {"dup"}}, hx...), http.StatusOK)
		res := r.post("/clients/c1/tokens", url.Values{"label": {"dup"}}, hx...)
		status(t, res, http.StatusConflict)
		contains(t, res, `data-error-code="conflict"`, "同じラベルのトークンがすでにあります。")
		lacks(t, res, "prm_", "data-token-reveal")
	})
	t.Run("unknown client is a 404 toast", func(t *testing.T) {
		res := newRig(t).post("/clients/zzz/tokens", url.Values{"label": {"a"}}, hx...)
		status(t, res, http.StatusNotFound)
		contains(t, res, `data-error-code="not_found"`)
		lacks(t, res, "prm_")
	})
	t.Run("internal failure does not leak the cause", func(t *testing.T) {
		r := newRig(t, func(f *guitest.Fakes) {
			seedClient(f, "c1", "alice-desktop", true, true, false)
			f.Admin.IssueErr = errCleanup
		})
		res := r.post("/clients/c1/tokens", url.Values{"label": {"a"}}, hx...)
		status(t, res, http.StatusInternalServerError)
		contains(t, res, `data-error-code="internal"`)
		lacks(t, res, "cleanup boom")
	})
}

func TestRevokeToken(t *testing.T) {
	future := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	r := newRig(t, func(f *guitest.Fakes) {
		seedClient(f, "c1", "alice-desktop", true, true, false)
		seedToken(f, "c1", "t1", "claude-desktop", &future, nil)
		seedToken(f, "c1", "t2", "ci-bot", &future, nil)
	})
	conf := r.get("/clients/c1/tokens/t1/revoke/confirm", hx...)
	status(t, conf, http.StatusOK)
	contains(t, conf, `role="dialog"`, "claude-desktop", `hx-delete="/clients/c1/tokens/t1"`, `hx-target="#token-list"`, "失効する")

	res := r.do(http.MethodDelete, "/clients/c1/tokens/t1", nil, hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="token-list"`, "失効済み", `id="modal-root" hx-swap-oob="true"`, "トークンを失効しました。",
		`hx-get="/clients/c1/tokens/t2/revoke/confirm"`)
	lacks(t, res, "t1/revoke", "<html")

	for _, res := range []resp{r.get("/clients/c1/tokens/nope/revoke/confirm", hx...), r.do(http.MethodDelete, "/clients/c1/tokens/nope", nil, hx...)} {
		status(t, res, http.StatusNotFound)
		contains(t, res, `data-error-code="not_found"`)
	}
}
