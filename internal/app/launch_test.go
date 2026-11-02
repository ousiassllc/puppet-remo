package app

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/web/middleware"
)

const (
	launchExe  = `C:\Tools\notepad.exe`
	launchArg  = "--sentinel-arg"
	launchWork = `C:\Work\sentinel-dir`
)

// 組み立てた App で、MCP から許可リストのアプリを一覧・起動できる（AppRegistry と Control と mcp の結線）。
func TestMCPCanListAndLaunchAnAllowedApp(t *testing.T) {
	e := newEnv(t)
	_, token := e.registerClient()
	e.launcher.AddExecutable(launchExe)
	e.launcher.AddDir(launchWork)
	// 起動したプロセスの前面ウィンドウが最初の観測で見つかるようにして、ウィンドウ待ちの実時間待ちを避ける。
	e.launcher.OnLaunch(func(pid uint32) {
		e.windows.SetWindows(domain.WindowInfo{ID: "88", Title: "無題 - メモ帳", Process: "notepad.exe", PID: pid, Foreground: true})
	})
	app, err := e.app.apps.Create(context.Background(), domain.AppInput{
		Label: "メモ帳", ExePath: launchExe, Args: []string{launchArg}, WorkingDir: launchWork,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)
	cs := e.dialMCP(token)

	list := callTool(t, cs, "list_launchable_apps")
	if list.IsError {
		t.Fatalf("list_launchable_apps: %+v", list.Content)
	}
	raw, _ := json.Marshal(list.StructuredContent)
	if want := `{"apps":[{"id":"` + app.ID + `","label":"メモ帳"}]}`; string(raw) != want {
		t.Errorf("list_launchable_apps = %s, want %s", raw, want)
	}
	for _, secret := range []string{launchExe, launchArg, launchWork} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the listing leaks %q", secret)
		}
	}
	if _, held := e.holder(); held {
		t.Error("listing acquired the lease")
	}

	res := callTool(t, cs, "launch_app", map[string]any{"app_id": app.ID})
	if res.IsError {
		t.Fatalf("launch_app: %+v", res.Content)
	}
	var got struct {
		OK    bool   `json:"ok"`
		AppID string `json:"app_id"`
		PID   uint32 `json:"pid"`
	}
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &got); err != nil || !got.OK || got.AppID != app.ID || got.PID == 0 {
		t.Errorf("launch_app = %s (%v)", b, err)
	}
	calls := e.launcher.CallsTo("Launch")
	if len(calls) != 1 {
		t.Fatalf("Launch calls = %d, want 1", len(calls))
	}
	spec, _ := calls[0].Args[0].(platform.LaunchSpec)
	if spec.ExePath != launchExe || !slices.Equal(spec.Args, []string{launchArg}) || spec.WorkingDir != launchWork {
		t.Errorf("spec = %+v, want the registered values", spec)
	}
	if _, held := e.holder(); !held {
		t.Error("launch_app did not acquire the lease implicitly (FR-L04)")
	}

	// 許可リストに無い ID は起動しない。応答にパス・引数は出ない。
	bad := callTool(t, cs, "launch_app", map[string]any{"app_id": "01JZ0000000000000000000000"})
	if !bad.IsError {
		t.Fatal("an unknown app_id was launched")
	}
	b, _ = json.Marshal(bad.StructuredContent)
	if !strings.Contains(string(b), `"app_not_found"`) || strings.Contains(string(b), launchExe) {
		t.Errorf("error = %s", b)
	}
	if n := len(e.launcher.CallsTo("Launch")); n != 1 {
		t.Errorf("Launch calls = %d after the rejected call, want still 1", n)
	}
}

// 許可リストを書く経路（AppRegistry）は GUI 側だけにある。リモートルーターには、同名の経路も、AppRegistry も届いていない（FR-H12、NFR-S20）。
func TestRemoteRouterHasNoAllowListRoutes(t *testing.T) {
	e := newEnv(t)
	_, token := e.registerClient()
	e.launcher.AddExecutable(launchExe)
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)

	client := &http.Client{Transport: remoteTransport{e: e, token: token}}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/apps"}, {http.MethodGet, "/apps/new"}, {http.MethodGet, "/apps/pick"},
		{http.MethodPost, "/apps"}, {http.MethodPost, "/apps/pick"},
		{http.MethodPut, "/apps/app-1"}, {http.MethodDelete, "/apps/app-1"},
		{http.MethodGet, "/api/v1/apps"}, {http.MethodPost, "/api/v1/apps"}, {http.MethodGet, "/settings/apps"},
	} {
		req, _ := http.NewRequest(tc.method, "http://"+testTailnetIP+":8765"+tc.path, strings.NewReader(`{"label":"x","exe_path":"`+`C:\\Tools\\notepad.exe`+`"}`))
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
	if apps, err := e.app.apps.List(context.Background()); err != nil || len(apps) != 0 {
		t.Errorf("a remote request changed the allow list: %+v, %v", apps, err)
	}
}
