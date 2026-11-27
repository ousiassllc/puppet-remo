// Package catalog は Codex の app-server の「固定した版」の一覧（#247）。
//
// 公式 GitHub リリース rust-v<Version> の Windows（amd64 / arm64）向けアセット 1 つずつについて、ダウンロードする zip の
// URL・SHA-256・サイズと、その中の実行ファイル 1 本の名前・SHA-256・サイズを埋め込みの JSON に固定する。
// アプリは Codex のバイト列を持ち歩かず、取得したものをここに書いた（レビューした）値と突き合わせる。
// 版を上げるときはこの JSON（と固定したスキーマ・第三者通知）を一緒に直す。JSON-RPC の型は版ごとに変わるので、
// アプリは確認した版だけを使う。
package catalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
)

//go:embed data/catalog.json
var data []byte

// License は Codex のライセンス（取得の案内に添える）。
type License struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// FormatZip はアーカイブの形式（Windows のアセットは zip だけ）。
const FormatZip = "zip"

// Asset は 1 つのプラットフォームの app-server: ダウンロードするアーカイブと、その中の実行ファイル。
type Asset struct {
	URL    string `json:"url"`
	Format string `json:"format"` // FormatZip。URL の末尾と一致する
	SHA256 string `json:"sha256"` // アーカイブの SHA-256（小文字 16 進 64 桁）
	Size   int64  `json:"size"`   // アーカイブのバイト数
	// Exe はアーカイブにある唯一のファイルの名前。ExeSHA256・ExeSize は展開後のそれが満たすべき値。
	Exe       string `json:"exe"`
	ExeSHA256 string `json:"exe_sha256"`
	ExeSize   int64  `json:"exe_size"`
}

// Catalog は固定した版。
type Catalog struct {
	Version   string           `json:"version"` // 例: 0.164.0（リリースは rust-v<Version>）
	Tag       string           `json:"tag"`
	SourceURL string           `json:"source_url"`
	License   License          `json:"license"`
	Assets    map[string]Asset `json:"assets"` // Key（"windows/amd64"）ごと
}

var (
	once    sync.Once
	loaded  Catalog
	loadErr error
)

// Default は埋め込みのカタログ。埋め込みの値が不正ならパニックする（ビルドの不備で、テストが見つける）。
func Default() Catalog {
	once.Do(func() { loaded, loadErr = Parse(data) })
	if loadErr != nil {
		panic(fmt.Sprintf("codex catalog: %v", loadErr))
	}
	return loaded
}

// Parse はカタログを読んで検査する。未知の項目は拒否する。
func Parse(b []byte) (Catalog, error) {
	var c Catalog
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Catalog{}, err
	}
	return c, c.Validate()
}

// Key は Assets のキー: "<GOOS>/<GOARCH>"（例: "windows/amd64"）。
func Key(goos, goarch string) string { return goos + "/" + goarch }

// Tag は版からリリースのタグを作る。
func Tag(version string) string { return "rust-v" + version }

// AssetFor はプラットフォームのアセット。固定したビルドが無いプラットフォーム（Linux・macOS など）は ok = false。
func (c Catalog) AssetFor(goos, goarch string) (Asset, bool) {
	a, ok := c.Assets[Key(goos, goarch)]
	return a, ok
}

// platforms はカタログがアセットを固定してよいプラットフォーム（Windows の amd64 / arm64 だけ）。
var platforms = map[string]bool{"windows/amd64": true, "windows/arm64": true}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Validate はすべてのアセットが既知のプラットフォームのもので、ダウンロード先が github.com の当該タグのリリースの
// https の URL（末尾が形式の拡張子）で、SHA-256 とサイズを持つことを確かめる。
func (c Catalog) Validate() error {
	var errs []error
	if c.Version == "" || c.Tag != Tag(c.Version) {
		errs = append(errs, fmt.Errorf("tag %q is not %s", c.Tag, Tag(c.Version)))
	}
	if c.License.ID == "" || c.License.URL == "" {
		errs = append(errs, errors.New("license is empty"))
	}
	if len(c.Assets) == 0 {
		errs = append(errs, errors.New("no asset"))
	}
	for key, a := range c.Assets {
		errs = append(errs, c.validateAsset(key, a)...)
	}
	return errors.Join(errs...)
}

func (c Catalog) validateAsset(key string, a Asset) []error {
	if !platforms[key] {
		return []error{fmt.Errorf("%s: not a platform with a Codex build (%s)", key, strings.Join(slices.Sorted(maps.Keys(platforms)), ", "))}
	}
	var errs []error
	prefix := "/openai/codex/releases/download/" + c.Tag + "/"
	u, err := url.Parse(a.URL)
	switch {
	case err != nil || u.Scheme != "https" || u.Host != "github.com" || !strings.HasPrefix(u.Path, prefix):
		errs = append(errs, fmt.Errorf("%s: url %q is not a release asset of %s", key, a.URL, c.Tag))
	case u.RawQuery != "" || u.Fragment != "":
		errs = append(errs, fmt.Errorf("%s: url has a query or fragment", key))
	}
	if a.Format != FormatZip || !strings.HasSuffix(a.URL, "."+a.Format) {
		errs = append(errs, fmt.Errorf("%s: format %q must be %s and end the url", key, a.Format, FormatZip))
	}
	if !sha256Hex.MatchString(a.SHA256) || !sha256Hex.MatchString(a.ExeSHA256) {
		errs = append(errs, fmt.Errorf("%s: sha256 must be 64 lower-case hex digits", key))
	}
	if a.Size <= 0 || a.ExeSize <= 0 {
		errs = append(errs, fmt.Errorf("%s: size must be positive", key))
	}
	// Windows の実行ファイルは .exe で、区切りのない名前 1 つ。
	if a.Exe == "" || strings.ContainsAny(a.Exe, `/\:`) || a.Exe == "." || a.Exe == ".." || !strings.HasSuffix(a.Exe, ".exe") {
		errs = append(errs, fmt.Errorf("%s: exe %q must be a bare .exe file name", key, a.Exe))
	}
	return errs
}
