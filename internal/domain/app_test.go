package domain_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func validInput() domain.AppInput {
	return domain.AppInput{Label: "メモ帳", ExePath: `C:\Windows\notepad.exe`}
}

// 実行ファイルのパスの形。ValidateAppInput と CheckLaunchTarget が同じ結論を出すことも確かめる。
func TestExePathShape(t *testing.T) {
	tests := []struct {
		name string
		path string
		ok   bool
	}{
		{"absolute exe", `C:\Windows\notepad.exe`, true},
		{"lower-case drive letter", `d:\tools\a.exe`, true},
		{"upper-case extension", `C:\Tools\A.EXE`, true},
		{"mixed-case extension", `C:\Tools\a.ExE`, true},
		{"forward slashes", `C:/Tools/a.exe`, true},
		{"directory with spaces and japanese", `C:\Program Files\アプリ\a b.exe`, true},
		{"dot inside names", `C:\a.b\c.d.exe`, true},
		{"double separator inside", `C:\Tools\\a.exe`, true},

		{"empty", ``, false},
		{"relative", `notepad.exe`, false},
		{"relative with dir", `Tools\a.exe`, false},
		{"drive relative", `C:a.exe`, false},
		{"rooted without drive", `\Windows\a.exe`, false},
		{"unc", `\\server\share\a.exe`, false},
		{"device path question", `\\?\C:\Tools\a.exe`, false},
		{"device path dot", `\\.\C:\Tools\a.exe`, false},
		{"device path forward slashes", `//?/C:/Tools/a.exe`, false},
		{"unc forward slashes", `//server/share/a.exe`, false},
		{"dot-dot element", `C:\Tools\..\Windows\a.exe`, false},
		{"dot-dot first element", `C:\..\a.exe`, false},
		{"dot-dot with forward slash", `C:/Tools/../a.exe`, false},
		{"triple dot element", `C:\Tools\...\a.exe`, false},
		{"dot-dot with trailing space", "C:\\Tools\\.. \\a.exe", false},
		{"single dot element", `C:\Tools\.\a.exe`, false},
		{"not exe", `C:\Windows\notepad.com`, false},
		{"no extension", `C:\Windows\notepad`, false},
		{"bat", `C:\Windows\a.bat`, false},
		{"lnk", `C:\Users\a.lnk`, false},
		{"exe as a suffix of the name", `C:\Tools\a.exe.txt`, false},
		{"only the extension", `C:\Tools\.exe`, false},
		{"extension right after separator slash", `C:/.exe`, false},
		{"drive root only", `C:\`, false},
		{"directory ending with separator", `C:\Tools\a.exe\`, false},
		{"digit drive", `1:\a.exe`, false},
		{"nul in path", "C:\\Tools\\a\x00.exe", false},
		{"newline in path", "C:\\Tools\\a\n.exe", false},
		{"non-ascii drive letter", `Ａ:\a.exe`, false},

		// 代替データストリーム・使えない文字・制御文字
		{"ads on file", `C:\a\f.txt:x.exe`, false},
		{"colon in directory", `C:\a:b\c.exe`, false},
		{"colon in file name", `C:\a:b.exe`, false},
		{"data stream suffix", `C:\a\f.exe::$DATA`, false},
		{"stream with exe name", `C:\a\f.exe:x.exe`, false},
		{"second drive colon", `C:\D:\a.exe`, false},
		{"less-than", `C:\a<b.exe`, false},
		{"greater-than", `C:\a>b.exe`, false},
		{"double quote", `C:\a"b.exe`, false},
		{"pipe", `C:\a|b.exe`, false},
		{"question mark", `C:\a?b.exe`, false},
		{"asterisk", `C:\a*b.exe`, false},
		{"wildcard in directory", `C:\a*\b.exe`, false},
		{"tab", "C:\\a\tb.exe", false},
		{"unit separator 0x1f", "C:\\a\x1fb.exe", false},
		{"escape 0x1b", "C:\\a\x1bb.exe", false},
		{"del 0x7f", "C:\\a\x7fb.exe", false},
		{"space and unicode are fine", "C:\\a b\\ａ　b.exe", true},

		// 予約デバイス名
		{"CON file", `C:\Tools\CON.exe`, false},
		{"lower-case con file", `C:\Tools\con.exe`, false},
		{"PRN file", `C:\Tools\PRN.exe`, false},
		{"AUX file", `C:\Tools\Aux.exe`, false},
		{"NUL file", `C:\Tools\nul.exe`, false},
		{"COM1 file", `C:\Tools\COM1.exe`, false},
		{"COM9 file", `C:\Tools\com9.exe`, false},
		{"LPT1 file", `C:\Tools\LPT1.exe`, false},
		{"LPT9 file", `C:\Tools\lpt9.exe`, false},
		{"reserved name with extra extension", `C:\Tools\NUL.tar.exe`, false},
		{"reserved name with trailing space before dot", `C:\Tools\NUL .exe`, false},
		{"reserved name as directory", `C:\Tools\CON\a.exe`, false},
		{"reserved name as directory with extension", `C:\Tools\aux.d\a.exe`, false},
		{"reserved name as directory with trailing dot", `C:\Tools\COM3.\a.exe`, false},
		{"reserved name directory with forward slash", `C:/PRN/a.exe`, false},
		{"COM superscript one", `C:\Tools\COM¹.exe`, false},
		{"COM superscript two", `C:\Tools\com².exe`, false},
		{"LPT superscript three", `C:\Tools\LPT³.exe`, false},
		{"superscript reserved name as directory", `C:\Tools\lpt¹\a.exe`, false},
		{"COM0 is not reserved", `C:\Tools\COM0.exe`, true},
		{"COM10 is not reserved", `C:\Tools\COM10.exe`, true},
		{"LPT0 is not reserved", `C:\Tools\LPT0.exe`, true},
		{"COM with two superscripts is not reserved", `C:\Tools\COM¹².exe`, true},
		{"name only starting with a reserved name", `C:\Tools\console.exe`, true},
		{"name ending with a reserved name", `C:\Tools\mycon.exe`, true},
		{"reserved name after the first dot only", `C:\Tools\a.con.exe`, true},
		{"conf is not reserved", `C:\Tools\conf.exe`, true},
		{"com alone is not reserved", `C:\Tools\com.exe`, true},

		// 通常の名前（空白・括弧・記号）
		{"program files with spaces and parentheses", `C:\Program Files (x86)\a b\c.exe`, true},
		{"punctuation in the file name", `C:\Tools\&%!';[]$~=@.exe`, true},

		// 長さ（UTF-16 のコードユニットで 32,766 まで。終端の NUL を含めて 32,767）
		{"longest path", exeOfUnits(32766), true},
		{"one unit too long", exeOfUnits(32767), false},
		{"astral characters count as two units", `C:\` + strings.Repeat("😀", 16380) + ".exe", false}, // 3 + 32760 + 4 = 32767
		{"astral characters within the limit", `C:\` + strings.Repeat("😀", 16379) + "a.exe", true},   // 3 + 32758 + 1 + 4 = 32766
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			in.ExePath = tt.path
			errs := domain.ValidateAppInput(in)
			if got := errs == nil; got != tt.ok {
				t.Errorf("ValidateAppInput ok = %v, want %v (errs %v)", got, tt.ok, errs)
			}
			if !tt.ok && (len(errs) != 1 || errs[domain.AppFieldExePath] == "") {
				t.Errorf("errs = %v, want exactly the exe_path field", errs)
			}

			err := domain.CheckLaunchTarget(domain.LaunchableApp{ID: "x", Label: "l", ExePath: tt.path})
			if tt.ok && err != nil {
				t.Errorf("CheckLaunchTarget = %v, want nil", err)
			}
			if !tt.ok && !errors.Is(err, domain.ErrAppNotFound) {
				t.Errorf("CheckLaunchTarget = %v, want ErrAppNotFound", err)
			}
		})
	}
}

// exeOfUnits は UTF-16 で units ユニットの、形は有効な実行ファイルのパスを返す。
func exeOfUnits(units int) string {
	return `C:\` + strings.Repeat("a", units-len(`C:\`)-len(".exe")) + ".exe"
}

// dirOfUnits は UTF-16 で units ユニットの、形は有効な作業ディレクトリを返す。
func dirOfUnits(units int) string {
	return `C:\` + strings.Repeat("a", units-len(`C:\`))
}
func TestWorkingDirShape(t *testing.T) {
	tests := []struct {
		name string
		dir  string
		ok   bool
	}{
		{"empty means the executable's directory", ``, true},
		{"absolute", `C:\Users\me\Documents`, true},
		{"drive root", `C:\`, true},
		{"trailing separator", `C:\Work\`, true},
		{"forward slashes", `C:/Work`, true},
		{"does not need to be an exe", `C:\Work\data.exe.d`, true},

		{"relative", `Work`, false},
		{"dot", `.`, false},
		{"unc", `\\server\share`, false},
		{"device path", `\\?\C:\Work`, false},
		{"dot-dot element", `C:\Work\..\x`, false},
		{"drive relative", `C:Work`, false},
		{"nul", "C:\\Wo\x00rk", false},

		{"ads on directory", `C:\Work:x`, false},
		{"data stream", `C:\Work::$DATA`, false},
		{"colon in file name", `C:\a:b`, false},
		{"wildcard", `C:\Work\*`, false},
		{"question mark", `C:\Wo?k`, false},
		{"pipe", `C:\Wo|k`, false},
		{"quote", `C:\Wo"k`, false},
		{"angle brackets", `C:\<Work>`, false},
		{"tab", "C:\\Wo\tk", false},
		{"del", "C:\\Wo\x7fk", false},
		{"reserved CON", `C:\Work\CON`, false},
		{"reserved nul with extension", `C:\Work\nul.txt`, false},
		{"reserved COM1 with trailing dot", `C:\Work\COM1.`, false},
		{"reserved LPT9 in the middle", `C:\LPT9\Work`, false},
		{"reserved COM superscript", `C:\Work\COM²`, false},
		{"reserved lpt superscript", `C:\Work\lpt³`, false},
		{"COM0 is not reserved", `C:\Work\COM0`, true},
		{"lpt0 is not reserved", `C:\Work\lpt0`, true},
		{"COM10 is not reserved", `C:\Work\COM10`, true},
		{"conf is not reserved", `C:\Work\conf`, true},
		{"spaces and parentheses", `C:\Program Files (x86)\a b`, true},
		{"punctuation in the name", `C:\Work\&%!';[]$~=@`, true},
		{"longest", dirOfUnits(32766), true},
		{"one unit too long", dirOfUnits(32767), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			in.WorkingDir = tt.dir
			errs := domain.ValidateAppInput(in)
			if got := errs == nil; got != tt.ok {
				t.Errorf("ValidateAppInput ok = %v, want %v (errs %v)", got, tt.ok, errs)
			}
			if !tt.ok && (len(errs) != 1 || errs[domain.AppFieldWorkingDir] == "") {
				t.Errorf("errs = %v, want exactly the working_dir field", errs)
			}
			err := domain.CheckLaunchTarget(domain.LaunchableApp{ExePath: in.ExePath, WorkingDir: tt.dir})
			if (err == nil) != tt.ok {
				t.Errorf("CheckLaunchTarget = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func repeatArgs(n int, arg string) []string {
	args := make([]string, n)
	for i := range args {
		args[i] = arg
	}
	return args
}

func TestArgsLimits(t *testing.T) {
	tests := []struct {
		name string
		args []string
		ok   bool
	}{
		{"none", nil, true},
		{"empty slice", []string{}, true},
		{"quotes and spaces are data", []string{`--name=a "b" c`, `/x`}, true},
		{"newline inside one argument", []string{"a\nb"}, true},
		{"whitespace-only argument", []string{" "}, true},
		{"32 arguments", repeatArgs(32, "a"), true},
		{"33 arguments", repeatArgs(33, "a"), false},
		{"total exactly 8192 chars", repeatArgs(8, strings.Repeat("a", 1024)), true},
		{"total 8193 chars", append(repeatArgs(8, strings.Repeat("a", 1024)), "a"), false},
		{"single argument of 8192 chars", []string{strings.Repeat("a", 8192)}, true},
		{"single argument of 8193 chars", []string{strings.Repeat("a", 8193)}, false},
		{"length counts characters, not bytes", []string{strings.Repeat("あ", 8192)}, true},
		{"length counts characters over the limit", []string{strings.Repeat("あ", 8193)}, false},
		{"NUL in an argument", []string{"a", "b\x00c"}, false},
		{"empty argument", []string{"a", ""}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			in.Args = tt.args
			errs := domain.ValidateAppInput(in)
			if got := errs == nil; got != tt.ok {
				t.Errorf("ValidateAppInput ok = %v, want %v (errs %v)", got, tt.ok, errs)
			}
			if !tt.ok && (len(errs) != 1 || errs[domain.AppFieldArgs] == "") {
				t.Errorf("errs = %v, want exactly the args field", errs)
			}
			err := domain.CheckLaunchTarget(domain.LaunchableApp{ExePath: in.ExePath, Args: tt.args})
			if (err == nil) != tt.ok {
				t.Errorf("CheckLaunchTarget = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestLabelLimits(t *testing.T) {
	tests := []struct {
		name  string
		label string
		ok    bool
	}{
		{"one char", "a", true},
		{"64 chars", strings.Repeat("a", 64), true},
		{"65 chars", strings.Repeat("a", 65), false},
		{"64 multibyte chars", strings.Repeat("あ", 64), true},
		{"65 multibyte chars", strings.Repeat("あ", 65), false},
		{"empty", "", false},
		{"only spaces", " \t\u3000", false},
		{"surrounding whitespace is trimmed before counting", "  " + strings.Repeat("a", 64) + "\t", true},
		{"inner whitespace counts", "a b", true},
		{"65 chars once trimmed", " " + strings.Repeat("a", 65) + " ", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			in.Label = tt.label
			errs := domain.ValidateAppInput(in)
			if got := errs == nil; got != tt.ok {
				t.Errorf("ValidateAppInput ok = %v, want %v (errs %v)", got, tt.ok, errs)
			}
			if !tt.ok && (len(errs) != 1 || errs[domain.AppFieldLabel] == "") {
				t.Errorf("errs = %v, want exactly the label field", errs)
			}
		})
	}
}

func TestValidateAppInputReportsEveryField(t *testing.T) {
	errs := domain.ValidateAppInput(domain.AppInput{
		Label: "", ExePath: `a.exe`, Args: repeatArgs(33, "a"), WorkingDir: "rel",
	})
	for _, f := range []string{domain.AppFieldLabel, domain.AppFieldExePath, domain.AppFieldArgs, domain.AppFieldWorkingDir} {
		if errs[f] == "" {
			t.Errorf("no message for %s: %v", f, errs)
		}
	}
	if len(errs) != 4 {
		t.Errorf("errs = %v, want 4 fields", errs)
	}
}

// 検証メッセージに入力値（パス・引数）を載せない（NFR-S20 (5)）。
func TestValidationMessagesDoNotEchoInput(t *testing.T) {
	const secretPath = `\\secret-host\hidden-share\token-abc.exe`
	const secretArg = "--password=hunter2"
	errs := domain.ValidateAppInput(domain.AppInput{
		Label: "l", ExePath: secretPath, Args: append(repeatArgs(32, secretArg), secretArg), WorkingDir: `rel\secret-dir`,
	})
	if len(errs) != 3 {
		t.Fatalf("errs = %v, want 3 fields", errs)
	}
	for field, msg := range errs {
		for _, secret := range []string{"secret", "hunter2", "token-abc"} {
			if strings.Contains(msg, secret) {
				t.Errorf("%s message %q contains %q", field, msg, secret)
			}
		}
	}
}

// 起動のたびの再検査は、保存済みの行の安全に関わる項目だけを見る。
func TestCheckLaunchTarget(t *testing.T) {
	ok := domain.LaunchableApp{
		ID: "01JA", Label: "メモ帳", ExePath: `C:\Windows\notepad.exe`,
		Args: []string{"/a"}, WorkingDir: `C:\Work`,
	}
	if err := domain.CheckLaunchTarget(ok); err != nil {
		t.Fatalf("valid row: %v", err)
	}

	for name, mutate := range map[string]func(*domain.LaunchableApp){
		"exe rewritten to UNC":       func(a *domain.LaunchableApp) { a.ExePath = `\\evil\share\a.exe` },
		"exe rewritten to non-exe":   func(a *domain.LaunchableApp) { a.ExePath = `C:\Windows\a.cmd` },
		"exe rewritten with dot-dot": func(a *domain.LaunchableApp) { a.ExePath = `C:\x\..\a.exe` },
		"too many args":              func(a *domain.LaunchableApp) { a.Args = repeatArgs(33, "a") },
		"working dir rewritten":      func(a *domain.LaunchableApp) { a.WorkingDir = `..\x` },
	} {
		t.Run(name, func(t *testing.T) {
			a := ok
			mutate(&a)
			err := domain.CheckLaunchTarget(a)
			if !errors.Is(err, domain.ErrAppNotFound) {
				t.Fatalf("err = %v, want ErrAppNotFound", err)
			}
			if got := domain.CodeOf(err); got.HTTPStatus() != 404 {
				t.Errorf("status = %d, want 404", got.HTTPStatus())
			}
		})
	}

	// 表示名は起動の安全に関わらない。
	a := ok
	a.Label = ""
	if err := domain.CheckLaunchTarget(a); err != nil {
		t.Errorf("label is not part of the launch check: %v", err)
	}
}

func TestParseArgsLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", []string{}},
		{"only newlines", "\n\r\n\n", []string{}},
		{"one", "a", []string{"a"}},
		{"lf", "a\nb\nc", []string{"a", "b", "c"}},
		{"crlf", "a\r\nb\r\n", []string{"a", "b"}},
		{"mixed", "a\r\nb\nc", []string{"a", "b", "c"}},
		{"blank lines dropped", "a\n\n\r\nb\n", []string{"a", "b"}},
		{"leading and trailing spaces kept", " a \n\tb", []string{" a ", "\tb"}},
		{"whitespace-only line kept", "a\n \nb", []string{"a", " ", "b"}},
		{"quotes kept as is", `--x="a b"` + "\n" + `'c'`, []string{`--x="a b"`, `'c'`}},
		{"japanese", "あ\r\nい", []string{"あ", "い"}},
		{"lone CR inside a line is data", "a\rb", []string{"a\rb"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domain.ParseArgsLines(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseArgsLines(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ParseArgsLines の結果は、そのまま ValidateAppInput に渡せる（空文字の引数を作らない）。
func TestParseArgsLinesFeedsValidation(t *testing.T) {
	in := validInput()
	in.Args = domain.ParseArgsLines("a\r\n\r\nb\n")
	if errs := domain.ValidateAppInput(in); errs != nil {
		t.Errorf("errs = %v", errs)
	}
}

func TestAppSummaryExposesOnlyIDAndLabel(t *testing.T) {
	a := domain.LaunchableApp{
		ID: "01JA", Label: "メモ帳", ExePath: `C:\Windows\notepad.exe`,
		Args: []string{"--secret"}, WorkingDir: `C:\Work`,
	}
	s := a.Summary()
	if s.ID != "01JA" || s.Label != "メモ帳" {
		t.Errorf("Summary = %+v", s)
	}
	// 型に ID と Label 以外が無い（パス・引数・作業ディレクトリが型に入る余地が無い）。
	typ := reflect.TypeOf(s)
	if typ.NumField() != 2 || typ.Field(0).Name != "ID" || typ.Field(1).Name != "Label" {
		t.Errorf("AppSummary fields = %v", typ)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"notepad", "secret", "Work"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("serialized summary %s contains %q", b, leak)
		}
	}
}

func TestMaxLaunchableApps(t *testing.T) {
	if domain.MaxLaunchableApps != 50 {
		t.Errorf("MaxLaunchableApps = %d, want 50 (NFR-C06)", domain.MaxLaunchableApps)
	}
}

func TestLaunchFailedError(t *testing.T) {
	for _, reason := range []domain.LaunchFailure{
		domain.LaunchFileMissing, domain.LaunchAccessDenied, domain.LaunchElevationRequired, domain.LaunchOther,
	} {
		t.Run(string(reason), func(t *testing.T) {
			err := domain.NewLaunchFailed(reason)
			if !errors.Is(err, domain.ErrLaunchFailed) || err.Code != domain.CodeLaunchFailed {
				t.Fatalf("err = %v", err)
			}
			if err.Code.HTTPStatus() != 409 {
				t.Errorf("status = %d, want 409", err.Code.HTTPStatus())
			}
			if got := err.Detail["reason"]; got != reason {
				t.Errorf("Detail[reason] = %v, want %v", got, reason)
			}
			if errors.Is(err, domain.ErrAppNotFound) {
				t.Error("launch_failed matched app_not_found")
			}
		})
	}
}

// reason は API 仕様 §2 の 4 値だけ。未知の値（パスや引数が紛れ込みうる文字列）は other にする。
func TestNewLaunchFailedNormalizesUnknownReason(t *testing.T) {
	err := domain.NewLaunchFailed(domain.LaunchFailure(`C:\secret\a.exe`))
	if got := err.Detail["reason"]; got != domain.LaunchOther {
		t.Errorf("Detail[reason] = %v, want other", got)
	}
	if strings.Contains(err.Error(), "secret") || len(err.Detail) != 1 {
		t.Errorf("error leaks input: %v %v", err, err.Detail)
	}
	if domain.ErrLaunchFailed.Detail != nil {
		t.Error("NewLaunchFailed mutated the shared sentinel")
	}
}

func TestLaunchFailureValid(t *testing.T) {
	for _, s := range []string{"file_missing", "access_denied", "elevation_required", "other"} {
		if !domain.LaunchFailure(s).Valid() {
			t.Errorf("%q is not valid", s)
		}
	}
	for _, s := range []string{"", "File_Missing", "unknown"} {
		if domain.LaunchFailure(s).Valid() {
			t.Errorf("%q should not be valid", s)
		}
	}
}
