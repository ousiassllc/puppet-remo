package mcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// controlAudited は Control（Do・Launch）が自分で監査を記録するツール。mcp 層は記録しない（二重記録を避ける）。
// それ以外の全ツールは、mcp 層が 1 回だけ記録する（FR-AU01）。
var controlAudited = []string{
	"mouse_move", "left_click", "right_click", "middle_click", "double_click", "triple_click",
	"left_click_drag", "scroll", "key", "type", "wait", "focus_window", "clipboard_read", "clipboard_write", "get_ui_tree",
	"find_ui_element", "wait_for", "launch_app",
	// ファイル転送は service.FileTransfer が自分で監査する（#264。パス・大きさ・SHA-256 を持つので、mcp 層の引数の要約では足りない）。
	"put_file", "get_file", "list_files", "delete_file",
	// コマンド実行も service.CommandExec が自分で監査する（#265。項目名・引数の値・終了コード・出力の大きさを持つ）。
	"run_command", "list_commands",
}

// toolCallArgs は全ツールを成功させる引数。
var toolCallArgs = map[string]map[string]any{
	"mouse_move":      {"x": 1, "y": 2},
	"left_click":      {"x": 1, "y": 2},
	"right_click":     {"x": 1, "y": 2},
	"middle_click":    {"x": 1, "y": 2},
	"double_click":    {"x": 1, "y": 2},
	"triple_click":    {"x": 1, "y": 2},
	"left_click_drag": {"from": map[string]any{"x": 1, "y": 2}, "to": map[string]any{"x": 3, "y": 4}},
	"scroll":          {"x": 1, "y": 2, "direction": "down", "amount": 1},
	"key":             {"keys": "enter"},
	"type":            {"text": "hunter2"},
	"wait":            {"ms": 0},
	"focus_window":    {"id": "w1"},
	"clipboard_write": {"text": "hunter2"},
	"get_ui_tree":     {"window_id": "w1"},
	"find_ui_element": {"name": "hunter2", "window_id": "w1"},
	"wait_for":        {"window_id": "w1", "state": "appear", "timeout_ms": 1000},
	"screenshot":      {"monitor": 0, "format": "png"},
	"launch_app":      {"app_id": "app-1"},
	"put_file":        {"path": "a.txt", "content_base64": "aHVudGVyMg=="},
	"get_file":        {"path": "a.txt"},
	"list_files":      {},
	"delete_file":     {"path": "a.txt"},
	"run_command":     {"name": "echo-version"},
	"list_commands":   {},
	"run_task":        {"prompt": "hunter2"},
	"get_task":        {"task_id": "task-seed"},
	"cancel_task":     {"task_id": "task-seed"},
}

func TestMCPLayerAuditsExactlyTheToolsControlDoesNotAudit(t *testing.T) {
	for name := range wantTools {
		t.Run(name, func(t *testing.T) {
			a := &auditLog{}
			e := newEnv(t, func(c *Config) { c.Audit = a })
			e.ctl.tree = sampleTree()
			e.ctl.apps = []domain.AppSummary{{ID: "app-1", Label: "Notepad"}}
			e.agent.put("c1", domain.Task{ID: "task-seed", Status: domain.TaskRunning})
			e.files.files["a.txt"] = []byte("x")
			cs := e.connect("c1")

			res := callTool(t, cs, name, toolCallArgs[name])
			if res.IsError {
				t.Fatalf("%s failed: %s", name, res.text())
			}
			got := a.all()
			for _, en := range got {
				if strings.Contains(string(en.DetailJSON), "hunter2") {
					t.Errorf("a text body leaked into the audit detail: %s", en.DetailJSON)
				}
			}
			if slices.Contains(controlAudited, name) {
				if len(got) != 0 {
					t.Fatalf("%s is audited by Control.Do; the mcp layer recorded %d entries too", name, len(got))
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("%s: %d audit entries, want exactly 1", name, len(got))
			}
			en := got[0]
			if en.Kind != "tool" || en.Event != "tool."+name || en.Tool != name || en.Result != "ok" {
				t.Errorf("entry = %+v", en)
			}
			if en.ActorType != domain.ActorMCP || en.ClientID != "c1" || en.SessionID == "" {
				t.Errorf("actor of the entry = %+v", en)
			}
			if en.DurationMS == nil {
				t.Error("no duration")
			}
		})
	}
}

func TestMCPLayerAuditRecordsFailuresAndArgsSummary(t *testing.T) {
	a := &auditLog{}
	e := newEnv(t, func(c *Config) { c.Audit = a })
	e.ctl.err = domain.ErrDesktopUnavailable
	cs := e.connect("c1")

	callTool(t, cs, "screenshot", map[string]any{"monitor": 1, "format": "jpeg"})
	got := a.all()
	if len(got) != 1 {
		t.Fatalf("%d entries, want 1", len(got))
	}
	if got[0].Result != "error" || got[0].ErrorCode != string(domain.CodeDesktopUnavailable) {
		t.Errorf("entry = %+v", got[0])
	}
	var detail map[string]any
	if err := json.Unmarshal(got[0].DetailJSON, &detail); err != nil {
		t.Fatal(err)
	}
	if detail["monitor"] != float64(1) || detail["format"] != "jpeg" || len(detail) != 2 {
		t.Errorf("detail = %v", detail)
	}
}

// window_id つきの screenshot は、拒否されても監査に残り、引数の要約に window_id・include_owned が入る（タイトルなどウィンドウの中身は入らない）。
func TestMCPLayerAuditRecordsWindowScreenshot(t *testing.T) {
	a := &auditLog{}
	e := newEnv(t, func(c *Config) { c.Audit = a })
	e.ctl.err = domain.ErrWindowNotFound
	cs := e.connect("c1")

	callTool(t, cs, "screenshot", map[string]any{"window_id": "1001", "include_owned": true})
	got := a.all()
	if len(got) != 1 || got[0].Result != "error" || got[0].ErrorCode != string(domain.CodeWindowNotFound) {
		t.Fatalf("entries = %+v", got)
	}
	var detail map[string]any
	if err := json.Unmarshal(got[0].DetailJSON, &detail); err != nil {
		t.Fatal(err)
	}
	if detail["window_id"] != "1001" || detail["include_owned"] != true || len(detail) != 2 {
		t.Errorf("detail = %v", detail)
	}
}

func TestMCPLayerAuditRecordsKillSwitchDenial(t *testing.T) {
	a := &auditLog{}
	e := newEnv(t, func(c *Config) { c.Audit = a })
	cs := e.connect("c1")
	e.kill.active.Store(true)

	callTool(t, cs, "get_screen_info", nil)
	got := a.all()
	if len(got) != 1 || got[0].Result != "denied" || got[0].ErrorCode != string(domain.CodeKillSwitchActive) {
		t.Fatalf("entries = %+v", got)
	}
}
