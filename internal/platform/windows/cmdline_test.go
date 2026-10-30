package windows

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestBuildCommandLine(t *testing.T) {
	const exe = `C:\Program Files\App\app.exe`
	tests := []struct {
		name string
		exe  string
		args []string
		want string
	}{
		{"no args", exe, nil, `"C:\Program Files\App\app.exe"`},
		{"plain args stay unquoted", exe, []string{"-v", "--name=value", `C:\data\file.txt`}, `"C:\Program Files\App\app.exe" -v --name=value C:\data\file.txt`},
		{"empty arg is quoted", exe, []string{""}, `"C:\Program Files\App\app.exe" ""`},
		{"space", exe, []string{"hello world"}, `"C:\Program Files\App\app.exe" "hello world"`},
		{"tab", exe, []string{"a\tb"}, "\"C:\\Program Files\\App\\app.exe\" \"a\tb\""},
		{"newline", exe, []string{"a\nb"}, "\"C:\\Program Files\\App\\app.exe\" \"a\nb\""},
		{"quote", exe, []string{`say "hi"`}, `"C:\Program Files\App\app.exe" "say \"hi\""`},
		{"lone quote", exe, []string{`"`}, `"C:\Program Files\App\app.exe" "\""`},
		{"backslashes without space are literal", exe, []string{`a\\b\`}, `"C:\Program Files\App\app.exe" a\\b\`},
		{"backslash before quote is doubled", exe, []string{`a\"b`}, `"C:\Program Files\App\app.exe" "a\\\"b"`},
		{"trailing backslashes are doubled inside quotes", exe, []string{`dir with space\`}, `"C:\Program Files\App\app.exe" "dir with space\\"`},
		{"trailing double backslash", exe, []string{`x y\\`}, `"C:\Program Files\App\app.exe" "x y\\\\"`},
		{"japanese", `C:\アプリ\テスト.exe`, []string{"日本語 の 引数", "ファイル.txt"}, `"C:\アプリ\テスト.exe" "日本語 の 引数" ファイル.txt`},
		{"shell metacharacters pass through", exe, []string{"a&b", "%PATH%", "a|b", "a>b", "^x", "$(id)", "`id`", "a;b"}, `"C:\Program Files\App\app.exe" a&b %PATH% a|b a>b ^x $(id) ` + "`id`" + ` a;b`},
		{"shell metacharacters with space", exe, []string{"a & b", "100% > 5"}, `"C:\Program Files\App\app.exe" "a & b" "100% > 5"`},
		{"exe path ending in a backslash-free dir", `D:\tools\x.exe`, []string{"/c", "dir"}, `"D:\tools\x.exe" /c dir`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildCommandLine(tc.exe, tc.args)
			if err != nil {
				t.Fatalf("buildCommandLine: %v", err)
			}
			if got != tc.want {
				t.Errorf("command line = %s\nwant           = %s", got, tc.want)
			}
			// 不変条件: CommandLineToArgvW の規則で解釈すると、元の exe と引数に戻る。
			wantArgv := append([]string{tc.exe}, tc.args...)
			if argv := splitCommandLine(got); !reflect.DeepEqual(argv, wantArgv) {
				t.Errorf("round trip = %q, want %q", argv, wantArgv)
			}
		})
	}
}

func TestBuildCommandLineRoundTripsEveryCombinationOfSpecialCharacters(t *testing.T) {
	alphabet := []string{`\`, `"`, " ", "\t", "a", "&", "%", "あ"}
	var args []string
	var gen func(prefix string, n int)
	gen = func(prefix string, n int) {
		args = append(args, prefix)
		if n == 0 {
			return
		}
		for _, c := range alphabet {
			gen(prefix+c, n-1)
		}
	}
	gen("", 4)

	const exe = `C:\a b\x.exe`
	for _, a := range args {
		line, err := buildCommandLine(exe, []string{a, a})
		if err != nil {
			t.Fatalf("buildCommandLine(%q): %v", a, err)
		}
		if got, want := splitCommandLine(line), []string{exe, a, a}; !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip of %q: line %s parsed as %q", a, line, got)
		}
	}
}

func TestBuildCommandLineRejectsValuesThatCannotBeQuoted(t *testing.T) {
	tests := []struct {
		name string
		exe  string
		args []string
	}{
		{"empty exe", "", nil},
		{"quote in exe", `C:\a"b.exe`, nil},
		{"NUL in exe", "C:\\a\x00b.exe", nil},
		{"NUL in arg", `C:\a.exe`, []string{"ok", "a\x00b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			line, err := buildCommandLine(tc.exe, tc.args)
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
			if line != "" {
				t.Errorf("command line = %q, want empty on error", line)
			}
			for _, secret := range append([]string{tc.exe}, tc.args...) {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Errorf("error %q contains the path or an argument", err)
				}
			}
		})
	}
}

// splitCommandLine は CommandLineToArgvW（2008 以降の規則）の再実装。buildCommandLine の結果が元の値に戻ることの検査に使う。
// 先頭のトークンだけは、円記号をエスケープとして扱わず、先頭が二重引用符ならその次の二重引用符までを 1 つのトークンにする。
func splitCommandLine(line string) []string {
	var argv []string
	i := 0
	// argv[0]
	if strings.HasPrefix(line, `"`) {
		end := strings.IndexByte(line[1:], '"')
		argv = append(argv, line[1:1+end])
		i = 1 + end + 1
	} else {
		end := strings.IndexAny(line, " \t")
		if end < 0 {
			return []string{line}
		}
		argv = append(argv, line[:end])
		i = end
	}
	for {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			return argv
		}
		var arg strings.Builder
		inQuote := false
		for i < len(line) {
			c := line[i]
			if c == '\\' {
				n := 0
				for i < len(line) && line[i] == '\\' {
					n++
					i++
				}
				if i < len(line) && line[i] == '"' {
					arg.WriteString(strings.Repeat(`\`, n/2))
					if n%2 == 1 {
						arg.WriteByte('"')
						i++
					}
					continue
				}
				arg.WriteString(strings.Repeat(`\`, n))
				continue
			}
			if c == '"' {
				inQuote = !inQuote
				i++
				continue
			}
			if !inQuote && (c == ' ' || c == '\t') {
				break
			}
			arg.WriteByte(c)
			i++
		}
		argv = append(argv, arg.String())
	}
}
