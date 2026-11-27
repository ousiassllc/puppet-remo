package catalog_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/codex/catalog"
)

func TestDefaultは固定した版のWindowsのamd64とarm64だけを持つ(t *testing.T) {
	c := catalog.Default()
	if c.Version != "0.164.0" || c.Tag != "rust-v0.164.0" {
		t.Fatalf("version = %q, tag = %q", c.Version, c.Tag)
	}
	const base = "https://github.com/openai/codex/releases/download/rust-v0.164.0/"
	want := map[[2]string]struct{ url, exe, sha, exeSHA string }{
		{"windows", "amd64"}: {
			base + "codex-app-server-x86_64-pc-windows-msvc.exe.zip", "codex-app-server-x86_64-pc-windows-msvc.exe",
			"35b93235a1d502352c840ebf960be6194aa9f3bec724fee478d2c7c86492fb27", "82314aa236834a8b7240cc5fe4fdf71d4502822e476105ccbe010ef6689083d7",
		},
		{"windows", "arm64"}: {
			base + "codex-app-server-aarch64-pc-windows-msvc.exe.zip", "codex-app-server-aarch64-pc-windows-msvc.exe",
			"3054bab7cdb1110d53a6ea79f25e2acb21a56b4d3aecc88f64e4dfe75f3adbb8", "b9801d0d133178185dfb61a484e883dceb29b7eaa97ee94c6b9db89653acb50c",
		},
	}
	if len(c.Assets) != len(want) {
		t.Fatalf("assets = %d, want %d", len(c.Assets), len(want))
	}
	for p, w := range want {
		a, ok := c.AssetFor(p[0], p[1])
		if !ok {
			t.Fatalf("no asset for %s/%s", p[0], p[1])
		}
		if a.URL != w.url || a.Format != catalog.FormatZip || a.Exe != w.exe || a.SHA256 != w.sha || a.ExeSHA256 != w.exeSHA {
			t.Errorf("%s/%s = %+v", p[0], p[1], a)
		}
		if a.Size <= 0 || a.ExeSize <= 0 {
			t.Errorf("%s/%s has no size", p[0], p[1])
		}
	}
	for _, p := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "amd64"}, {"windows", "386"}, {"", "amd64"}} {
		if _, ok := c.AssetFor(p[0], p[1]); ok {
			t.Errorf("%s/%s must have no asset", p[0], p[1])
		}
	}
}

func TestDefaultのタグは版と一致し検査を通る(t *testing.T) {
	c := catalog.Default()
	if c.Tag != catalog.Tag(c.Version) {
		t.Fatalf("tag %q does not match version %q", c.Tag, c.Version)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for key, a := range c.Assets {
		if !strings.Contains(a.URL, "/"+c.Tag+"/") {
			t.Errorf("%s: url does not point to the %s release", key, c.Tag)
		}
	}
}

func TestParseは固定していないデータや壊れたデータを拒否する(t *testing.T) {
	base := catalog.Default()
	good := base.Assets["windows/amd64"]
	mutate := func(f func(c *catalog.Catalog, a *catalog.Asset)) []byte {
		c := base
		c.Assets = map[string]catalog.Asset{"windows/amd64": good}
		a := good
		f(&c, &a)
		c.Assets["windows/amd64"] = a
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := map[string][]byte{
		"版とタグの不一致": mutate(func(c *catalog.Catalog, _ *catalog.Asset) { c.Tag = "rust-v0.1.0" }),
		"別のホストの URL": mutate(func(_ *catalog.Catalog, a *catalog.Asset) {
			a.URL = strings.Replace(a.URL, "github.com", "example.com", 1)
		}),
		"http の URL": mutate(func(_ *catalog.Catalog, a *catalog.Asset) { a.URL = strings.Replace(a.URL, "https:", "http:", 1) }),
		"別のタグの URL": mutate(func(_ *catalog.Catalog, a *catalog.Asset) {
			a.URL = strings.Replace(a.URL, "rust-v0.164.0", "rust-v0.1.0", 1)
		}),
		"クエリつきの URL":        mutate(func(_ *catalog.Catalog, a *catalog.Asset) { a.URL += "?x=1" }),
		"zip 以外の形式":         mutate(func(_ *catalog.Catalog, a *catalog.Asset) { a.Format = "tar.gz" }),
		"大文字のハッシュ":          mutate(func(_ *catalog.Catalog, a *catalog.Asset) { a.SHA256 = strings.ToUpper(a.SHA256) }),
		"短いハッシュ":            mutate(func(_ *catalog.Catalog, a *catalog.Asset) { a.ExeSHA256 = "abcd" }),
		"サイズなし":             mutate(func(_ *catalog.Catalog, a *catalog.Asset) { a.Size = 0 }),
		"パス区切りのある exe 名":    mutate(func(_ *catalog.Catalog, a *catalog.Asset) { a.Exe = `sub\codex.exe` }),
		".exe でない exe 名":    mutate(func(_ *catalog.Catalog, a *catalog.Asset) { a.Exe = "codex" }),
		"ライセンスなし":           mutate(func(c *catalog.Catalog, _ *catalog.Asset) { c.License = catalog.License{} }),
		"未知の項目":             []byte(`{"version":"0.164.0","unknown":1}`),
		"アセットなし":            []byte(`{"version":"0.164.0","tag":"rust-v0.164.0","license":{"id":"a","name":"a","url":"u"},"assets":{}}`),
		"固定してはならないプラットフォーム": mutateKey(t, base, "linux/amd64"),
	}
	for name, b := range cases {
		if _, err := catalog.Parse(b); err == nil {
			t.Errorf("%s: Parse accepted it", name)
		}
	}
}

func mutateKey(t *testing.T, base catalog.Catalog, key string) []byte {
	t.Helper()
	c := base
	c.Assets = map[string]catalog.Asset{key: base.Assets["windows/amd64"]}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
