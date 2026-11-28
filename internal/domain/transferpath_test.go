package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestCleanTransferPathAccepts(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a.txt", "a.txt"},
		{"logs/app.log", "logs/app.log"},
		{`logs\app.log`, "logs/app.log"},
		{"build/v1.2.3/Nefos Veil Setup.exe", "build/v1.2.3/Nefos Veil Setup.exe"},
		{"日本語/ファイル.txt", "日本語/ファイル.txt"},
		{".hidden", ".hidden"},
		{"a..b", "a..b"},
		{"console.txt", "console.txt"},
		{"com10", "com10"},
		{"a~1.txt", "a~1.txt"},
	}
	for _, tt := range tests {
		got, err := domain.CleanTransferPath(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("CleanTransferPath(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestCleanTransferPathRejects(t *testing.T) {
	tests := map[string]string{
		"empty":                "",
		"dot":                  ".",
		"dotdot":               "..",
		"dotdot prefix":        "../a.txt",
		"dotdot middle":        "a/../../b.txt",
		"dotdot backslash":     `a\..\..\b.txt`,
		"dotdot tail":          "a/..",
		"triple dot":           ".../a",
		"dotdot with space":    ".. /a",
		"absolute posix":       "/etc/passwd",
		"absolute backslash":   `\Windows\win.ini`,
		"drive":                `C:\Windows\win.ini`,
		"drive slash":          "C:/Windows/win.ini",
		"drive relative":       "C:foo.txt",
		"unc":                  `\\host\share\a.txt`,
		"unc slash":            "//host/share/a.txt",
		"device question":      `\\?\C:\a.txt`,
		"device dot":           `\\.\pipe\x`,
		"stream":               "a.txt:hidden",
		"stream data":          "a.txt::$DATA",
		"stream in dir":        "dir:x/a.txt",
		"empty segment":        "a//b",
		"trailing slash":       "a/",
		"trailing dot":         "a.",
		"trailing space":       "a ",
		"trailing dot in dir":  "a./b",
		"reserved con":         "CON",
		"reserved lower":       "nul",
		"reserved ext":         "aux.txt",
		"reserved in dir":      "dir/COM1/a",
		"reserved lpt":         "lpt9.log",
		"reserved superscript": "COM¹",
		"reserved conout":      "CONOUT$",
		"wildcard":             "a*.txt",
		"question":             "a?.txt",
		"pipe":                 "a|b",
		"angle":                "<a>",
		"quote":                `a"b`,
		"nul":                  "a\x00b",
		"control":              "a\nb",
		"del":                  "a\x7fb",
		"invalid utf8":         "a\xffb",
		"too long":             strings.Repeat("a/", 101),
		"long element":         strings.Repeat("a", 256),
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := domain.CleanTransferPath(in)
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("CleanTransferPath(%q) = %q, %v; want invalid_argument", in, got, err)
			}
			if len(in) >= 8 && strings.Contains(err.Error(), in) {
				t.Errorf("the error echoes the path: %v", err)
			}
		})
	}
}

func TestSplitTransferPath(t *testing.T) {
	got, err := domain.SplitTransferPath(`a\b/c.txt`)
	if err != nil || strings.Join(got, "|") != "a|b|c.txt" {
		t.Fatalf("SplitTransferPath = %v, %v", got, err)
	}
	if _, err := domain.SplitTransferPath("../x"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTransferRoot(t *testing.T) {
	good := []string{"/srv/inbox", "/home/qa/PuppetRemo/inbox"}
	for _, r := range good {
		if err := domain.ValidateTransferRoot(r); err != nil {
			t.Errorf("ValidateTransferRoot(%q) = %v", r, err)
		}
	}
	bad := map[string]string{
		"empty":      "",
		"relative":   "inbox",
		"dot":        ".",
		"parent":     "../inbox",
		"unc":        `\\host\share\inbox`,
		"unc slash":  "//host/share/inbox",
		"device":     `\\?\C:\inbox`,
		"drive root": "/",
		"unclean":    "/srv/../inbox",
		"trailing":   "/srv/inbox/",
		"control":    "/srv/in\nbox",
		"nul":        "/srv/in\x00box",
		"too long":   "/" + strings.Repeat("a", 250),
	}
	for name, r := range bad {
		if err := domain.ValidateTransferRoot(r); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: ValidateTransferRoot(%q) = %v", name, r, err)
		}
	}
}
