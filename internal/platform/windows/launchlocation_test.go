package windows

import "testing"

func TestIsLocalFixedDrive(t *testing.T) {
	// GetDriveType の戻り値: 0 不明・1 ルートなし・2 リムーバブル・3 固定・4 ネットワーク・5 CD-ROM・6 RAM ディスク。
	tests := []struct {
		driveType uint32
		want      bool
	}{
		{0, false}, {1, false}, {2, false}, {3, true}, {4, false}, {5, false}, {6, false},
	}
	for _, tt := range tests {
		if got := isLocalFixedDrive(tt.driveType); got != tt.want {
			t.Errorf("isLocalFixedDrive(%d) = %v, want %v", tt.driveType, got, tt.want)
		}
	}
}

func TestFinalPathMatches(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		final     string
		want      bool
	}{
		{"same path", `C:\Tools\a.exe`, `\\?\C:\Tools\a.exe`, true},
		{"case differs", `c:\tools\A.EXE`, `\\?\C:\Tools\a.exe`, true},
		{"forward slashes", `C:/Tools/a.exe`, `\\?\C:\Tools\a.exe`, true},
		{"doubled separators", `C:\\Tools\\a.exe`, `\\?\C:\Tools\a.exe`, true},

		{"symlinked directory resolves elsewhere", `C:\Link\a.exe`, `\\?\C:\Real\a.exe`, false},
		{"symlinked file resolves elsewhere", `C:\Tools\a.exe`, `\\?\C:\Tools\b.exe`, false},
		{"subst drive resolves to the underlying path", `X:\a.exe`, `\\?\C:\Tools\a.exe`, false},
		{"mapped drive resolves to UNC", `Z:\a.exe`, `\\?\UNC\server\share\a.exe`, false},
		{"volume GUID path", `C:\a.exe`, `\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}\a.exe`, false},
		{"8.3 short name does not match (fail closed)", `C:\PROGRA~1\a.exe`, `\\?\C:\Program Files\a.exe`, false},
		{"final without the device prefix", `C:\Tools\a.exe`, `C:\Tools\a.exe`, false},
		{"final is a device path", `C:\Tools\a.exe`, `\\.\C:\Tools\a.exe`, false},
		{"final is too short", `C:\Tools\a.exe`, `\\?\C:`, false},
		{"empty final", `C:\Tools\a.exe`, ``, false},
		{"prefix is a prefix of the other path", `C:\Tools\a.exe`, `\\?\C:\Tools\a.exe.bak`, false},
	}
	for _, tt := range tests {
		if got := finalPathMatches(tt.requested, tt.final); got != tt.want {
			t.Errorf("%s: finalPathMatches(%q, %q) = %v, want %v", tt.name, tt.requested, tt.final, got, tt.want)
		}
	}
}

func TestDriveLetterPath(t *testing.T) {
	tests := []struct {
		final string
		want  string
		ok    bool
	}{
		{`\\?\C:\Windows\System32\cmd.exe`, `C:\Windows\System32\cmd.exe`, true},
		{`\\?\d:\a.exe`, `d:\a.exe`, true},
		{`\\?\UNC\server\share\a.exe`, ``, false},
		{`\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}\a.exe`, ``, false},
		{`C:\a.exe`, ``, false},
		{`\\.\C:\a.exe`, ``, false},
		{`\\?\C:`, ``, false},
		{``, ``, false},
	}
	for _, tt := range tests {
		got, ok := driveLetterPath(tt.final)
		if got != tt.want || ok != tt.ok {
			t.Errorf("driveLetterPath(%q) = %q, %v, want %q, %v", tt.final, got, ok, tt.want, tt.ok)
		}
	}
}
