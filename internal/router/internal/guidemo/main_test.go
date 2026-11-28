package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ousiassllc/puppet-remo/internal/router"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui"
	"github.com/ousiassllc/puppet-remo/static"
)

// デモのデータで、タスク・承認・ダッシュボードの画面が描画できる（スクリーンショットの元になるので、画面ごとの要点を確かめる）。
func TestSeedRendersTheM4Screens(t *testing.T) {
	get := func(t *testing.T, lease, approval, path string) (int, string) {
		t.Helper()
		f := seed(lease, approval)
		d := f.Deps()
		d.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
		h, err := gui.New(d)
		if err != nil {
			t.Fatal(err)
		}
		dist := fstest.MapFS{"css/app.css": {Data: []byte("body{}")}, "js/app.js": {Data: []byte("")}}
		r, err := router.NewGUI(router.GUIConfig{Handler: h, Static: static.NewHandler(dist, true)})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}
	tests := []struct {
		name, lease, approval, path string
		want                        []string
	}{
		{"dashboard with the agent task", leaseHeldAgent, approvalNone, "/", []string{"data-running-task", "エージェント", "強制剥奪", "タスクが中断されます", "メモ帳を開いて"}},
		{"dashboard without a running task", leaseHeld, approvalNone, "/", []string{`id="running-tasks"`}},
		{"task list", leaseHeld, approvalNone, "/tasks", []string{"電卓を開いて", "失敗", "上限に到達", "承認が拒否された", "中断"}},
		{"task detail", leaseHeld, approvalNone, "/tasks/01JDEMOTASK00000000000FIN1", []string{"goal_maybe_done", "高速パス", "/audit/9101/thumbnail", "408", "モデル（OpenAI）の申告"}},
		{"failed detail", leaseHeld, approvalNone, "/tasks/01JDEMOTASK00000000000FAI2", []string{"judge_denied"}},
		{"safety check", leaseHeld, approvalSafetyCheck, "/approvals/ap-demo-sc", []string{"OpenAI の安全チェック", "malicious_instructions", "自動許可"}},
		{"judge action", leaseHeld, approvalJudgeAction, "/approvals/ap-demo-ja", []string{"エージェントのアクション", "regedit.exe"}},
		{"lease approval", leasePending, approvalNone, "/approvals/ap-demo-001", []string{"操作権の取得"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := get(t, tt.lease, tt.approval, tt.path)
			if code != http.StatusOK {
				t.Fatalf("status = %d\n%s", code, body)
			}
			for _, w := range tt.want {
				if !strings.Contains(body, w) {
					t.Errorf("body lacks %q", w)
				}
			}
		})
	}
	// 承認のデモを選んでいなければ、承認ダイアログは無い（410）。
	if code, _ := get(t, leaseHeld, approvalNone, "/approvals/ap-demo-sc"); code != http.StatusGone {
		t.Errorf("without -approval, status = %d, want 410", code)
	}
}

func TestRunRejectsUnknownFlagValues(t *testing.T) {
	if err := run("127.0.0.1:0", "nope", approvalNone, agentEmpty); err == nil {
		t.Error("an unknown -lease must be rejected")
	}
	if err := run("127.0.0.1:0", leaseHeld, "nope", agentEmpty); err == nil {
		t.Error("an unknown -approval must be rejected")
	}
	if err := run("127.0.0.1:0", leaseHeld, approvalNone, "nope"); err == nil {
		t.Error("an unknown -agent must be rejected")
	}
}

// -agent の各状態で /agent が描画でき、スクリーンショットの元になる要点（カードの状態・架空のメール）が出る。
func TestSeedRendersEveryAgentState(t *testing.T) {
	want := map[string][]string{
		agentEmpty:               {`data-chatgpt-state="signed_out"`, "Continue with ChatGPT"},
		agentRegistered:          {`data-connection="openai_api_key"`, `data-key-state="registered"`},
		agentChatGPTSignedOut:    {`data-chatgpt-state="signed_out"`},
		agentChatGPTSigningIn:    {`data-chatgpt-state="signing_in"`},
		agentChatGPTConnected:    {`data-chatgpt-state="connected"`, "demo-user@example.com", "ChatGPT のプランを使用中"},
		agentChatGPTUsageLimited: {`data-chatgpt-state="usage_limited"`, "使用量の上限に達しました", "API キーに切り替える…"},
		agentChatGPTPolicyDenied: {`data-chatgpt-problem="policy_denied"`, "Codex 経由で使う"},
		agentCodexMissing:        {`data-codex-engine="missing"`, `data-connection="chatgpt-codex"`},
		agentCodexInstalling:     {`data-codex-engine="installing"`, "42%"},
		agentCodexSignedOut:      {`data-codex-engine="installed"`, "Codex でログイン"},
		agentCodexConnected:      {`data-codex-state="connected"`, "demo-user@example.com", "plus"},
		agentCodexUsageLimited:   {`data-codex-state="usage_limited"`, "API キーに切り替える…"},
	}
	for _, state := range agentStates {
		t.Run(state, func(t *testing.T) {
			f := seed(leaseHeld, approvalNone)
			seedAgent(f, state)
			d := f.Deps()
			d.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
			h, err := gui.New(d)
			if err != nil {
				t.Fatal(err)
			}
			dist := fstest.MapFS{"css/app.css": {Data: []byte("body{}")}, "js/app.js": {Data: []byte("")}}
			r, err := router.NewGUI(router.GUIConfig{Handler: h, Static: static.NewHandler(dist, true)})
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/agent", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			for _, w := range want[state] {
				if !strings.Contains(rec.Body.String(), w) {
					t.Errorf("body lacks %q", w)
				}
			}
		})
	}
}
