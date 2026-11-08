package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"aead.dev/minisign"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは Updater のテスト。GitHub は httptest の偽のサーバー（ネットワークを使わない）、鍵はテスト専用に生成したもの、
// 時間は偽の Clock・AfterFunc で進める（time.Sleep を使わない）。

const (
	updRepo      = "ousiassllc/puppet-remo"
	updSecretTag = "SECRETQUERY-d41d8cd9" // URL のクエリに入れて、ログ・監査に出ないことを確かめる目印
)

type updKeys struct {
	pubText []byte
	priv    minisign.PrivateKey
}

func newUpdKeys(t *testing.T) updKeys {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	text, err := pub.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	return updKeys{pubText: text, priv: priv}
}

// signFile は data のインストーラに、trusted comment `version=<version> file=<file>` つきの minisign 署名（事前ハッシュ）を付ける。
func (k updKeys) sign(t *testing.T, data []byte, version, file string) []byte {
	t.Helper()
	r := minisign.NewReader(bytes.NewReader(data))
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	return r.SignWithComments(k.priv, UpdateTrustedComment(version, file), "untrusted")
}

// fakeRelease は偽の GitHub の Release 1 件。
type fakeRelease struct {
	Tag        string
	Prerelease bool
	Draft      bool
	Body       string
	Assets     map[string][]byte // 配信するファイル
	Listed404  []string          // 一覧には載るが、ダウンロードは 404 のファイル
}

func (r fakeRelease) json() map[string]any {
	assets := []map[string]any{}
	for name, b := range r.Assets {
		assets = append(assets, map[string]any{"name": name, "size": len(b)})
	}
	for _, name := range r.Listed404 {
		assets = append(assets, map[string]any{"name": name, "size": 10})
	}
	return map[string]any{
		"tag_name": r.Tag, "body": r.Body, "draft": r.Draft, "prerelease": r.Prerelease,
		"published_at": "2026-11-01T00:00:00Z", "assets": assets,
	}
}

// signedRelease は version の正式版・ナイトリーの Release を、keys で正しく署名して作る。
func signedRelease(t *testing.T, keys updKeys, version string) fakeRelease {
	t.Helper()
	v, err := domain.ParseVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	asset := v.InstallerAsset()
	data := []byte("installer-bytes-of-" + version + strings.Repeat("-padding", 100))
	return fakeRelease{
		Tag: v.ReleaseTag(), Prerelease: v.Nightly, Body: "release notes of " + version,
		Assets: map[string][]byte{asset: data, asset + ".sha256": []byte("deadbeef  " + asset), asset + ".minisig": keys.sign(t, data, version, asset)},
	}
}

type fakeGitHub struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	releases []fakeRelease // 新しい順
	hits     []string
	authSeen bool
	// override が true を返したら、通常の処理をしない。
	override func(w http.ResponseWriter, r *http.Request) bool
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{t: t}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) setReleases(rs ...fakeRelease) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.releases = rs
}

func (g *fakeGitHub) setOverride(f func(w http.ResponseWriter, r *http.Request) bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.override = f
}

// hitCount は path に部分一致するリクエストの数。
func (g *fakeGitHub) hitCount(substr string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, h := range g.hits {
		if strings.Contains(h, substr) {
			n++
		}
	}
	return n
}

func (g *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.hits = append(g.hits, r.URL.Path)
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
		g.authSeen = true
	}
	override := g.override
	rels := slices.Clone(g.releases)
	g.mu.Unlock()
	if override != nil && override(w, r) {
		return
	}
	switch {
	case r.URL.Path == "/repos/"+updRepo+"/releases/latest":
		for _, rel := range rels {
			if !rel.Prerelease && !rel.Draft {
				writeJSON(w, rel.json())
				return
			}
		}
		http.NotFound(w, r)
	case r.URL.Path == "/repos/"+updRepo+"/releases":
		list := []map[string]any{}
		for _, rel := range rels {
			list = append(list, rel.json())
		}
		writeJSON(w, list)
	case strings.HasPrefix(r.URL.Path, "/"+updRepo+"/releases/download/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"+updRepo+"/releases/download/"), "/")
		if len(parts) == 2 {
			for _, rel := range rels {
				if rel.Tag == parts[0] {
					if b, ok := rel.Assets[parts[1]]; ok {
						_, _ = w.Write(b)
						return
					}
				}
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// updInstaller は platform.Installer の偽物。起動したパスと、その時点のファイルの中身を記録する。
type updInstaller struct {
	mu    sync.Mutex
	runs  []string
	datas [][]byte
	err   error
}

func (i *updInstaller) Run(_ context.Context, path string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.err != nil {
		return i.err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	i.runs = append(i.runs, path)
	i.datas = append(i.datas, b)
	return nil
}

func (i *updInstaller) runCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.runs)
}

type updLeases struct {
	mu      sync.Mutex
	held    bool
	pending bool
}

func (l *updLeases) Holder() (Holder, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return Holder{}, l.held
}

func (l *updLeases) Pending() (PendingLease, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return PendingLease{}, l.pending
}

func (l *updLeases) set(held, pending bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held, l.pending = held, pending
}

type updSessions struct {
	mu sync.Mutex
	n  int
}

func (s *updSessions) Sessions() []SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return make([]SessionInfo, s.n)
}

func (s *updSessions) set(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n = n
}

// updTimers は AfterFunc の偽物。張ったタイマーを溜め、テストが Fire で満了させる（コールバックは呼び出し元で同期に動く）。
type updTimers struct {
	mu     sync.Mutex
	timers []*updTimer
}

type updTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (ts *updTimers) AfterFunc(d time.Duration, f func()) func() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := &updTimer{d: d, f: f}
	ts.timers = append(ts.timers, t)
	return func() bool {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

// active は止められていない（未発火の）タイマーの待ち時間。
func (ts *updTimers) active() []time.Duration {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	var out []time.Duration
	for _, t := range ts.timers {
		if !t.stopped {
			out = append(out, t.d)
		}
	}
	return out
}

// fire は未発火のタイマーのうち最も古いものを満了させ、コールバックの終了まで待つ。無ければ false。
func (ts *updTimers) fire() bool {
	ts.mu.Lock()
	var f func()
	for _, t := range ts.timers {
		if !t.stopped {
			t.stopped = true
			f = t.f
			break
		}
	}
	ts.mu.Unlock()
	if f == nil {
		return false
	}
	f()
	return true
}

// safeBuf はログの出力先（並行に書かれてもよい）。
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type updEnv struct {
	t         *testing.T
	gh        *fakeGitHub
	keys      updKeys
	up        Updater
	raw       *updater
	settings  *ctlSettings
	audit     *recordingAudit
	clock     *fakeClock
	installer *updInstaller
	leases    *updLeases
	sessions  *updSessions
	timers    *updTimers
	bus       *Bus
	toasts    <-chan Event
	logs      *safeBuf
	tmp       string
	quits     int
	tasks     int
}

type updOpt func(*UpdaterConfig, *updEnv)

func withVersion(v string) updOpt {
	return func(c *UpdaterConfig, _ *updEnv) { c.CurrentVersion = v }
}
func withPublicKey(b []byte) updOpt { return func(c *UpdaterConfig, _ *updEnv) { c.PublicKey = b } }
func withHTTPClient(cl *http.Client) updOpt {
	return func(c *UpdaterConfig, _ *updEnv) { c.HTTPClient = cl }
}

func newUpdEnv(t *testing.T, opts ...updOpt) *updEnv {
	t.Helper()
	e := &updEnv{
		t: t, gh: newFakeGitHub(t), keys: newUpdKeys(t), settings: newCtlSettings(), audit: &recordingAudit{}, clock: newFakeClock(),
		installer: &updInstaller{}, leases: &updLeases{}, sessions: &updSessions{}, timers: &updTimers{}, bus: NewBus(),
		logs: &safeBuf{}, tmp: t.TempDir(),
	}
	e.toasts, _ = e.bus.Subscribe(TopicToast)
	cfg := UpdaterConfig{
		Settings: e.settings, Audit: e.audit, Clock: e.clock, Installer: e.installer, Leases: e.leases, Sessions: e.sessions,
		RunningTasks: func() int { return e.tasks }, Bus: e.bus, Quit: func() { e.quits++ },
		CurrentVersion: "0.1.0", PublicKey: e.keys.pubText,
		APIBase: e.gh.srv.URL, DownloadBase: e.gh.srv.URL, TempDir: e.tmp, AfterFunc: e.timers.AfterFunc,
		Logger: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, o := range opts {
		o(&cfg, e)
	}
	up, err := NewUpdater(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.up, e.raw = up, up.(*updater)
	t.Cleanup(up.Close)
	return e
}

func (e *updEnv) setSetting(key string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		e.t.Fatal(err)
	}
	e.settings.put(key, string(raw))
}

func (e *updEnv) check() UpdateStatus {
	e.t.Helper()
	return e.up.Check(context.Background(), domain.ActorHost)
}

func (e *updEnv) apply() error {
	e.t.Helper()
	return e.up.Apply(context.Background(), domain.ActorHost)
}

// leftoverDirs は TempDir に残った検証済みインストーラのディレクトリの数。
func (e *updEnv) leftoverDirs() int {
	m, _ := filepath.Glob(filepath.Join(e.tmp, updateTempPrefix+"*"))
	return len(m)
}

func (e *updEnv) auditEvents() []string { return e.audit.events() }

func (e *updEnv) lastAudit() repository.AuditEntry {
	e.audit.mu.Lock()
	defer e.audit.mu.Unlock()
	return e.audit.entries[len(e.audit.entries)-1]
}

func (e *updEnv) toastCount() int {
	n := 0
	for {
		select {
		case <-e.toasts:
			n++
		default:
			return n
		}
	}
}

// ---- 正常系 ----

func TestUpdaterVerifiedUpdateIsNotifiedAndApplied(t *testing.T) {
	e := newUpdEnv(t)
	rel := signedRelease(t, e.keys, "0.1.1")
	e.gh.setReleases(rel)

	st := e.check()
	if st.Latest == nil || !st.Latest.Verified || st.Latest.Version != "0.1.1" || st.Latest.Tag != "v0.1.1" || st.LastError != "" {
		t.Fatalf("status after check = %+v (latest %+v)", st, st.Latest)
	}
	if !strings.Contains(st.Latest.Notes, "release notes of 0.1.1") || !st.Updatable || !st.KeyConfigured {
		t.Errorf("status = %+v", st)
	}
	if got := e.auditEvents(); !slices.Equal(got, []string{UpdateDetectedAuditEvent}) {
		t.Fatalf("audit = %v, want [detected]", got)
	}
	if a := e.lastAudit(); a.Kind != "update" || a.ActorType != domain.ActorHost || a.Result != "ok" {
		t.Errorf("audit entry = %+v", a)
	}
	// トーストは 1 行の短文（操作の案内は付けない。O3）。
	select {
	case ev := <-e.toasts:
		if got, want := ev.Data.(Toast).Message, "新しい版 0.1.1 があります。"; got != want {
			t.Errorf("toast message = %q, want %q", got, want)
		}
	default:
		t.Error("no toast for the verified version")
	}
	if n := e.toastCount(); n != 0 {
		t.Errorf("extra toasts = %d, want only 1 for the verified version", n)
	}
	// 同じ版を再確認しても、取得し直さず・通知も監査も繰り返さない。
	downloads := e.gh.hitCount("/releases/download/")
	e.check()
	if e.gh.hitCount("/releases/download/") != downloads || e.toastCount() != 0 || len(e.auditEvents()) != 1 {
		t.Errorf("a second check re-downloaded or re-notified (hits %d→%d, audit %v)", downloads, e.gh.hitCount("/releases/download/"), e.auditEvents())
	}

	if err := e.apply(); err != nil {
		t.Fatalf("Apply = %v", err)
	}
	if e.installer.runCount() != 1 || !bytes.Equal(e.installer.datas[0], rel.Assets["puppet-remo-0.1.1-amd64-installer.exe"]) {
		t.Fatalf("installer runs = %v", e.installer.runs)
	}
	if e.quits != 1 {
		t.Errorf("Quit called %d times, want 1", e.quits)
	}
	if got := e.auditEvents(); !slices.Equal(got, []string{UpdateDetectedAuditEvent, UpdateApplyStartedAuditEvent}) {
		t.Errorf("audit = %v", got)
	}
	if !e.up.Status(context.Background()).Applying {
		t.Error("status must report applying after the installer started")
	}
	// 適用の開始後は、確認も 2 回目の適用もしない。
	if err := e.apply(); err == nil {
		t.Error("a second Apply must be refused")
	}
	if e.installer.runCount() != 1 {
		t.Errorf("installer ran %d times", e.installer.runCount())
	}
	if e.gh.authSeen {
		t.Error("a credential header was sent to GitHub")
	}
}

func TestUpdaterSavesLastCheckedAt(t *testing.T) {
	e := newUpdEnv(t)
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.0"))
	if st := e.up.Status(context.Background()); !st.LastCheckedAt.IsZero() {
		t.Fatalf("LastCheckedAt before any check = %v", st.LastCheckedAt)
	}
	st := e.check()
	want := e.clock.Now()
	if !st.LastCheckedAt.Equal(want) {
		t.Errorf("LastCheckedAt = %v, want %v", st.LastCheckedAt, want)
	}
	if got := string(e.settings.values["update.last_checked_at"]); got != fmt.Sprint(want.UnixMilli()) {
		t.Errorf("stored update.last_checked_at = %s, want %d (UTC ms)", got, want.UnixMilli())
	}
	// 再起動後（別の Updater）も、保存した時刻を読む。
	e2 := newUpdEnv(t)
	e2.settings.values["update.last_checked_at"] = e.settings.values["update.last_checked_at"]
	if got := e2.up.Status(context.Background()).LastCheckedAt; !got.Equal(want) {
		t.Errorf("restored LastCheckedAt = %v, want %v", got, want)
	}
}

func TestUpdaterSameAndOlderVersionsAreNotOffered(t *testing.T) {
	tests := []struct {
		name      string
		current   string
		latest    string
		wantAudit []string
	}{
		{"same version", "0.1.1", "0.1.1", nil},
		{"downgrade", "0.2.0", "0.1.5", []string{UpdateDowngradeRejectedAuditEvent}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newUpdEnv(t, withVersion(tc.current))
			e.gh.setReleases(signedRelease(t, e.keys, tc.latest))
			st := e.check()
			if st.Latest != nil {
				t.Fatalf("Latest = %+v, want none", st.Latest)
			}
			// 取得もしない（署名の検証に進まない）。
			if n := e.gh.hitCount("/releases/download/"); n != 0 {
				t.Errorf("downloaded %d files for a non-newer release", n)
			}
			if got := e.auditEvents(); !slices.Equal(got, tc.wantAudit) {
				t.Errorf("audit = %v, want %v", got, tc.wantAudit)
			}
			if err := e.apply(); !errors.Is(err, ErrUpdateUnavailable) || domain.CodeOf(err) != domain.CodeConflict {
				t.Errorf("Apply = %v, want ErrUpdateUnavailable (conflict)", err)
			}
			if e.installer.runCount() != 0 || e.toastCount() != 0 {
				t.Error("nothing may run or be notified")
			}
			// 繰り返しの確認で、同じ拒否を監査に重ねない。
			e.check()
			if got := e.auditEvents(); !slices.Equal(got, tc.wantAudit) {
				t.Errorf("audit after a second check = %v, want %v", got, tc.wantAudit)
			}
		})
	}
}

// ---- 署名の検証 ----

func TestUpdaterRejectsBadSignatures(t *testing.T) {
	other := newUpdKeys(t)
	const ver = "0.1.1"
	const asset = "puppet-remo-0.1.1-amd64-installer.exe"
	tests := []struct {
		name   string
		mutate func(t *testing.T, e *updEnv, rel *fakeRelease)
		reason string
	}{
		{"one byte of the installer is tampered", func(t *testing.T, _ *updEnv, rel *fakeRelease) {
			b := bytes.Clone(rel.Assets[asset])
			b[len(b)/2] ^= 0x01
			rel.Assets[asset] = b
		}, UpdateRejectSignatureInvalid},
		{"signed by another key", func(t *testing.T, _ *updEnv, rel *fakeRelease) {
			rel.Assets[asset+".minisig"] = other.sign(t, rel.Assets[asset], ver, asset)
		}, UpdateRejectSignatureInvalid},
		{"garbage signature", func(*testing.T, *updEnv, *fakeRelease) {}, UpdateRejectSignatureInvalid},
		{"signature missing from the release", func(_ *testing.T, _ *updEnv, rel *fakeRelease) {
			delete(rel.Assets, asset+".minisig")
		}, UpdateRejectSignatureMissing},
		{"signature listed but not downloadable", func(_ *testing.T, _ *updEnv, rel *fakeRelease) {
			delete(rel.Assets, asset+".minisig")
			rel.Listed404 = []string{asset + ".minisig"}
		}, UpdateRejectSignatureMissing},
		{"signed old version relabeled as a new one", func(t *testing.T, e *updEnv, rel *fakeRelease) {
			// 0.1.0 のために署名された（正しい鍵・正しい中身の）署名を、0.1.1 の Release に付け替える。
			rel.Assets[asset+".minisig"] = e.keys.sign(t, rel.Assets[asset], "0.1.0", "puppet-remo-0.1.0-amd64-installer.exe")
		}, UpdateRejectVersionMismatch},
		{"signed for another file name", func(t *testing.T, e *updEnv, rel *fakeRelease) {
			rel.Assets[asset+".minisig"] = e.keys.sign(t, rel.Assets[asset], ver, "other.exe")
		}, UpdateRejectVersionMismatch},
		{"signature without a binding comment", func(t *testing.T, e *updEnv, rel *fakeRelease) {
			r := minisign.NewReader(bytes.NewReader(rel.Assets[asset]))
			_, _ = io.Copy(io.Discard, r)
			rel.Assets[asset+".minisig"] = r.Sign(e.keys.priv) // trusted comment は timestamp
		}, UpdateRejectVersionMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newUpdEnv(t)
			rel := signedRelease(t, e.keys, ver)
			tc.mutate(t, e, &rel)
			if tc.name == "garbage signature" {
				rel.Assets[asset+".minisig"] = []byte("not a signature")
			}
			e.gh.setReleases(rel)

			st := e.check()
			if st.Latest == nil || st.Latest.Verified || st.Latest.RejectReason != tc.reason {
				t.Fatalf("Latest = %+v, want unverified (%s)", st.Latest, tc.reason)
			}
			if got := e.auditEvents(); !slices.Equal(got, []string{UpdateVerifyFailedAuditEvent}) {
				t.Fatalf("audit = %v, want [verify_failed]", got)
			}
			if a := e.lastAudit(); a.Result != "denied" || a.ErrorCode != tc.reason {
				t.Errorf("audit entry = %+v", a)
			}
			if e.toastCount() != 0 {
				t.Error("an unverified update must not be notified")
			}
			if err := e.apply(); !errors.Is(err, ErrUpdateUnavailable) {
				t.Errorf("Apply = %v, want ErrUpdateUnavailable", err)
			}
			if e.installer.runCount() != 0 || e.quits != 0 {
				t.Error("the installer must not run")
			}
			if n := e.leftoverDirs(); n != 0 {
				t.Errorf("%d temp dirs left behind", n)
			}
		})
	}
}

func TestUpdaterRefusesWhenPublicKeyIsNotConfigured(t *testing.T) {
	placeholder := []byte("# placeholder\n\n")
	for name, key := range map[string][]byte{"nil": nil, "empty": {}, "comments only": placeholder} {
		t.Run(name, func(t *testing.T) {
			e := newUpdEnv(t, withPublicKey(key))
			e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
			st := e.check()
			if st.KeyConfigured || st.Latest == nil || st.Latest.Verified || st.Latest.RejectReason != UpdateRejectKeyUnset {
				t.Fatalf("status = %+v (latest %+v)", st, st.Latest)
			}
			if n := e.gh.hitCount("/releases/download/"); n != 0 {
				t.Errorf("downloaded %d files without a key to verify with", n)
			}
			if err := e.apply(); !errors.Is(err, ErrUpdateKeyNotConfigured) {
				t.Errorf("Apply = %v, want ErrUpdateKeyNotConfigured", err)
			}
			if e.installer.runCount() != 0 || e.toastCount() != 0 {
				t.Error("nothing may run or be notified")
			}
		})
	}
}

func TestNewUpdaterRejectsMalformedKeyAndBases(t *testing.T) {
	e := newUpdEnv(t)
	base := UpdaterConfig{
		Settings: e.settings, Clock: e.clock, Installer: e.installer, Leases: e.leases, Sessions: e.sessions, CurrentVersion: "0.1.0",
		PublicKey: e.keys.pubText,
	}
	bad := map[string]func(*UpdaterConfig){
		"garbage key":        func(c *UpdaterConfig) { c.PublicKey = []byte("garbage!!") },
		"http api base":      func(c *UpdaterConfig) { c.APIBase = "http://api.github.com" },
		"http download base": func(c *UpdaterConfig) { c.DownloadBase = "http://example.com" },
		"base with slash":    func(c *UpdaterConfig) { c.APIBase = "https://api.github.com/" },
		"base with query":    func(c *UpdaterConfig) { c.APIBase = "https://api.github.com?x=1" },
		"no settings":        func(c *UpdaterConfig) { c.Settings = nil },
		"no installer":       func(c *UpdaterConfig) { c.Installer = nil },
		"no version":         func(c *UpdaterConfig) { c.CurrentVersion = "" },
	}
	for name, mutate := range bad {
		cfg := base
		mutate(&cfg)
		if _, err := NewUpdater(cfg); err == nil {
			t.Errorf("%s: NewUpdater succeeded", name)
		}
	}
	if _, err := NewUpdater(base); err != nil {
		t.Errorf("valid config: %v", err)
	}
}

// 埋め込んだ公開鍵は、書式の正しい minisign の公開鍵である（プレースホルダのまま・壊れた鍵を出荷しない。
// 未設定だと出荷したアプリは更新を適用できない）。
func TestEmbeddedUpdatePublicKeyIsValid(t *testing.T) {
	if _, err := parseUpdatePublicKey(EmbeddedUpdatePublicKey()); err != nil {
		t.Fatalf("the embedded update public key is unset or malformed: %v", err)
	}
}

func TestParseUpdatePublicKeyAcceptsMinisignFileFormat(t *testing.T) {
	k := newUpdKeys(t)
	lines := strings.SplitN(string(k.pubText), "\n", 2) // pubText は minisign の .pub の形（コメント行 + 鍵）
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "untrusted comment: ") {
		t.Fatalf("unexpected public key text: %q", k.pubText)
	}
	for name, text := range map[string]string{
		"with the comment line":   string(k.pubText),
		"with a trailing newline": string(k.pubText) + "\n",
		"the bare key":            lines[1],
	} {
		if _, err := parseUpdatePublicKey([]byte(text)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// 適用の直前にもう一度検証する: 取得後にディスク上のインストーラが差し替えられたら、起動しない。
func TestUpdaterReverifiesJustBeforeApplying(t *testing.T) {
	e := newUpdEnv(t)
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
	st := e.check()
	if st.Latest == nil || !st.Latest.Verified {
		t.Fatalf("status = %+v", st.Latest)
	}
	files, _ := filepath.Glob(filepath.Join(e.tmp, updateTempPrefix+"*", "*.exe"))
	if len(files) != 1 {
		t.Fatalf("installer files = %v", files)
	}
	if err := os.WriteFile(files[0], []byte("malware"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := e.apply()
	var rej *rejection
	if !errors.As(err, &rej) || rej.reason != UpdateRejectSignatureInvalid {
		t.Fatalf("Apply = %v, want a signature rejection", err)
	}
	if e.installer.runCount() != 0 || e.quits != 0 {
		t.Error("a tampered installer must not run")
	}
	if got := e.auditEvents(); !slices.Equal(got, []string{UpdateDetectedAuditEvent, UpdateVerifyFailedAuditEvent}) {
		t.Errorf("audit = %v", got)
	}
	if st := e.up.Status(context.Background()); st.Latest != nil || e.leftoverDirs() != 0 {
		t.Errorf("the tampered download must be dropped (latest %+v, dirs %d)", st.Latest, e.leftoverDirs())
	}
}

// ---- チャネル ----

func TestUpdaterChannels(t *testing.T) {
	e := newUpdEnv(t, withVersion("0.1.0"))
	e.gh.setReleases(
		signedRelease(t, e.keys, "0.1.1-nightly.20261102+abc1234"),
		signedRelease(t, e.keys, "0.1.1-nightly.20261101+0000000"),
		signedRelease(t, e.keys, "0.1.0"),
	)

	// stable（既定）: プレリリース（ナイトリー）は対象外。0.1.0 は同じ版。
	if st := e.check(); st.Channel != domain.ChannelStable || st.Latest != nil {
		t.Fatalf("stable: status = %+v (latest %+v)", st, st.Latest)
	}
	if e.gh.hitCount("/releases/latest") != 1 || e.gh.hitCount("/releases/download/") != 0 {
		t.Errorf("stable must use /releases/latest and download nothing for the same version")
	}

	// nightly: 一覧から最も新しいナイトリー（日付）を選ぶ。
	e.setSetting("update.channel", "nightly")
	st := e.check()
	if st.Channel != domain.ChannelNightly || st.Latest == nil || st.Latest.Version != "0.1.1-nightly.20261102+abc1234" || !st.Latest.Verified {
		t.Fatalf("nightly: status = %+v (latest %+v)", st, st.Latest)
	}
	if st.Latest.Tag != "nightly-20261102-abc1234" {
		t.Errorf("tag = %s", st.Latest.Tag)
	}

	// stable へ戻すと、nightly の確認結果は見せず、適用もしない。
	e.setSetting("update.channel", "stable")
	if st := e.up.Status(context.Background()); st.Latest != nil {
		t.Errorf("a nightly result must not be shown on the stable channel: %+v", st.Latest)
	}
	if err := e.apply(); !errors.Is(err, ErrUpdateUnavailable) || e.installer.runCount() != 0 {
		t.Errorf("Apply on the stable channel = %v (runs %d)", err, e.installer.runCount())
	}
}

func TestUpdaterNightlyToNightly(t *testing.T) {
	cur := "0.1.1-nightly.20261101+1111111"
	tests := []struct {
		name   string
		latest string
		want   bool
	}{
		{"newer date", "0.1.1-nightly.20261102+2222222", true},
		{"same date, another sha", "0.1.1-nightly.20261101+2222222", false},
		{"older date", "0.1.1-nightly.20261031+2222222", false},
		{"stable of the same core", "0.1.1", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newUpdEnv(t, withVersion(cur))
			e.setSetting("update.channel", "nightly")
			e.gh.setReleases(signedRelease(t, e.keys, tc.latest))
			if st := e.check(); (st.Latest != nil) != tc.want {
				t.Fatalf("latest = %+v, want offered=%v", st.Latest, tc.want)
			}
		})
	}
}

func TestUpdaterDevBuildNeverChecks(t *testing.T) {
	for _, v := range []string{"dev", "garbage"} {
		e := newUpdEnv(t, withVersion(v))
		e.gh.setReleases(signedRelease(t, e.keys, "9.9.9"))
		st := e.check()
		if st.Updatable || st.Latest != nil || len(e.gh.hits) != 0 {
			t.Errorf("%s: status = %+v, hits = %v", v, st, e.gh.hits)
		}
		e.up.Start()
		if len(e.timers.active()) != 0 {
			t.Errorf("%s: a periodic check was scheduled", v)
		}
		if err := e.apply(); !errors.Is(err, ErrUpdateUnavailable) {
			t.Errorf("%s: Apply = %v", v, err)
		}
	}
}

// ---- 適用の可否 ----

func TestUpdaterApplyIsRefusedWhileBusy(t *testing.T) {
	tests := []struct {
		name   string
		busy   func(e *updEnv)
		idle   func(e *updEnv)
		reason string
	}{
		{"lease held", func(e *updEnv) { e.leases.set(true, false) }, func(e *updEnv) { e.leases.set(false, false) }, UpdateBusyLeaseHeld},
		{"lease pending", func(e *updEnv) { e.leases.set(false, true) }, func(e *updEnv) { e.leases.set(false, false) }, UpdateBusyLeasePending},
		{"sessions connected", func(e *updEnv) { e.sessions.set(2) }, func(e *updEnv) { e.sessions.set(0) }, UpdateBusySessions},
		{"tasks running", func(e *updEnv) { e.tasks = 1 }, func(e *updEnv) { e.tasks = 0 }, UpdateBusyTasks},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newUpdEnv(t)
			e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
			e.check()
			tc.busy(e)
			err := e.apply()
			var busy *UpdateBusyError
			if !errors.As(err, &busy) || !slices.Equal(busy.Reasons, []string{tc.reason}) {
				t.Fatalf("Apply = %v, want busy (%s)", err, tc.reason)
			}
			if domain.CodeOf(err) != domain.CodeConflict {
				t.Errorf("code = %s, want conflict", domain.CodeOf(err))
			}
			if UpdateBusyReasonText(tc.reason) == UpdateBusyReasonText("") {
				t.Errorf("no user-facing text for %s", tc.reason)
			}
			if e.installer.runCount() != 0 || e.quits != 0 {
				t.Error("the installer must not run while busy")
			}
			// 空いたら適用できる（待たされた状態は残らない）。
			tc.idle(e)
			if err := e.apply(); err != nil || e.installer.runCount() != 1 {
				t.Fatalf("Apply after idle = %v (runs %d)", err, e.installer.runCount())
			}
		})
	}
}

func TestUpdaterBusyReportsEveryReason(t *testing.T) {
	e := newUpdEnv(t)
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
	e.check()
	e.leases.set(true, true)
	e.sessions.set(1)
	e.tasks = 3
	var busy *UpdateBusyError
	if err := e.apply(); !errors.As(err, &busy) || len(busy.Reasons) != 4 {
		t.Fatalf("Apply = %v", err)
	}
}

func TestUpdaterApplyFailureIsAuditedAndRetryable(t *testing.T) {
	e := newUpdEnv(t)
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
	e.check()
	e.installer.err = errors.New("the user canceled the UAC prompt")
	if err := e.apply(); err == nil {
		t.Fatal("Apply must report the failure")
	}
	if e.quits != 0 || e.up.Status(context.Background()).Applying {
		t.Error("a failed start must not quit the app or stay in applying")
	}
	events := e.auditEvents()
	if !slices.Equal(events, []string{UpdateDetectedAuditEvent, UpdateApplyStartedAuditEvent, UpdateApplyFailedAuditEvent}) {
		t.Fatalf("audit = %v", events)
	}
	if a := e.lastAudit(); a.Result != "error" || a.ErrorCode != "start_failed" || strings.Contains(string(a.DetailJSON), "UAC") {
		t.Errorf("audit entry = %+v (detail %s)", a, a.DetailJSON)
	}
	e.installer.err = nil
	if err := e.apply(); err != nil || e.quits != 1 {
		t.Fatalf("retry = %v (quits %d)", err, e.quits)
	}
}

func TestUpdaterAuditActorFollowsCaller(t *testing.T) {
	e := newUpdEnv(t)
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
	e.up.Check(context.Background(), domain.ActorSystem)
	if a := e.lastAudit(); a.ActorType != domain.ActorSystem {
		t.Errorf("detected actor = %s, want system", a.ActorType)
	}
	if err := e.up.Apply(context.Background(), domain.ActorHost); err != nil {
		t.Fatal(err)
	}
	if a := e.lastAudit(); a.Event != UpdateApplyStartedAuditEvent || a.ActorType != domain.ActorHost {
		t.Errorf("apply actor = %+v, want host", a)
	}
}

// ---- 定期確認・auto_idle ----

func TestUpdaterPeriodicCheckUsesAfterFunc(t *testing.T) {
	e := newUpdEnv(t)
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.0"))
	e.up.Start()
	e.up.Start() // 2 回目は何もしない
	if got := e.timers.active(); len(got) != 1 || got[0] != DefaultUpdateStartDelay {
		t.Fatalf("timers after Start = %v, want [%v]", got, DefaultUpdateStartDelay)
	}
	if len(e.gh.hits) != 0 {
		t.Fatal("Start must not hit the network before the delay")
	}

	e.timers.fire()
	if e.gh.hitCount("/releases/latest") != 1 {
		t.Fatalf("the first scheduled check did not run: hits %v", e.gh.hits)
	}
	if got := e.timers.active(); len(got) != 1 || got[0] != DefaultUpdateInterval {
		t.Fatalf("timers after a check = %v, want [%v] (6 h)", got, DefaultUpdateInterval)
	}
	e.timers.fire()
	if e.gh.hitCount("/releases/latest") != 2 {
		t.Errorf("the second scheduled check did not run")
	}

	e.up.Close()
	e.up.Close() // 何度呼んでもよい
	if got := e.timers.active(); len(got) != 0 {
		t.Errorf("timers after Close = %v", got)
	}
	hits := len(e.gh.hits)
	e.timers.timers[len(e.timers.timers)-1].f() // 止め損ねたコールバックが走っても、何もしない
	if len(e.gh.hits) != hits {
		t.Error("a callback after Close must not check")
	}
}

func TestUpdaterScheduledCheckIsAuditedAsSystem(t *testing.T) {
	e := newUpdEnv(t)
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
	e.up.Start()
	e.timers.fire()
	if a := e.lastAudit(); a.Event != UpdateDetectedAuditEvent || a.ActorType != domain.ActorSystem {
		t.Errorf("audit = %+v", a)
	}
	if e.installer.runCount() != 0 {
		t.Error("confirm mode must not apply by itself")
	}
}

func TestUpdaterAutoIdleAppliesOnlyWhenIdle(t *testing.T) {
	e := newUpdEnv(t)
	e.setSetting("update.apply_mode", "auto_idle")
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
	e.leases.set(true, false)
	e.up.Start()
	e.timers.fire() // 定期確認 → 新版を検証 → 空き待ちのタイマー

	if e.installer.runCount() != 0 {
		t.Fatal("applied while a lease was held")
	}
	if got := e.timers.active(); !slices.Contains(got, DefaultUpdateIdlePoll) {
		t.Fatalf("no idle poll armed: %v", got)
	}
	// 接続中の間も、リースが空いても、セッションがあれば適用しない。
	e.timers.fire() // idle poll（保持中）
	e.leases.set(false, false)
	e.sessions.set(1)
	e.timers.fire() // idle poll の次回（接続中）
	if e.installer.runCount() != 0 || e.quits != 0 {
		t.Fatal("applied while a session was connected")
	}
	e.sessions.set(0)
	for e.installer.runCount() == 0 && e.timers.fire() {
	}
	if e.installer.runCount() != 1 || e.quits != 1 {
		t.Fatalf("auto_idle did not apply once idle (runs %d, quits %d)", e.installer.runCount(), e.quits)
	}
	last := e.lastAudit()
	if last.Event != UpdateApplyStartedAuditEvent || last.ActorType != domain.ActorSystem {
		t.Errorf("auto apply audit = %+v, want apply_started by system", last)
	}
}

func TestUpdaterConfirmModeNeverAppliesOnItsOwn(t *testing.T) {
	e := newUpdEnv(t) // 既定は confirm
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
	e.check()
	for range 5 {
		if !e.timers.fire() {
			break
		}
	}
	if e.installer.runCount() != 0 {
		t.Fatal("confirm mode applied without the host's confirmation")
	}
	// 設定を auto_idle に変えると、次の空き待ちの確認で適用される。
	e.setSetting("update.apply_mode", "auto_idle")
	for e.installer.runCount() == 0 && e.timers.fire() {
	}
	if e.installer.runCount() != 1 {
		t.Error("switching to auto_idle did not apply on the next idle poll")
	}
}

func TestUpdaterAutoIdleDoesNotRetryAfterAFailure(t *testing.T) {
	e := newUpdEnv(t)
	e.setSetting("update.apply_mode", "auto_idle")
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
	e.installer.err = errors.New("boom")
	e.up.Start()
	e.timers.fire()
	e.timers.fire() // idle poll: 適用に失敗
	// 残るのは次の定期確認（6 時間後）だけ。空き待ちのタイマーは張り直さない。
	if got := e.timers.active(); len(got) != 1 || got[0] != DefaultUpdateInterval {
		t.Errorf("timers after a failed auto-apply = %v, want only the next scheduled check", got)
	}
}

// ---- 失敗しても止まらない ----

func TestUpdaterCheckFailuresDoNotStopTheApp(t *testing.T) {
	big := bytes.Repeat([]byte("x"), maxReleaseJSONBytes+10)
	tests := []struct {
		name     string
		override func(w http.ResponseWriter, r *http.Request) bool
		client   *http.Client
		code     string
	}{
		{"private repository (404)", func(w http.ResponseWriter, r *http.Request) bool { http.NotFound(w, r); return true }, nil, UpdateErrNotFound},
		{"private repository (401)", func(w http.ResponseWriter, _ *http.Request) bool { w.WriteHeader(http.StatusUnauthorized); return true }, nil, UpdateErrUnauthorize},
		{"rate limited (403)", func(w http.ResponseWriter, _ *http.Request) bool { w.WriteHeader(http.StatusForbidden); return true }, nil, UpdateErrForbidden},
		{"server error", func(w http.ResponseWriter, _ *http.Request) bool { w.WriteHeader(http.StatusBadGateway); return true }, nil, UpdateErrHTTP},
		{"invalid JSON", func(w http.ResponseWriter, _ *http.Request) bool { _, _ = w.Write([]byte("{not json")); return true }, nil, UpdateErrBadResponse},
		{"huge response", func(w http.ResponseWriter, _ *http.Request) bool { _, _ = w.Write(big); return true }, nil, UpdateErrTooLarge},
		{"no installer in the release", func(w http.ResponseWriter, _ *http.Request) bool {
			writeJSON(w, map[string]any{"tag_name": "v9.9.9", "assets": []any{}})
			return true
		}, nil, UpdateErrNoInstaller},
		{"timeout", func(w http.ResponseWriter, r *http.Request) bool { <-r.Context().Done(); return true },
			&http.Client{Timeout: 50 * time.Millisecond}, UpdateErrNetwork},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var opts []updOpt
			if tc.client != nil {
				opts = append(opts, withHTTPClient(tc.client))
			}
			e := newUpdEnv(t, opts...)
			e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
			e.gh.setOverride(tc.override)
			st := e.check() // パニックせず、ブロックせず、戻る
			if st.LastError != tc.code || st.Latest != nil {
				t.Fatalf("status = %+v, want LastError %s", st, tc.code)
			}
			if st.LastCheckedAt.IsZero() {
				t.Error("a failed check must still record the time")
			}
			if !strings.Contains(e.logs.String(), "update check failed") || strings.Contains(e.logs.String(), e.gh.srv.URL) {
				t.Errorf("log = %q (must mention the failure but not the URL)", e.logs.String())
			}
			if len(e.auditEvents()) != 0 || e.toastCount() != 0 {
				t.Error("a failed check must not audit or notify")
			}
			// 直れば、次の確認で拾う。
			e.gh.setOverride(nil)
			if st := e.check(); st.LastError != "" || st.Latest == nil || !st.Latest.Verified {
				t.Errorf("recovery: status = %+v (latest %+v)", st, st.Latest)
			}
		})
	}
}

func TestUpdaterOfflineIsOnlyLogged(t *testing.T) {
	e := newUpdEnv(t)
	e.gh.srv.Close() // 接続できない
	st := e.check()
	if st.LastError != UpdateErrNetwork || st.Latest != nil {
		t.Fatalf("status = %+v", st)
	}
	if strings.Contains(e.logs.String(), e.gh.srv.URL) {
		t.Errorf("the log leaks the URL: %s", e.logs.String())
	}
	// 定期確認も止まらない（失敗しても次のタイマーを張る）。
	e.up.Start()
	e.timers.fire()
	if got := e.timers.active(); len(got) != 1 || got[0] != DefaultUpdateInterval {
		t.Errorf("timers after a failed scheduled check = %v", got)
	}
}

func TestUpdaterDownloadFailureIsNotAcceptedAndNotNotified(t *testing.T) {
	e := newUpdEnv(t)
	rel := signedRelease(t, e.keys, "0.1.1")
	e.gh.setReleases(rel)
	e.gh.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, ".exe") {
			w.WriteHeader(http.StatusInternalServerError)
			return true
		}
		return false
	})
	st := e.check()
	if st.Latest == nil || st.Latest.Verified || st.Latest.RejectReason != UpdateRejectDownloadFailed {
		t.Fatalf("status = %+v (latest %+v)", st, st.Latest)
	}
	if e.toastCount() != 0 || e.leftoverDirs() != 0 {
		t.Errorf("toast/leftovers: %d / %d", e.toastCount(), e.leftoverDirs())
	}
	if err := e.apply(); !errors.Is(err, ErrUpdateUnavailable) {
		t.Errorf("Apply = %v", err)
	}
}

func TestUpdaterInstallerSizeCap(t *testing.T) {
	t.Run("size in the release listing", func(t *testing.T) {
		e := newUpdEnv(t)
		e.raw.maxInstaller = 100 // バイト
		e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
		st := e.check()
		if st.Latest == nil || st.Latest.Verified || st.LastError != UpdateErrTooLarge {
			t.Fatalf("status = %+v (latest %+v)", st, st.Latest)
		}
		if n := e.gh.hitCount(".exe"); n != 0 {
			t.Errorf("downloaded the oversized installer %d times", n)
		}
	})
	t.Run("actual body larger than the cap", func(t *testing.T) {
		e := newUpdEnv(t)
		e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
		e.raw.maxInstaller = 1 << 20
		// 一覧の size は上限以内と偽り、実際の本体は上限を超える（Content-Length なしのチャンク転送）。
		e.gh.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
			if strings.HasSuffix(r.URL.Path, ".exe") {
				chunk := bytes.Repeat([]byte("x"), 64<<10)
				for range 20 {
					_, _ = w.Write(chunk)
				}
				return true
			}
			return false
		})
		st := e.check()
		if st.Latest == nil || st.Latest.Verified || st.LastError != UpdateErrTooLarge {
			t.Fatalf("an oversized installer was accepted: %+v (%s)", st.Latest, st.LastError)
		}
		if e.leftoverDirs() != 0 {
			t.Error("an oversized download must be removed")
		}
	})
}

func TestUpdaterSignatureSizeCap(t *testing.T) {
	e := newUpdEnv(t)
	rel := signedRelease(t, e.keys, "0.1.1")
	rel.Assets["puppet-remo-0.1.1-amd64-installer.exe.minisig"] = bytes.Repeat([]byte("A"), maxSignatureBytes+1)
	e.gh.setReleases(rel)
	if st := e.check(); st.Latest == nil || st.Latest.Verified || st.Latest.RejectReason != UpdateRejectSignatureInvalid {
		t.Fatalf("latest = %+v", st.Latest)
	}
}

// ---- 秘密・送信先 ----

func TestUpdaterNeverLogsOrAuditsDownloadURLs(t *testing.T) {
	e := newUpdEnv(t)
	rel := signedRelease(t, e.keys, "0.1.1")
	e.gh.setReleases(rel)
	// ダウンロードは、クエリに署名つきトークンの付いた URL へのリダイレクト（GitHub の実際の配信）。失敗させて、エラーの経路を通す。
	e.gh.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case strings.HasPrefix(r.URL.Path, "/blob/"):
			w.WriteHeader(http.StatusInternalServerError)
			return true
		case strings.HasSuffix(r.URL.Path, ".exe"):
			http.Redirect(w, r, "/blob/object?X-Amz-Signature="+updSecretTag, http.StatusFound)
			return true
		}
		return false
	})
	e.check()
	e.setSetting("update.channel", "stable")
	all := e.logs.String()
	e.audit.mu.Lock()
	for _, en := range e.audit.entries {
		all += string(en.DetailJSON) + en.ErrorCode + en.Event
	}
	e.audit.mu.Unlock()
	for _, secret := range []string{updSecretTag, "X-Amz-Signature", e.gh.srv.URL, "127.0.0.1"} {
		if strings.Contains(all, secret) {
			t.Errorf("logs/audit leak %q:\n%s", secret, all)
		}
	}
	if !strings.Contains(all, "the update was not accepted") {
		t.Errorf("the failure was not logged:\n%s", all)
	}
	// 同じ経路で、成功したときも URL は記録されない。
	e.gh.setOverride(nil)
	e.check()
	e.audit.mu.Lock()
	defer e.audit.mu.Unlock()
	for _, en := range e.audit.entries {
		if strings.Contains(string(en.DetailJSON), "http") || strings.Contains(string(en.DetailJSON), ".exe") {
			t.Errorf("audit detail contains a URL or path: %s", en.DetailJSON)
		}
	}
}

func TestUpdaterRedirectsOnlyToAllowedHosts(t *testing.T) {
	e := newUpdEnv(t)
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("other")) }))
	defer other.Close()
	// 起点（127.0.0.1）と別のホスト名（localhost。同じサーバーを指していても、許可した名前ではない）への転送は拒否する。
	e.gh.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, ".minisig") {
			http.Redirect(w, r, strings.Replace(other.URL, "127.0.0.1", "localhost", 1)+"/x", http.StatusFound)
			return true
		}
		return false
	})
	st := e.check()
	if st.Latest == nil || st.Latest.Verified || st.Latest.RejectReason != UpdateRejectDownloadFailed {
		t.Fatalf("a redirect to a disallowed host was followed: %+v", st.Latest)
	}
}

func TestGithubHostAllowed(t *testing.T) {
	for host, want := range map[string]bool{
		"github.com": true, "api.github.com": true, "objects.githubusercontent.com": true, "release-assets.githubusercontent.com": true,
		"githubusercontent.com": true, "GitHub.com": true,
		"evil.com": false, "github.com.evil.com": false, "notgithub.com": false, "evilgithubusercontent.com": false, "": false,
	} {
		if got := githubHostAllowed(host); got != want {
			t.Errorf("githubHostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestUpdaterRequestsCarryNoCredentials(t *testing.T) {
	e := newUpdEnv(t)
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
	var headers []http.Header
	var mu sync.Mutex
	e.gh.setOverride(func(_ http.ResponseWriter, r *http.Request) bool {
		mu.Lock()
		headers = append(headers, r.Header.Clone())
		mu.Unlock()
		return false
	})
	e.check()
	mu.Lock()
	defer mu.Unlock()
	if len(headers) < 3 {
		t.Fatalf("requests = %d", len(headers))
	}
	for _, h := range headers {
		for _, k := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
			if h.Get(k) != "" {
				t.Errorf("request carries %s", k)
			}
		}
		if !strings.HasPrefix(h.Get("User-Agent"), "puppet-remo/") {
			t.Errorf("User-Agent = %q", h.Get("User-Agent"))
		}
	}
}

func TestUpdaterReleaseNotesAreSummarized(t *testing.T) {
	long := strings.Repeat("あ", maxReleaseNotesRunes+50)
	got := summarizeNotes("<script>alert(1)</script>\r\nline\x00two\x1b[31m\n" + long)
	if strings.ContainsAny(got, "\r\x00\x1b") {
		t.Errorf("control characters survived: %q", got)
	}
	if !strings.Contains(got, "<script>") { // HTML のエスケープは表示側（templ）の仕事で、ここでは文字列のまま
		t.Errorf("notes were altered: %q", got)
	}
	if n := len([]rune(got)); n > maxReleaseNotesRunes+1 {
		t.Errorf("notes are %d runes, want <= %d (+ the ellipsis)", n, maxReleaseNotesRunes)
	}
}

func TestCandidateFromIgnoresDraftsAndMislabeledAssets(t *testing.T) {
	asset := "puppet-remo-0.1.1-amd64-installer.exe"
	rel := githubRelease{TagName: "v0.1.1", Assets: []githubAsset{{Name: asset}, {Name: asset + ".minisig"}}}
	if c, ok := candidateFrom(rel); !ok || c.Version.String() != "0.1.1" || !c.HasSig {
		t.Fatalf("candidateFrom = %+v, %v", c, ok)
	}
	draft := rel
	draft.Draft = true
	if _, ok := candidateFrom(draft); ok {
		t.Error("a draft release was accepted")
	}
	// タグと添付の名前の版が違う（別の Release の添付の付け替え）は候補にしない。
	mislabeled := rel
	mislabeled.TagName = "v0.1.2"
	if _, ok := candidateFrom(mislabeled); ok {
		t.Error("an asset whose version differs from the tag was accepted")
	}
	// ナイトリーの版はタグ（nightly-YYYYMMDD-sha）と一致する。
	n := githubRelease{TagName: "nightly-20261102-abc1234", Prerelease: true,
		Assets: []githubAsset{{Name: "puppet-remo-0.1.1-nightly.20261102.abc1234-amd64-installer.exe"}}}
	if c, ok := candidateFrom(n); !ok || c.Version.String() != "0.1.1-nightly.20261102+abc1234" || c.HasSig {
		t.Errorf("nightly candidate = %+v, %v", c, ok)
	}
}

func TestUpdaterCloseRemovesUnappliedInstaller(t *testing.T) {
	e := newUpdEnv(t)
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
	e.check()
	if e.leftoverDirs() != 1 {
		t.Fatalf("dirs = %d, want 1", e.leftoverDirs())
	}
	e.up.Close()
	if e.leftoverDirs() != 0 {
		t.Error("Close left the downloaded installer behind")
	}
}

func TestUpdaterStartCleansStaleDirs(t *testing.T) {
	e := newUpdEnv(t)
	stale := filepath.Join(e.tmp, updateTempPrefix+"old")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(e.tmp, "unrelated")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	e.up.Start()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a stale update directory was not removed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("an unrelated directory was removed")
	}
}

func TestUpdaterApplyWhileCheckingIsBusy(t *testing.T) {
	e := newUpdEnv(t)
	e.gh.setReleases(signedRelease(t, e.keys, "0.1.1"))
	e.check()
	entered, release := make(chan struct{}), make(chan struct{})
	e.gh.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/repos/"+updRepo+"/releases/latest" {
			close(entered)
			<-release
		}
		return false
	})
	done := make(chan struct{})
	go func() { defer close(done); e.check() }()
	<-entered
	var busy *UpdateBusyError
	if err := e.apply(); !errors.As(err, &busy) || !slices.Equal(busy.Reasons, []string{UpdateBusyApplying}) {
		t.Errorf("Apply during a check = %v", err)
	}
	close(release)
	<-done
	if err := e.apply(); err != nil {
		t.Errorf("Apply after the check = %v", err)
	}
}
