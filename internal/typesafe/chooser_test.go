package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 明らかな偽物のキー。ログ・エラー・応答に出ないことをテストで確かめる。
const canaryKey = "canary-NOT-A-REAL-KEY-7f3a9c"

func fixedKey(key string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return key, nil }
}

// okHandler はゴールデンの応答を返す。
func okHandler(t *testing.T) http.HandlerFunc {
	resp := readFixture(t, "response.json")
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}
}

func newServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestChooseSuccess(t *testing.T) {
	var (
		mu      sync.Mutex
		gotBody []byte
		gotHdr  http.Header
		gotPath string
		gotMeth string
	)
	ok := okHandler(t)
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotHdr, gotPath, gotMeth = b, r.Header.Clone(), r.URL.Path, r.Method
		mu.Unlock()
		ok(w, r)
	}))

	c := New(fixedKey(canaryKey), Config{Endpoint: srv.URL + "/v1/systemone"})
	got, err := c.Choose(t.Context(), fixtureRequest())
	if err != nil {
		t.Fatal(err)
	}
	if want := (ChoiceResult{Choice: "b1c2d3e4f5a60718", Confidence: 0.91, GoalMaybeDone: 0.12}); got != want {
		t.Fatalf("result = %+v, want %+v", got, want)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMeth != http.MethodPost || gotPath != "/v1/systemone" {
		t.Errorf("request = %s %s", gotMeth, gotPath)
	}
	if h := gotHdr.Get("Authorization"); h != "Bearer "+canaryKey {
		t.Errorf("Authorization header not sent as Bearer key")
	}
	if ct := gotHdr.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	// 本文にキーが入っていないこと（キーはヘッダーだけ）。
	if bytes.Contains(gotBody, []byte(canaryKey)) {
		t.Error("API key leaked into request body")
	}
	var sent wireRequest
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Model != "jev-1.13" || len(sent.Questions) != 2 {
		t.Errorf("sent = %+v", sent)
	}
}

// キーは呼び出しごとに読む（登録し直し・削除がすぐ効く）。
func TestChooseReadsKeyPerCall(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []string
	)
	ok := okHandler(t)
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		ok(w, r)
	}))
	keys := []string{"key-one", "key-two"}
	var n int
	c := New(func(context.Context) (string, error) { k := keys[n]; n++; return k, nil }, Config{Endpoint: srv.URL})
	for range keys {
		if _, err := c.Choose(t.Context(), fixtureRequest()); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "Bearer key-one" || seen[1] != "Bearer key-two" {
		t.Fatalf("Authorization headers = %q", seen)
	}
}

func TestChooseHTTPStatusClasses(t *testing.T) {
	for _, c := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusTooManyRequests, ErrUnavailable},
		{http.StatusInternalServerError, ErrUnavailable},
		{http.StatusBadGateway, ErrUnavailable},
		{http.StatusServiceUnavailable, ErrUnavailable},
		{http.StatusBadRequest, ErrUnavailable},
		{http.StatusUnprocessableEntity, ErrUnavailable},
		{http.StatusForbidden, ErrUnavailable},
	} {
		t.Run(fmt.Sprint(c.status), func(t *testing.T) {
			var hits atomic.Int32
			srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(`{"detail":"nope"}`))
			}))
			_, err := New(fixedKey(canaryKey), Config{Endpoint: srv.URL}).Choose(t.Context(), fixtureRequest())
			assertOnly(t, err, c.want)
			// 自動再試行はしない（NFR-S18）。
			if n := hits.Load(); n != 1 {
				t.Errorf("server hit %d times, want 1", n)
			}
		})
	}
}

// assertOnly は err が want に当たり、他の 2 つの分類には当たらないことを確かめる。
func assertOnly(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	for _, other := range []error{ErrUnavailable, ErrUnauthorized, ErrBadResponse} {
		if other != want && errors.Is(err, other) {
			t.Fatalf("err = %v also matches %v", err, other)
		}
	}
}

func TestChooseBadResponseBodies(t *testing.T) {
	for name, body := range map[string]string{
		"JSON でない":        `<html>gateway</html>`,
		"空本文":             ``,
		"項目なし":            `{"model":"m","answers":{}}`,
		"送っていない選択肢":       answers(`"choice":"zzz","confidence":0.9`, `"noul":0.1`),
		"範囲外の confidence": answers(`"choice":"abstain","confidence":3`, `"noul":0.1`),
	} {
		t.Run(name, func(t *testing.T) {
			srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			_, err := New(fixedKey(canaryKey), Config{Endpoint: srv.URL}).Choose(t.Context(), fixtureRequest())
			assertOnly(t, err, ErrBadResponse)
		})
	}
}

func TestChooseOversizedResponseIsBadResponse(t *testing.T) {
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat(" ", maxResponseBody+10)))
	}))
	_, err := New(fixedKey(canaryKey), Config{Endpoint: srv.URL}).Choose(t.Context(), fixtureRequest())
	assertOnly(t, err, ErrBadResponse)
}

// リダイレクトは辿らない。送信先は api.typesafe.ai だけ（NFR-S18）で、キーを別の宛先へ送らない。
func TestChooseDoesNotFollowRedirects(t *testing.T) {
	var other atomic.Int32
	target := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		other.Add(1)
	}))
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	_, err := New(fixedKey(canaryKey), Config{Endpoint: srv.URL}).Choose(t.Context(), fixtureRequest())
	assertOnly(t, err, ErrUnavailable)
	if other.Load() != 0 {
		t.Fatal("redirect target was contacted")
	}
}

func TestChooseNetworkErrorIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // 接続を拒否させる
	_, err := New(fixedKey(canaryKey), Config{Endpoint: url}).Choose(t.Context(), fixtureRequest())
	assertOnly(t, err, ErrUnavailable)
}

// 本文が MaxBody を超えるときは、送信前に ErrUnavailable（キーの読み出しも通信もしない）。
func TestChooseOversizedRequestBodyNotSent(t *testing.T) {
	var hits, keyReads atomic.Int32
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	key := func(context.Context) (string, error) { keyReads.Add(1); return canaryKey, nil }

	req := fixtureRequest()
	req.Goal = strings.Repeat("あ", 200)
	body, _, err := marshalRequest(req)
	if err != nil {
		t.Fatal(err)
	}

	// 境界: ちょうど上限なら送り、1 バイト超えたら送らない。
	atLimit := New(key, Config{Endpoint: srv.URL, MaxBody: len(body)})
	_, err = atLimit.Choose(t.Context(), req)
	if hits.Load() != 1 {
		t.Fatalf("body of exactly MaxBody bytes was not sent: hits=%d err=%v", hits.Load(), err)
	}
	hits.Store(0)
	keyReads.Store(0)

	over := New(key, Config{Endpoint: srv.URL, MaxBody: len(body) - 1})
	_, err = over.Choose(t.Context(), req)
	assertOnly(t, err, ErrUnavailable)
	if hits.Load() != 0 || keyReads.Load() != 0 {
		t.Fatalf("oversized request reached the network: hits=%d keyReads=%d", hits.Load(), keyReads.Load())
	}
}

func TestChooseDefaultMaxBodyIs256KiB(t *testing.T) {
	var hits atomic.Int32
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	req := fixtureRequest()
	req.Goal = strings.Repeat("x", 256<<10)
	_, err := New(fixedKey(canaryKey), Config{Endpoint: srv.URL}).Choose(t.Context(), req)
	assertOnly(t, err, ErrUnavailable)
	if hits.Load() != 0 {
		t.Fatal("oversized request was sent")
	}
}

func TestConfigDefaults(t *testing.T) {
	c := newChooser(fixedKey("k"), Config{}, http.DefaultTransport)
	if c.endpoint != "https://api.typesafe.ai/v1/systemone" || c.timeout != 5*time.Second || c.maxBody != 256<<10 {
		t.Fatalf("defaults = %q %v %d", c.endpoint, c.timeout, c.maxBody)
	}
}

// 呼び出しの期限（Config.Timeout）に達したら ErrUnavailable。ハンドラーは通知用チャネルで待たせ、sleep は使わない。
func TestChooseTimeoutIsUnavailable(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var hits atomic.Int32
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		started <- struct{}{}
		_, _ = io.Copy(io.Discard, r.Body) // 本文を読み切ると、サーバーが切断を検知できる
		select {                           // クライアントが諦めるまで応答しない（解放は後始末の保険）
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release) }) // newServer の後始末（Close）より先に走らせる
	c := New(fixedKey(canaryKey), Config{Endpoint: srv.URL, Timeout: 50 * time.Millisecond})

	done := make(chan error, 1)
	go func() {
		_, err := c.Choose(t.Context(), fixtureRequest())
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("handler was not reached")
	}
	select {
	case err := <-done:
		assertOnly(t, err, ErrUnavailable)
		if errors.Is(err, context.Canceled) {
			t.Errorf("timeout must not look like caller cancellation: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Choose did not time out")
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d, want 1 (no retry)", hits.Load())
	}
}

// 呼び出し側の ctx が取り消されたら、HTTP を中断して ctx のエラーを返す（ErrUnavailable にしない）。
func TestChooseContextCancel(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release) })
	c := New(fixedKey(canaryKey), Config{Endpoint: srv.URL, Timeout: time.Minute})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.Choose(ctx, fixtureRequest())
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("handler was not reached")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if errors.Is(err, ErrUnavailable) {
			t.Fatalf("cancellation must not be reported as ErrUnavailable: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Choose did not return after cancel")
	}
}

func TestChooseAlreadyCancelledContextSendsNothing(t *testing.T) {
	var hits, keyReads atomic.Int32
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	key := func(context.Context) (string, error) { keyReads.Add(1); return canaryKey, nil }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := New(key, Config{Endpoint: srv.URL}).Choose(ctx, fixtureRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if hits.Load() != 0 || keyReads.Load() != 0 {
		t.Fatalf("hits=%d keyReads=%d", hits.Load(), keyReads.Load())
	}
}

func TestChooseKeyProblems(t *testing.T) {
	var hits atomic.Int32
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))

	t.Run("キーの読み出しに失敗", func(t *testing.T) {
		boom := errors.New("credential manager locked")
		key := func(context.Context) (string, error) { return "", boom }
		_, err := New(key, Config{Endpoint: srv.URL}).Choose(t.Context(), fixtureRequest())
		assertOnly(t, err, ErrUnavailable)
		if !errors.Is(err, boom) {
			t.Errorf("cause lost: %v", err)
		}
	})
	for name, k := range map[string]string{
		"空のキー":    "",
		"空白を含む":   "has space",
		"改行を含む":   "line\nbreak",
		"NUL を含む": "nul\x00byte",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(fixedKey(k), Config{Endpoint: srv.URL}).Choose(t.Context(), fixtureRequest())
			assertOnly(t, err, ErrUnauthorized)
			if k != "" && strings.Contains(err.Error(), k) {
				t.Errorf("key leaked into error: %q", err)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("request was sent with an unusable key (%d hits)", hits.Load())
	}
}

func TestChooseRejectsUnusableCandidatesBeforeSending(t *testing.T) {
	var hits, keyReads atomic.Int32
	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	key := func(context.Context) (string, error) { keyReads.Add(1); return canaryKey, nil }
	req := fixtureRequest()
	req.Elements = append(req.Elements, req.Elements[0]) // 重複
	_, err := New(key, Config{Endpoint: srv.URL}).Choose(t.Context(), req)
	assertOnly(t, err, ErrBadResponse)
	if hits.Load() != 0 || keyReads.Load() != 0 {
		t.Fatalf("hits=%d keyReads=%d", hits.Load(), keyReads.Load())
	}
}

// NFR-S14・NFR-S18: キーはログにもエラーにも出ない。サーバーがキーを応答本文に反射しても、
// どの失敗でも返るエラーとログ出力に現れない。
func TestKeyNeverAppearsInErrorsOrLogs(t *testing.T) {
	var logs bytes.Buffer
	prevSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	prevLogOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&logs)
	t.Cleanup(func() {
		slog.SetDefault(prevSlog)
		log.SetOutput(prevLogOut)
		log.SetFlags(prevFlags)
	})

	srv := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 受け取った Authorization をそのまま応答に反射する。
		reflected := r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/401":
			w.WriteHeader(http.StatusUnauthorized)
		case "/500":
			w.WriteHeader(http.StatusInternalServerError)
		case "/429":
			w.WriteHeader(http.StatusTooManyRequests)
		}
		_, _ = w.Write([]byte(reflected))
	}))

	var errs []error
	for _, path := range []string{"/401", "/500", "/429", "/200-garbage"} {
		_, err := New(fixedKey(canaryKey), Config{Endpoint: srv.URL + path}).Choose(t.Context(), fixtureRequest())
		if err == nil {
			t.Fatalf("%s: expected an error", path)
		}
		errs = append(errs, err)
	}
	// 接続失敗（URL 入りの *url.Error）と期限切れ。
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	_, err := New(fixedKey(canaryKey), Config{Endpoint: deadURL}).Choose(t.Context(), fixtureRequest())
	errs = append(errs, err)
	// 空のキーでも、不正なキーでも、キー自体は文面に出ない。
	_, err = New(fixedKey(canaryKey+" bad"), Config{Endpoint: srv.URL}).Choose(t.Context(), fixtureRequest())
	errs = append(errs, err)

	for _, err := range errs {
		for _, format := range []string{"%v", "%+v", "%#v", "%q", "%s"} {
			if s := fmt.Sprintf(format, err); strings.Contains(s, canaryKey) {
				t.Errorf("key leaked via %s: %s", format, s)
			}
		}
	}
	if strings.Contains(logs.String(), canaryKey) {
		t.Errorf("key leaked into logs: %s", logs.String())
	}
}

// 差し替えられたトランスポートがキーをエラーに入れても、redactTransport が伏せる。
func TestRedactTransportScrubsKeyFromErrors(t *testing.T) {
	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial failed while sending %q", req.Header.Get("Authorization"))
	})
	c := newChooser(fixedKey(canaryKey), Config{Endpoint: "http://example.invalid/v1/systemone"}, redactTransport{inner: inner})
	_, err := c.Choose(t.Context(), fixtureRequest())
	assertOnly(t, err, ErrUnavailable)
	if strings.Contains(err.Error(), canaryKey) {
		t.Fatalf("key leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected a redaction marker: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
