package domain

import "testing"

// 期待値の根拠: Windows の大文字小文字を無視した比較（.NET の StringComparison.OrdinalIgnoreCase・NTFS の $UpCase 表）は、
// 1 文字を 1 文字へ大文字にする単純な写像で両辺をそろえて比べ、ASCII 以外の文字は ASCII へ写さない（dotnet/runtime#32247）。
// strings.EqualFold（Unicode の等価類）とずれる行には「EqualFold とずれる」と書いた（Issue #183）。
func TestEqualOrdinalIgnoreCase(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"ASCII, same", "cmd.exe", "cmd.exe", true},
		{"ASCII, case differs", "cmd.exe", "CMD.EXE", true},
		{"ASCII, different", "cmd.exe", "cmd2.exe", false},
		{"ASCII, prefix", "cmd", "cmd.exe", false},
		{"both empty", "", "", true},
		{"one empty", "", "a", false},
		{"Latin-1 letters", "é.exe", "É.EXE", true},
		{"Cyrillic", "прогр.exe", "ПРОГР.EXE", true},

		// ß / ẞ: どちらも大文字化してもそのまま。別の文字（EqualFold とずれる）
		{"sharp s, same", "ß.exe", "ß.exe", true},
		{"sharp s vs capital sharp s", "ß.exe", "ẞ.exe", false},
		{"sharp s vs SS", "ß.exe", "SS.exe", false},

		// İ / ı: ASCII の i・I とは別（ı は ASCII へ写さない）
		{"dotted capital I vs i", "İ.exe", "i.exe", false},
		{"dotted capital I vs I", "İ.exe", "I.exe", false},
		{"dotted capital I, same", "İ.exe", "İ.exe", true},
		{"dotless i vs I", "ı.exe", "I.exe", false},
		{"dotless i vs i", "ı.exe", "i.exe", false},
		{"dotless i vs dotted capital I", "ı.exe", "İ.exe", false},
		{"dotless i, same", "ı.exe", "ı.exe", true},

		// Σ / ς / σ: すべて Σ になるので同じ
		{"sigma vs final sigma", "σ.exe", "ς.exe", true},
		{"capital sigma vs sigma", "Σ.exe", "σ.exe", true},
		{"capital sigma vs final sigma", "Σ.exe", "ς.exe", true},
		{"sigma vs Latin s", "σ.exe", "s.exe", false},

		// KELVIN SIGN（U+212A）: 大文字化してもそのまま。k・K とは別（EqualFold とずれる）
		{"kelvin sign vs k", "\u212A.exe", "k.exe", false},
		{"kelvin sign vs K", "\u212A.exe", "K.exe", false},
		{"kelvin sign, same", "\u212A.exe", "\u212A.exe", true},
		{"k vs K", "k.exe", "K.exe", true},

		// ſ（U+017F LONG S）は s・S とは別（OrdinalIgnoreCase は "s" と "ſ" を別とする。EqualFold とずれる）
		{"long s vs s", "ſ.exe", "s.exe", false},
		{"long s vs S", "ſ.exe", "S.exe", false},
		{"long s, same", "ſ.exe", "ſ.exe", true},

		// ǅ（U+01C5 titlecase）・ǆ・Ǆ: どれも Ǆ（U+01C4）になるので同じ
		{"titlecase DZ vs lower", "ǅ.exe", "ǆ.exe", true},
		{"titlecase DZ vs upper", "ǅ.exe", "Ǆ.exe", true},
		{"lower dz vs upper", "ǆ.exe", "Ǆ.exe", true},
		{"titlecase DZ vs plain dz letters", "ǅ.exe", "dz.exe", false},

		// BMP の外: Deseret 𐐨（U+10428）は 𐐀（U+10400）の小文字。畳む（Windows が別物とする側に倒れるだけで取りこぼさない）
		{"supplementary letters", "\U00010428.exe", "\U00010400.exe", true},

		// 不正な UTF-8 は決められないので false（MatchDenied は、これを当たったものとして拒否する）
		{"invalid UTF-8, same bytes", "a\xffb", "a\xffb", false},
		{"invalid UTF-8 on one side", "a\xffb", "a\uFFFDb", false},
		{"encoded surrogate", "a\xed\xa0\x80", "a\xed\xa0\x80", false},
	}
	for _, tt := range tests {
		if got := EqualOrdinalIgnoreCase(tt.a, tt.b); got != tt.want {
			t.Errorf("%s: EqualOrdinalIgnoreCase(%q, %q) = %v, want %v", tt.name, tt.a, tt.b, got, tt.want)
		}
		if got := EqualOrdinalIgnoreCase(tt.b, tt.a); got != tt.want {
			t.Errorf("%s: EqualOrdinalIgnoreCase(%q, %q) = %v, want %v (symmetry)", tt.name, tt.b, tt.a, got, tt.want)
		}
	}
}

// MatchDenied は、Windows の OrdinalIgnoreCase の結果どおりに当たる・当たらないを決める。不正な UTF-8 は当たったものとして拒否する（fail closed）。
func TestMatchDeniedUsesOrdinalIgnoreCase(t *testing.T) {
	name := func(v string) []DeniedApp { return []DeniedApp{{Kind: DeniedName, Value: v}} }
	path := func(v string) []DeniedApp { return []DeniedApp{{Kind: DeniedPath, Value: v}} }
	tests := []struct {
		name      string
		entries   []DeniedApp
		requested string
		resolved  string
		want      bool
	}{
		{"name, sharp s differs from capital sharp s", name("ß.exe"), `C:\T\ẞ.exe`, `C:\T\ẞ.exe`, false},
		{"name, sigma variants", name("σ.exe"), `C:\T\Σ.exe`, `C:\T\Σ.exe`, true},
		{"name, final sigma matches capital sigma", name("ς.exe"), `C:\T\Σ.exe`, `C:\T\Σ.exe`, true},
		{"name, kelvin sign is not k", name("\u212A.exe"), `C:\T\k.exe`, `C:\T\k.exe`, false},
		{"name, k is not kelvin sign", name("k.exe"), "C:\\T\\\u212A.exe", "C:\\T\\\u212A.exe", false},
		{"name, dotless i is not I", name("ı.exe"), `C:\T\I.exe`, `C:\T\I.exe`, false},
		{"name, dotted capital I is not i", name("İ.exe"), `C:\T\i.exe`, `C:\T\i.exe`, false},
		{"name, titlecase dz matches lower dz", name("ǅ.exe"), `C:\T\ǆ.exe`, `C:\T\ǆ.exe`, true},
		{"name, matches through the requested spelling", name("σ.exe"), `C:\T\Σ.exe`, `C:\T\other.exe`, true},
		{"path, sigma variants in a folder", path(`C:\Σ\a.exe`), `C:\σ\a.exe`, `C:\σ\a.exe`, true},
		{"path, kelvin sign folder is not k", path("C:\\\u212A\\a.exe"), `C:\k\a.exe`, `C:\k\a.exe`, false},
		{"path, sharp s folder is not capital sharp s", path(`C:\ß\a.exe`), `C:\ẞ\a.exe`, `C:\ẞ\a.exe`, false},

		{"fail closed, invalid UTF-8 in resolved", name("cmd.exe"), `C:\T\a.exe`, "C:\\T\\a\xff.exe", true},
		{"fail closed, invalid UTF-8 in requested", name("cmd.exe"), "C:\\T\\a\xff.exe", `C:\T\a.exe`, true},
		{"fail closed, invalid UTF-8 in a name entry", name("a\xff.exe"), `C:\T\a.exe`, `C:\T\a.exe`, true},
		{"fail closed, invalid UTF-8 in a path entry", path("C:\\T\\a\xff.exe"), `C:\T\a.exe`, `C:\T\a.exe`, true},
		{"an empty denylist denies nothing even for invalid UTF-8", nil, "a\xff", "a\xff", false},
	}
	for _, tt := range tests {
		if got := MatchDenied(tt.entries, tt.requested, tt.resolved); got != tt.want {
			t.Errorf("%s: MatchDenied(%q → %q) = %v, want %v", tt.name, tt.requested, tt.resolved, got, tt.want)
		}
	}
}
