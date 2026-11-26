package main

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestCheckLoopback(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		minPort  int
		wantPort int
		wantErr  string // 空なら成功。エラーメッセージに含まれるべき文字列
	}{
		{"default remote addr", defaultRemoteAddr, minRemotePort, 8765, ""},
		{"another port", "127.0.0.1:9000", minRemotePort, 9000, ""},
		{"lowest remote port", "127.0.0.1:1024", minRemotePort, 1024, ""},
		{"highest port", "127.0.0.1:65535", minRemotePort, 65535, ""},
		{"ipv6 loopback", "[::1]:9000", minRemotePort, 9000, ""},
		{"other loopback address", "127.0.0.2:9000", minRemotePort, 9000, ""},
		{"gui addr may ask the OS for a port", "127.0.0.1:0", 0, 0, ""},
		{"empty", "", minRemotePort, 0, "IP:ポート"},
		{"no port", "127.0.0.1", minRemotePort, 0, "IP:ポート"},
		{"empty port", "127.0.0.1:", minRemotePort, 0, "ポート"},
		{"empty host", ":9000", minRemotePort, 0, "loopback"},
		{"port is not a number", "127.0.0.1:http", minRemotePort, 0, "ポート"},
		{"port with a sign", "127.0.0.1:+9000", minRemotePort, 0, "ポート"},
		{"port above 65535", "127.0.0.1:65536", minRemotePort, 0, "ポート"},
		{"negative port", "127.0.0.1:-1", minRemotePort, 0, "ポート"},
		{"port below the server.port minimum", "127.0.0.1:1023", minRemotePort, 0, "1024"},
		{"port 0 for the remote router", "127.0.0.1:0", minRemotePort, 0, "1024"},
		{"not loopback: any address", "0.0.0.0:9000", minRemotePort, 0, "loopback"},
		{"not loopback: tailnet address", "100.64.0.1:9000", minRemotePort, 0, "loopback"},
		{"not loopback: name", "localhost:9000", minRemotePort, 0, "loopback"},
		{"not loopback: ipv6 any", "[::]:9000", minRemotePort, 0, "loopback"},
	}
	for _, tt := range tests {
		port, err := checkLoopback("addr", tt.addr, tt.minPort)
		if tt.wantErr == "" {
			if err != nil || port != tt.wantPort {
				t.Errorf("%s: checkLoopback(%q) = %d, %v; want %d", tt.name, tt.addr, port, err, tt.wantPort)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "-addr") {
			t.Errorf("%s: checkLoopback(%q) error = %v; want it to mention -addr and %q", tt.name, tt.addr, err, tt.wantErr)
		}
	}
}

// run は不正な -addr を、待ち受けを始める前に理由つきのエラーで断る。
func TestRunRejectsInvalidAddr(t *testing.T) {
	for _, addr := range []string{"", "127.0.0.1", "127.0.0.1:99999", "0.0.0.0:9000", "127.0.0.1:80"} {
		err := run(context.Background(), nil, addr, "127.0.0.1:0", domain.ApprovalAuto, false, false, "", false, 0, false)
		if err == nil || !strings.Contains(err.Error(), "-addr") {
			t.Errorf("run(-addr %q) error = %v; want an error that mentions -addr", addr, err)
		}
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// getWithHost は addr へ Host ヘッダーを host にして GET し、状態コードを返す。接続できるまで（上限つきで）やり直す。
func getWithHost(t *testing.T, addr, host, path string) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return resp.StatusCode
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s (Host %s): %v", addr+path, host, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// -addr の既定以外のポートで起動でき、Host ヘッダーの検査（設定 server.port）がそのポートに追従する:
// 偽の tailnet の IP と -addr のポートは通り、既定の 8765 の Host は invalid host で断られる。
func TestRunListensOnTheAddrPortAndHostCheckFollows(t *testing.T) {
	t.Setenv("PUPPET_REMO_STATIC_DIR", t.TempDir()) // dist は要らない（API だけを見る）
	port := freeLoopbackPort(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, func(net.Addr) { close(ready) }, addr, "127.0.0.1:0", domain.ApprovalAuto, false, false, "", false, 0, false)
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("run returned %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("run did not stop after the context was cancelled")
		}
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("run stopped before it was ready: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("run was not ready in time")
	}

	if got := getWithHost(t, addr, demoTailnet+":"+strconv.Itoa(port), "/api/v1/status"); got != http.StatusOK {
		t.Errorf("Host %s:%d: status = %d, want 200", demoTailnet, port, got)
	}
	if got := getWithHost(t, addr, demoTailnet+":8765", "/api/v1/status"); got != http.StatusBadRequest { // CodeInvalidHost
		t.Errorf("Host %s:8765 (not the -addr port): status = %d, want 400 (invalid host)", demoTailnet, got)
	}
}
