package app

import (
	"bytes"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"aead.dev/minisign"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// このファイルは更新（service.Updater。#74）の組み立てを確かめる: 偽の GitHub（httptest）・テスト専用の鍵・偽のタイマーで、
// GUI の /update/check → /update/apply が、署名を検証してインストーラを起動し、アプリの終了を依頼し、kind = update で監査する。

const (
	updAsset = "puppet-remo-0.1.1-amd64-installer.exe"
	updRepo  = "/repos/" + service.DefaultUpdateRepo
)

type updateRig struct {
	e       *env
	sink    *auditSink
	gui     guiClient
	timers  *manualTimers
	data    []byte
	quitted chan struct{}
}

// manualTimers は service.AfterFunc の偽物（張ったタイマーを溜めるだけ）。
type manualTimers struct {
	mu sync.Mutex
	ds []time.Duration
}

func (m *manualTimers) AfterFunc(d time.Duration, _ func()) func() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ds = append(m.ds, d)
	return func() bool { return true }
}

func (m *manualTimers) all() []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.ds)
}

func newUpdateRig(t *testing.T, sign bool) *updateRig {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubText, _ := pub.MarshalText()
	data := []byte("installer-" + strings.Repeat("x", 512))
	sig := func() []byte {
		r := minisign.NewReader(bytes.NewReader(data))
		_, _ = io.Copy(io.Discard, r)
		return r.SignWithComments(priv, service.UpdateTrustedComment("0.1.1", updAsset), "test")
	}()
	if !sign {
		sig = nil
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case updRepo + "/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tag_name":"v0.1.1","body":"notes <b>x</b>","published_at":"2026-11-01T00:00:00Z","assets":[`+
				`{"name":"`+updAsset+`","size":`+itoa(len(data))+`},{"name":"`+updAsset+`.minisig","size":10}]}`)
		case "/" + service.DefaultUpdateRepo + "/releases/download/v0.1.1/" + updAsset:
			_, _ = w.Write(data)
		case "/" + service.DefaultUpdateRepo + "/releases/download/v0.1.1/" + updAsset + ".minisig":
			if sig == nil {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gh.Close)

	sink := &auditSink{}
	timers := &manualTimers{}
	e := newEnv(t, func(c *Config) {
		c.Repo = newAuditRepo(c.Repo, sink)
		c.Version = "0.1.0"
		c.Update = UpdateOptions{PublicKey: pubText, APIBase: gh.URL, DownloadBase: gh.URL, TempDir: t.TempDir(), AfterFunc: timers.AfterFunc}
	})
	e.installer.AcceptAny()
	rig := &updateRig{e: e, sink: sink, gui: guiClient{t: t, h: e.app.GUIHandler()}, timers: timers, data: data, quitted: make(chan struct{}, 1)}
	e.app.SetQuit(func() { rig.quitted <- struct{}{} })
	return rig
}

func itoa(n int) string { return strconv.Itoa(n) }

func (r *updateRig) updateEvents() []string {
	r.e.t.Helper()
	if err := r.e.app.audit.Flush(r.e.t.Context()); err != nil {
		r.e.t.Fatal(err)
	}
	var out []string
	for _, en := range r.sink.all() {
		if en.Kind == "update" {
			out = append(out, en.Event+":"+string(en.ActorType))
		}
	}
	return out
}

func TestUpdateFlowThroughTheGUI(t *testing.T) {
	r := newUpdateRig(t, true)

	page := r.gui.do(http.MethodGet, "/update", nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "未確認") || !strings.Contains(page.Body.String(), "0.1.0") {
		t.Fatalf("GET /update = %d\n%s", page.Code, page.Body)
	}

	res := r.gui.do(http.MethodPost, "/update/check", url.Values{})
	body := res.Body.String()
	if res.Code != http.StatusOK || !strings.Contains(body, "署名を確認済み") || !strings.Contains(body, "更新する") ||
		strings.Contains(body, "<b>x</b>") { // リリースノートは HTML として解釈しない
		t.Fatalf("POST /update/check = %d\n%s", res.Code, body)
	}

	res = r.gui.do(http.MethodPost, "/update/apply", url.Values{})
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "更新を開始しました。") {
		t.Fatalf("POST /update/apply = %d\n%s", res.Code, res.Body)
	}
	if got := r.e.installer.Started(); len(got) != 1 || !strings.HasSuffix(got[0], updAsset) {
		t.Fatalf("installer started = %v", got)
	}
	select {
	case <-r.quitted:
	case <-time.After(waitLimit):
		t.Fatal("the app was not asked to quit after the installer started")
	}
	if got, want := r.updateEvents(), []string{"update.detected:host", "update.apply_started:host"}; !slices.Equal(got, want) {
		t.Errorf("audit = %v, want %v", got, want)
	}
	for _, en := range r.sink.all() {
		if en.Kind == "update" && (strings.Contains(string(en.DetailJSON), "http") || strings.Contains(string(en.DetailJSON), ".exe")) {
			t.Errorf("update audit leaks a URL or path: %s", en.DetailJSON)
		}
	}
}

func TestUpdateWithoutSignatureIsNotApplied(t *testing.T) {
	r := newUpdateRig(t, false)
	res := r.gui.do(http.MethodPost, "/update/check", url.Values{})
	if body := res.Body.String(); res.Code != http.StatusOK || !strings.Contains(body, "署名ファイルがありません") || strings.Contains(body, "更新する") {
		t.Fatalf("POST /update/check = %d\n%s", res.Code, body)
	}
	if res := r.gui.do(http.MethodPost, "/update/apply", url.Values{}); res.Code != http.StatusConflict {
		t.Fatalf("POST /update/apply = %d, want 409\n%s", res.Code, res.Body)
	}
	if n := len(r.e.installer.Started()); n != 0 {
		t.Errorf("installer started %d times", n)
	}
	if got, want := r.updateEvents(), []string{"update.verify_failed:host"}; !slices.Equal(got, want) {
		t.Errorf("audit = %v, want %v", got, want)
	}
}

func TestUpdateApplyIsConflictWhileALeaseIsHeld(t *testing.T) {
	r := newUpdateRig(t, true)
	clientID, _ := r.e.registerClient()
	r.gui.do(http.MethodPost, "/update/check", url.Values{})
	who := domain.Actor{Kind: domain.ActorMCP, ClientID: clientID, Label: "c1", SessionID: "s1"}
	if _, err := r.e.app.leases.Acquire(t.Context(), who); err != nil {
		t.Fatal(err)
	}
	res := r.gui.do(http.MethodPost, "/update/apply", url.Values{})
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "操作権（リース）を保持しているクライアントがいます。") {
		t.Fatalf("POST /update/apply with a lease = %d\n%s", res.Code, res.Body)
	}
	if n := len(r.e.installer.Started()); n != 0 {
		t.Errorf("installer started %d times while a lease was held", n)
	}
}

// 定期確認は Start が張り（起動の 1 分後）、Shutdown が止める。dev ビルドは張らない。
func TestUpdateScheduleStartsWithTheApp(t *testing.T) {
	r := newUpdateRig(t, true)
	if got := r.timers.all(); len(got) != 0 {
		t.Fatalf("timers before Start = %v", got)
	}
	r.e.start()
	if got := r.timers.all(); len(got) != 1 || got[0] != service.DefaultUpdateStartDelay {
		t.Errorf("timers after Start = %v, want [%v]", got, service.DefaultUpdateStartDelay)
	}

	dev := newEnv(t, func(c *Config) { c.Version = "dev"; c.Update.AfterFunc = r.timers.AfterFunc })
	dev.start()
	if got := r.timers.all(); len(got) != 1 {
		t.Errorf("a dev build scheduled an update check: %v", got)
	}
}

func TestSettingsPageOffersUpdateChannel(t *testing.T) {
	e := newEnv(t)
	res := guiClient{t: t, h: e.app.GUIHandler()}.do(http.MethodGet, "/settings", nil)
	body := res.Body.String()
	if res.Code != http.StatusOK || !strings.Contains(body, `name="update.channel"`) || !strings.Contains(body, `<option value="stable" selected>`) {
		t.Errorf("GET /settings = %d\n%s", res.Code, body)
	}
}
