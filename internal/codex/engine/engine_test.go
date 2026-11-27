package engine_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/codex/catalog"
	"github.com/ousiassllc/puppet-remo/internal/codex/engine"
)

const exeName = "codex-app-server-test.exe"

var exeBody = bytes.Repeat([]byte("fake-codex-app-server"), 1000)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type entry struct {
	name string
	body []byte
	mode os.FileMode // 0 は通常ファイル
}

func makeZip(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			hdr.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// assetOf は archive を配るアセットの値（アーカイブと exe のハッシュ・サイズは実際のバイト列から計算する）。
func assetOf(url string, archive, exe []byte) catalog.Asset {
	return catalog.Asset{
		URL: url + "/codex.zip", Format: catalog.FormatZip, SHA256: sum(archive), Size: int64(len(archive)),
		Exe: exeName, ExeSHA256: sum(exe), ExeSize: int64(len(exe)),
	}
}

type rig struct {
	srv  *httptest.Server
	inst *engine.Installer
	root string
}

// newRig は archive を配る偽の HTTPS サーバーと、それを使う Installer を作る。asset は配る前に差し替えられる。
func newRig(t *testing.T, served []byte, adjust func(*catalog.Asset)) *rig {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(served)
	}))
	t.Cleanup(srv.Close)
	return newRigOn(t, srv, served, adjust)
}

func newRigOn(t *testing.T, srv *httptest.Server, archive []byte, adjust func(*catalog.Asset)) *rig {
	t.Helper()
	a := assetOf(srv.URL, archive, exeBody)
	if adjust != nil {
		adjust(&a)
	}
	root := filepath.Join(t.TempDir(), "codex")
	return &rig{srv: srv, root: root, inst: &engine.Installer{
		Layout: engine.Layout{Root: root}, Version: "0.0.1", Asset: a, Client: srv.Client(),
		Now: func() time.Time { return time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC) },
	}}
}

func goodZip(t *testing.T) []byte { return makeZip(t, entry{name: exeName, body: exeBody}) }

// noLeftovers は導入の失敗後に、エンジン・展開の途中・.part・記録が残っていないこと。
func noLeftovers(t *testing.T, r *rig) {
	t.Helper()
	for _, p := range []string{r.inst.Layout.EngineDir(), r.inst.Layout.EngineNewDir(), r.inst.Layout.RecordPath()} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was left behind", p)
		}
	}
	if ents, _ := os.ReadDir(r.inst.Layout.DownloadsDir()); len(ents) > 0 {
		t.Errorf("downloads was left behind: %v", ents)
	}
}

func TestInstallは検証して置き記録を残す(t *testing.T) {
	r := newRig(t, goodZip(t), nil)
	var phases []engine.Phase
	err := r.inst.Install(context.Background(), func(p engine.Phase, done, total int64) {
		if len(phases) == 0 || phases[len(phases)-1] != p {
			phases = append(phases, p)
		}
		if done > total {
			t.Errorf("%s: done %d > total %d", p, done, total)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 2 || phases[0] != engine.PhaseDownload || phases[1] != engine.PhaseExtract {
		t.Errorf("phases = %v", phases)
	}
	exe, ok := r.inst.ExePath()
	if !ok || filepath.Base(exe) != exeName {
		t.Fatalf("ExePath = %q, %v", exe, ok)
	}
	if got, err := os.ReadFile(exe); err != nil || !bytes.Equal(got, exeBody) {
		t.Fatalf("exe content differs: %v", err)
	}
	var rec engine.Record
	b, err := os.ReadFile(r.inst.Layout.RecordPath())
	if err != nil || json.Unmarshal(b, &rec) != nil {
		t.Fatalf("record: %v", err)
	}
	if rec.Version != "0.0.1" || rec.Exe != exeName || rec.ExeSHA256 != sum(exeBody) || rec.ExeSize != int64(len(exeBody)) || rec.InstalledAt.IsZero() {
		t.Errorf("record = %+v", rec)
	}
	if st := r.inst.Status(); !st.Installed || st.Outdated || st.Broken || st.Version != "0.0.1" {
		t.Errorf("status = %+v", st)
	}
	if _, err := os.Stat(r.inst.Layout.DownloadsDir()); err == nil {
		t.Error("downloads must be removed")
	}
}

func TestInstallはアーカイブのハッシュ不一致を拒否して何も残さない(t *testing.T) {
	r := newRig(t, goodZip(t), func(a *catalog.Asset) { a.SHA256 = sum([]byte("other")) })
	if err := r.inst.Install(context.Background(), nil); !errors.Is(err, engine.ErrMismatch) {
		t.Fatalf("err = %v, want ErrMismatch", err)
	}
	noLeftovers(t, r)
	if _, ok := r.inst.ExePath(); ok {
		t.Error("ExePath must be false")
	}
}

func TestInstallはアーカイブのサイズ不一致を拒否する(t *testing.T) {
	z := goodZip(t)
	for name, size := range map[string]int64{"固定より小さい": int64(len(z)) - 1, "固定より大きい": int64(len(z)) + 1} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, z, func(a *catalog.Asset) { a.Size = size })
			if err := r.inst.Install(context.Background(), nil); !errors.Is(err, engine.ErrMismatch) {
				t.Fatalf("err = %v, want ErrMismatch", err)
			}
			noLeftovers(t, r)
		})
	}
}

func TestInstallは展開後の実行ファイルのハッシュ不一致を拒否する(t *testing.T) {
	r := newRig(t, goodZip(t), func(a *catalog.Asset) { a.ExeSHA256 = sum([]byte("other")) })
	if err := r.inst.Install(context.Background(), nil); !errors.Is(err, engine.ErrMismatch) {
		t.Fatalf("err = %v, want ErrMismatch", err)
	}
	noLeftovers(t, r)
}

func TestInstallは展開後の実行ファイルのサイズ不一致を拒否する(t *testing.T) {
	for name, size := range map[string]int64{"小さい": int64(len(exeBody)) - 1, "大きい": int64(len(exeBody)) + 1} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, goodZip(t), func(a *catalog.Asset) { a.ExeSize = size })
			if err := r.inst.Install(context.Background(), nil); !errors.Is(err, engine.ErrMismatch) {
				t.Fatalf("err = %v, want ErrMismatch", err)
			}
			noLeftovers(t, r)
		})
	}
}

func TestInstallは危険なzipを拒否して外へ書かない(t *testing.T) {
	cases := map[string][]entry{
		"親ディレクトリ":      {{name: "../" + exeName, body: exeBody}},
		"サブディレクトリ":     {{name: "sub/" + exeName, body: exeBody}},
		"バックスラッシュの区切り": {{name: `sub\` + exeName, body: exeBody}},
		"絶対パス":         {{name: "/" + exeName, body: exeBody}},
		"ドライブ":         {{name: `C:\` + exeName, body: exeBody}},
		"別の名前":         {{name: "other.exe", body: exeBody}},
		"2 つ目のエントリ":    {{name: exeName, body: exeBody}, {name: "extra.txt", body: []byte("x")}},
		"同名の 2 つのエントリ": {{name: exeName, body: exeBody}, {name: exeName, body: exeBody}},
		"ディレクトリのエントリ":  {{name: exeName + "/", body: nil}},
		"シンボリックリンク":    {{name: exeName, body: []byte("target.exe"), mode: os.ModeSymlink | 0o777}},
		"エントリなし":       {},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			z := makeZip(t, entries...)
			r := newRig(t, z, nil)
			err := r.inst.Install(context.Background(), nil)
			if !errors.Is(err, engine.ErrUnsafeArchive) {
				t.Fatalf("err = %v, want ErrUnsafeArchive", err)
			}
			noLeftovers(t, r)
			// 展開先の外（Root の親）に何も作られていない。
			ents, _ := os.ReadDir(filepath.Dir(r.root))
			for _, e := range ents {
				if e.Name() != "codex" {
					t.Errorf("a file was created outside of the root: %s", e.Name())
				}
			}
		})
	}
}

func TestInstallは壊れたzipを拒否する(t *testing.T) {
	junk := bytes.Repeat([]byte("not a zip"), 100)
	r := newRig(t, junk, nil)
	if err := r.inst.Install(context.Background(), nil); !errors.Is(err, engine.ErrUnsafeArchive) {
		t.Fatalf("err = %v, want ErrUnsafeArchive", err)
	}
	noLeftovers(t, r)
}

func TestInstallはhttps以外のURLを拒否する(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the plain http server must not be called")
	}))
	t.Cleanup(plain.Close)
	r := newRigOn(t, plain, goodZip(t), nil)
	if err := r.inst.Install(context.Background(), nil); !errors.Is(err, engine.ErrInsecureURL) {
		t.Fatalf("err = %v, want ErrInsecureURL", err)
	}
	noLeftovers(t, r)
}

func TestInstallはhttpへのリダイレクトを拒否する(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the redirect target must not be called")
	}))
	t.Cleanup(plain.Close)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, plain.URL+"/x.zip", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	r := newRigOn(t, srv, goodZip(t), nil)
	if err := r.inst.Install(context.Background(), nil); !errors.Is(err, engine.ErrInsecureURL) {
		t.Fatalf("err = %v, want ErrInsecureURL", err)
	}
	noLeftovers(t, r)
}

func TestInstallはhttpの失敗を返しURLを含めない(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	r := newRigOn(t, srv, goodZip(t), nil)
	err := r.inst.Install(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("err = %v", err)
	}
	noLeftovers(t, r)
}

func TestInstallはctxの中断で止まり何も残さない(t *testing.T) {
	big := bytes.Repeat([]byte{1}, 4<<20)
	z := makeZip(t, entry{name: exeName, body: big})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(z)
	}))
	t.Cleanup(srv.Close)
	r := newRigOn(t, srv, z, func(a *catalog.Asset) { a.ExeSHA256, a.ExeSize = sum(big), int64(len(big)) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := r.inst.Install(ctx, func(p engine.Phase, done, total int64) {
		if p == engine.PhaseDownload {
			cancel() // 最初の進みの通知で中断する（待ち時間に頼らない）
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	noLeftovers(t, r)
}

func TestInstallは導入済みを置き換え古い版はOutdatedになる(t *testing.T) {
	r := newRig(t, goodZip(t), nil)
	if err := r.inst.Install(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	// 固定した版が上がった（記録の版と違う）。
	r.inst.Version = "0.0.2"
	if st := r.inst.Status(); !st.Installed || !st.Outdated || st.Version != "0.0.1" {
		t.Fatalf("status = %+v", st)
	}
	if _, ok := r.inst.ExePath(); ok {
		t.Fatal("an outdated engine must not be used")
	}
	if err := r.inst.Install(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if st := r.inst.Status(); !st.Installed || st.Outdated || st.Version != "0.0.2" {
		t.Fatalf("status after the second install = %+v", st)
	}
	if _, err := os.Stat(r.inst.Layout.EngineOldDir()); err == nil {
		t.Error("engine.old must be removed")
	}
}

func TestStatusは消された実行ファイルやサイズの違う実行ファイルや記録と違う名前をBrokenにする(t *testing.T) {
	r := newRig(t, goodZip(t), nil)
	if err := r.inst.Install(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(r.inst.Layout.EngineDir(), exeName)
	if err := os.WriteFile(exe, []byte("short"), 0o700); err != nil {
		t.Fatal(err)
	}
	if st := r.inst.Status(); st.Installed || !st.Broken {
		t.Fatalf("size change: %+v", st)
	}
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	if st := r.inst.Status(); st.Installed || !st.Broken {
		t.Fatalf("deleted: %+v", st)
	}
	// 記録の exe が固定したアセットの名前でない（記録を書き換えて別のファイルを起動させる）。
	rec := engine.Record{Version: "0.0.1", Exe: "evil.exe", ExeSize: 1}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(r.inst.Layout.RecordPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.inst.Layout.EngineDir(), "evil.exe"), []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.inst.ExePath(); ok {
		t.Fatal("a record naming another file must not be used")
	}
}

func TestRemoveはエンジンと記録を消す(t *testing.T) {
	r := newRig(t, goodZip(t), nil)
	if err := r.inst.Install(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := r.inst.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.root); err == nil {
		t.Error("an empty root must be removed")
	}
	if st := r.inst.Status(); st.Installed || st.Broken || st.Version != "" {
		t.Errorf("status = %+v", st)
	}
	// 何も無くても失敗しない。
	if err := r.inst.Remove(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanLeftoversは途中の残りを消し置き換えの途中を戻す(t *testing.T) {
	r := newRig(t, goodZip(t), nil)
	l := r.inst.Layout
	for _, d := range []string{l.EngineNewDir(), l.EngineOldDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(l.EngineOldDir(), exeName), []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	r.inst.CleanLeftovers() // engine が無く engine.old がある: 戻す
	if _, err := os.Stat(filepath.Join(l.EngineDir(), exeName)); err != nil {
		t.Fatalf("engine.old was not restored: %v", err)
	}
	if _, err := os.Stat(l.EngineNewDir()); err == nil {
		t.Error("engine.new must be removed")
	}
	if _, err := os.Stat(l.EngineOldDir()); err == nil {
		t.Error("engine.old must be gone after the restore")
	}
}
