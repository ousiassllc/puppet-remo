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
	// 前面のウィンドウを確かめられなかったときは window を付けず、reused_existing_window は常に付く。
	if keys := res.keys(t); !slices.Equal(keys, []string{"app_id", "ok", "pid", "reused_existing_window"}) {
		t.Errorf("keys = %v, want ok, app_id, pid and reused_existing_window", keys)
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

// 起動したアプリのウィンドウをそのまま返す（FR-M19。#85）。
func TestLaunchAppReportsTheLaunchedWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window *domain.WindowInfo
		reused bool
	}{
		{name: "handed over to a process that opened a new window", window: &domain.WindowInfo{ID: "99", Title: "無題 - メモ帳", Process: "notepad.exe", PID: 501}},
		{name: "new window of the launched pid", window: &domain.WindowInfo{ID: "88", Title: "新規", Process: "notepad.exe", PID: 4120}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.ctl.apps = sampleApps
			e.ctl.launchPID = 4120
			e.ctl.launchWindow, e.ctl.launchReused = tc.window, tc.reused

			res := callTool(t, e.connect("c1"), "launch_app", map[string]any{"app_id": sampleApps[0].ID})
			if res.IsError {
				t.Fatal(res.text())
			}
			var got launchAppBody
			res.decode(t, &got)
			w := tc.window
			if got.PID != 4120 || got.ReusedExistingWindow != tc.reused || got.Window == nil ||
				*got.Window != (launchWindowBody{ID: w.ID, Title: w.Title, PID: w.PID, Process: w.Process}) {
				t.Errorf("launch_app = %+v (window %+v), want window %+v and reused_existing_window=%v", got, got.Window, w, tc.reused)
			}
			if keys := res.keys(t); !slices.Equal(keys, []string{"app_id", "ok", "pid", "reused_existing_window", "window"}) {
				t.Errorf("keys = %v", keys)
			}
		})
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

// 起動の試行を監査に書けないと Control.Launch が audit_unavailable で起動を断る（fail-closed。NFR-S20）。ツールエラーは同じコードで返る。
func TestLaunchAppAuditUnavailableIsAToolError(t *testing.T) {
	e := newEnv(t)
	e.ctl.apps = sampleApps
	e.ctl.launchErr = domain.ErrAuditUnavailable
	res := callTool(t, e.connect("c1"), "launch_app", map[string]any{"app_id": sampleApps[0].ID})
	if code := res.errCode(t); code != domain.CodeAuditUnavailable {
		t.Fatalf("code = %s, want audit_unavailable", code)
	}
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

// 万一 platform・service が reason に任意の値（パスなど）を入れても、API の 4 値以外は other にして、応答に出さない。
// string 型の値と、LaunchFailure 型だが未知の値（launchReason の型アサーションを通り、Valid() で弾かれる）の両方を確かめる。
func TestLaunchFailedNeverEchoesAnUnexpectedReason(t *testing.T) {
	reasons := map[string]any{
		"untyped string":                  leakMark,
		"typed but unknown LaunchFailure": domain.LaunchFailure(leakMark),
		"typed empty LaunchFailure":       domain.LaunchFailure(""),
		"not a string":                    42,
	}
	for name, reason := range reasons {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.ctl.apps = sampleApps
			e.ctl.launchErr = domain.ErrLaunchFailed.WithDetail("reason", reason).WithDetail("path", leakMark)
			res := callTool(t, e.connect("c1"), "launch_app", map[string]any{"app_id": sampleApps[0].ID})
			if res.errCode(t) != domain.CodeLaunchFailed {
				t.Fatalf("result = %s", res.text())
			}
			raw, _ := json.Marshal(res.CallToolResult)
			if strings.Contains(string(raw), "sentinel-app") || strings.Contains(string(raw), `C:\\Secret`) {
				t.Errorf("the response leaks the injected value: %s", raw)
			}
			var body map[string]any
			res.decode(t, &body)
			if body["reason"] != "other" {
				t.Errorf("structuredContent.reason = %v, want other", body["reason"])
			}
		})
	}
}

// launch_app が受け付けるのは app_id だけ（FR-M19）。追加の引数・型違い・欠落はスキーマ違反で、起動しない。
// 応答は SDK のスキーマ検証のもの: CallToolResult{IsError: true}（プロトコルのエラーではない）で、本文は
// `validating "arguments"` で始まり、structuredContent（API 仕様 §2 のエラー本体）は無い。入力値は本文に出さない。
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
			if err != nil {
				t.Fatalf("CallTool = %v, want a CallToolResult with IsError", err)
			}
			if !res.IsError {
				t.Fatalf("the call was accepted: %+v", res)
			}
			if res.StructuredContent != nil {
				t.Errorf("StructuredContent = %v, want nil", res.StructuredContent)
			}
			text := result{res}.text()
			if !strings.HasPrefix(text, `validating "arguments"`) {
				t.Errorf("text = %q, want it to start with validating \"arguments\"", text)
			}
			if strings.Contains(text, "sentinel-app") {
				t.Errorf("the error echoes the path: %q", text)
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

// スキーマ違反の launch_app は Control に届かないので、mcp 層が tool.launch_app（denied・invalid_argument）を 1 件記録する（FR-AU06）。
// 引数（未検証の値）は記録に入れない。スキーマに合う呼び出しは Control.Launch が記録し、mcp 層は記録しない（二重にしない）。
func TestLaunchAppSchemaViolationIsAudited(t *testing.T) {
	a := &auditLog{}
	e := newEnv(t, func(c *Config) { c.Audit = a })
	e.ctl.apps = sampleApps
	cs := e.connect("c1")
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()

	if res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "launch_app", Arguments: map[string]any{"app_id": sampleApps[0].ID, "path": leakMark}}); err != nil || !res.IsError {
		t.Fatalf("CallTool = %+v, %v, want a rejected call", res, err)
	}
	got := a.all()
	if len(got) != 1 {
		t.Fatalf("%d audit entries, want 1: %+v", len(got), got)
	}
	en := got[0]
	if en.Kind != "tool" || en.Event != "tool.launch_app" || en.Tool != "launch_app" || en.Result != "denied" || en.ErrorCode != string(domain.CodeInvalidArgument) {
		t.Errorf("entry = %+v", en)
	}
	if en.ActorType != domain.ActorMCP || en.ClientID != "c1" || en.SessionID == "" || en.DurationMS == nil {
		t.Errorf("actor / duration of the entry = %+v", en)
	}
	if len(en.DetailJSON) != 0 || strings.Contains(string(en.DetailJSON), "sentinel") {
		t.Errorf("detail = %s, want none (the rejected values are not recorded)", en.DetailJSON)
	}

	// 他のツールのスキーマ違反はこの層では記録しない（FR-AU06 の対象は launch_app）。
	if _, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "mouse_move", Arguments: map[string]any{"x": "no"}}); err != nil {
		t.Fatal(err)
	}
	// スキーマに合う呼び出しは、この層では記録しない（e.ctl は Control の偽物で、記録しない）。
	callTool(t, cs, "launch_app", map[string]any{"app_id": sampleApps[0].ID})
	if n := len(a.all()); n != 1 {
		t.Errorf("%d audit entries after the other calls, want still 1", n)
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
