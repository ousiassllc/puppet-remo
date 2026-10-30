package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルの固定値には、出力に漏れたら見つけられるよう、パス・引数らしい文字列を使う（実在しない）。
const leakMark = `C:\Secret\sentinel-app.exe`

var sampleApps = []domain.AppSummary{{ID: "01JA0000000000000000000001", Label: "メモ帳"}, {ID: "01JB0000000000000000000002", Label: "電卓"}}

// keys は structuredContent のトップレベルのキーを返す。
func (r result) keys(t *testing.T) []string {
	t.Helper()
	m, ok := r.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structuredContent = %#v, want an object", r.StructuredContent)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func TestListLaunchableApps(t *testing.T) {
	e := newEnv(t)
	e.ctl.apps = sampleApps
	cs := e.connect("c1")

	res := callTool(t, cs, "list_launchable_apps", nil)
	if res.IsError {
		t.Fatal(res.text())
	}
	if got := res.keys(t); !slices.Equal(got, []string{"apps"}) {
		t.Errorf("keys = %v, want only apps", got)
	}
	apps, _ := res.StructuredContent.(map[string]any)["apps"].([]any)
	if len(apps) != 2 {
		t.Fatalf("apps = %v", apps)
	}
	for i, a := range apps {
		m, _ := a.(map[string]any)
		if len(m) != 2 || m["id"] != sampleApps[i].ID || m["label"] != sampleApps[i].Label {
			t.Errorf("apps[%d] = %v, want only id and label", i, m)
		}
	}
	// 本文のテキストも同じ内容（パス・引数の入る余地が無い）。
	var viaText struct {
		Apps []appBody `json:"apps"`
	}
	if err := json.Unmarshal([]byte(res.text()), &viaText); err != nil || len(viaText.Apps) != 2 {
		t.Errorf("text = %q (%v)", res.text(), err)
	}

	// リース不要: Do を通らず、リースを取らない。
	if e.ctl.callCount() != 0 {
		t.Error("list_launchable_apps must not go through Control.Do")
	}
	if _, held := e.leases.Holder(); held {
		t.Error("list_launchable_apps acquired the lease")
	}
}

func TestListLaunchableAppsEmptyIsAnEmptyArray(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	res := callTool(t, cs, "list_launchable_apps", nil)
	if res.IsError {
		t.Fatal(res.text())
	}
	if got := res.text(); got != `{"apps":[]}` {
		t.Errorf("text = %s, want {\"apps\":[]}", got)
	}
}

func TestListLaunchableAppsWhileAnotherSessionHoldsTheLease(t *testing.T) {
	e := newEnv(t)
	e.ctl.apps = sampleApps
	holder := e.connect("c1")
	if res := callTool(t, holder, "acquire_control", nil); res.IsError {
		t.Fatal(res.text())
	}
	res := callTool(t, e.connect("c2"), "list_launchable_apps", nil)
	if res.IsError {
		t.Fatalf("listing needs no lease: %s", res.text())
	}
}

func TestListLaunchableAppsKillSwitch(t *testing.T) {
	e := newEnv(t)
	e.ctl.apps = sampleApps
	cs := e.connect("c1")
	e.kill.active.Store(true)
	if code := callTool(t, cs, "list_launchable_apps", nil).errCode(t); code != domain.CodeKillSwitchActive {
		t.Errorf("code = %s", code)
	}
}

func TestListLaunchableAppsAuditsTheCount(t *testing.T) {
	a := &auditLog{}
	e := newEnv(t, func(c *Config) { c.Audit = a })
	e.ctl.apps = sampleApps
	cs := e.connect("c1")

	callTool(t, cs, "list_launchable_apps", nil)
	got := a.all()
	if len(got) != 1 {
		t.Fatalf("%d audit entries, want 1", len(got))
	}
	en := got[0]
	if en.Kind != "tool" || en.Event != "tool.list_launchable_apps" || en.Tool != "list_launchable_apps" || en.Result != "ok" {
		t.Errorf("entry = %+v", en)
	}
	if string(en.DetailJSON) != `{"count":2}` {
		t.Errorf("detail = %s, want {\"count\":2}", en.DetailJSON)
	}

	// 失敗（キルスイッチ）は件数なしで、denied として記録される。
	e.kill.active.Store(true)
	callTool(t, cs, "list_launchable_apps", nil)
	got = a.all()
	if len(got) != 2 || got[1].Result != "denied" || got[1].ErrorCode != "kill_switch_active" || len(got[1].DetailJSON) != 0 {
		t.Errorf("entries = %+v", got)
	}
}

func TestLaunchApp(t *testing.T) {
	e := newEnv(t)
	e.ctl.apps = sampleApps
	e.ctl.launchPID = 4120
	cs := e.connect("c1")

	res := callTool(t, cs, "launch_app", map[string]any{"app_id": sampleApps[0].ID})
	if res.IsError {
		t.Fatal(res.text())
	}
	var got launchAppBody
	res.decode(t, &got)
	if want := (launchAppBody{OK: true, AppID: sampleApps[0].ID, PID: 4120}); got != want {
		t.Errorf("launch_app = %+v, want %+v", got, want)
	}
	if keys := res.keys(t); !slices.Equal(keys, []string{"app_id", "ok", "pid"}) {
		t.Errorf("keys = %v, want ok, app_id and pid", keys)
	}
	if ids := e.ctl.launchRequests(); !slices.Equal(ids, []string{sampleApps[0].ID}) {
		t.Errorf("Control.Launch got %v", ids)
	}
	// 操作主体は MCP セッション。リースは未保持なら暗黙に取得される（FR-L04）。
	if h, held := e.leases.Holder(); !held || h.Actor.ClientID != "c1" {
		t.Errorf("holder = %+v, %v; want c1 (implicit acquire)", h, held)
	}
	if e.ctl.callCount() != 0 {
		t.Error("launch_app must not go through Control.Do")
	}
}

func TestLaunchAppErrors(t *testing.T) {
	t.Run("unknown app", func(t *testing.T) {
		e := newEnv(t)
		e.ctl.apps = sampleApps
		res := callTool(t, e.connect("c1"), "launch_app", map[string]any{"app_id": "nope"})
		if code := res.errCode(t); code != domain.CodeAppNotFound {
			t.Errorf("code = %s, want app_not_found", code)
		}
	})
	t.Run("kill switch", func(t *testing.T) {
		e := newEnv(t)
		e.ctl.apps = sampleApps
		cs := e.connect("c1")
		e.kill.active.Store(true)
		res := callTool(t, cs, "launch_app", map[string]any{"app_id": sampleApps[0].ID})
		if code := res.errCode(t); code != domain.CodeKillSwitchActive {
			t.Errorf("code = %s, want kill_switch_active", code)
		}
	})
	t.Run("lease held by another session", func(t *testing.T) {
		e := newEnv(t)
		e.ctl.apps = sampleApps
		holder := e.connect("c1")
		if res := callTool(t, holder, "acquire_control", nil); res.IsError {
			t.Fatal(res.text())
		}
		res := callTool(t, e.connect("c2"), "launch_app", map[string]any{"app_id": sampleApps[0].ID})
		if code := res.errCode(t); code != domain.CodeLeaseHeldByOther {
			t.Errorf("code = %s, want lease_held_by_other", code)
		}
		var body errorBody
		res.decode(t, &body)
		if body.Holder == nil || body.Holder.Kind != "mcp" {
			t.Errorf("holder = %+v", body.Holder)
		}
	})
}

func TestLaunchAppFailureCarriesTheReasonOnly(t *testing.T) {
	for _, reason := range []domain.LaunchFailure{
		domain.LaunchFileMissing, domain.LaunchAccessDenied, domain.LaunchElevationRequired, domain.LaunchOther,
	} {
		t.Run(string(reason), func(t *testing.T) {
			e := newEnv(t)
			e.ctl.apps = sampleApps
			e.ctl.launchErr = domain.NewLaunchFailed(reason)
			res := callTool(t, e.connect("c1"), "launch_app", map[string]any{"app_id": sampleApps[0].ID})
			if code := res.errCode(t); code != domain.CodeLaunchFailed {
				t.Fatalf("code = %s, want launch_failed", code)
			}
			var body map[string]any
			res.decode(t, &body)
			if body["reason"] != string(reason) {
				t.Errorf("reason = %v, want %s", body["reason"], reason)
			}
			if _, has := body["detail"]; has {
				t.Errorf("structuredContent has a detail: %v", body)
			}
		})
	}
}

// 万一 platform・service が reason に任意の文字列（パスなど）を入れても、API の 4 値以外は other にして、応答に出さない。
func TestLaunchFailedNeverEchoesAnUnexpectedReason(t *testing.T) {
	e := newEnv(t)
	e.ctl.apps = sampleApps
	e.ctl.launchErr = domain.ErrLaunchFailed.WithDetail("reason", leakMark).WithDetail("path", leakMark)
	res := callTool(t, e.connect("c1"), "launch_app", map[string]any{"app_id": sampleApps[0].ID})
	if res.errCode(t) != domain.CodeLaunchFailed {
		t.Fatalf("result = %s", res.text())
	}
	raw, _ := json.Marshal(res.CallToolResult)
	if strings.Contains(string(raw), "sentinel-app") {
		t.Errorf("the response leaks the injected value: %s", raw)
	}
	var body map[string]any
	res.decode(t, &body)
	if body["reason"] != "other" {
		t.Errorf("reason = %v, want other", body["reason"])
	}
}

// launch_app が受け付けるのは app_id だけ（FR-M19）。追加の引数・型違い・欠落はスキーマ違反（invalid params）で、起動しない。
func TestLaunchAppRejectsSchemaViolations(t *testing.T) {
	cases := map[string]map[string]any{
		"extra args":        {"app_id": sampleApps[0].ID, "args": []string{"--evil"}},
		"extra path":        {"app_id": sampleApps[0].ID, "path": leakMark},
		"extra cwd":         {"app_id": sampleApps[0].ID, "cwd": `C:\`},
		"path only":         {"path": leakMark},
		"missing app_id":    {},
		"app_id not string": {"app_id": 7},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.ctl.apps = sampleApps
			cs := e.connect("c1")
			ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
			defer cancel()
			res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "launch_app", Arguments: args})
			if err == nil && (res == nil || !res.IsError) {
				t.Fatalf("the call was accepted: %+v", res)
			}
			if err != nil && strings.Contains(err.Error(), "sentinel-app") {
				t.Errorf("the error echoes the path: %v", err)
			}
			if ids := e.ctl.launchRequests(); len(ids) != 0 {
				t.Errorf("Control.Launch was reached with %v", ids)
			}
			if _, held := e.leases.Holder(); held {
				t.Error("a rejected call acquired the lease")
			}
		})
	}
}

// 公開されるスキーマも app_id だけ（追加のプロパティは許さない）。
func TestLaunchAppInputSchemaHasOnlyAppID(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range list.Tools {
		if tool.Name != "launch_app" && tool.Name != "list_launchable_apps" {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Properties           map[string]json.RawMessage `json:"properties"`
			Required             []string                   `json:"required"`
			AdditionalProperties *json.RawMessage           `json:"additionalProperties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.AdditionalProperties == nil || string(*schema.AdditionalProperties) != "false" {
			t.Errorf("%s: additionalProperties = %v, want false (%s)", tool.Name, schema.AdditionalProperties, raw)
		}
		switch tool.Name {
		case "launch_app":
			if len(schema.Properties) != 1 || schema.Properties["app_id"] == nil || !slices.Equal(schema.Required, []string{"app_id"}) {
				t.Errorf("launch_app schema = %s", raw)
			}
		case "list_launchable_apps":
			if len(schema.Properties) != 0 {
				t.Errorf("list_launchable_apps takes no input: %s", raw)
			}
		}
	}
}
