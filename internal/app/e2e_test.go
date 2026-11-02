//go:build e2e

package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestE2ELoopbackAddr(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:0":       true,
		"127.0.0.1:18766":   true,
		"[::1]:18766":       true,
		"127.1.2.3:8080":    true,
		"0.0.0.0:18766":     false,
		":18766":            false,
		"[::]:18766":        false,
		"localhost:18766":   false, // 名前は loopback 以外へ解決されうる
		"192.0.2.1:18766":   false,
		"100.64.0.1:18766":  false,
		"127.0.0.1":         false, // ポートが無い
		"127.0.0.1:70000":   false,
		"127.0.0.1:invalid": false,
	} {
		if err := e2eLoopbackAddr(addr); (err == nil) != ok {
			t.Errorf("e2eLoopbackAddr(%q) = %v, want ok=%v", addr, err, ok)
		}
	}
}

func TestRunE2ERefusesNonLoopbackControlAddr(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "192.0.2.1:0", "localhost:0"} {
		err := RunE2E(context.Background(), E2EOptions{Port: 18765, ControlAddr: addr, Static: http.NotFoundHandler()})
		if err == nil {
			t.Errorf("RunE2E accepted the control address %q", addr)
		}
	}
}

func TestRunE2ERejectsInvalidOptions(t *testing.T) {
	if err := RunE2E(context.Background(), E2EOptions{Port: 80, ControlAddr: "127.0.0.1:0", Static: http.NotFoundHandler()}); err == nil {
		t.Error("RunE2E accepted port 80")
	}
	if err := RunE2E(context.Background(), E2EOptions{Port: 18765, ControlAddr: "127.0.0.1:0"}); err == nil {
		t.Error("RunE2E accepted a nil Static")
	}
}

// loopback 以外の接続元は、bind が loopback だけでも制御エンドポイントが 403 にする（多層防御）。
func TestE2EControlRejectsNonLoopbackPeer(t *testing.T) {
	h := (&e2eServer{}).handler()
	for remote, want := range map[string]int{
		"192.0.2.1:50000": http.StatusForbidden,
		"[2001:db8::1]:1": http.StatusForbidden,
		"garbage":         http.StatusForbidden,
		"127.0.0.1:50000": http.StatusBadRequest, // 通ったうえで、as の指定が無いので 400
		"[::1]:50000":     http.StatusBadRequest,
	} {
		req := httptest.NewRequest(http.MethodPost, "/identity", nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("peer %q: status = %d, want %d", remote, rec.Code, want)
		}
	}
}

// freePort は今空いている loopback のポートを返す（取ってすぐ閉じる。RunE2E の Listen の 1 回にだけ使う）。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

type e2eHarness struct {
	t       *testing.T
	remote  string // 127.0.0.1:port
	control string
	client  *http.Client
}

func startE2E(t *testing.T) *e2eHarness {
	t.Helper()
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(chan E2EAddrs, 1)
	done := make(chan error, 1)
	go func() {
		done <- RunE2E(ctx, E2EOptions{
			Port: port, ControlAddr: "127.0.0.1:0", Static: http.NotFoundHandler(),
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Ready:  func(a E2EAddrs) { addrs <- a },
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("RunE2E returned %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("RunE2E did not stop")
		}
	})

	h := &e2eHarness{t: t, client: &http.Client{Timeout: 5 * time.Second}}
	select {
	case a := <-addrs:
		h.remote, h.control = a.Remote, a.Control
	case err := <-done:
		t.Fatalf("RunE2E returned early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("RunE2E did not become ready")
	}
	// リモートルーターの待ち受けは Start の goroutine が始めるので、/ready が 200 になるまで上限つきで待つ。
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := h.client.Get("http://" + h.control + "/ready")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("/ready did not become 200 (last err %v)", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return h
}

func (h *e2eHarness) ctl(method, path string) (int, string) {
	h.t.Helper()
	req, err := http.NewRequest(method, "http://"+h.control+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.do(req)
}

func (h *e2eHarness) do(req *http.Request) (int, string) {
	h.t.Helper()
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// viewer はリモートルーターの /viewer をブラウザのように（Host は報告された tailnet の IP の 127.0.0.1 とポート）取得する。
func (h *e2eHarness) viewer() (int, string) {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+h.remote+"/viewer", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Accept", "text/html")
	return h.do(req)
}

func (h *e2eHarness) expect(method, path string, want int) string {
	h.t.Helper()
	code, body := h.ctl(method, path)
	if code != want {
		h.t.Fatalf("%s %s = %d %s, want %d", method, path, code, body, want)
	}
	return body
}

// 制御エンドポイントは loopback の IP にだけ bind し、リモートルーターも loopback で待ち受ける。
func TestRunE2EListensOnLoopbackOnly(t *testing.T) {
	h := startE2E(t)
	for _, addr := range []string{h.control, h.remote} {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatal(err)
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			t.Errorf("%q is not a loopback address", addr)
		}
	}
	// 制御エンドポイントのポートをリモートルーターの Host で叩いても、制御エンドポイントは届かない（別のソケット）。
	req, err := http.NewRequest(http.MethodGet, "http://"+h.remote+"/ready", nil)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := h.do(req); code == http.StatusOK {
		t.Error("the remote router serves the control endpoints")
	}
}

// 偽の tailnet・偽の platform の上で、本物の service とリモートルーターが、E2E のシナリオの順に動く。
func TestRunE2EScenario(t *testing.T) {
	h := startE2E(t)

	// 未登録のデバイス → 待機ページ（接続要求が作られる）。
	if code, body := h.viewer(); code != http.StatusForbidden || strings.Contains(body, "remo-screen-viewer") {
		t.Fatalf("unregistered device: viewer = %d %q", code, body)
	}
	var reqs []e2eRequest
	if err := json.Unmarshal([]byte(h.expect("GET", "/requests", 200)), &reqs); err != nil || len(reqs) != 1 {
		t.Fatalf("requests = %v (%v)", reqs, err)
	}

	// ホストが許可すると、同じデバイスがビューアを見られる。
	h.expect("POST", "/requests/allow", 200)
	if code, body := h.viewer(); code != http.StatusOK || !strings.Contains(body, "remo-screen-viewer") {
		t.Fatalf("after allow: viewer = %d %q", code, body)
	}
	h.expect("POST", "/requests/allow", 404) // もう保留中の要求は無い

	// キルスイッチ中は 503、解除すれば戻る。
	h.expect("POST", "/killswitch", 200)
	if code, _ := h.viewer(); code != http.StatusServiceUnavailable {
		t.Fatalf("kill switch: viewer = %d", code)
	}
	h.expect("POST", "/killswitch/release", 200)
	if code, _ := h.viewer(); code != http.StatusOK {
		t.Fatalf("after release: viewer = %d", code)
	}

	// マウスの入力は何も届いていない（キルスイッチは押下中の入力の解放 ReleaseAll だけを呼ぶ）。リースも空き。
	var calls []e2eInputCall
	if err := json.Unmarshal([]byte(h.expect("GET", "/inputs", 200)), &calls); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if c.Method != "ReleaseAll" {
			t.Fatalf("unexpected input %+v", c)
		}
	}
	if body := h.expect("GET", "/lease", 200); !strings.Contains(body, `"held":false`) {
		t.Fatalf("lease = %s", body)
	}
	h.expect("POST", "/lease/revoke", 200) // 保持者がいなくても冪等

	// reset で未登録に戻り、もう一度待機ページ。
	h.expect("POST", "/reset", 200)
	if code, _ := h.viewer(); code != http.StatusForbidden {
		t.Fatalf("after reset: viewer = %d", code)
	}
	// 登録済みのデバイスを名乗ると、許可なしでビューアが見られる。
	h.expect("POST", "/identity?as=registered", 200)
	if code, _ := h.viewer(); code != http.StatusOK {
		t.Fatalf("registered device: viewer = %d", code)
	}
	h.expect("POST", "/identity?as=unknown", 400)
}

func TestE2EPortIsTheHostCheckPort(t *testing.T) {
	h := startE2E(t)
	_, port, _ := net.SplitHostPort(h.remote)
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatal(err)
	}
	// 設定のポート以外の Host は 400（NFR-S02）。
	req, err := http.NewRequest(http.MethodGet, "http://"+h.remote+"/viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "127.0.0.1:1"
	if code, _ := h.do(req); code != http.StatusBadRequest {
		t.Fatalf("wrong port in Host: status = %d, want 400", code)
	}
}
