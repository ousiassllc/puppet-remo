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
	if want := `{"apps":[{"id":"` + app.ID + `","label":"メモ帳"}],"mode":"allowlist"}`; string(raw) != want {
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
	if got := errorCode(t, bad); got != domain.CodeAppNotFound || strings.Contains(string(b), launchExe) {
		t.Errorf("error code = %q, body = %s, want app_not_found without the path", got, b)
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
		// 起動の方式と拒否リスト（#88）も GUI だけ。
		{http.MethodPut, "/apps/mode"}, {http.MethodGet, "/apps/mode/confirm"}, {http.MethodGet, "/apps/denied"},
		{http.MethodPost, "/apps/denied"}, {http.MethodPut, "/apps/denied/den-1"}, {http.MethodDelete, "/apps/denied/den-1"},
		{http.MethodGet, "/apps/denied/den-1/edit"}, {http.MethodGet, "/apps/denied/den-1/delete/confirm"},
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
	if mode, err := e.app.apps.LaunchMode(context.Background()); err != nil || mode != domain.LaunchModeAllowlist {
		t.Errorf("a remote request changed the launch mode: %q, %v", mode, err)
	}
}

// 方式の切り替え（ホスト GUI の AppRegistry）→ MCP の launch_app(path) → 拒否リストに当たるものは起動しない、までを本物の結線（DB・Control・MCP）で通す。
// allowlist（既定）に戻すと path は拒否され、登録済みの app_id は今まで通り起動できる。
func TestMCPLaunchByPathFollowsTheLaunchMode(t *testing.T) {
	const (
		okExe     = `C:\Tools\ok.exe`
		blockedLn = `C:\Link\blocked.exe`
		blocked   = `C:\Tools\blocked.exe`
	)
	ctx := context.Background()
	e := newEnv(t)
	_, token := e.registerClient()
	for _, p := range []string{launchExe, okExe, blocked} {
		e.launcher.AddExecutable(p)
	}
	e.launcher.AddLink(blockedLn, blocked)
	e.launcher.OnLaunch(func(pid uint32) {
		e.windows.SetWindows(domain.WindowInfo{ID: "88", Title: "w", Process: "x.exe", PID: pid, Foreground: true})
	})
	reg, err := e.app.apps.Create(ctx, domain.AppInput{Label: "メモ帳", ExePath: launchExe})
	if err != nil {
		t.Fatal(err)
	}
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)
	cs := e.dialMCP(token)
	launches := func() int { return len(e.launcher.CallsTo("Launch")) }

	// 既定（allowlist）: path は拒否される（起動しない）。
	if res := callTool(t, cs, "launch_app", map[string]any{"path": okExe}); !res.IsError || launches() != 0 {
		t.Fatalf("path launch in allowlist mode: isError=%v launches=%d, want rejected", res.IsError, launches())
	}

	if err := e.app.apps.SetLaunchMode(ctx, domain.LaunchModeDenylist); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.apps.CreateDenied(ctx, blocked); err != nil {
		t.Fatal(err)
	}
	list := callTool(t, cs, "list_launchable_apps")
	raw, _ := json.Marshal(list.StructuredContent)
	if !strings.Contains(string(raw), `"mode":"denylist"`) || !strings.Contains(string(raw), reg.ID) || strings.Contains(string(raw), "blocked") {
		t.Errorf("list_launchable_apps = %s, want mode denylist, the registered app, and no denylist contents", raw)
	}

	if res := callTool(t, cs, "launch_app", map[string]any{"path": okExe}); res.IsError || launches() != 1 {
		t.Fatalf("unlisted path: isError=%v launches=%d (%+v), want launched", res.IsError, launches(), res.Content)
	}
	spec, _ := e.launcher.CallsTo("Launch")[0].Args[0].(platform.LaunchSpec)
	if spec.ExePath != okExe || len(spec.Args) != 0 || spec.WorkingDir != "" {
		t.Errorf("spec = %+v, want the path only (no args, no working dir)", spec)
	}
	for _, p := range []string{blocked, blockedLn, strings.ToUpper(blocked), `c:/tools//BLOCKED.exe`} {
		res := callTool(t, cs, "launch_app", map[string]any{"path": p})
		if !res.IsError || errorCode(t, res) != domain.CodeAppDenied || launches() != 1 {
			t.Errorf("denied path %q: isError=%v launches=%d content=%+v, want app_denied and no launch", p, res.IsError, launches(), res.StructuredContent)
		}
	}
	if res := callTool(t, cs, "launch_app", map[string]any{"path": okExe, "app_id": reg.ID}); !res.IsError || launches() != 1 {
		t.Errorf("app_id and path together: isError=%v launches=%d, want rejected", res.IsError, launches())
	}

	// allowlist に戻す: path は拒否、登録済みの app_id は起動できる。
	if err := e.app.apps.SetLaunchMode(ctx, domain.LaunchModeAllowlist); err != nil {
		t.Fatal(err)
	}
	if res := callTool(t, cs, "launch_app", map[string]any{"path": okExe}); !res.IsError || launches() != 1 {
		t.Errorf("path after switching back: isError=%v launches=%d, want rejected", res.IsError, launches())
	}
	if res := callTool(t, cs, "launch_app", map[string]any{"app_id": reg.ID}); res.IsError || launches() != 2 {
		t.Errorf("registered app after switching back: isError=%v launches=%d, want launched", res.IsError, launches())
	}
}
