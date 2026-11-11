package licenses_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/licenses"
)

// 同梱するフォント・アイコンのライセンスの全文（OFL・MIT）が入っている。
func TestThirdPartyNoticesHaveBundledFontsAndIcons(t *testing.T) {
	text := licenses.ThirdPartyNotices
	for _, want := range []string{
		"SIL OPEN FONT LICENSE Version 1.1", "The Sora Project Authors", "The JetBrains Mono Project Authors",
		"Reserved Font Name 'Source'", "Noto Sans JP", "Iconoir", "Copyright (c) 2021 Luca Burgio",
		"Permission is hereby granted, free of charge", "tailwindcss", "htmx.org", "Go 標準ライブラリ",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("THIRD_PARTY_NOTICES.txt does not contain %q", want)
		}
	}
}

// static/package.json の実行時の依存（フォント・htmx・Lit）を足したのに表記を作り直し忘れると落ちる。
func TestThirdPartyNoticesCoverFrontendDependencies(t *testing.T) {
	raw, err := os.ReadFile("../static/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		DevDependencies map[string]string `json:"devDependencies"`
		Dependencies    map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	bundled := make([]string, 0, len(pkg.Dependencies)+4)
	for name := range pkg.Dependencies {
		bundled = append(bundled, name)
	}
	for name := range pkg.DevDependencies {
		if strings.HasPrefix(name, "@fontsource-variable/") || name == "tailwindcss" {
			bundled = append(bundled, name)
		}
	}
	if len(bundled) == 0 {
		t.Fatal("no bundled frontend dependencies found in static/package.json")
	}
	for _, name := range bundled {
		if !strings.Contains(licenses.ThirdPartyNotices, name) {
			t.Errorf("THIRD_PARTY_NOTICES.txt does not mention %q; run `cd static && bun scripts/third-party-notices.mjs`", name)
		}
	}
}
