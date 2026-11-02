package remote_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/remote/remotetest"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
)

func TestRootRedirectsToViewer(t *testing.T) {
	res := newRig(t).get("/")
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/viewer" {
		t.Errorf("GET / = %d, Location %q; want 302 /viewer", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestViewerFullPage(t *testing.T) {
	r := newRig(t)
	res := r.get("/viewer")
	status(t, res, http.StatusOK)
	contains(t, res,
		"<html", `<main id="viewer-root"`,
		// viewer_id を発行して X-Viewer-Id と ws-url に埋め込む（API 仕様 §4.1）。X-Puppet-Remo は <body hx-headers>。
		`X-Viewer-Id`, `VIEWER01`, `ws-url="ws://100.64.0.1:8765/ws/screen?viewer_id=VIEWER01"`,
		`X-Puppet-Remo`,
		// <remo-screen-viewer> と screen-viewer.js・htmx の読み込み（外部ファイルだけ）。
		`<remo-screen-viewer`, `src="/static/js/screen-viewer.js"`, `src="/static/js/htmx.min.js"`, `src="/static/js/htmx-config.js"`,
		`monitor="0"`, `quality="70"`, `fps="10"`,
		// 操作権がないので readonly。UI から Ctrl+Alt+Del は送れない旨（FR-W08）。
		`readonly`, `Ctrl+Alt+Del は送れません`,
	)
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	// 設定に従う既定値・モニターの選択肢（FR-W04・W05）。
	contains(t, res, `hx-post="/viewer/settings"`, `name="monitor"`, `name="quality"`, `name="fps"`, `value="70"`, `value="10"`,
		`モニター 1（1920×1080） メイン`, `モニター 2（1280×720）`)
	// 状態駆動: 空きなので「取得」だけ。クリップボード・ショートカットは操作権が無いので描画しない。
	contains(t, res, `hx-post="/viewer/lease/acquire"`)
	lacks(t, res, "/viewer/lease/release", "/viewer/lease/revoke", "clipboard", "/viewer/keys/send")
}

func TestViewerUsesConfiguredDefaultsAndPrimaryMonitor(t *testing.T) {
	r := newRig(t, func(f *remotetest.Fakes) {
		f.Settings.Values.ViewerDefaultQuality, f.Settings.Values.ViewerDefaultFPS = 40, 25
		f.Control.Mons[0].Primary, f.Control.Mons[1].Primary = false, true
	})
	res := r.get("/viewer")
	contains(t, res, `monitor="1"`, `quality="40"`, `fps="25"`)
}

func TestViewerFragmentForHTMX(t *testing.T) {
	res := newRig(t).get("/viewer", hx...)
	status(t, res, http.StatusOK)
	contains(t, res, `<main id="viewer-root"`, `<remo-screen-viewer`)
	lacks(t, res, "<html", "<head", "htmx.min.js")
}

func TestViewerIssuesAFreshViewerIDEachTime(t *testing.T) {
	r := newRig(t)
	a, _ := r.viewer()
	b, _ := r.viewer()
	if a == b {
		t.Errorf("viewer ids are equal: %q", a)
	}
}

// 他者が保持しているときは、種別・名前・状態を出し、強制剥奪だけを描画する（取得・解放は描画しない）。
func TestViewerShowsHolderAndOnlyRevokeWhenOthersHold(t *testing.T) {
	r := newRig(t, func(f *remotetest.Fakes) {
		f.Leases.Hold(domain.Actor{Kind: domain.ActorMCP, ClientID: "c9", Label: "claude-desktop", SessionID: "s1"})
	})
	res := r.get("/viewer")
	contains(t, res, "保持中", "claude-desktop", ">mcp<", `data-lease-state="other"`, `hx-post="/viewer/lease/revoke"`, "readonly")
	lacks(t, res, "/viewer/lease/acquire", "/viewer/lease/release")
}

func TestViewerEscapesHolderLabel(t *testing.T) {
	r := newRig(t, func(f *remotetest.Fakes) {
		f.Leases.Hold(domain.Actor{Kind: domain.ActorMCP, ClientID: "c9", Label: `<script>alert(1)</script>`, SessionID: "s1"})
	})
	res := r.get("/viewer")
	lacks(t, res, "<script>alert")
	contains(t, res, "&lt;script&gt;")
}

func TestViewerPageFailures(t *testing.T) {
	t.Run("設定を読めない", func(t *testing.T) {
		r := newRig(t, func(f *remotetest.Fakes) { f.Settings.Err = domain.ErrInvalidArgument.WithMsg("db: secret details") })
		res := r.get("/viewer")
		status(t, res, http.StatusBadRequest)
		contains(t, res, `data-error-code="invalid_argument"`, "<html")
		lacks(t, res, "secret details")
	})
	t.Run("モニターを読めない（想定外は 500 internal・詳細を出さない）", func(t *testing.T) {
		r := newRig(t, func(f *remotetest.Fakes) { f.Control.MonitorsErr = errUnexpected })
		res := r.get("/viewer")
		status(t, res, http.StatusInternalServerError)
		contains(t, res, `data-error-code="internal"`)
		lacks(t, res, "secret details")
	})
	t.Run("HX-Request ならフラグメントのエラー", func(t *testing.T) {
		r := newRig(t, func(f *remotetest.Fakes) { f.Control.MonitorsErr = errUnexpected })
		res := r.get("/viewer", hx...)
		status(t, res, http.StatusInternalServerError)
		contains(t, res, `data-error-code="internal"`)
		lacks(t, res, "<html")
	})
}

func TestUnknownPathIs404(t *testing.T) {
	r := newRig(t)
	for _, path := range []string{"/nope", "/viewer/nope", "/viewer/lease/nope"} {
		res := r.get(path)
		status(t, res, http.StatusNotFound)
		contains(t, res, `data-error-code="not_found"`)
	}
	// ホスト GUI のルートは、リモートには存在しない（構造で分離する）。
	for _, path := range []string{"/requests", "/clients", "/settings", "/killswitch/trigger", "/lease/revoke", "/logs", "/logs/open-folder"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if res := r.do(method, path, nil); res.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404", method, path, res.StatusCode)
			}
		}
	}
}

func TestStateChangingRoutesRequireTheCSRFHeader(t *testing.T) {
	r := newRig(t)
	_, viewerHeader := r.viewer()
	for _, path := range []string{"/viewer/lease/acquire", "/viewer/settings", "/viewer/clipboard/send", "/viewer/keys/send"} {
		res := r.post(path, nil, append(viewerHeader, middleware.CSRFHeader, "")...)
		status(t, res, http.StatusForbidden)
		contains(t, res, `data-error-code="missing_header"`)
	}
	if n := r.f.Leases.Acquires; n != 0 {
		t.Errorf("acquire was reached %d times without the header", n)
	}
}

var errUnexpected = errors.New("db: secret details")
