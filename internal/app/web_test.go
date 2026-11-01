package app

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
)

// このファイルはリモートの Web UI（/viewer）の組み立て（ハンドラ・viewer_id・リース・待機ページ）を、リモートルーターの
// Listener 経由で確かめる。

// webDo は tailnet の IP の Host ヘッダーで loopback の待ち受けへ要求する。
func (e *env) webDo(method, path string, form url.Values, headers ...string) (*http.Response, string) {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, "http://"+testTailnetIP+":8765"+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == http.MethodPost {
		req.Header.Set(middleware.CSRFHeader, "1")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	client := &http.Client{
		Transport:     remoteTransport{e: e},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp, string(b)
}

var viewerIDInPage = regexp.MustCompile(`ws/screen\?viewer_id=([0-9A-Za-z]+)`)

// 登録済みのブラウザ: / → /viewer → viewer_id を埋めたページ → その viewer_id で WebSocket が開き、操作権の取得・設定の変更・
// ショートカットの送信・解放が通る（FR-W01・W04〜W08）。
func TestWebUIViewerFlow(t *testing.T) {
	e := newEnv(t)
	e.registerClient()
	e.windows.SetWindows(domain.WindowInfo{ID: "88", Title: "無題 - メモ帳", Process: "notepad.exe", PID: 4242, Foreground: true})
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)

	if resp, _ := e.webDo(http.MethodGet, "/", nil); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/viewer" {
		t.Fatalf("GET / = %d (Location %q), want 302 /viewer", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, page := e.webDo(http.MethodGet, "/viewer", nil)
	m := viewerIDInPage.FindStringSubmatch(page)
	if resp.StatusCode != http.StatusOK || m == nil || !strings.Contains(page, "<remo-screen-viewer") {
		t.Fatalf("GET /viewer = %d: %s", resp.StatusCode, page)
	}
	viewerID := m[1]
	h := []string{"X-Viewer-Id", viewerID}

	// この viewer_id で WebSocket を開く（ready は偽のモニター 640×480、既定の画質 70・10 fps）。
	c, _, err := e.dialWS(viewerID)
	if err != nil {
		t.Fatal(err)
	}
	if ready := wsReadJSON(t, c); ready["type"] != "ready" || ready["width"] != 640.0 {
		t.Fatalf("ready = %v", ready)
	}

	// 操作権の取得 → 保持者は種別 web・このビューア。
	resp, body := e.webDo(http.MethodPost, "/viewer/lease/acquire", nil, h...)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "/viewer/lease/release") {
		t.Fatalf("acquire = %d: %s", resp.StatusCode, body)
	}
	holder, held := e.holder()
	if !held || holder.Actor.Kind != domain.ActorWeb || holder.Actor.SessionID != viewerID {
		t.Fatalf("holder = %+v, %v; want this web viewer", holder, held)
	}

	// 設定の変更は、接続中のセッションに反映される。
	resp, body = e.webDo(http.MethodPost, "/viewer/settings", url.Values{"monitor": {"0"}, "quality": {"50"}, "fps": {"15"}}, h...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings = %d: %s", resp.StatusCode, body)
	}
	for m := wsReadJSON(t, c); m["type"] != "config"; m = wsReadJSON(t, c) {
		// 画面・リース以外の ready 直後のメッセージは読み飛ばす。
	}

	// ショートカットの送信は Control を通って、偽の injector に届く。ctrl+alt+delete は 409。
	resp, body = e.webDo(http.MethodPost, "/viewer/keys/send", url.Values{"keys": {"alt+tab"}}, h...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("keys/send = %d: %s", resp.StatusCode, body)
	}
	resp, body = e.webDo(http.MethodPost, "/viewer/keys/send", url.Values{"keys": {"ctrl+alt+delete"}}, h...)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, `data-error-code="input_blocked"`) {
		t.Fatalf("ctrl+alt+delete = %d: %s", resp.StatusCode, body)
	}

	// クリップボードの送受信。
	text := "from-" + viewerID
	if resp, body = e.webDo(http.MethodPost, "/viewer/clipboard/send", url.Values{"text": {text}}, h...); resp.StatusCode != http.StatusOK {
		t.Fatalf("clipboard/send = %d: %s", resp.StatusCode, body)
	}
	resp, body = e.webDo(http.MethodPost, "/viewer/clipboard/fetch", nil, h...)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, text) {
		t.Fatalf("clipboard/fetch = %d: %s", resp.StatusCode, body)
	}

	// 解放。
	if resp, body = e.webDo(http.MethodPost, "/viewer/lease/release", nil, h...); resp.StatusCode != http.StatusOK {
		t.Fatalf("release = %d: %s", resp.StatusCode, body)
	}
	if _, held := e.holder(); held {
		t.Error("the lease must be released")
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
}

// 未登録のブラウザ: 403 device_not_registered の待機ページ（FR-W02）。ホストが拒否すると、拒否のページに変わり、
// 24 時間は新しい接続要求を作らない（FR-A06 の挙動は変えない）。
func TestWebUIUnregisteredDeviceSeesTheConnectionRequestState(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)

	resp, body := e.webDo(http.MethodGet, "/viewer", nil, "Accept", "text/html")
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, `data-connection-state="pending"`) ||
		!strings.Contains(body, `data-error-code="device_not_registered"`) {
		t.Fatalf("GET /viewer = %d: %s", resp.StatusCode, body)
	}
	if n, err := e.app.PendingRequests(t.Context()); err != nil || n != 1 {
		t.Fatalf("PendingRequests = %d, %v; want the one request this made", n, err)
	}
	// 同じデバイスの再アクセスは、1 件に集約される。
	e.webDo(http.MethodGet, "/viewer", nil, "Accept", "text/html")
	if n, _ := e.app.PendingRequests(t.Context()); n != 1 {
		t.Errorf("PendingRequests = %d after a repeat, want 1 (aggregated)", n)
	}

	pending, err := e.app.admin.PendingRequests(t.Context())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v, %v", pending, err)
	}
	if err := e.app.admin.DenyRequest(t.Context(), pending[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/viewer", "/static/js/app.js"} {
		resp, body = e.webDo(http.MethodGet, path, nil, "Accept", "text/html")
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, `data-connection-state="denied"`) {
			t.Fatalf("GET %s after deny = %d: %s", path, resp.StatusCode, body)
		}
	}
	if n, _ := e.app.PendingRequests(context.Background()); n != 0 {
		t.Errorf("PendingRequests = %d after deny, want 0 (no new request for 24h)", n)
	}
}
