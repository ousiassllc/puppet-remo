package remote_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// held は viewer を発行して操作権を取得した状態にする。
func held(t *testing.T, r *rig) []string {
	t.Helper()
	_, h := r.viewer()
	status(t, r.post("/viewer/lease/acquire", nil, h...), http.StatusOK)
	return h
}

// 秘密にあたるテスト用の文字列（テストの実行時に作る。固定の秘密情報は持たない）。
func sample() string { return strings.Repeat("clip-", 3) + "<b>&" }

func TestClipboardSend(t *testing.T) {
	r := newRig(t)
	h := held(t, r)
	text := sample()
	res := r.post("/viewer/clipboard/send", url.Values{"text": {text}}, h...)
	status(t, res, http.StatusOK)
	contains(t, res, "ホストのクリップボードへ送信しました", "（19 文字）")
	// 送ったテキストは応答に出さない（文字数だけ）。
	lacks(t, res, "clip-", "<html")
	if r.f.Control.Clipboard != text {
		t.Errorf("host clipboard = %q, want %q", r.f.Control.Clipboard, text)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestClipboardSendCountsCharactersNotBytes(t *testing.T) {
	r := newRig(t)
	h := held(t, r)
	res := r.post("/viewer/clipboard/send", url.Values{"text": {"こんにちは"}}, h...)
	contains(t, res, "（5 文字）")
}

func TestClipboardSendLimits(t *testing.T) {
	r := newRig(t)
	h := held(t, r)
	// 1 MiB ちょうどまで（ASCII。フォームの本文も 1 MiB に収まる）。
	status(t, r.post("/viewer/clipboard/send", url.Values{"text": {strings.Repeat("a", 1<<20-len("text="))}}, h...), http.StatusOK)
	// 1 MiB を超えるテキストは、本文の上限（1 MiB）で拒否する。
	failed(t, r.post("/viewer/clipboard/send", url.Values{"text": {strings.Repeat("a", 1<<20+1)}}, h...), http.StatusBadRequest, "invalid_argument")
	// 項目が無い。
	failed(t, r.post("/viewer/clipboard/send", url.Values{}, h...), http.StatusBadRequest, "invalid_argument")
}

func TestClipboardSendNeedsTheLease(t *testing.T) {
	r := newRig(t)
	_, h := r.viewer()
	failed(t, r.post("/viewer/clipboard/send", url.Values{"text": {"x"}}, h...), http.StatusConflict, "lease_required")
	if len(r.f.Control.Ops) != 0 {
		t.Error("the clipboard must not be written without the lease")
	}
}

func TestClipboardSendWhenAnotherHoldsTheLease(t *testing.T) {
	r := newRig(t)
	_, h := r.viewer()
	r.f.Leases.Hold(domain.Actor{Kind: domain.ActorMCP, ClientID: "c9", SessionID: "s1"})
	failed(t, r.post("/viewer/clipboard/send", url.Values{"text": {"x"}}, h...), http.StatusConflict, "lease_held_by_other")
}

func TestClipboardSendAfterRevocation(t *testing.T) {
	r := newRig(t)
	h := held(t, r)
	if err := r.f.Leases.Revoke(service.HostActor); err != nil {
		t.Fatal(err)
	}
	failed(t, r.post("/viewer/clipboard/send", url.Values{"text": {"x"}}, h...), http.StatusConflict, "lease_revoked")
}

func TestClipboardFetchShowsAReadOnlyTextarea(t *testing.T) {
	r := newRig(t)
	h := held(t, r)
	r.f.Control.Clipboard = `</textarea><b>host</b>`
	res := r.post("/viewer/clipboard/fetch", nil, h...)
	status(t, res, http.StatusOK)
	contains(t, res, "<textarea", " readonly", `id="clipboard-fetched"`, "&lt;/textarea&gt;&lt;b&gt;host&lt;/b&gt;")
	lacks(t, res, "<b>host</b>", "<html")
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestClipboardFetchNeedsTheLease(t *testing.T) {
	r := newRig(t)
	_, h := r.viewer()
	r.f.Control.Clipboard = "host-secret"
	res := r.post("/viewer/clipboard/fetch", nil, h...)
	failed(t, res, http.StatusConflict, "lease_required")
	lacks(t, res, "host-secret")
}

func TestClipboardFailuresDoNotLeakTheText(t *testing.T) {
	r := newRig(t)
	h := held(t, r)
	r.f.Control.DoErr = domain.ErrDesktopUnavailable.WithMsg("clipboard: secret details")
	res := r.post("/viewer/clipboard/send", url.Values{"text": {"typed-secret"}}, h...)
	failed(t, res, http.StatusServiceUnavailable, "desktop_unavailable")
	lacks(t, res, "typed-secret", "secret details")
}

func TestKeysSend(t *testing.T) {
	r := newRig(t)
	h := held(t, r)
	for _, keys := range []string{"alt+tab", "alt+f4", "win+d", "ctrl+w", "escape", "CTRL+W"} {
		res := r.post("/viewer/keys/send", url.Values{"keys": {keys}}, h...)
		status(t, res, http.StatusOK)
		contains(t, res, "を送信しました")
		lacks(t, res, "<html")
	}
	if n := len(r.f.Control.Ops); n != 6 {
		t.Fatalf("ops = %d, want 6", n)
	}
	got, ok := r.f.Control.Ops[0].(domain.KeyPress)
	if !ok || got.Keys.String() != "alt+tab" {
		t.Errorf("first op = %#v, want KeyPress alt+tab", r.f.Control.Ops[0])
	}
}

// Ctrl+Alt+Del は送れない（FR-W08）。InputGuard が拒否し、409 input_blocked。
func TestKeysSendBlocksCtrlAltDelete(t *testing.T) {
	r := newRig(t)
	h := held(t, r)
	for _, keys := range []string{"ctrl+alt+delete", "alt+ctrl+DELETE", "ctrl+alt+shift+delete"} {
		res := r.post("/viewer/keys/send", url.Values{"keys": {keys}}, h...)
		failed(t, res, http.StatusConflict, "input_blocked")
		contains(t, res, "Ctrl+Alt+Del は送れません")
	}
	if len(r.f.Control.Ops) != 0 {
		t.Errorf("ops = %v; ctrl+alt+delete must not be sent", r.f.Control.Ops)
	}
}

func TestKeysSendInputBlockedReasons(t *testing.T) {
	r := newRig(t)
	h := held(t, r)
	tests := []struct {
		reason string
		want   string
	}{
		{domain.ReasonElevatedWindow, "管理者権限"},
		{domain.ReasonOwnWindow, "puppet-remo 自身"},
		{"unknown", "入力できません"},
	}
	for _, tt := range tests {
		r.f.Control.DoErr = domain.ErrInputBlocked.WithDetail("reason", tt.reason)
		res := r.post("/viewer/keys/send", url.Values{"keys": {"alt+tab"}}, h...)
		failed(t, res, http.StatusConflict, "input_blocked")
		contains(t, res, tt.want)
	}
}

func TestKeysSendValidation(t *testing.T) {
	r := newRig(t)
	h := held(t, r)
	for _, form := range []url.Values{{}, {"keys": {""}}, {"keys": {"ctrl+nope"}}, {"keys": {"hyper+a"}}, {"keys": {"ctrl+"}}} {
		failed(t, r.post("/viewer/keys/send", form, h...), http.StatusBadRequest, "invalid_argument")
	}
	if len(r.f.Control.Ops) != 0 {
		t.Errorf("ops = %v; invalid keys must not be sent", r.f.Control.Ops)
	}
}

func TestKeysSendNeedsTheLease(t *testing.T) {
	r := newRig(t)
	_, h := r.viewer()
	failed(t, r.post("/viewer/keys/send", url.Values{"keys": {"alt+tab"}}, h...), http.StatusConflict, "lease_required")
}

func TestKeysSendNeedsAValidViewerID(t *testing.T) {
	r := newRig(t)
	failed(t, r.post("/viewer/keys/send", url.Values{"keys": {"alt+tab"}}), http.StatusBadRequest, "invalid_argument")
	failed(t, r.post("/viewer/clipboard/fetch", nil), http.StatusBadRequest, "invalid_argument")
	failed(t, r.post("/viewer/clipboard/send", url.Values{"text": {"x"}}), http.StatusBadRequest, "invalid_argument")
}

// ショートカット送信メニューは最小の組で、ctrl+alt+delete を含まない。
func TestKeyMenuItems(t *testing.T) {
	r := newRig(t)
	_, h := r.viewer()
	res := r.post("/viewer/lease/acquire", nil, h...)
	contains(t, res, "Alt+Tab", "Alt+F4", "Win+D", "Ctrl+W", "Esc", `hx-post="/viewer/keys/send"`)
	lacks(t, res, "delete", "Delete", "navigator.clipboard")
}
