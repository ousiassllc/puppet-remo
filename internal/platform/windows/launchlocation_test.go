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
		{"lower-case drive letter, forward slashes and case together", `c:/tools/A.EXE`, `\\?\C:\Tools\a.exe`, true},
		{"final with a lower-case drive letter", `C:\Tools\a.exe`, `\\?\c:\Tools\a.exe`, true},
		{"Japanese path", `C:\ツール\あぷり.exe`, `\\?\C:\ツール\あぷり.exe`, true},
		{"non-ASCII case folding", `C:\ÜNI\a.exe`, `\\?\C:\üni\a.exe`, true},
		{"path with spaces", `C:\Program Files\App Dir\a.exe`, `\\?\C:\Program Files\App Dir\a.exe`, true},

		{"symlinked directory resolves elsewhere", `C:\Link\a.exe`, `\\?\C:\Real\a.exe`, false},
		{"symlinked file resolves elsewhere", `C:\Tools\a.exe`, `\\?\C:\Tools\b.exe`, false},
		{"subst drive resolves to the underlying path", `X:\a.exe`, `\\?\C:\Tools\a.exe`, false},
		{"mapped drive resolves to UNC", `Z:\a.exe`, `\\?\UNC\server\share\a.exe`, false},
		{"volume GUID path", `C:\a.exe`, `\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}\a.exe`, false},
		{"8.3 short name does not match (fail closed)", `C:\PROGRA~1\a.exe`, `\\?\C:\Program Files\a.exe`, false},
		{"8.3 short file name does not match (fail closed)", `C:\Tools\LONGAP~1.EXE`, `\\?\C:\Tools\long application name.exe`, false},
		{"UNC registration resolved to the same UNC", `\\server\share\a.exe`, `\\?\UNC\server\share\a.exe`, false},
		{"UNC registration against a drive-letter path", `\\server\share\a.exe`, `\\?\C:\share\a.exe`, false},
		{`\\?\-prefixed registration does not match (fail closed)`, `\\?\C:\Tools\a.exe`, `\\?\C:\Tools\a.exe`, false},
		{"dot segments in the registration do not match (fail closed)", `C:\Tools\..\Tools\a.exe`, `\\?\C:\Tools\a.exe`, false},
		{`.\-segment in the registration (fail closed)`, `C:\Tools\.\a.exe`, `\\?\C:\Tools\.\a.exe`, false},
		{`..\-segment in the registration, same spelling as final (fail closed)`, `C:\a\..\b\x.exe`, `\\?\C:\a\..\b\x.exe`, false},
		{`..\-segment in the registration that resolves to final (fail closed)`, `C:\a\..\b\x.exe`, `\\?\C:\b\x.exe`, false},
		{"..-segment with forward slashes (fail closed)", `C:/a/../b/x.exe`, `\\?\C:\b\x.exe`, false},
		{"..-segment with forward slashes, same spelling (fail closed)", `C:/a/../b/x.exe`, `\\?\C:\a\..\b\x.exe`, false},
		{"trailing .. segment (fail closed)", `C:\Tools\..`, `\\?\C:\Tools\..`, false},
		{"trailing . segment (fail closed)", `C:\Tools\.`, `\\?\C:\Tools\.`, false},
		{`.\-segment in final only (fail closed)`, `C:\Tools\a.exe`, `\\?\C:\Tools\.\a.exe`, false},
		{`..\-segment in final only (fail closed)`, `C:\Tools\a.exe`, `\\?\C:\Tools\x\..\a.exe`, false},
		{`\\.\-prefixed registration (fail closed)`, `\\.\C:\Tools\a.exe`, `\\?\C:\Tools\a.exe`, false},
		{`\\.\-prefixed registration, same spelling as final (fail closed)`, `\\.\C:\Tools\a.exe`, `\\.\C:\Tools\a.exe`, false},
		{"//?/-prefixed registration (fail closed)", `//?/C:/Tools/a.exe`, `\\?\C:\Tools\a.exe`, false},
		{"//./-prefixed registration (fail closed)", `//./C:/Tools/a.exe`, `\\?\C:\Tools\a.exe`, false},
		{`\\?\-prefixed registration against a drive-letter final (fail closed)`, `\\?\C:\Tools\a.exe`, `\\?\C:\Tools\a.exe`, false},
		{`doubled \\?\ in final (fail closed)`, `C:\Tools\a.exe`, `\\?\\\?\C:\Tools\a.exe`, false},
		{"names that only look like dot segments are allowed", `C:\Tools\.hidden\a..b\...\a.exe`, `\\?\C:\Tools\.hidden\a..b\...\a.exe`, true},
		{"other drive letter", `D:\Tools\a.exe`, `\\?\C:\Tools\a.exe`, false},
		{"final is a GLOBALROOT device path", `C:\Tools\a.exe`, `\\?\GLOBALROOT\Device\HarddiskVolume3\Tools\a.exe`, false},
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

func TestHasDevicePrefix(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{`\\?\C:\a.exe`, true}, {`\\.\C:\a.exe`, true}, {`//?/C:/a.exe`, true}, {`//./C:/a.exe`, true}, {`\/?\C:\a.exe`, true},
		{`\\server\share\a.exe`, false}, {`C:\a.exe`, false}, {`\?\C:\a.exe`, false}, {`\\?`, false}, {`\\?x`, false}, {``, false},
	}
	for _, tt := range tests {
		if got := hasDevicePrefix(tt.in); got != tt.want {
			t.Errorf("hasDevicePrefix(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestHasDotSegment(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{`C:\a\..\b`, true}, {`C:\a\.\b`, true}, {`C:/a/../b`, true}, {`C:/a/./b`, true}, {`C:\a\..`, true}, {`C:\a\.`, true},
		{`..\a`, true}, {`.`, true}, {`C:\a/..\b`, true},
		{`C:\a\b.exe`, false}, {`C:\.a\b`, false}, {`C:\a.\b`, false}, {`C:\...\b`, false}, {`C:\a..b\c`, false}, {`C:\a\`, false}, {``, false},
	}
	for _, tt := range tests {
		if got := hasDotSegment(tt.in); got != tt.want {
			t.Errorf("hasDotSegment(%q) = %v, want %v", tt.in, got, tt.want)
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
		{`\\?\C:\`, `C:\`, true},
		{`\\?\C:a.exe`, ``, false},
		{`\\?\1:\a.exe`, ``, false},
		{`\\?\GLOBALROOT\Device\HarddiskVolume3\a.exe`, ``, false},
		{``, ``, false},
	}
	for _, tt := range tests {
		got, ok := driveLetterPath(tt.final)
		if got != tt.want || ok != tt.ok {
			t.Errorf("driveLetterPath(%q) = %q, %v, want %q, %v", tt.final, got, ok, tt.want, tt.ok)
		}
	}
}

func TestNormalizeSeparators(t *testing.T) {
	tests := []struct{ in, want string }{
		{`C:\Tools\a.exe`, `C:\Tools\a.exe`},
		{`C:/Tools/a.exe`, `C:\Tools\a.exe`},
		{`C:\\Tools//a.exe`, `C:\Tools\a.exe`},
		{`C:\Tools\`, `C:\Tools\`},
		{`C:/Tools/ツール/a.exe`, `C:\Tools\ツール\a.exe`},
		{``, ``},
	}
	for _, tt := range tests {
		if got := normalizeSeparators(tt.in); got != tt.want {
			t.Errorf("normalizeSeparators(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
