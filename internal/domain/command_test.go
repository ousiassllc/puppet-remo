package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// cmdAbs は OS の流儀の絶対パス（Windows ならドライブ文字つき）。
func cmdAbs(parts ...string) string {
	if runtime.GOOS == "windows" {
		return `C:\` + strings.Join(parts, `\`)
	}
	return "/" + strings.Join(parts, "/")
}

// cmdFile は 1 項目だけの許可リストの JSON。mutate で項目を書き換えられる。
func cmdFile(t *testing.T, mutate func(e map[string]any)) []byte {
	t.Helper()
	e := map[string]any{"name": "tool", "executable": cmdAbs("opt", "tool.exe"), "enabled": true}
	if mutate != nil {
		mutate(e)
	}
	b, err := json.Marshal(map[string]any{"version": 1, "commands": []any{e}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustParse(t *testing.T, mutate func(e map[string]any)) CommandEntry {
	t.Helper()
	list, err := ParseCommandAllowlist(cmdFile(t, mutate))
	if err != nil {
		t.Fatalf("ParseCommandAllowlist: %v", err)
	}
	return list.Entries[0]
}

func TestParseCommandAllowlistAcceptsFullEntry(t *testing.T) {
	e := mustParse(t, func(e map[string]any) {
		e["description"] = "ping a host"
		e["args"] = []string{"--max-time", "5", "http://{host}:{port}/", "--mode={mode}", "{{literal}}"}
		e["params"] = map[string]any{
			"host": map[string]any{"type": "string", "pattern": `[A-Za-z0-9.-]{1,64}`},
			"port": map[string]any{"type": "int", "min": 1, "max": 65535},
			"mode": map[string]any{"type": "enum", "values": []string{"a", "b"}},
		}
		e["working_dir"] = cmdAbs("work")
		e["timeout_sec"] = 5
		e["max_output_bytes"] = 1000
	})
	if e.Name != "tool" || e.Timeout != 5*time.Second || e.MaxOutputBytes != 1000 || !e.Enabled || len(e.Params) != 3 || e.Params[0].Name != "host" {
		t.Fatalf("entry = %+v", e)
	}
	args, err := e.Bind(map[string]any{"host": "example.com", "port": float64(8080), "mode": "b"})
	if err != nil {
		t.Fatal(err)
	}
	got := e.Argv(args)
	want := []string{"--max-time", "5", "http://example.com:8080/", "--mode=b", "{literal}"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("Argv = %q, want %q", got, want)
	}
}

func TestParseCommandAllowlistDefaults(t *testing.T) {
	e := mustParse(t, func(e map[string]any) { delete(e, "enabled") })
	if e.Enabled {
		t.Error("an entry without enabled must be disabled")
	}
	if e.Timeout != DefaultCommandTimeout || e.MaxOutputBytes != DefaultCommandOutputBytes {
		t.Errorf("defaults = %v / %d", e.Timeout, e.MaxOutputBytes)
	}
	if e.Executable == "" || len(e.Argv(nil)) != 0 {
		t.Errorf("entry = %+v", e)
	}
}

func TestParseCommandAllowlistRejects(t *testing.T) {
	strParam := map[string]any{"type": "string", "pattern": `[a-z]+`}
	cases := map[string]func(e map[string]any){
		"relative executable":    func(e map[string]any) { e["executable"] = `tools\x.exe` },
		"bare executable name":   func(e map[string]any) { e["executable"] = "tasklist.exe" },
		"UNC executable":         func(e map[string]any) { e["executable"] = `\\host\share\x.exe` },
		"UNC with slashes":       func(e map[string]any) { e["executable"] = `//host/share/x.exe` },
		"device path":            func(e map[string]any) { e["executable"] = `\\?\C:\x.exe` },
		"dotdot executable":      func(e map[string]any) { e["executable"] = cmdAbs("opt", "..", "x.exe") },
		"empty executable":       func(e map[string]any) { e["executable"] = "" },
		"batch file":             func(e map[string]any) { e["executable"] = cmdAbs("opt", "run.bat") },
		"cmd script":             func(e map[string]any) { e["executable"] = cmdAbs("opt", "RUN.CMD") },
		"powershell script":      func(e map[string]any) { e["executable"] = cmdAbs("opt", "x.ps1") },
		"no extension":           func(e map[string]any) { e["executable"] = cmdAbs("opt", "run") },
		"trailing dot hides ext": func(e map[string]any) { e["executable"] = cmdAbs("opt", "run.bat.") },
		"relative working dir":   func(e map[string]any) { e["working_dir"] = "work" },
		"UNC working dir":        func(e map[string]any) { e["working_dir"] = `\\host\share` },
		"bad name":               func(e map[string]any) { e["name"] = "has space" },
		"empty name":             func(e map[string]any) { e["name"] = "" },
		"unknown key":            func(e map[string]any) { e["shell"] = true },
		"timeout too long":       func(e map[string]any) { e["timeout_sec"] = 601 },
		"negative timeout":       func(e map[string]any) { e["timeout_sec"] = -1 },
		"output cap too large":   func(e map[string]any) { e["max_output_bytes"] = MaxCommandOutputBytes + 1 },
		"undefined placeholder":  func(e map[string]any) { e["args"] = []string{"{nope}"} },
		"unused param":           func(e map[string]any) { e["params"] = map[string]any{"p": strParam} },
		"stray brace":            func(e map[string]any) { e["args"] = []string{"a}b"} },
		"unclosed brace":         func(e map[string]any) { e["args"] = []string{"a{b"} },
		"string without pattern": func(e map[string]any) {
			e["args"] = []string{"{p}"}
			e["params"] = map[string]any{"p": map[string]any{"type": "string"}}
		},
		"bad regexp": func(e map[string]any) {
			e["args"] = []string{"{p}"}
			e["params"] = map[string]any{"p": map[string]any{"type": "string", "pattern": "("}}
		},
		"int without range": func(e map[string]any) {
			e["args"] = []string{"{p}"}
			e["params"] = map[string]any{"p": map[string]any{"type": "int"}}
		},
		"int inverted range": func(e map[string]any) {
			e["args"] = []string{"{p}"}
			e["params"] = map[string]any{"p": map[string]any{"type": "int", "min": 5, "max": 1}}
		},
		"enum without values": func(e map[string]any) {
			e["args"] = []string{"{p}"}
			e["params"] = map[string]any{"p": map[string]any{"type": "enum"}}
		},
		"enum duplicate values": func(e map[string]any) {
			e["args"] = []string{"{p}"}
			e["params"] = map[string]any{"p": map[string]any{"type": "enum", "values": []string{"a", "a"}}}
		},
		"inbox without ext": func(e map[string]any) {
			e["args"] = []string{"{p}"}
			e["params"] = map[string]any{"p": map[string]any{"type": "inbox_path"}}
		},
		"inbox bad ext": func(e map[string]any) {
			e["args"] = []string{"{p}"}
			e["params"] = map[string]any{"p": map[string]any{"type": "inbox_path", "extensions": []string{"msi"}}}
		},
		"unknown type": func(e map[string]any) {
			e["args"] = []string{"{p}"}
			e["params"] = map[string]any{"p": map[string]any{"type": "any"}}
		},
		"field of another type": func(e map[string]any) {
			e["args"] = []string{"{p}"}
			e["params"] = map[string]any{"p": map[string]any{"type": "int", "min": 1, "max": 2, "pattern": "x"}}
		},
		"bad param name": func(e map[string]any) { e["args"] = []string{"{P}"}; e["params"] = map[string]any{"P": strParam} },
		"powershell with string": func(e map[string]any) {
			e["executable"] = cmdAbs("w", "powershell.exe")
			e["args"] = []string{"-Command", "{p}"}
			e["params"] = map[string]any{"p": strParam}
		},
		"cmd.exe with string": func(e map[string]any) {
			e["executable"] = cmdAbs("w", "CMD.EXE")
			e["args"] = []string{"/c", "echo", "{p}"}
			e["params"] = map[string]any{"p": strParam}
		},
		"pwsh with inbox_path": func(e map[string]any) {
			e["executable"] = cmdAbs("w", "pwsh.exe")
			e["args"] = []string{"-File", "{p}"}
			e["params"] = map[string]any{"p": map[string]any{"type": "inbox_path", "extensions": []string{".ps1"}}}
		},
		"wscript with string": func(e map[string]any) {
			e["executable"] = cmdAbs("w", "wscript.exe")
			e["args"] = []string{"{p}"}
			e["params"] = map[string]any{"p": strParam}
		},
		"mshta with string": func(e map[string]any) {
			e["executable"] = cmdAbs("w", "mshta.exe")
			e["args"] = []string{"{p}"}
			e["params"] = map[string]any{"p": strParam}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCommandAllowlist(cmdFile(t, mutate)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestParseCommandAllowlistInterpreterWithFixedArgsAndIntIsAllowed(t *testing.T) {
	// 自由な入力を渡せなければ、インタープリターも項目にできる（ホスト所有者が固定の内容を決める）。
	mustParse(t, func(e map[string]any) {
		e["executable"] = cmdAbs("w", "powershell.exe")
		e["args"] = []string{"-NoProfile", "-File", cmdAbs("opt", "check.ps1"), "{n}", "{m}"}
		e["params"] = map[string]any{
			"n": map[string]any{"type": "int", "min": 1, "max": 9},
			"m": map[string]any{"type": "enum", "values": []string{"x"}},
		}
	})
}

func TestParseCommandAllowlistFileLevel(t *testing.T) {
	good := string(cmdFile(t, nil))
	for name, data := range map[string]string{
		"not JSON":        "nope",
		"empty":           "",
		"wrong version":   strings.Replace(good, `"version":1`, `"version":2`, 1),
		"missing version": `{"commands":[]}`,
		"trailing data":   good + " {}",
		"unknown top key": strings.Replace(good, `"version":1`, `"version":1,"extra":1`, 1),
		"too large":       strings.Repeat(" ", MaxCommandFileBytes+1),
	} {
		if _, err := ParseCommandAllowlist([]byte(data)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// 重複した名前は全体を拒否する。
	dup := fmt.Sprintf(`{"version":1,"commands":[{"name":"a","executable":%q},{"name":"a","executable":%q}]}`, cmdAbs("x.exe"), cmdAbs("y.exe"))
	if _, err := ParseCommandAllowlist([]byte(dup)); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("duplicate names: err = %v", err)
	}
	// 1 項目が不正なら、ほかの項目が正しくても全体を拒否する（部分的に読み込まない）。
	mixed := fmt.Sprintf(`{"version":1,"commands":[{"name":"ok","executable":%q},{"name":"bad","executable":"relative.exe"}]}`, cmdAbs("x.exe"))
	if _, err := ParseCommandAllowlist([]byte(mixed)); err == nil || !strings.Contains(err.Error(), `"bad"`) {
		t.Errorf("mixed: err = %v", err)
	}
	// 空の許可リストは有効（何も実行できないだけ）。
	if list, err := ParseCommandAllowlist([]byte(`{"version":1,"commands":[]}`)); err != nil || len(list.Entries) != 0 {
		t.Errorf("empty list = %+v, %v", list, err)
	}
}

func TestCommandAllowlistEntryLookup(t *testing.T) {
	data := fmt.Sprintf(`{"version":1,"commands":[{"name":"b","executable":%q},{"name":"a","executable":%q}]}`, cmdAbs("b.exe"), cmdAbs("a.exe"))
	list, err := ParseCommandAllowlist([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if list.Entries[0].Name != "a" {
		t.Errorf("entries are not sorted by name: %v", list.Entries[0].Name)
	}
	if e, ok := list.Entry("b"); !ok || e.Name != "b" {
		t.Errorf("Entry(b) = %v, %v", e, ok)
	}
	if _, ok := list.Entry("c"); ok {
		t.Error("Entry(c) found")
	}
}

// 型つきの引数は、シェルの文字列・オプションの注入を受け付けない。
func TestCommandBindRejectsShellLikeAndBadValues(t *testing.T) {
	e := mustParse(t, func(e map[string]any) {
		e["args"] = []string{"{name}", "{pid}", "{mode}", "x={tail}"}
		e["params"] = map[string]any{
			"name": map[string]any{"type": "string", "pattern": `[A-Za-z0-9._-]{1,64}`},
			"pid":  map[string]any{"type": "int", "min": 1, "max": 4294967295},
			"mode": map[string]any{"type": "enum", "values": []string{"fast", "slow"}},
			"tail": map[string]any{"type": "string", "pattern": `.*`, "max_length": 8},
		}
	})
	ok := func() map[string]any {
		return map[string]any{"name": "svc-1", "pid": 42, "mode": "fast", "tail": "-ok"}
	}
	if _, err := e.Bind(ok()); err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	mut := func(k string, v any) map[string]any { m := ok(); m[k] = v; return m }
	bad := map[string]map[string]any{
		"ampersand":        mut("name", "a & calc"),
		"command subst":    mut("name", "$(x)"),
		"pipe":             mut("name", "| more"),
		"backtick":         mut("name", "`calc`"),
		"redirect":         mut("name", "a>b"),
		"semicolon":        mut("name", "a;b"),
		"newline":          mut("name", "a\nb"),
		"NUL":              mut("name", "a\x00b"),
		"space":            mut("name", "a b"),
		"empty string":     mut("name", ""),
		"too long":         mut("name", strings.Repeat("a", 65)),
		"option injection": mut("name", "-rf"),
		"switch injection": mut("name", "/c"),
		"pid zero":         mut("pid", 0),
		"pid too big":      mut("pid", float64(4294967296)),
		"pid negative":     mut("pid", -1),
		"pid fractional":   mut("pid", 1.5),
		"pid shell":        mut("pid", "1 & calc"),
		"pid wrong type":   mut("pid", true),
		"pid list":         mut("pid", []any{1}),
		"enum other":       mut("mode", "turbo"),
		"enum case":        mut("mode", "Fast"),
		"enum number":      mut("mode", 1),
		"tail too long":    mut("tail", "123456789"),
		"unknown arg":      mut("extra", "x"),
		"command line":     mut("command_line", "calc.exe"),
		"nil value":        mut("pid", nil),
	}
	for name, args := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := e.Bind(args)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
			if strings.Contains(err.Error(), "calc") && name != "command line" {
				t.Errorf("the error echoes the rejected value: %v", err)
			}
		})
	}
	for _, missing := range []string{"name", "pid", "mode", "tail"} {
		args := ok()
		delete(args, missing)
		if _, err := e.Bind(args); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("missing %s: err = %v", missing, err)
		}
	}
	if _, err := e.Bind(nil); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("no args: err = %v", err)
	}
	// 先頭でない差し込みは "-" で始まってよい（x=-ok）。整数は文字列の数字も受ける。
	if v, err := e.Bind(mut("pid", "7")); err != nil || v["pid"] != "7" {
		t.Errorf("string int = %v, %v", v, err)
	}
}

func TestCommandBindInboxPath(t *testing.T) {
	e := mustParse(t, func(e map[string]any) {
		e["args"] = []string{"/i", "{file}", "/qn"}
		e["params"] = map[string]any{"file": map[string]any{"type": "inbox_path", "extensions": []string{".msi", ".exe"}}}
	})
	v, err := e.Bind(map[string]any{"file": `builds\App.MSI`})
	if err != nil || v["file"] != "builds/App.MSI" {
		t.Fatalf("Bind = %v, %v", v, err)
	}
	for name, p := range map[string]string{
		"traversal":      "../x.msi",
		"nested ..":      "a/../../x.msi",
		"absolute":       "/etc/x.msi",
		"drive":          `C:\x.msi`,
		"UNC":            `\\host\share\x.msi`,
		"stream":         "x.msi:evil",
		"wrong ext":      "x.bat",
		"no ext":         "x",
		"dir":            "a/",
		"empty":          "",
		"reserved":       "NUL.msi",
		"trailing dot":   "x.msi.",
		"hidden ext dot": "x.msi ",
	} {
		if _, err := e.Bind(map[string]any{"file": p}); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%s: err = %v, want invalid_argument", name, err)
		}
	}
	// 解決した実体のパスで置き換えて Argv を作る。
	got := e.Argv(map[string]string{"file": cmdAbs("inbox", "x.msi")})
	if len(got) != 3 || got[0] != "/i" || got[1] != cmdAbs("inbox", "x.msi") || got[2] != "/qn" {
		t.Errorf("Argv = %q", got)
	}
}

func TestArgvKeepsValuesInOneElement(t *testing.T) {
	e := mustParse(t, func(e map[string]any) {
		e["args"] = []string{"--q={q}"}
		e["params"] = map[string]any{"q": map[string]any{"type": "string", "pattern": `[a-z ]+`}}
	})
	v, err := e.Bind(map[string]any{"q": "two words"})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Argv(v); len(got) != 1 || got[0] != "--q=two words" {
		t.Errorf("Argv = %q: a value must stay inside its one argument", got)
	}
}

func TestValidateHostFilePath(t *testing.T) {
	good := []string{cmdAbs("opt", "tool.exe"), cmdAbs("a b", "c")}
	for _, p := range good {
		if err := ValidateHostFilePath(p); err != nil {
			t.Errorf("%q rejected: %v", p, err)
		}
	}
	for _, p := range []string{"", "rel/x", `\\h\s\x`, "//h/s/x", cmdAbs("a", "..", "b"), cmdAbs("a", ".", "b"), cmdAbs("a", "b\x00"), cmdAbs("a", "b?"),
		cmdAbs("a", "b:s"), cmdAbs("a", "b|c"), "/", cmdAbs(strings.Repeat("a", 300)), cmdAbs("a\xffb")} {
		if err := ValidateHostFilePath(p); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%q: err = %v, want invalid_argument", p, err)
		}
	}
}

func TestIsShellInterpreter(t *testing.T) {
	for p, want := range map[string]bool{
		cmdAbs("w", "cmd.exe"): true, cmdAbs("w", "PowerShell.EXE"): true, cmdAbs("w", "pwsh.exe"): true, cmdAbs("w", "wscript.exe"): true,
		cmdAbs("w", "cscript.exe"): true, cmdAbs("w", "mshta.exe"): true, cmdAbs("w", "tasklist.exe"): false, cmdAbs("w", "msiexec.exe"): false,
		cmdAbs("w", "curl.exe"): false,
	} {
		if got := IsShellInterpreter(p); got != want {
			t.Errorf("IsShellInterpreter(%q) = %v", p, got)
		}
	}
}

func TestDecodeCommandOutput(t *testing.T) {
	utf16 := []byte{0xFF, 0xFE, 'h', 0, 'i', 0, 0x42, 0x30} // BOM + "hi" + "あ"
	cases := []struct {
		name      string
		in        []byte
		truncated bool
		text, enc string
	}{
		{"empty", nil, false, "", "utf-8"},
		{"ascii", []byte("ok\r\n"), false, "ok\r\n", "utf-8"},
		{"japanese utf-8", []byte("日本語"), false, "日本語", "utf-8"},
		{"utf-16le with BOM", utf16, false, "hiあ", "utf-16le"},
		{"utf-16le odd byte (cut)", append(utf16[:6:6], 0x42), true, "hi", "utf-16le"},
		{"cp932 is binary", []byte{0x93, 0xfa, 0x96, 0x7b}, false, "\uFFFD{", "binary"},
		{"truncated mid-rune is trimmed", []byte("a日")[:3], true, "a", "utf-8"},
		{"mid-rune but not truncated is binary", []byte("a日")[:3], false, "a\uFFFD", "binary"},
	}
	for _, c := range cases {
		text, enc := DecodeCommandOutput(c.in, c.truncated)
		if text != c.text || enc != c.enc {
			t.Errorf("%s: = (%q, %s), want (%q, %s)", c.name, text, enc, c.text, c.enc)
		}
	}
}

// 許可リストは Windows のパスで書く。どの OS のテストでも、ドライブ文字つきのパスを同じ規則で検査できる。
func TestValidateHostFilePathWindowsForms(t *testing.T) {
	for _, p := range []string{`C:\Windows\System32\tasklist.exe`, `C:/Windows/System32/tasklist.exe`, `d:\tools\a b\x.exe`} {
		if err := ValidateCommandExecutable(p); err != nil {
			t.Errorf("%q rejected: %v", p, err)
		}
	}
	for _, p := range []string{`C:\`, `C:/`, `C:`, `C:x.exe`, `\x.exe`, `C:\a\..\b.exe`, `C:\a\x.exe:stream`, `\\?\C:\x.exe`, `\\.\pipe\x`, `C:\a\run.cmd`, `C:\a\RUN.BAT`, `C:\a\x.exe.`} {
		if err := ValidateCommandExecutable(p); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%q: err = %v, want invalid_argument", p, err)
		}
	}
}

// docs/operations/command-allowlist.example.json は、そのまま使える（有効にする前に、書き方・パスを環境に合わせる）例。
// 全項目が既定で無効で、この規則（実行ファイル・差し込みの型・インタープリターの扱い）を通ること。
func TestShippedExampleAllowlistIsValidAndDisabled(t *testing.T) {
	data, err := os.ReadFile("../../docs/operations/command-allowlist.example.json")
	if err != nil {
		t.Fatal(err)
	}
	list, err := ParseCommandAllowlist(data)
	if err != nil {
		t.Fatalf("the shipped example does not pass the loader: %v", err)
	}
	var names []string
	for _, e := range list.Entries {
		names = append(names, e.Name)
		if e.Enabled {
			t.Errorf("%s: examples must be disabled", e.Name)
		}
	}
	want := []string{"app-version", "file-exists", "file-version", "http-check", "install-msi", "listening-ports", "ping-host", "process-find", "process-list", "verify-signature"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("names = %v, want %v", names, want)
	}
	// 差し込みを入れた引数が、意図どおりの argv になる（%{{http_code}} の波括弧のエスケープを含む）。
	e, _ := list.Entry("http-check")
	v, err := e.Bind(map[string]any{"host": "localhost", "port": 8765})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Argv(v); got[len(got)-1] != "http://localhost:8765/" || got[len(got)-2] != "%{http_code}" {
		t.Errorf("http-check argv = %q", got)
	}
	// 例のとおりに、引数の注入は拒否される。
	e, _ = list.Entry("ping-host")
	for _, host := range []string{"-f", "/?", "a b", "a&b", "host;calc"} {
		if _, err := e.Bind(map[string]any{"host": host}); err == nil {
			t.Errorf("ping-host accepted %q", host)
		}
	}
}
