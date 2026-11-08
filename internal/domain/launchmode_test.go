package domain

import (
	"strings"
	"testing"
)

func TestLaunchModeValid(t *testing.T) {
	for m, want := range map[LaunchMode]bool{
		LaunchModeAllowlist: true, LaunchModeDenylist: true, "": false, "Allowlist": false, "blacklist": false, "whitelist": false,
	} {
		if got := m.Valid(); got != want {
			t.Errorf("LaunchMode(%q).Valid() = %v, want %v", m, got, want)
		}
	}
}

func TestClassifyDenied(t *testing.T) {
	tests := []struct {
		in       string
		wantKind DeniedKind
		wantOK   bool
	}{
		{`cmd.exe`, DeniedName, true},
		{`CMD.EXE`, DeniedName, true},
		{`power shell.exe`, DeniedName, true},
		{`C:\Windows\System32\cmd.exe`, DeniedPath, true},
		{`c:/windows/system32/cmd.exe`, DeniedPath, true},

		{``, DeniedName, false},
		{`cmd`, DeniedName, false},
		{`.exe`, DeniedName, false},
		{`cmd.exe `, DeniedName, false},
		{`con.exe`, DeniedName, false},
		{`a*.exe`, DeniedName, false},
		{`a?.exe`, DeniedName, false},
		{`a<b.exe`, DeniedName, false},
		{`sub\cmd.exe`, DeniedPath, false},
		{`..\cmd.exe`, DeniedPath, false},
		{`\\server\share\cmd.exe`, DeniedPath, false},
		{`\\?\C:\cmd.exe`, DeniedPath, false},
		{`C:\Windows\cmd.com`, DeniedPath, false},
		{`C:\..\cmd.exe`, DeniedPath, false},
		{`C:\a.exe:stream`, DeniedPath, false},
		{`cmd.exe:stream`, DeniedPath, false},
		{`C:cmd.exe`, DeniedPath, false},
	}
	for _, tt := range tests {
		kind, msg := ClassifyDenied(tt.in)
		if (msg == "") != tt.wantOK {
			t.Errorf("ClassifyDenied(%q) msg = %q, want ok=%v", tt.in, msg, tt.wantOK)
			continue
		}
		if tt.wantOK && kind != tt.wantKind {
			t.Errorf("ClassifyDenied(%q) kind = %q, want %q", tt.in, kind, tt.wantKind)
		}
		if msg != "" && tt.in != "" && len(tt.in) > 8 && strings.Contains(msg, tt.in) {
			t.Errorf("ClassifyDenied(%q) message echoes the input: %q", tt.in, msg)
		}
	}
}

func TestValidateLaunchPath(t *testing.T) {
	for p, want := range map[string]bool{
		`C:\Tools\a.exe`: true, `d:/x/A.EXE`: true,
		``: false, `a.exe`: false, `\Tools\a.exe`: false, `C:\Tools\a.bat`: false, `C:\Tools\`: false,
		`\\host\share\a.exe`: false, `\\?\C:\a.exe`: false, `C:\a\..\b.exe`: false, `C:\a.exe:x`: false, `C:\NUL\a.exe`: false,
	} {
		if got := ValidateLaunchPath(p); got != want {
			t.Errorf("ValidateLaunchPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestDeniedKeyIgnoresCaseAndSeparators(t *testing.T) {
	if DeniedKey(DeniedPath, `C:\Tools\A.exe`) != DeniedKey(DeniedPath, `c:/tools//a.EXE`) {
		t.Error("path keys must ignore case and separator spelling")
	}
	if DeniedKey(DeniedName, `cmd.exe`) != DeniedKey(DeniedName, `CMD.EXE`) {
		t.Error("name keys must ignore case")
	}
	if DeniedKey(DeniedName, `cmd.exe`) == DeniedKey(DeniedPath, `cmd.exe`) {
		t.Error("the same text under different kinds must be different keys")
	}
}

// 拒否リストの照合: 実体のパス（解決済み）・名前・大文字小文字・指定の綴りの名前（すり抜け対策）。
func TestMatchDenied(t *testing.T) {
	const real = `C:\Windows\System32\cmd.exe`
	byPath := []DeniedApp{{Kind: DeniedPath, Value: real}}
	byName := []DeniedApp{{Kind: DeniedName, Value: "cmd.exe"}}
	tests := []struct {
		name      string
		entries   []DeniedApp
		requested string
		resolved  string
		want      bool
	}{
		{"path entry, same path", byPath, real, real, true},
		{"path entry, case differs", byPath, `c:\windows\SYSTEM32\CMD.EXE`, `C:\WINDOWS\system32\Cmd.Exe`, true},
		{"path entry, separators differ", byPath, `C:/Windows//System32/cmd.exe`, real, true},
		{"path entry, symlink resolves to the denied file", byPath, `C:\Users\me\Link\run.exe`, real, true},
		{"path entry, junction in the directory", byPath, `C:\Junction\cmd.exe`, real, true},
		{"path entry, 8.3 short name resolves to the denied file", byPath, `C:\WINDOW~1\SYSTEM~1\CMD~1.EXE`, real, true},
		{"path entry, a different file is not denied", byPath, `C:\Tools\ok.exe`, `C:\Tools\ok.exe`, false},
		{"path entry, same name in another folder is not denied", byPath, `C:\Tools\cmd.exe`, `C:\Tools\cmd.exe`, false},
		{"path entry, a longer path with the same prefix", byPath, real + `.bak`, real + `.bak`, false},
		{"name entry, any folder", byName, `C:\Tools\cmd.exe`, `C:\Tools\cmd.exe`, true},
		{"name entry, case differs", byName, `C:\Tools\CMD.EXE`, `C:\Tools\CMD.EXE`, true},
		{"name entry, resolved name matches though the link is named otherwise", byName, `C:\Tools\totally-fine.exe`, real, true},
		{"name entry, requested name matches though the target is named otherwise", byName, `C:\Tools\cmd.exe`, `C:\Tools\harmless.exe`, true},
		{"name entry, other names are not denied", byName, `C:\Tools\cmd2.exe`, `C:\Tools\cmd2.exe`, false},
		{"name entry, a folder named like the exe is not a match", byName, `C:\cmd.exe\other.exe`, `C:\cmd.exe\other.exe`, false},
		{"empty denylist", nil, real, real, false},
		{"both kinds, second matches", append(append([]DeniedApp{}, byPath...), byName...), `C:\x\cmd.exe`, `C:\x\cmd.exe`, true},
	}
	for _, tt := range tests {
		if got := MatchDenied(tt.entries, tt.requested, tt.resolved); got != tt.want {
			t.Errorf("%s: MatchDenied(%q → %q) = %v, want %v", tt.name, tt.requested, tt.resolved, got, tt.want)
		}
	}
}
