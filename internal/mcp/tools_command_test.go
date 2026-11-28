package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

type commandBody struct {
	OK              bool   `json:"ok"`
	Name            string `json:"name"`
	ExitCode        int    `json:"exit_code"`
	TimedOut        bool   `json:"timed_out"`
	DurationMS      int64  `json:"duration_ms"`
	Stdout          string `json:"stdout"`
	StdoutEncoding  string `json:"stdout_encoding"`
	StdoutBase64    string `json:"stdout_base64"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	Stderr          string `json:"stderr"`
	StderrEncoding  string `json:"stderr_encoding"`
	StderrTruncated bool   `json:"stderr_truncated"`
}

func TestRunCommandReturnsExitCodeAndOutput(t *testing.T) {
	e := newEnv(t)
	e.cmds.result = service.CommandRunResult{
		Name: "echo-version", ExitCode: 4, Stdout: []byte("1.2.3 日本語\n"), Stderr: []byte("warn"), StderrTruncated: true, Duration: 250 * time.Millisecond,
	}
	cs := e.connect("c1")
	res := callTool(t, cs, "run_command", map[string]any{"name": "echo-version", "args": map[string]any{"pid": 7, "host": "example.com"}})
	if res.IsError {
		t.Fatal(res.text())
	}
	var body commandBody
	res.decode(t, &body)
	want := commandBody{
		OK: true, Name: "echo-version", ExitCode: 4, DurationMS: 250, Stdout: "1.2.3 日本語\n", StdoutEncoding: "utf-8", Stderr: "warn", StderrEncoding: "utf-8", StderrTruncated: true,
	}
	if body != want {
		t.Fatalf("body = %+v, want %+v", body, want)
	}
	// 出力に実行ファイルのパスの入る余地が無い。base64 は binary のときだけ付く。
	if got := res.keys(t); !slices.Equal(got, []string{"duration_ms", "exit_code", "name", "ok", "stderr", "stderr_encoding", "stderr_truncated", "stdout", "stdout_encoding", "stdout_truncated", "timed_out"}) {
		t.Errorf("keys = %v", got)
	}
	// 引数は名前つきの値のまま CommandExec へ届く（数値は数値のまま）。
	if len(e.cmds.runs) != 1 || e.cmds.runs[0].Name != "echo-version" || e.cmds.runs[0].Args["host"] != "example.com" || e.cmds.runs[0].Args["pid"] != float64(7) {
		t.Errorf("runs = %+v", e.cmds.runs)
	}
	// 実行はリースを暗黙に取得する。
	if h, ok := e.leases.Holder(); !ok || h.Actor.ClientID != "c1" {
		t.Errorf("holder = %+v, %v", h, ok)
	}
}

func TestRunCommandTimedOutAndBinaryOutput(t *testing.T) {
	e := newEnv(t)
	cp932 := []byte{0x93, 0xfa, 0x96, 0x7b, 0x8c, 0xea}
	e.cmds.result = service.CommandRunResult{Name: "c", ExitCode: -1, TimedOut: true, Stdout: cp932, StdoutTruncated: true, Stderr: []byte{0xFF, 0xFE, 'o', 0, 'k', 0}}
	cs := e.connect("c1")
	res := callTool(t, cs, "run_command", map[string]any{"name": "c"})
	if res.IsError {
		t.Fatal(res.text())
	}
	var body commandBody
	res.decode(t, &body)
	raw, err := base64.StdEncoding.DecodeString(body.StdoutBase64)
	if !body.TimedOut || body.ExitCode != -1 || body.StdoutEncoding != "binary" || err != nil || string(raw) != string(cp932) || !body.StdoutTruncated {
		t.Errorf("binary / timeout body = %+v (%v)", body, err)
	}
	if body.Stderr != "ok" || body.StderrEncoding != "utf-16le" {
		t.Errorf("utf-16 stderr = %q (%s)", body.Stderr, body.StderrEncoding)
	}
}

func TestRunCommandFailuresAreToolErrors(t *testing.T) {
	for _, c := range []struct {
		err  error
		code domain.Code
	}{
		{domain.ErrCommandDisabled, domain.CodeCommandDisabled},
		{domain.ErrCommandNotFound, domain.CodeCommandNotFound},
		{domain.ErrCommandAllowlistInvalid, domain.CodeCommandAllowlistInvalid},
		{domain.ErrCommandStartFailed, domain.CodeCommandStartFailed},
		{domain.ErrInvalidArgument.WithMsg("argument \"pid\": must be an integer"), domain.CodeInvalidArgument},
		{domain.ErrKillSwitchActive, domain.CodeKillSwitchActive},
	} {
		e := newEnv(t)
		e.cmds.err = c.err
		res := callTool(t, e.connect("c1"), "run_command", map[string]any{"name": "x"})
		if !res.IsError || res.errCode(t) != c.code {
			t.Errorf("%v: IsError = %v, code = %s (%s)", c.err, res.IsError, res.errCode(t), res.text())
		}
	}
}

func TestRunCommandOtherHolderIsRejected(t *testing.T) {
	e := newEnv(t)
	owner := e.connect("owner")
	if res := callTool(t, owner, "acquire_control", nil); res.IsError {
		t.Fatal(res.text())
	}
	res := callTool(t, e.connect("other"), "run_command", map[string]any{"name": "echo-version"})
	if !res.IsError || res.errCode(t) != domain.CodeLeaseHeldByOther {
		t.Fatalf("run_command while another client holds the lease: %v (%s)", res.IsError, res.text())
	}
	if len(e.cmds.runs) != 0 {
		t.Error("the command ran without the lease")
	}
}

// 公開されるスキーマは name と args だけ（追加のプロパティは許さない）。コマンドライン・シェル・実行ファイルを渡す入力は無い。
func TestRunCommandInputSchemaHasOnlyNameAndArgs(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range list.Tools {
		if tool.Name != "run_command" {
			continue
		}
		found = true
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
			t.Errorf("additionalProperties = %v, want false (%s)", schema.AdditionalProperties, raw)
		}
		keys := make([]string, 0, 2)
		for k := range schema.Properties {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"args", "name"}) || !slices.Equal(schema.Required, []string{"name"}) {
			t.Errorf("run_command schema = %s", raw)
		}
	}
	if !found {
		t.Fatal("run_command is not registered")
	}
}

// 余計なプロパティ（command・cmd・shell など）は SDK が拒否し、本体には届かない。試みは監査に残る（値は残さない）。
func TestRunCommandRejectsRawCommandLineInputs(t *testing.T) {
	a := &auditLog{}
	e := newEnv(t, func(c *Config) { c.Audit = a })
	cs := e.connect("c1")
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	for _, extra := range []string{"command", "command_line", "cmd", "shell", "executable", "argv", "path"} {
		res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "run_command", Arguments: map[string]any{"name": "echo-version", extra: "calc.exe & whoami"}})
		if err != nil || !res.IsError {
			t.Fatalf("%s: CallTool = %+v, %v, want a rejected call", extra, res, err)
		}
	}
	if len(e.cmds.runs) != 0 {
		t.Fatalf("a rejected call reached the command executor: %+v", e.cmds.runs)
	}
	got := a.all()
	if len(got) != 7 {
		t.Fatalf("%d audit entries, want 7 (one per rejected attempt)", len(got))
	}
	for _, en := range got {
		if en.Event != "tool.run_command" || en.Result != "denied" || en.ErrorCode != string(domain.CodeInvalidArgument) || len(en.DetailJSON) != 0 {
			t.Errorf("entry = %+v (%s)", en, en.DetailJSON)
		}
	}
	// name の型違い・欠落も拒否。
	for _, args := range []map[string]any{{}, {"name": 5}, {"name": "x", "args": "pid=1"}, {"name": "x", "args": []any{"a"}}} {
		if res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "run_command", Arguments: args}); err != nil || !res.IsError {
			t.Errorf("%v: CallTool = %+v, %v, want a rejected call", args, res, err)
		}
	}
}

func TestListCommands(t *testing.T) {
	e := newEnv(t)
	lo, hi := int64(1), int64(65535)
	e.cmds.infos = []service.CommandInfo{
		{Name: "port-check", Description: "connect", Timeout: 20 * time.Second, MaxOutputBytes: 1000, Params: []domain.CommandParam{
			{Name: "host", Type: domain.CommandParamString, Pattern: `[a-z.]+`, MaxLength: 64},
			{Name: "port", Type: domain.CommandParamInt, Min: lo, Max: hi},
			{Name: "mode", Type: domain.CommandParamEnum, Values: []string{"a", "b"}},
			{Name: "file", Type: domain.CommandParamInboxPath, Extensions: []string{".msi"}},
		}},
		{Name: "version", Timeout: 30 * time.Second, MaxOutputBytes: 64 << 10},
	}
	cs := e.connect("c1")
	res := callTool(t, cs, "list_commands", nil)
	if res.IsError {
		t.Fatal(res.text())
	}
	var out struct {
		Commands []struct {
			Name           string `json:"name"`
			Description    string `json:"description"`
			TimeoutSec     int    `json:"timeout_sec"`
			MaxOutputBytes int    `json:"max_output_bytes"`
			Params         []struct {
				Name       string   `json:"name"`
				Type       string   `json:"type"`
				Min        *int64   `json:"min"`
				Max        *int64   `json:"max"`
				Values     []string `json:"values"`
				Pattern    string   `json:"pattern"`
				MaxLength  int      `json:"max_length"`
				Extensions []string `json:"extensions"`
			} `json:"params"`
		} `json:"commands"`
	}
	res.decode(t, &out)
	if len(out.Commands) != 2 || out.Commands[0].Name != "port-check" || out.Commands[0].TimeoutSec != 20 || out.Commands[0].MaxOutputBytes != 1000 || len(out.Commands[0].Params) != 4 {
		t.Fatalf("commands = %+v", out)
	}
	p := out.Commands[0].Params
	if p[0].Type != "string" || p[0].Pattern != `[a-z.]+` || p[0].MaxLength != 64 || p[1].Type != "int" || *p[1].Min != 1 || *p[1].Max != 65535 ||
		!slices.Equal(p[2].Values, []string{"a", "b"}) || !slices.Equal(p[3].Extensions, []string{".msi"}) {
		t.Errorf("params = %+v", p)
	}
	// 固定の項目は params が空の配列（null ではない）。実行ファイルのパス・固定の引数は型に無い。
	raw := res.StructuredContent.(map[string]any)["commands"].([]any)
	if params, ok := raw[1].(map[string]any)["params"].([]any); !ok || len(params) != 0 {
		t.Errorf("params of a fixed command = %#v, want []", raw[1].(map[string]any)["params"])
	}
	for _, c := range raw {
		for k := range c.(map[string]any) {
			if strings.Contains(k, "exec") || strings.Contains(k, "path") || k == "args" {
				t.Errorf("a command exposes %q", k)
			}
		}
	}
	// リースは要らない。
	if _, ok := e.leases.Holder(); ok {
		t.Error("list_commands acquired the lease")
	}
	// 空でも [] 。
	e.cmds.infos = nil
	res = callTool(t, cs, "list_commands", nil)
	if cmds, ok := res.StructuredContent.(map[string]any)["commands"].([]any); !ok || len(cmds) != 0 {
		t.Errorf("empty list = %#v", res.StructuredContent)
	}
	e.cmds.err = domain.ErrCommandDisabled
	if res := callTool(t, cs, "list_commands", nil); !res.IsError || res.errCode(t) != domain.CodeCommandDisabled {
		t.Errorf("list when off: %v %s", res.IsError, res.text())
	}
}

func TestRunCommandHasALongerToolTimeout(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := e.srv.toolContext(context.Background(), "run_command")
	defer cancel()
	d, ok := ctx.Deadline()
	if !ok || time.Until(d) < DefaultLeaseToolTimeout+domain.MaxCommandTimeout-5*time.Second {
		t.Errorf("run_command deadline = %v, want about %v", time.Until(d), DefaultLeaseToolTimeout+domain.MaxCommandTimeout)
	}
}
