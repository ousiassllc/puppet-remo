package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
)

const defaultHotkey = "Ctrl+Alt+Shift+K" // FR-S02 の既定

func TestNewRequiresDependencies(t *testing.T) {
	full := newEnv(t)
	cases := map[string]func(*Config){
		"Repo":      func(c *Config) { c.Repo = nil },
		"Capturer":  func(c *Config) { c.Platform.Capturer = nil },
		"Input":     func(c *Config) { c.Platform.Input = nil },
		"Windows":   func(c *Config) { c.Platform.Windows = nil },
		"Clipboard": func(c *Config) { c.Platform.Clipboard = nil },
		"UI":        func(c *Config) { c.Platform.UI = nil },
		"Hotkey":    func(c *Config) { c.Platform.Hotkey = nil },
		"Watcher":   func(c *Config) { c.Watcher = nil },
		"WhoIs":     func(c *Config) { c.WhoIs = nil },
		"Static":    func(c *Config) { c.Static = nil },
	}
	for name, clear := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := full.app.cfg
			clear(&cfg)
			if _, err := New(t.Context(), cfg); err == nil {
				t.Errorf("New without %s succeeded", name)
			}
		})
	}
}

// 組み立てだけではネットワークにもホットキーにも触れない（動かすのは Start。起動順序: docs/components/overview.md §9）。
func TestNewDoesNotTouchTheNetworkOrTheHotkey(t *testing.T) {
	e := newEnv(t)
	e.watcher.mu.Lock()
	watching := len(e.watcher.subs)
	e.watcher.mu.Unlock()
	if watching != 0 {
		t.Errorf("%d tailscaled watchers were started by New", watching)
	}
	if got := e.listens.Requested(); len(got) != 0 {
		t.Errorf("listened before Start: %v", got)
	}
	if e.hotkey.Trigger(defaultHotkey) {
		t.Error("the kill switch hotkey was registered before Start")
	}
}

func TestStartRegistersTheDefaultKillSwitchHotkey(t *testing.T) {
	e := newEnv(t)
	e.start()
	if got := e.app.hotkey.Spec(); got != defaultHotkey {
		t.Errorf("registered hotkey = %q, want the default %q (FR-S02)", got, defaultHotkey)
	}
}

func TestStartTwiceFails(t *testing.T) {
	e := newEnv(t)
	e.start()
	if err := e.app.Start(t.Context()); err == nil {
		t.Error("the second Start must fail")
	}
}

// FR-H06 / FR-S02: ホットキー → KillSwitch → 全リース剥奪・全 MCP セッション切断・受付停止。実際の MCP クライアントを
// リモートルーターの Listener 経由でつないで確かめる。
func TestHotkeyTriggersKillSwitchRevokesLeasesAndClosesMCPSessions(t *testing.T) {
	e := newEnv(t)
	_, token := e.registerClient()
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)

	cs := e.dialMCP(token)
	if res := callTool(t, cs, "acquire_control"); res.IsError {
		t.Fatalf("acquire_control failed: %+v", res.Content)
	}
	if _, held := e.holder(); !held {
		t.Fatal("the lease was not acquired")
	}
	eventually(t, "the session to be listed", func() bool { return len(e.app.mcp.Sessions()) == 1 })

	events, cancel := e.app.Bus().Subscribe(service.TopicKillSwitch)
	defer cancel()

	if !e.hotkey.Trigger(defaultHotkey) {
		t.Fatal("the kill switch hotkey is not registered")
	}

	if !e.app.KillSwitch().Active() {
		t.Error("the kill switch is not active after the hotkey")
	}
	if _, held := e.holder(); held {
		t.Error("the lease was not revoked")
	}
	eventually(t, "the MCP session to close", func() bool { return len(e.app.mcp.Sessions()) == 0 })
	select {
	case ev := <-events:
		if ev.Topic != service.TopicKillSwitch {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(waitLimit):
		t.Error("no killswitch event on the bus")
	}

	// 受付停止: リモートの全リクエストが 503 kill_switch_active（認証前に断る）。
	req, _ := http.NewRequest(http.MethodPost, "http://"+testTailnetIP+":8765/mcp", strings.NewReader("{}"))
	resp, err := (&http.Client{Transport: remoteTransport{e: e, token: token}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status after the kill switch = %d, want 503", resp.StatusCode)
	}
}

func TestKillSwitchStateSurvivesARestart(t *testing.T) {
	db := openDB(t)
	e := newEnvWithDB(t, db)
	if err := e.app.KillSwitch().Trigger("test"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	e2 := newEnvWithDB(t, db)
	if !e2.app.KillSwitch().Active() {
		t.Fatal("the kill switch must stay active across a restart (NFR-S07)")
	}
	req := httptest.NewRequest(http.MethodPost, "http://"+testTailnetIP+":8765/mcp", nil)
	req.RemoteAddr = "100.64.0.9:4000"
	e2.watcher.Set(runningStatus(testTailnetIP))
	e2.start()
	eventually(t, "the tailnet status", func() bool { return len(e2.app.ServerStatus(t.Context()).TailnetIPs) == 1 })
	rec := httptest.NewRecorder()
	e2.app.RemoteHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("remote status after a restart with the kill switch active = %d, want 503", rec.Code)
	}
}

// NFR-S01 / FR-A01: tailnet のアドレスだけで Listen し、loopback・0.0.0.0・tailnet 以外では決して Listen しない。
func TestRemoteRouterBindsOnlyTailnetAddresses(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP, "127.0.0.1", "0.0.0.0", "::1", "::", testTailnetIP6, "192.168.1.10"))
	e.waitListening(2)

	got := e.listens.Requested()
	slices.Sort(got)
	want := []string{"tcp4 100.64.0.1:8765", "tcp6 [fd7a:115c:a1e0::1]:8765"}
	if !slices.Equal(got, want) {
		t.Errorf("Listen requests = %v, want exactly %v", got, want)
	}
	for _, r := range got {
		for _, banned := range []string{"127.0.0.1", "0.0.0.0", "[::]", "[::1]", "192.168."} {
			if strings.Contains(r, banned) {
				t.Errorf("Listen was requested on a non-tailnet address: %s", r)
			}
		}
	}
}

func TestRemoteRouterDoesNotListenUntilTailscaledHasAnAddress(t *testing.T) {
	e := newEnv(t)
	e.start()

	e.watcher.Set(loggedOutStatus())
	eventually(t, "the logged-out status", func() bool {
		return e.app.ServerStatus(t.Context()).Tailscaled == service.TailscaledNeedsLogin && e.app.listen.State().Reason != ""
	})
	if got := e.listens.Requested(); len(got) != 0 {
		t.Fatalf("listened while logged out: %v", got)
	}
	if st := e.app.ServerStatus(t.Context()); st.Listening || st.Tailscaled != service.TailscaledNeedsLogin {
		t.Errorf("status = %+v, want not listening and needs_login", st)
	}

	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)
	if st := e.app.ServerStatus(t.Context()); !st.Listening || st.Tailscaled != service.TailscaledRunning {
		t.Errorf("status = %+v, want listening and running", st)
	}
}

// 設定画面のポートの変更は、保存すると新しいポートで待ち受け直す。Host ヘッダーの検査も新しいポートに追従する。
func TestChangingThePortRelistensOnTheNewPort(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)

	s, err := e.app.settings.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s.Port = 9100
	if _, err := e.app.settings.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the listener to move to the new port", func() bool {
		return slices.Equal(e.app.listen.State().Addrs, []string{"100.64.0.1:9100"})
	})
	if hosts, port := e.app.allowedHosts(); port != 9100 || !slices.Contains(hosts, testTailnetIP) {
		t.Errorf("allowedHosts = %v, %d; want the tailnet IP with the new port", hosts, port)
	}
	for _, r := range e.listens.Requested() {
		if strings.Contains(r, "127.0.0.1") || strings.Contains(r, "0.0.0.0") {
			t.Errorf("Listen was requested on %s", r)
		}
	}
	if st := e.app.ServerStatus(t.Context()); st.Port != 9100 {
		t.Errorf("status port = %d, want 9100", st.Port)
	}
}

func TestAllowedHostsFollowTailscaled(t *testing.T) {
	e := newEnv(t)
	if hosts, port := e.app.allowedHosts(); len(hosts) != 0 || port != testPort {
		t.Fatalf("before any status: %v, %d", hosts, port)
	}
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP, testTailnetIP6))
	eventually(t, "the hosts to be known", func() bool { h, _ := e.app.allowedHosts(); return len(h) == 3 })
	hosts, _ := e.app.allowedHosts()
	for _, want := range []string{testTailnetIP, testTailnetIP6, testDNSName} {
		if !slices.Contains(hosts, want) {
			t.Errorf("hosts %v lack %q", hosts, want)
		}
	}
}

// ホスト GUI は Wails の AssetOptions.Handler で配信する。ソケットを開かず、Listener が無くても到達できる。
func TestGUIHandlerIsReachableWithoutAnyListener(t *testing.T) {
	e := newEnv(t)
	h := e.app.GUIHandler()

	for _, path := range []string{"/", "/requests", "/clients", "/settings", "/static/js/app.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != middleware.CSPValue {
			t.Errorf("GET %s: CSP = %q", path, got)
		}
	}
	if got := e.listens.Requested(); len(got) != 0 {
		t.Errorf("serving the GUI opened sockets: %v", got)
	}
	// /mcp はリモートの経路で、GUI ルーターには存在しない。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /mcp on the GUI router = %d, want 404", rec.Code)
	}
}

// ホスト専用の操作（トークン発行・キルスイッチ解除・設定）はリモートルーターから到達できない。
func TestHostOnlyRoutesAreNotReachableRemotely(t *testing.T) {
	e := newEnv(t)
	_, token := e.registerClient()
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)

	client := &http.Client{Transport: remoteTransport{e: e, token: token}}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/"}, {http.MethodGet, "/settings"}, {http.MethodGet, "/events"}, {http.MethodGet, "/clients"},
		{http.MethodPost, "/killswitch/trigger"}, {http.MethodPost, "/killswitch/release"},
		{http.MethodPut, "/settings"}, {http.MethodPost, "/clients/client-1/tokens"},
	} {
		req, _ := http.NewRequest(tc.method, "http://"+testTailnetIP+":8765"+tc.path, nil)
		req.Header.Set(middleware.CSRFHeader, "1")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s over the tailnet = %d, want 404", tc.method, tc.path, resp.StatusCode)
		}
	}
	if e.app.KillSwitch().Active() {
		t.Error("a remote request toggled the kill switch")
	}
}

func TestUnregisteredDeviceGetsAConnectionRequest(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)
	events, cancel := e.app.Bus().Subscribe(service.TopicConnectionRequests)
	defer cancel()

	resp, err := (&http.Client{Transport: remoteTransport{e: e}}).Get("http://" + testTailnetIP + ":8765/mcp")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 device_not_registered", resp.StatusCode)
	}
	select {
	case <-events:
	case <-time.After(waitLimit):
		t.Fatal("no connection-requests event")
	}
	if n, err := e.app.PendingRequests(t.Context()); err != nil || n != 1 {
		t.Errorf("PendingRequests = %d, %v; want 1", n, err)
	}
}

// FR-S08: 設定画面のログオン時自動起動の切り替えが、OS の登録（Autostart）に届く。
func TestSettingsSaveTogglesAutostart(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	if !e.app.settings.AutostartAvailable() {
		t.Fatal("autostart must be available when the platform provides it")
	}
	s, err := e.app.settings.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Autostart {
		t.Fatal("autostart must be off by default (FR-S08)")
	}

	s.Autostart = true
	if _, err := e.app.settings.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	if on, _ := e.autostart.Enabled(ctx); !on {
		t.Error("enabling the setting did not register the autostart")
	}
	if got, _ := e.app.settings.Load(ctx); !got.Autostart {
		t.Error("Load does not reflect the registered autostart")
	}

	s.Autostart = false
	if _, err := e.app.settings.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	if on, _ := e.autostart.Enabled(ctx); on {
		t.Error("disabling the setting did not unregister the autostart")
	}
}

func TestWithoutAnAutostartBackendTheSettingIsUnavailable(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Platform.Autostart = nil })
	if e.app.settings.AutostartAvailable() {
		t.Error("autostart must be unavailable without a platform backend")
	}
}

func TestChangingTheHotkeySettingRebindsIt(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := t.Context()
	s, _ := e.app.settings.Load(ctx)
	s.KillSwitchHotkey = "Ctrl+Alt+Shift+L"
	if _, err := e.app.settings.Save(ctx, s); err != nil {
		t.Fatal(err)
	}

	if e.hotkey.Trigger(defaultHotkey) {
		t.Error("the old hotkey is still registered")
	}
	if !e.hotkey.Trigger("Ctrl+Alt+Shift+L") {
		t.Fatal("the new hotkey is not registered")
	}
	if !e.app.KillSwitch().Active() {
		t.Error("the rebound hotkey did not trigger the kill switch")
	}
}

func TestFailingToRebindTheHotkeyKeepsTheOldOneAndTellsTheHost(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := t.Context()
	// 別のアプリがすでに使っているホットキー。
	if _, err := e.hotkey.Register("Ctrl+Alt+Shift+J", func() {}); err != nil {
		t.Fatal(err)
	}
	toasts, cancel := e.app.Bus().Subscribe(service.TopicToast)
	defer cancel()

	s, _ := e.app.settings.Load(ctx)
	s.KillSwitchHotkey = "Ctrl+Alt+Shift+J"
	if _, err := e.app.settings.Save(ctx, s); err != nil {
		t.Fatal(err)
	}

	if got := e.app.hotkey.Spec(); got != defaultHotkey {
		t.Errorf("registered hotkey = %q, want the previous %q kept", got, defaultHotkey)
	}
	select {
	case ev := <-toasts:
		if toast, ok := ev.Data.(service.Toast); !ok || toast.Level != "error" || toast.Message == "" {
			t.Errorf("toast = %+v", ev.Data)
		}
	case <-time.After(waitLimit):
		t.Fatal("the host was not told that the hotkey could not be registered")
	}
	if !e.hotkey.Trigger(defaultHotkey) {
		t.Error("the previous hotkey stopped working")
	}
}

func TestAnUnavailableHotkeyDoesNotStopTheApp(t *testing.T) {
	e := newEnv(t)
	if _, err := e.hotkey.Register(defaultHotkey, func() {}); err != nil { // 使用済み
		t.Fatal(err)
	}
	toasts, cancel := e.app.Bus().Subscribe(service.TopicToast)
	defer cancel()
	e.start()

	if e.app.hotkey.Spec() != "" {
		t.Error("no hotkey should be registered")
	}
	select {
	case <-toasts:
	case <-time.After(waitLimit):
		t.Error("the host was not told about the missing hotkey")
	}
	// トレイ・ダッシュボードの経路は使える。
	if err := e.app.KillSwitch().Trigger("tray"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !e.app.KillSwitch().Active() {
		t.Error("the kill switch is not active")
	}
}

func TestChangingTheIdleTimeoutSettingAppliesToTheLeaseManager(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	s, _ := e.app.settings.Load(ctx)
	s.LeaseIdleTimeoutSec = 60
	if _, err := e.app.settings.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.leases.Acquire(ctx, domain.Actor{Kind: domain.ActorMCP, ClientID: "c1", Label: "c1", SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	h, held := e.holder()
	if !held || h.IdleRemaining > time.Minute || h.IdleRemaining < 30*time.Second {
		t.Errorf("IdleRemaining = %v (held %v), want about the new 60 s timeout", h.IdleRemaining, held)
	}
}

// 終了順序（docs/components/overview.md §9）: 入力・リース・セッションを解放し、監査を書き切ってから資源を閉じる。
func TestShutdownReleasesEverythingAndClosesResourcesInOrder(t *testing.T) {
	e := newEnv(t)
	_, token := e.registerClient()
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)
	cs := e.dialMCP(token)
	if res := callTool(t, cs, "acquire_control"); res.IsError {
		t.Fatalf("acquire_control failed: %+v", res.Content)
	}

	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := e.app.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if _, held := e.holder(); held {
		t.Error("the lease survived Shutdown")
	}
	if len(e.input.CallsTo("ReleaseAll")) == 0 {
		t.Error("held input was not released")
	}
	if e.hotkey.Trigger(defaultHotkey) {
		t.Error("the hotkey is still registered after Shutdown")
	}
	if got := e.closers.Names(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("closed resources = %v, want [a b] in order", got)
	}
	if err := e.app.audit.Flush(ctx); err == nil {
		t.Error("the audit queue must be closed after Shutdown")
	}
	if err := e.app.Shutdown(ctx); err != nil {
		t.Errorf("a second Shutdown must be a no-op, got %v", err)
	}
	eventually(t, "the MCP session to be gone", func() bool { return len(e.app.mcp.Sessions()) == 0 })
	if len(e.listens.Requested()) == 0 {
		t.Fatal("test setup: the listener never started")
	}
}

func TestShutdownBeforeStartStillClosesResources(t *testing.T) {
	e := newEnv(t)
	if err := e.app.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.closers.Names(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("closed resources = %v", got)
	}
	if err := e.app.Start(t.Context()); err == nil {
		t.Error("Start after Shutdown must fail")
	}
}
