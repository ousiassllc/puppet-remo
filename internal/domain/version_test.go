package domain

import (
	"errors"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"0.1.0", Version{Minor: 1}, true},
		{"1.2.3", Version{Major: 1, Minor: 2, Patch: 3}, true},
		{"65535.65535.65535", Version{Major: 65535, Minor: 65535, Patch: 65535}, true},
		{"0.1.1-nightly.20261102+abc1234", Version{Minor: 1, Patch: 1, Nightly: true, Date: "20261102", SHA: "abc1234"}, true},
		{"1.0.0-nightly.20260229+0123456789abcdef0123456789abcdef01234567", Version{}, false}, // 2026 年に 2/29 は無い
		{"dev", Version{}, false},
		{"", Version{}, false},
		{"v1.2.3", Version{}, false},
		{"1.2", Version{}, false},
		{"1.2.3.4", Version{}, false},
		{"01.2.3", Version{}, false},
		{"1.2.3-rc1", Version{}, false},
		{"65536.0.0", Version{}, false},
		{"1.2.3-nightly.20261102", Version{}, false},
		{"1.2.3-nightly.20261102+abc123", Version{}, false},  // sha が 7 桁未満
		{"1.2.3-nightly.20261102+ABC1234", Version{}, false}, // 大文字
		{"1.2.3-nightly.2026110+abc1234", Version{}, false},
		{"1.2.3-nightly.20261302+abc1234", Version{}, false}, // 13 月
		{"1.2.3-nightly.20261102.abc1234", Version{}, false}, // + ではなく .
		{" 1.2.3", Version{}, false},
		{"1.2.3\n", Version{}, false},
	}
	for _, tc := range tests {
		got, err := ParseVersion(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("ParseVersion(%q) error = %v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if !tc.ok {
			if !errors.Is(err, ErrInvalidVersion) || !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("ParseVersion(%q) error = %v, want ErrInvalidVersion (an invalid_argument)", tc.in, err)
			}
			continue
		}
		if got != tc.want {
			t.Errorf("ParseVersion(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
		if got.String() != tc.in {
			t.Errorf("ParseVersion(%q).String() = %q, want the input back", tc.in, got.String())
		}
	}
}

func TestParseCurrentVersionDev(t *testing.T) {
	if _, err := ParseCurrentVersion(DevVersion); !errors.Is(err, ErrNotUpdatable) {
		t.Errorf("ParseCurrentVersion(dev) error = %v, want ErrNotUpdatable", err)
	}
	if _, err := ParseCurrentVersion("garbage"); !errors.Is(err, ErrInvalidVersion) {
		t.Errorf("ParseCurrentVersion(garbage) error = %v, want ErrInvalidVersion", err)
	}
	if v, err := ParseCurrentVersion("0.1.0"); err != nil || v.Minor != 1 {
		t.Errorf("ParseCurrentVersion(0.1.0) = %+v, %v", v, err)
	}
}

func TestParseInstallerAsset(t *testing.T) {
	tests := []struct {
		name string
		want string // 版の文字列。空なら不正
	}{
		{"puppet-remo-0.1.0-amd64-installer.exe", "0.1.0"},
		{"puppet-remo-12.34.56-amd64-installer.exe", "12.34.56"},
		{"puppet-remo-0.1.1-nightly.20261102.abc1234-amd64-installer.exe", "0.1.1-nightly.20261102+abc1234"},
		{"puppet-remo-0.1.0-amd64-installer.exe.sha256", ""},
		{"puppet-remo-0.1.0-amd64-installer.exe.minisig", ""},
		{"puppet-remo-0.1.0-arm64-installer.exe", ""},
		{"puppet-remo-0.1.0-nightly.20261102-amd64-installer.exe", ""},
		{"../puppet-remo-0.1.0-amd64-installer.exe", ""},
		{"puppet-remo-0.1.0-amd64-installer.exe/", ""},
		{"puppet-remo-0.1.1-nightly.20261399.abc1234-amd64-installer.exe", ""},
		{"", ""},
	}
	for _, tc := range tests {
		v, err := ParseInstallerAsset(tc.name)
		if tc.want == "" {
			if !errors.Is(err, ErrInvalidVersion) {
				t.Errorf("ParseInstallerAsset(%q) = %v, %v; want ErrInvalidVersion", tc.name, v, err)
			}
			continue
		}
		if err != nil || v.String() != tc.want {
			t.Errorf("ParseInstallerAsset(%q) = %q, %v; want %q", tc.name, v, err, tc.want)
			continue
		}
		if v.InstallerAsset() != tc.name {
			t.Errorf("InstallerAsset() = %q, want %q (round trip)", v.InstallerAsset(), tc.name)
		}
	}
}

func TestVersionReleaseTag(t *testing.T) {
	stable := mustVersion(t, "1.2.3")
	nightly := mustVersion(t, "1.2.4-nightly.20261102+abc1234")
	if got := stable.ReleaseTag(); got != "v1.2.3" {
		t.Errorf("stable tag = %q", got)
	}
	if got := nightly.ReleaseTag(); got != "nightly-20261102-abc1234" {
		t.Errorf("nightly tag = %q", got)
	}
}

func mustVersion(t *testing.T, s string) Version {
	t.Helper()
	v, err := ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.2.3", "1.2.4", -1},
		{"1.10.0", "1.9.9", 1}, // 数として比べる（文字列ではない）
		{"2.0.0", "1.99.99", 1},
		// 同じ X.Y.Z: 正式版 > ナイトリー（semver の前リリース）
		{"1.2.3", "1.2.3-nightly.20261102+abc1234", 1},
		{"1.2.3-nightly.20261102+abc1234", "1.2.3", -1},
		// X.Y.Z が先に効く: 次の正式版向けのナイトリーは、現行の正式版より新しい
		{"1.2.4-nightly.20260101+abc1234", "1.2.3", 1},
		{"1.2.3-nightly.20261231+abc1234", "1.2.4", -1},
		// ナイトリー同士は日付。同じ日は sha に順序がないので同じ
		{"1.2.4-nightly.20261103+abc1234", "1.2.4-nightly.20261102+fff0000", 1},
		{"1.2.4-nightly.20261102+abc1234", "1.2.4-nightly.20261103+fff0000", -1},
		{"1.2.4-nightly.20261102+abc1234", "1.2.4-nightly.20261102+fff0000", 0},
		{"1.2.4-nightly.20261102+abc1234", "1.2.4-nightly.20261102+abc1234", 0},
		// 日付より X.Y.Z が先
		{"1.3.0-nightly.20260101+abc1234", "1.2.9-nightly.20261231+abc1234", 1},
	}
	for _, tc := range tests {
		a, b := mustVersion(t, tc.a), mustVersion(t, tc.b)
		if got := CompareVersions(a, b); got != tc.want {
			t.Errorf("CompareVersions(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := CompareVersions(b, a); got != -tc.want {
			t.Errorf("CompareVersions(%s, %s) = %d, want %d (antisymmetry)", tc.b, tc.a, got, -tc.want)
		}
	}
}

func TestDecideUpdate(t *testing.T) {
	tests := []struct {
		name      string
		ch        Channel
		cur, cand string
		want      UpdateDecision
	}{
		{"stable: newer patch", ChannelStable, "0.1.0", "0.1.1", UpdateAvailable},
		{"stable: newer minor", ChannelStable, "0.1.9", "0.2.0", UpdateAvailable},
		{"stable: same", ChannelStable, "0.1.1", "0.1.1", UpdateSame},
		{"stable: older is a downgrade", ChannelStable, "0.1.1", "0.1.0", UpdateDowngrade},
		{"stable: much older is a downgrade", ChannelStable, "1.0.0", "0.9.9", UpdateDowngrade},
		{"stable: nightly candidate is off channel", ChannelStable, "0.1.0", "0.1.1-nightly.20261102+abc1234", UpdateOffChannel},
		{"stable: nightly to the stable of the same core", ChannelStable, "0.1.1-nightly.20261102+abc1234", "0.1.1", UpdateAvailable},
		{"stable: nightly to a newer stable", ChannelStable, "0.1.1-nightly.20261102+abc1234", "0.1.2", UpdateAvailable},
		{"stable: nightly user is not pulled back to an older stable", ChannelStable, "0.2.0-nightly.20261102+abc1234", "0.1.5", UpdateDowngrade},
		{"nightly: newer date", ChannelNightly, "0.1.1-nightly.20261102+abc1234", "0.1.1-nightly.20261103+def5678", UpdateAvailable},
		{"nightly: same date is not newer", ChannelNightly, "0.1.1-nightly.20261102+abc1234", "0.1.1-nightly.20261102+def5678", UpdateSame},
		{"nightly: older date is a downgrade", ChannelNightly, "0.1.1-nightly.20261103+abc1234", "0.1.1-nightly.20261102+def5678", UpdateDowngrade},
		{"nightly: from stable to the nightly of the next core", ChannelNightly, "0.1.0", "0.1.1-nightly.20261102+abc1234", UpdateAvailable},
		{"nightly: a stable is never replaced by an older nightly", ChannelNightly, "0.1.1", "0.1.1-nightly.20261102+abc1234", UpdateDowngrade},
		{"nightly: a stable is never replaced by a nightly of an older core", ChannelNightly, "0.2.0", "0.1.9-nightly.20261102+abc1234", UpdateDowngrade},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecideUpdate(tc.ch, mustVersion(t, tc.cur), mustVersion(t, tc.cand)); got != tc.want {
				t.Errorf("DecideUpdate(%s, %s, %s) = %s, want %s", tc.ch, tc.cur, tc.cand, got, tc.want)
			}
		})
	}
}

func TestChannelValid(t *testing.T) {
	for c, want := range map[Channel]bool{ChannelStable: true, ChannelNightly: true, "": false, "beta": false, "Stable": false} {
		if got := c.Valid(); got != want {
			t.Errorf("Channel(%q).Valid() = %v, want %v", c, got, want)
		}
	}
}
