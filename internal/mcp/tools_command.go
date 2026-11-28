package mcp

import (
	"context"
	"encoding/base64"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// コマンド実行のツール（#265。FR-M32。API 仕様 §3.3「コマンド実行」）。
// 実行できるのは、ホストの許可リスト（ホスト所有者が手で書いたファイル）の項目だけで、呼ぶ側が渡すのは項目の名前と、項目が定めた型つきの
// 引数だけ。コマンドライン文字列・シェル・実行ファイルのパスを渡す入力は無い。ON/OFF（既定 OFF）・許可リストを変える経路もこの層には無い
// （入出力・エラーにホストのパス・許可リストの中身は出ない）。検査（キルスイッチ・ON/OFF・リース・許可リスト・引数）と監査
// （tool.run_command。出力の中身は記録しない）はすべて service.CommandExec の仕事で、この層は出力の整形だけをする。
// CommandExec が自分で監査するので addTool で登録する（addAuditedTool では二重に記録される）。

type runCommandIn struct {
	Name string         `json:"name" jsonschema:"name of an allowlisted command, from list_commands"`
	Args map[string]any `json:"args,omitempty" jsonschema:"values for the parameters the command declares in list_commands (all required, no others): numbers for int, strings for the rest. There is no way to pass a command line, a shell string or an executable"`
}

// commandOutputFields は stdout・stderr 1 本分の出力。text は UTF-8 の文字列（encoding が binary のときは、不正なバイトを U+FFFD にしたもの）。
// base64 は encoding が binary のときだけ付く、元のバイト列（切り詰めたあと）。
type commandOutput struct {
	Text      string
	Encoding  string
	Base64    string
	Truncated bool
}

func newCommandOutput(b []byte, truncated bool) commandOutput {
	text, enc := domain.DecodeCommandOutput(b, truncated)
	out := commandOutput{Text: text, Encoding: enc, Truncated: truncated}
	if enc == "binary" {
		out.Base64 = base64.StdEncoding.EncodeToString(b)
	}
	return out
}

type runCommandBody struct {
	OK              bool   `json:"ok"`
	Name            string `json:"name"`
	ExitCode        int    `json:"exit_code"`
	TimedOut        bool   `json:"timed_out"`
	DurationMS      int64  `json:"duration_ms"`
	Stdout          string `json:"stdout"`
	StdoutEncoding  string `json:"stdout_encoding"`
	StdoutBase64    string `json:"stdout_base64,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	Stderr          string `json:"stderr"`
	StderrEncoding  string `json:"stderr_encoding"`
	StderrBase64    string `json:"stderr_base64,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated"`
}

type commandParamBody struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Min        *int64   `json:"min,omitempty"`
	Max        *int64   `json:"max,omitempty"`
	Values     []string `json:"values,omitempty"`
	Pattern    string   `json:"pattern,omitempty"`
	MaxLength  int      `json:"max_length,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
}

type commandInfoBody struct {
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	Params         []commandParamBody `json:"params"`
	TimeoutSec     int                `json:"timeout_sec"`
	MaxOutputBytes int                `json:"max_output_bytes"`
}

type listCommandsBody struct {
	Commands []commandInfoBody `json:"commands"`
}

const commandToolNote = " Needs the host owner to have turned command execution on (it is off by default; command_disabled otherwise). " +
	"Only commands the host owner put in the allowlist can run; nothing else, and the allowlist and its settings cannot be changed from here."

// addCommandTools はコマンド実行のツールを登録する: FR-M32。run_command だけがリースを要する（暗黙に取得する。leaseAcquiringTools）。
func (s *Server) addCommandTools() {
	addTool(s, "list_commands",
		"List the commands the host owner allowed run_command to run: name, description, the parameters each takes (type, range / values / pattern / extensions), "+
			"the timeout and the output cap. Executable paths and fixed arguments are never exposed. Does not need the control lease. "+
			"Fails with command_disabled, command_allowlist_invalid (the host's allowlist file is missing or broken), kill_switch_active."+commandToolNote,
		true, func(ctx context.Context, who domain.Actor, _ noArgs) (toolResult, error) {
			infos, err := s.commands.List(ctx, who)
			if err != nil {
				return toolResult{}, err
			}
			out := listCommandsBody{Commands: make([]commandInfoBody, 0, len(infos))}
			for _, info := range infos {
				body := commandInfoBody{
					Name: info.Name, Description: info.Description, Params: make([]commandParamBody, 0, len(info.Params)),
					TimeoutSec: int(info.Timeout.Seconds()), MaxOutputBytes: info.MaxOutputBytes,
				}
				for _, p := range info.Params {
					pb := commandParamBody{Name: p.Name, Type: string(p.Type), Values: p.Values, Pattern: p.Pattern, MaxLength: p.MaxLength, Extensions: p.Extensions}
					if p.Type == domain.CommandParamInt {
						pb.Min, pb.Max = &p.Min, &p.Max
					}
					body.Params = append(body.Params, pb)
				}
				out.Commands = append(out.Commands, body)
			}
			return toolResult{Structured: out}, nil
		})

	addTool(s, "run_command",
		"Run one allowlisted command on the host and return its exit code, stdout and stderr. Pass the command's name (from list_commands) and its named args; "+
			"it is started directly with those values as separate arguments (no shell, no pipes, no redirection), so a value can never add arguments or run something else; "+
			"values that do not match the declared type, range, list or pattern are rejected with invalid_argument. "+
			"A non-zero exit code is a normal result, not an error. After the command's timeout the process (and its children) is killed and timed_out is true (exit_code -1); "+
			"output beyond the command's cap is cut and stdout_truncated / stderr_truncated is true. "+
			"stdout and stderr are UTF-8 text (stdout_encoding / stderr_encoding utf-8; a UTF-16 output is converted, utf-16le); output in another encoding such as a legacy code page "+
			"comes back as encoding binary with lossy text plus the exact bytes in stdout_base64 / stderr_base64. "+
			"Some commands run a file you put with put_file (a parameter of type inbox_path); the command may show UI on the host. "+
			"Fails with command_disabled, command_not_found (unknown name or disabled entry), invalid_argument, command_allowlist_invalid, "+
			"command_start_failed, conflict (too many commands running), lease_held_by_other, kill_switch_active; a window-scoped lease cannot use it (input_blocked)."+
			commandToolNote+" Needs the control lease (acquired implicitly).",
		false, func(ctx context.Context, who domain.Actor, in runCommandIn) (toolResult, error) {
			res, err := s.commands.Run(ctx, who, service.CommandRunRequest{Name: in.Name, Args: in.Args})
			if err != nil {
				return toolResult{}, err
			}
			stdout, stderr := newCommandOutput(res.Stdout, res.StdoutTruncated), newCommandOutput(res.Stderr, res.StderrTruncated)
			return toolResult{Structured: runCommandBody{
				OK: true, Name: res.Name, ExitCode: res.ExitCode, TimedOut: res.TimedOut, DurationMS: res.Duration.Milliseconds(),
				Stdout: stdout.Text, StdoutEncoding: stdout.Encoding, StdoutBase64: stdout.Base64, StdoutTruncated: stdout.Truncated,
				Stderr: stderr.Text, StderrEncoding: stderr.Encoding, StderrBase64: stderr.Base64, StderrTruncated: stderr.Truncated,
			}}, nil
		})
}
