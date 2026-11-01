package app

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// secretForms は token の平文・そのハッシュ（SHA-256）の 16 進・base64（標準・URL）・Authorization ヘッダーの値。
// どれもログにも画面にも出てはならない（NFR-S13）。
func secretForms(token string) []string {
	h := repository.HashToken(token)
	return []string{
		token, token[len("prm_"):],
		hex.EncodeToString(h[:]), strings.ToUpper(hex.EncodeToString(h[:])),
		base64.StdEncoding.EncodeToString(h[:]), base64.RawURLEncoding.EncodeToString(h[:]),
		"Bearer " + token,
	}
}

func assertNoSecrets(t *testing.T, where, text string, secrets []string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(text, s) {
			t.Errorf("%s contains the secret %q", where, s)
		}
	}
}

func readLogFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// waitForLog はログファイルに want が現れるまで待つ（ログは別 goroutine から出ることがある）。
func waitForLog(t *testing.T, path, want string) {
	t.Helper()
	eventually(t, "log line "+want, func() bool {
		raw, err := os.ReadFile(path)
		return err == nil && strings.Contains(string(raw), want)
	})
}

func getGUI(t *testing.T, h http.Handler, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", target, rec.Code)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	return string(body)
}

// 安全網: 呼び出し側が秘密を渡してしまっても、NewLogger はキー名（token・hash・authorization など）と、文字列・エラーの中の
// トークン・ハッシュ・Bearer の値を隠す。token_id のような ID は隠さない。
func TestLoggerRedactsSecretsEvenWhenCallersPassThem(t *testing.T) {
	const token = "prm_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	secrets := secretForms(token)
	dir := t.TempDir()
	log, closer, err := NewLogger(dir, slog.LevelDebug)
	if err != nil {
		t.Fatal(err)
	}
	h := repository.HashToken(token)
	log.Info("issued "+token, "token", token, "token_hash", h[:], "hash", hex.EncodeToString(h[:]),
		"authorization", "Bearer "+token, "password", "hunter2", "api_key", "sk-123", "OPENAI_API_KEY", "sk-456",
		"error", errors.New("lookup "+token+" failed"), "detail", "got "+hex.EncodeToString(h[:])+" and Bearer "+token,
		slog.Group("req", slog.String("secret", "s3cr3t"), slog.String("note", "token "+token)),
		"token_id", "tok-001", "label", "laptop")
	log.Warn("bearer authentication failed", "reason", "unknown")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	raw := readLogFile(t, LogFilePath(dir))
	assertNoSecrets(t, "the log file", raw, append(secrets, "hunter2", "sk-123", "sk-456", "s3cr3t"))
	for _, keep := range []string{`"token_id":"tok-001"`, `"label":"laptop"`, `"reason":"unknown"`, redactedValue} {
		if !strings.Contains(raw, keep) {
			t.Errorf("log file lost %q:\n%s", keep, raw)
		}
	}
	if !strings.Contains(raw, "bearer authentication failed") {
		t.Error("an ordinary sentence containing the word 'bearer' must not be redacted")
	}
}

// 防御の二重化: ファイルに（別の経路で）秘密が入っていても、ログ画面の読み出しが隠す。
func TestLogReaderRedactsSecretsAlreadyInTheFile(t *testing.T) {
	const token = "prm_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	h := repository.HashToken(token)
	r := NewLogReader(t.TempDir())
	writeLogFile(t, r.Path(),
		logLine(0, "ERROR", "failed for "+token, "token", fmt.Sprintf("%q", token), "x", fmt.Sprintf("%q", hex.EncodeToString(h[:])), "authorization", `"Bearer `+token+`"`))
	p, err := r.Entries(t.Context(), service.AppLogAll, 1)
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("entries = %v, %v", p.Items, err)
	}
	out := fmt.Sprint(p.Items[0])
	assertNoSecrets(t, "the log page", out, secretForms(token))
	if !strings.Contains(out, redactedValue) {
		t.Errorf("nothing was redacted: %s", out)
	}
}

// トークンまわりの実際の経路（発行・成功した接続・不正なトークン・失効したトークン・キルスイッチ）を通し、
// ログファイルと、ログ画面（GUI の /logs。全レベルと絞り込み）に、トークンの平文もハッシュも出ないこと。
// あわせて、成功（INFO）と失敗（WARN）の両方が出ていること（FR-H13）。
func TestAppLogNeverContainsTokenSecretsAndShowsSuccessAndFailure(t *testing.T) {
	dir := t.TempDir()
	log, closer, err := NewLogger(dir, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	e := newEnv(t, func(c *Config) { c.Logger = log; c.Logs = NewLogReader(dir) })
	logPath := LogFilePath(dir)

	clientID, token := e.registerClient() // 発行（token issued）
	secrets := secretForms(token)
	e.start()
	e.watcher.Set(runningStatus(testTailnetIP))
	e.waitListening(1)

	// 成功: 接続してリースを取得する。
	cs := e.dialMCP(token)
	if res := callTool(t, cs, "acquire_control"); res.IsError {
		t.Fatalf("acquire_control failed: %+v", res.Content)
	}

	// 失敗: 形式は正しいが未知のトークン・形式の壊れたトークン・失効したトークン。
	unknown := "prm_" + strings.Repeat("A", 43) // 形式は正しく（末尾のビットが 0）、DB にない
	malformed := "prm_tooShort"
	secrets = append(secrets, unknown, malformed)
	for _, bad := range []string{unknown, malformed} {
		secrets = append(secrets, secretForms(bad)...)
		post(t, e, bad, http.StatusUnauthorized)
	}
	tokens, err := e.app.admin.Tokens(t.Context(), clientID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("tokens = %v, %v", tokens, err)
	}
	if err := e.app.admin.RevokeToken(t.Context(), clientID, tokens[0].ID); err != nil {
		t.Fatal(err)
	}
	post(t, e, token, http.StatusUnauthorized)

	// キルスイッチ。
	if !e.hotkey.Trigger(defaultHotkey) {
		t.Fatal("the kill switch hotkey is not registered")
	}
	for _, want := range []string{"token issued", "tailscaled is running", "tailnet listener is listening", "lease acquired", "mcp tool succeeded", "bearer authentication failed", "token revoked", "kill switch triggered", "lease released"} {
		waitForLog(t, logPath, want)
	}

	raw := readLogFile(t, logPath)
	assertNoSecrets(t, "the log file", raw, secrets)
	for _, want := range []string{`"reason":"unknown"`, `"reason":"malformed"`, `"reason":"revoked"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("log file lacks %s", want)
		}
	}

	// ログ画面: 成功（INFO）と失敗（WARN）がどちらも出て、秘密は出ない。
	h := e.app.GUIHandler()
	all := getGUI(t, h, "/logs")
	assertNoSecrets(t, "GET /logs", all, secrets)
	for _, want := range []string{`data-log-level="INFO"`, `data-log-level="WARN"`, "token issued", "lease acquired", "bearer authentication failed", "kill switch triggered"} {
		if !strings.Contains(all, want) {
			t.Errorf("GET /logs lacks %s", want)
		}
	}
	warn := getGUI(t, h, "/logs?level=warn")
	assertNoSecrets(t, "GET /logs?level=warn", warn, secrets)
	if !strings.Contains(warn, "bearer authentication failed") || strings.Contains(warn, `data-log-level="INFO"`) {
		t.Errorf("GET /logs?level=warn must show WARN entries and no INFO entries:\n%s", warn)
	}
	if !strings.Contains(all, logPath) && !strings.Contains(all, strings.ReplaceAll(logPath, "&", "&amp;")) {
		t.Errorf("GET /logs does not show the log file path %q", logPath)
	}
}

// post は token（空なら無し）で /mcp へ POST し、want のステータスを確かめる。
func post(t *testing.T, e *env, token string, want int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://"+testTailnetIP+":8765/mcp", strings.NewReader("{}"))
	resp, err := (&http.Client{Transport: remoteTransport{e: e, token: token}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("POST /mcp with %q = %d, want %d", token, resp.StatusCode, want)
	}
}
