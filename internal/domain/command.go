package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// コマンド実行（#265。MCP の run_command / list_commands。FR-M32）の許可リストの規則。OS に依らない純粋な検査で、ホストの
// 実ファイルには触れない（許可リストのファイルを読む・置き場所の中のパスを実体にする・プロセスを起こすのは service・platform）。
//
// 許可リストはホストの JSON ファイル（ホスト所有者が手で書く。MCP からは変えられない）で、1 項目は
// 「名前・実行ファイルの絶対パス・引数のテンプレート・型つきの差し込み（params）・タイムアウト・出力の上限」。
// 呼ぶ側は項目の名前と params の値だけを渡し、コマンドライン文字列は受け付けない。シェルは使わない（argv で直接起動する）。

const (
	// CommandFileVersion は許可リストのファイルの形式の版。
	CommandFileVersion = 1
	// MaxCommandFileBytes は許可リストのファイルの大きさの上限。
	MaxCommandFileBytes = 256 << 10
	// MaxCommandEntries は許可リストの項目数の上限。
	MaxCommandEntries = 256

	// DefaultCommandTimeout・MaxCommandTimeout は項目の timeout_sec の既定（30 秒）と上限（10 分）。
	DefaultCommandTimeout = 30 * time.Second
	MaxCommandTimeout     = 10 * time.Minute
	// DefaultCommandOutputBytes・MaxCommandOutputBytes は項目の max_output_bytes（stdout・stderr それぞれ）の既定（64 KiB）と上限（512 KiB）。
	DefaultCommandOutputBytes = 64 << 10
	MaxCommandOutputBytes     = 512 << 10

	maxCommandArgs        = 64   // 1 項目の引数テンプレートの数
	maxCommandParams      = 16   // 1 項目の params の数
	maxCommandArgUnits    = 1024 // 引数テンプレート 1 つの長さ（UTF-16 のコードユニット）
	maxCommandDescription = 200  // 説明の文字数
	maxCommandPathUnits   = 260  // 実行ファイル・作業フォルダ・許可リストのパスの長さ（UTF-16 のコードユニット）
	// DefaultCommandStringMax・MaxCommandStringMax は string 型の差し込みの max_length の既定と上限（文字数）。
	DefaultCommandStringMax = 256
	MaxCommandStringMax     = 1024
	maxCommandEnumValues    = 64
	maxCommandEnumUnits     = 128
	maxCommandExtensions    = 16
	// MaxCommandIntDigits は int 型の差し込みを文字列で受け取るときの桁数の上限（int64 に収まる桁数）。
	maxCommandIntDigits = 18
)

// CommandParamType は差し込み（params）の型。
type CommandParamType string

const (
	// CommandParamInt は min〜max の整数。
	CommandParamInt CommandParamType = "int"
	// CommandParamEnum は values のどれか 1 つ（完全一致）。
	CommandParamEnum CommandParamType = "enum"
	// CommandParamString は pattern（全体に一致。必須）に合う、max_length 以下の文字列。
	CommandParamString CommandParamType = "string"
	// CommandParamInboxPath はファイル転送の置き場所の中の、実在する通常ファイルの相対パス（extensions のどれかの拡張子）。
	// 実行のときに service.FileTransfer.ResolveInRoot で実体の絶対パスにして渡す（リンクを経由しない・置き場所の外に出ない）。
	CommandParamInboxPath CommandParamType = "inbox_path"
)

// CommandParam は差し込み 1 つの定義。必ず指定が要る（省略・既定値は無い）。
type CommandParam struct {
	Name string
	Type CommandParamType
	// int
	Min, Max int64
	// enum
	Values []string
	// string
	Pattern   string
	MaxLength int
	re        *regexp.Regexp
	// inbox_path: 小文字の、"." で始まる拡張子（".msi"）
	Extensions []string
	// leading は、この差し込みが引数の先頭に置かれるテンプレートがあること。string の値は "-"・"/" で始まれない（オプションの注入を防ぐ）。
	leading bool
}

// templatePart はテンプレートの断片: param が空なら固定の文字列 lit、そうでなければ差し込み。
type templatePart struct {
	lit   string
	param string
}

// CommandEntry は許可リストの 1 項目。
type CommandEntry struct {
	Name           string
	Description    string
	Executable     string // ローカルの絶対パス（Clean 済み）
	Args           []string
	Params         []CommandParam // 名前の昇順
	WorkingDir     string         // 空ならプロセスの既定
	Timeout        time.Duration
	MaxOutputBytes int
	Enabled        bool

	parts [][]templatePart
}

// CommandAllowlist は読み込んだ許可リスト。Entries は名前の昇順。
type CommandAllowlist struct {
	Entries []CommandEntry
}

// Entry は名前の項目を返す。
func (a CommandAllowlist) Entry(name string) (CommandEntry, bool) {
	i, ok := slices.BinarySearchFunc(a.Entries, name, func(e CommandEntry, n string) int { return strings.Compare(e.Name, n) })
	if !ok {
		return CommandEntry{}, false
	}
	return a.Entries[i], true
}

var (
	commandNameRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	commandParamRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	commandExtRe   = regexp.MustCompile(`^\.[a-z0-9]{1,16}$`)
	// driveAbsRe・driveRootRe は Windows のドライブ文字つきの絶対パス・ドライブの直下。許可リストは Windows のパスで書くので、OS に依らず受け付ける
	// （Linux で動くテストでも検査できる。実際に起動できるのは Windows のパスだけ）。
	driveAbsRe  = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
	driveRootRe = regexp.MustCompile(`^[A-Za-z]:[\\/]*$`)
)

// shellInterpreters はシェル・スクリプトホスト・汎用のインタープリター（拡張子を除いた小文字の名前）。任意のコードを実行できるので、
// これを実行ファイルにする項目では、string と inbox_path の差し込みを使えない（固定の引数・int・enum だけ。MCP の呼び出し側が
// 実行内容を決められないようにする）。
var shellInterpreters = map[string]bool{
	"cmd": true, "powershell": true, "powershell_ise": true, "pwsh": true, "wscript": true, "cscript": true, "mshta": true,
	"wsl": true, "bash": true, "sh": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
	"python": true, "python3": true, "pythonw": true, "node": true, "perl": true, "ruby": true, "php": true,
}

// scriptExtensions は、実行ファイルにできない拡張子。.bat・.cmd は cmd.exe が引数を解釈し直すため、引数の検査（と Go の引数のクォート）が
// 効かない。ほかも、実行ファイルではなくスクリプト・ショートカットなど、別のホストプログラムが解釈するもの。
var scriptExtensions = map[string]bool{
	".bat": true, ".cmd": true, ".com": true, ".vbs": true, ".vbe": true, ".js": true, ".jse": true, ".wsf": true, ".wsh": true,
	".ps1": true, ".psm1": true, ".hta": true, ".lnk": true, ".scr": true, ".pif": true, ".cpl": true, ".msc": true, ".jar": true,
}

// ValidateHostFilePath は、ホスト所有者が設定するローカルのファイル・フォルダのパス p（実行ファイル・作業フォルダ・許可リストのファイル）の形を検査する。
// 絶対パスで（ドライブ文字で始まるもの。Windows 以外ではその OS の絶対パスも）、UNC・デバイスパス・"." と ".." の要素・代替データストリーム・ドライブの直下・制御文字・長すぎる値は不可。
// 区切りは "/" と "\" のどちらでもよい。満たさなければ ErrInvalidArgument（メッセージにパスは入れない）。
func ValidateHostFilePath(p string) error {
	bad := func(msg string) error { return ErrInvalidArgument.WithMsg(msg) }
	switch {
	case p == "" || !utf8.ValidString(p):
		return bad("the path must be a non-empty string")
	case UTF16Len(p) > maxCommandPathUnits:
		return bad("the path is too long")
	case strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//"):
		return bad("the path must be on a local drive (UNC and device paths are not allowed)")
	case !driveAbsRe.MatchString(p) && !filepath.IsAbs(p):
		return bad("the path must be an absolute path")
	case driveRootRe.MatchString(p) || filepath.Dir(filepath.Clean(p)) == filepath.Clean(p):
		return bad("the path must not be the root of a drive")
	}
	for i, r := range p {
		switch {
		case r < 0x20 || r == 0x7f:
			return bad("the path must not contain control characters")
		case r == ':' && i != 1:
			return bad("the path must not contain \":\" (alternate data streams are not allowed)")
		case strings.ContainsRune(`<>"|?*`, r):
			return bad(`the path must not contain any of <>"|?*`)
		}
	}
	for seg := range strings.SplitSeq(strings.ReplaceAll(p, `\`, "/"), "/") {
		if seg == "." || seg == ".." {
			return bad(`the path must not contain "." or ".." segments`)
		}
	}
	return nil
}

// commandBaseName は実行ファイルの名前（小文字。区切りの後ろ）と拡張子（"." つき）を返す。
func commandBaseName(exe string) (name, ext string) {
	base := strings.ToLower(exe[strings.LastIndexAny(exe, `/\`)+1:])
	ext = path.Ext(base)
	return strings.TrimSuffix(base, ext), ext
}

// ValidateCommandExecutable は許可リストの実行ファイル exe を検査する: ValidateHostFilePath に加え、末尾がドット・空白の名前（Windows は取り除く）、
// スクリプト・ショートカットの拡張子（scriptExtensions。.bat・.cmd を含む）を拒否する。
func ValidateCommandExecutable(exe string) error {
	if err := ValidateHostFilePath(exe); err != nil {
		return err
	}
	if base := exe[strings.LastIndexAny(exe, `/\`)+1:]; base == "" || strings.TrimRight(base, ". ") != base {
		return ErrInvalidArgument.WithMsg("the executable name must not end with a dot or a space")
	}
	if _, ext := commandBaseName(exe); scriptExtensions[ext] {
		return ErrInvalidArgument.WithMsg("scripts and shortcuts (" + ext + ") cannot be run: use a real executable (.bat and .cmd are re-parsed by cmd.exe, so argument checks do not hold)")
	}
	return nil
}

// IsShellInterpreter は exe が、任意のコードを実行できるシェル・スクリプトホスト・インタープリターかを返す。
func IsShellInterpreter(exe string) bool {
	name, _ := commandBaseName(exe)
	return shellInterpreters[name]
}

// commandFile は許可リストのファイル（JSON）。
type commandFile struct {
	Version  int                `json:"version"`
	Commands []commandEntryJSON `json:"commands"`
}

type commandEntryJSON struct {
	Name           string                      `json:"name"`
	Description    string                      `json:"description"`
	Executable     string                      `json:"executable"`
	Args           []string                    `json:"args"`
	Params         map[string]commandParamJSON `json:"params"`
	WorkingDir     string                      `json:"working_dir"`
	TimeoutSec     int                         `json:"timeout_sec"`
	MaxOutputBytes int                         `json:"max_output_bytes"`
	Enabled        bool                        `json:"enabled"` // 省略は false（既定で無効）
}

type commandParamJSON struct {
	Type       string   `json:"type"`
	Min        *int64   `json:"min"`
	Max        *int64   `json:"max"`
	Values     []string `json:"values"`
	Pattern    string   `json:"pattern"`
	MaxLength  int      `json:"max_length"`
	Extensions []string `json:"extensions"`
}

// ParseCommandAllowlist は許可リストのファイルの本文 data を読み、検査する。1 項目でも不備があれば全体を拒否する（fail closed。
// 部分的に読み込まない）。不明なキー・重複した名前・末尾の余分な内容も不備。エラーには不備の項目名と理由（パス・値は含まない）が入り、
// 運用ログ向け（MCP の呼び出し側には返さない）。
func ParseCommandAllowlist(data []byte) (CommandAllowlist, error) {
	if len(data) > MaxCommandFileBytes {
		return CommandAllowlist{}, errors.New("the allowlist file is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f commandFile
	if err := dec.Decode(&f); err != nil {
		return CommandAllowlist{}, fmt.Errorf("the allowlist file is not valid JSON of the expected shape: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return CommandAllowlist{}, errors.New("the allowlist file has content after the JSON document")
	}
	if f.Version != CommandFileVersion {
		return CommandAllowlist{}, fmt.Errorf("version must be %d", CommandFileVersion)
	}
	if len(f.Commands) > MaxCommandEntries {
		return CommandAllowlist{}, fmt.Errorf("at most %d commands are allowed", MaxCommandEntries)
	}
	out := CommandAllowlist{Entries: make([]CommandEntry, 0, len(f.Commands))}
	seen := map[string]bool{}
	for i, raw := range f.Commands {
		label := fmt.Sprintf("commands[%d]", i)
		if raw.Name != "" && commandNameRe.MatchString(raw.Name) {
			label = "command " + strconv.Quote(raw.Name)
		}
		e, err := buildCommandEntry(raw)
		if err != nil {
			return CommandAllowlist{}, fmt.Errorf("%s: %w", label, err)
		}
		if seen[e.Name] {
			return CommandAllowlist{}, fmt.Errorf("%s: the name is used twice", label)
		}
		seen[e.Name] = true
		out.Entries = append(out.Entries, e)
	}
	slices.SortFunc(out.Entries, func(a, b CommandEntry) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func buildCommandEntry(raw commandEntryJSON) (CommandEntry, error) {
	e := CommandEntry{Name: raw.Name, Description: raw.Description, Args: slices.Clone(raw.Args), Enabled: raw.Enabled}
	switch {
	case !commandNameRe.MatchString(raw.Name):
		return e, errors.New("name must match [A-Za-z0-9][A-Za-z0-9_.-]{0,63}")
	case utf8.RuneCountInString(raw.Description) > maxCommandDescription || !utf8.ValidString(raw.Description) || hasControl(raw.Description):
		return e, fmt.Errorf("description must be at most %d characters without control characters", maxCommandDescription)
	}
	if err := ValidateCommandExecutable(raw.Executable); err != nil {
		return e, fmt.Errorf("executable: %s", err.(*Error).Msg)
	}
	e.Executable = filepath.Clean(raw.Executable)
	if raw.WorkingDir != "" {
		if err := ValidateHostFilePath(raw.WorkingDir); err != nil {
			return e, fmt.Errorf("working_dir: %s", err.(*Error).Msg)
		}
		e.WorkingDir = filepath.Clean(raw.WorkingDir)
	}
	var err error
	if e.Timeout, err = commandTimeout(raw.TimeoutSec); err != nil {
		return e, err
	}
	switch {
	case raw.MaxOutputBytes == 0:
		e.MaxOutputBytes = DefaultCommandOutputBytes
	case raw.MaxOutputBytes < 0 || raw.MaxOutputBytes > MaxCommandOutputBytes:
		return e, fmt.Errorf("max_output_bytes must be between 1 and %d", MaxCommandOutputBytes)
	default:
		e.MaxOutputBytes = raw.MaxOutputBytes
	}
	if len(raw.Params) > maxCommandParams {
		return e, fmt.Errorf("at most %d params are allowed", maxCommandParams)
	}
	for name, p := range raw.Params {
		param, err := buildCommandParam(name, p)
		if err != nil {
			return e, fmt.Errorf("param %q: %w", name, err)
		}
		e.Params = append(e.Params, param)
	}
	slices.SortFunc(e.Params, func(a, b CommandParam) int { return strings.Compare(a.Name, b.Name) })
	if err := e.parseTemplates(); err != nil {
		return e, err
	}
	if IsShellInterpreter(e.Executable) {
		for _, p := range e.Params {
			if p.Type == CommandParamString || p.Type == CommandParamInboxPath {
				return e, fmt.Errorf("the executable is a shell or script interpreter, so param %q cannot be of type %s (only fixed arguments, int and enum)", p.Name, p.Type)
			}
		}
	}
	return e, nil
}

func commandTimeout(sec int) (time.Duration, error) {
	switch {
	case sec == 0:
		return DefaultCommandTimeout, nil
	case sec < 0 || time.Duration(sec)*time.Second > MaxCommandTimeout:
		return 0, fmt.Errorf("timeout_sec must be between 1 and %d", int(MaxCommandTimeout/time.Second))
	}
	return time.Duration(sec) * time.Second, nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

func buildCommandParam(name string, p commandParamJSON) (CommandParam, error) {
	if !commandParamRe.MatchString(name) {
		return CommandParam{}, errors.New("the name must match [a-z][a-z0-9_]{0,31}")
	}
	out := CommandParam{Name: name, Type: CommandParamType(p.Type)}
	// 型に関係のない項目は、書き間違いに気づけるよう拒否する。
	extra := func(field string, set bool) error {
		if set {
			return fmt.Errorf("%s does not apply to type %s", field, p.Type)
		}
		return nil
	}
	switch out.Type {
	case CommandParamInt:
		if p.Min == nil || p.Max == nil || *p.Min > *p.Max {
			return out, errors.New("type int needs min and max (min <= max)")
		}
		out.Min, out.Max = *p.Min, *p.Max
		return out, errors.Join(extra("values", p.Values != nil), extra("pattern", p.Pattern != ""), extra("max_length", p.MaxLength != 0), extra("extensions", p.Extensions != nil))
	case CommandParamEnum:
		if len(p.Values) == 0 || len(p.Values) > maxCommandEnumValues {
			return out, fmt.Errorf("type enum needs 1 to %d values", maxCommandEnumValues)
		}
		for _, v := range p.Values {
			if v == "" || utf8.RuneCountInString(v) > maxCommandEnumUnits || !utf8.ValidString(v) || hasControl(v) {
				return out, errors.New("enum values must be non-empty, without control characters, and short")
			}
		}
		if len(slices.Compact(slices.Sorted(slices.Values(p.Values)))) != len(p.Values) {
			return out, errors.New("enum values must be unique")
		}
		out.Values = slices.Clone(p.Values)
		return out, errors.Join(extra("min", p.Min != nil), extra("max", p.Max != nil), extra("pattern", p.Pattern != ""), extra("max_length", p.MaxLength != 0), extra("extensions", p.Extensions != nil))
	case CommandParamString:
		if p.Pattern == "" {
			return out, errors.New("type string needs a pattern (a regular expression the whole value must match)")
		}
		re, err := regexp.Compile(`^(?:` + p.Pattern + `)$`)
		if err != nil {
			return out, errors.New("pattern is not a valid regular expression")
		}
		out.Pattern, out.re = p.Pattern, re
		switch {
		case p.MaxLength == 0:
			out.MaxLength = DefaultCommandStringMax
		case p.MaxLength < 0 || p.MaxLength > MaxCommandStringMax:
			return out, fmt.Errorf("max_length must be between 1 and %d", MaxCommandStringMax)
		default:
			out.MaxLength = p.MaxLength
		}
		return out, errors.Join(extra("min", p.Min != nil), extra("max", p.Max != nil), extra("values", p.Values != nil), extra("extensions", p.Extensions != nil))
	case CommandParamInboxPath:
		if len(p.Extensions) == 0 || len(p.Extensions) > maxCommandExtensions {
			return out, fmt.Errorf("type inbox_path needs 1 to %d extensions (for example [\".msi\"])", maxCommandExtensions)
		}
		for _, ext := range p.Extensions {
			if !commandExtRe.MatchString(ext) {
				return out, errors.New("extensions must be lowercase with a leading dot, for example .msi")
			}
		}
		out.Extensions = slices.Clone(p.Extensions)
		return out, errors.Join(extra("min", p.Min != nil), extra("max", p.Max != nil), extra("values", p.Values != nil), extra("pattern", p.Pattern != ""), extra("max_length", p.MaxLength != 0))
	}
	return out, errors.New("type must be int, enum, string or inbox_path")
}

// parseTemplates は Args を断片に分け、差し込みの定義と突き合わせる（未定義の差し込み・使われない params は不備）。
func (e *CommandEntry) parseTemplates() error {
	if len(e.Args) > maxCommandArgs {
		return fmt.Errorf("at most %d args are allowed", maxCommandArgs)
	}
	used := map[string]bool{}
	e.parts = make([][]templatePart, len(e.Args))
	for i, tmpl := range e.Args {
		if !utf8.ValidString(tmpl) || UTF16Len(tmpl) > maxCommandArgUnits || strings.ContainsRune(tmpl, 0) {
			return fmt.Errorf("args[%d] must be valid UTF-8, without NUL, at most %d characters", i, maxCommandArgUnits)
		}
		parts, err := parseTemplate(tmpl)
		if err != nil {
			return fmt.Errorf("args[%d]: %w", i, err)
		}
		for j, part := range parts {
			if part.param == "" {
				continue
			}
			idx := slices.IndexFunc(e.Params, func(p CommandParam) bool { return p.Name == part.param })
			if idx < 0 {
				return fmt.Errorf("args[%d] uses {%s}, which is not defined in params", i, part.param)
			}
			used[part.param] = true
			if j == 0 {
				e.Params[idx].leading = true
			}
		}
		e.parts[i] = parts
	}
	for _, p := range e.Params {
		if !used[p.Name] {
			return fmt.Errorf("param %q is not used in args", p.Name)
		}
	}
	return nil
}

// parseTemplate はテンプレートを固定の文字列と {name} の差し込みに分ける。"{{" と "}}" は波括弧そのもの。
func parseTemplate(tmpl string) ([]templatePart, error) {
	var parts []templatePart
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, templatePart{lit: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(tmpl); i++ {
		c := tmpl[i]
		switch {
		case c == '{' && i+1 < len(tmpl) && tmpl[i+1] == '{':
			lit.WriteByte('{')
			i++
		case c == '}' && i+1 < len(tmpl) && tmpl[i+1] == '}':
			lit.WriteByte('}')
			i++
		case c == '{':
			end := strings.IndexByte(tmpl[i:], '}')
			if end < 0 {
				return nil, errors.New("unclosed { (write {{ for a literal brace)")
			}
			name := tmpl[i+1 : i+end]
			if !commandParamRe.MatchString(name) {
				return nil, errors.New("a {placeholder} must be a param name (write {{ for a literal brace)")
			}
			flush()
			parts = append(parts, templatePart{param: name})
			i += end
		case c == '}':
			return nil, errors.New("stray } (write }} for a literal brace)")
		default:
			lit.WriteByte(c)
		}
	}
	flush()
	return parts, nil
}

// Bind は呼び出し側の指定 args を検査し、差し込みごとの値（文字列）にする。足りない・余計な・型や範囲・パターンに合わない値は ErrInvalidArgument
// （メッセージに値は入れない）。inbox_path の値は置き場所からの相対パス（CleanTransferPath を通した形）のまま返す
// （実体への解決は service が行い、値を置き換えてから Argv に渡す）。
func (e CommandEntry) Bind(args map[string]any) (map[string]string, error) {
	for name := range args {
		if !slices.ContainsFunc(e.Params, func(p CommandParam) bool { return p.Name == name }) {
			return nil, ErrInvalidArgument.WithMsg("unknown argument " + strconv.Quote(TruncateRunes(name, 64)))
		}
	}
	out := make(map[string]string, len(e.Params))
	for _, p := range e.Params {
		raw, ok := args[p.Name]
		if !ok || raw == nil {
			return nil, ErrInvalidArgument.WithMsg("argument " + strconv.Quote(p.Name) + " is required")
		}
		v, err := p.bind(raw)
		if err != nil {
			return nil, ErrInvalidArgument.WithMsg("argument " + strconv.Quote(p.Name) + ": " + err.Error())
		}
		out[p.Name] = v
	}
	return out, nil
}

func (p CommandParam) bind(raw any) (string, error) {
	if p.Type == CommandParamInt {
		n, err := commandInt(raw)
		switch {
		case err != nil:
			return "", err
		case n < p.Min || n > p.Max:
			return "", fmt.Errorf("must be between %d and %d", p.Min, p.Max)
		}
		return strconv.FormatInt(n, 10), nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", errors.New("must be a string")
	}
	if !utf8.ValidString(s) || hasControl(s) {
		return "", errors.New("must be valid UTF-8 without control characters")
	}
	switch p.Type {
	case CommandParamEnum:
		if !slices.Contains(p.Values, s) {
			return "", errors.New("is not one of the allowed values: " + strings.Join(p.Values, ", "))
		}
	case CommandParamString:
		switch {
		case s == "" || utf8.RuneCountInString(s) > p.MaxLength:
			return "", fmt.Errorf("must be 1 to %d characters", p.MaxLength)
		case !p.re.MatchString(s):
			return "", errors.New("does not match the allowed pattern " + p.Pattern)
		case p.leading && (s[0] == '-' || s[0] == '/'):
			return "", errors.New(`must not start with "-" or "/" here`)
		}
	case CommandParamInboxPath:
		clean, err := CleanTransferPath(s)
		if err != nil {
			return "", err
		}
		if ext := strings.ToLower(path.Ext(clean)); !slices.Contains(p.Extensions, ext) {
			return "", errors.New("must have one of the extensions " + strings.Join(p.Extensions, ", "))
		}
		return clean, nil
	}
	return s, nil
}

// commandInt は JSON から来た整数（数値は float64。文字列の数字も受ける）を int64 にする。
func commandInt(raw any) (int64, error) {
	switch v := raw.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		if v != float64(int64(v)) || v > 1<<53 || v < -(1<<53) {
			return 0, errors.New("must be an integer")
		}
		return int64(v), nil
	case json.Number:
		return commandInt(string(v))
	case string:
		digits := strings.TrimPrefix(v, "-")
		if digits == "" || len(digits) > maxCommandIntDigits || strings.Trim(digits, "0123456789") != "" {
			return 0, errors.New("must be an integer")
		}
		return strconv.ParseInt(v, 10, 64)
	}
	return 0, errors.New("must be an integer")
}

// Argv は Bind（と inbox_path の解決）を経た values から、起動する引数（実行ファイルを除く）を作る。差し込みは 1 つの引数の中に
// 文字列としてだけ入り、分割・再解釈はされない。
func (e CommandEntry) Argv(values map[string]string) []string {
	argv := make([]string, 0, len(e.parts))
	for _, parts := range e.parts {
		var b strings.Builder
		for _, part := range parts {
			if part.param != "" {
				b.WriteString(values[part.param])
			} else {
				b.WriteString(part.lit)
			}
		}
		argv = append(argv, b.String())
	}
	return argv
}

// DecodeCommandOutput はコマンドの出力 b を MCP で返す文字列にする。truncated は上限で切り詰めたこと（途中で切れた文字を落とす）。
// encoding は "utf-8"（そのまま）・"utf-16le"（BOM つきの UTF-16 を UTF-8 にしたもの）・"binary"（UTF-8 でも UTF-16 でもない。
// 文字列は不正なバイトを U+FFFD にしたもので、呼び出し側は元のバイト列も返す。Windows の旧コードページ〔日本語環境の CP932 など〕の出力はここに入る）。
func DecodeCommandOutput(b []byte, truncated bool) (text, encoding string) {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		b = b[2:]
		if len(b)%2 == 1 {
			b = b[:len(b)-1] // 途中で切れた最後のバイト
		}
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
		}
		return strings.ToValidUTF8(string(utf16.Decode(u)), "\uFFFD"), "utf-16le"
	}
	if utf8.Valid(b) {
		return string(b), "utf-8"
	}
	if truncated {
		for n := 1; n <= utf8.UTFMax-1 && n <= len(b); n++ {
			if utf8.Valid(b[:len(b)-n]) {
				return string(b[:len(b)-n]), "utf-8"
			}
		}
	}
	return strings.ToValidUTF8(string(b), "\uFFFD"), "binary"
}
