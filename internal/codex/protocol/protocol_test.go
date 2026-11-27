package protocol_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/codex/protocol"
)

// forbidden は項目の名前（Go の名前と JSON の名前）に現れてはならない語（小文字）。
var forbidden = []string{"token", "secret", "password", "credential", "apikey", "api_key", "bearer", "accesskey"}

func walk(t *testing.T, ty reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()
	for ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Slice || ty.Kind() == reflect.Array || ty.Kind() == reflect.Map {
		ty = ty.Elem()
	}
	if ty.Kind() != reflect.Struct || seen[ty] {
		return
	}
	seen[ty] = true
	for i := 0; i < ty.NumField(); i++ {
		f := ty.Field(i)
		if f.Type.Kind() == reflect.Bool {
			continue // 真偽値は秘密を運べない（account/read の refreshToken は「更新を頼むか」の旗で、常に false）
		}
		names := strings.ToLower(f.Name + " " + f.Tag.Get("json"))
		for _, w := range forbidden {
			if strings.Contains(names, w) {
				t.Errorf("%s.%s names a credential (%q)", ty.Name(), f.Name, w)
			}
		}
		walk(t, f.Type, seen)
	}
}

func TestTypesHaveNoTokenField(t *testing.T) {
	seen := map[reflect.Type]bool{}
	for _, v := range protocol.Types {
		walk(t, reflect.TypeOf(v), seen)
	}
	if len(seen) < 20 {
		t.Fatalf("only %d types were checked", len(seen))
	}
}

func TestErrorInfoは文字列とオブジェクトの両方を読む(t *testing.T) {
	var a protocol.TurnError
	if err := json.Unmarshal([]byte(`{"message":"m","codexErrorInfo":"usageLimitExceeded"}`), &a); err != nil || a.CodexErrorInfo.Kind != protocol.ErrUsageLimitExceeded {
		t.Fatalf("string form: %+v %v", a, err)
	}
	var b protocol.TurnError
	if err := json.Unmarshal([]byte(`{"message":"m","codexErrorInfo":{"httpConnectionFailed":{"httpStatusCode":502}}}`), &b); err != nil ||
		b.CodexErrorInfo.Kind != "httpConnectionFailed" || b.CodexErrorInfo.HTTPStatus != 502 {
		t.Fatalf("object form: %+v %v", b, err)
	}
	var c protocol.TurnError
	if err := json.Unmarshal([]byte(`{"message":"m","codexErrorInfo":{"a":{},"b":{}}}`), &c); err != nil || c.CodexErrorInfo.Kind != protocol.ErrOther {
		t.Fatalf("ambiguous form: %+v %v", c, err)
	}
	var d protocol.TurnError
	if err := json.Unmarshal([]byte(`{"message":"m"}`), &d); err != nil || d.CodexErrorInfo.Kind != "" {
		t.Fatalf("absent: %+v %v", d, err)
	}
}

func TestRefusalは承認系を拒否しトークンの更新と未知はエラーにする(t *testing.T) {
	for _, m := range []string{protocol.ReqCommandApproval, protocol.ReqFileChangeApproval, protocol.ReqLegacyPatchApproval,
		protocol.ReqLegacyExecApproval, protocol.ReqPermissionsApproval, protocol.ReqToolUserInput, protocol.ReqMCPElicitation, protocol.ReqDynamicToolCall} {
		if _, ok := protocol.Refusal(m); !ok {
			t.Errorf("%s has no refusal", m)
		}
	}
	if a, _ := protocol.Refusal(protocol.ReqCommandApproval); a.(protocol.DecisionResponse).Decision != "decline" {
		t.Errorf("command approval = %+v", a)
	}
	if a, _ := protocol.Refusal(protocol.ReqLegacyExecApproval); a.(protocol.DecisionResponse).Decision != "abort" {
		t.Errorf("legacy approval = %+v", a)
	}
	for _, m := range []string{protocol.ReqAuthTokensRefresh, protocol.ReqAttestation, "something/new"} {
		if _, ok := protocol.Refusal(m); ok {
			t.Errorf("%s must be answered with an error", m)
		}
	}
}

func TestIsPlainItemは道具の使用を通さない(t *testing.T) {
	for _, ok := range []string{"userMessage", "agentMessage", "reasoning", "plan", "dynamicToolCall"} {
		if !protocol.IsPlainItem(ok) {
			t.Errorf("%s must be plain", ok)
		}
	}
	for _, bad := range []string{"commandExecution", "fileChange", "mcpToolCall", "webSearch", "imageView", "somethingNew", ""} {
		if protocol.IsPlainItem(bad) {
			t.Errorf("%s must not be plain", bad)
		}
	}
}

func TestToolSettingsは道具を無効にする設定を全部含む(t *testing.T) {
	joined := strings.Join(protocol.ToolSettings, "\n")
	for _, want := range []string{`web_search="disabled"`, "features.shell_tool=false", "features.unified_exec=false", "features.apply_patch_freeform=false",
		"features.view_image=false", "features.js_repl=false", "features.code_mode=false", "features.multi_agent=false", "features.collab=false",
		"features.apps=false", "features.plugins=false", "features.memories=false", "features.image_generation=false",
		"features.browser_use=false", "features.computer_use=false"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s", want)
		}
	}
}
