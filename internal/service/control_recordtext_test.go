package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// recordTextDetail は op を実行し、最後の監査エントリの detail_json を返す。
func recordTextDetail(t *testing.T, e *ctlEnv, who domain.Actor, op domain.Op) map[string]any {
	t.Helper()
	if _, err := e.control.Do(context.Background(), who, op); err != nil {
		t.Fatal(err)
	}
	entries := e.audit.Entries()
	var detail map[string]any
	if err := json.Unmarshal(entries[len(entries)-1].DetailJSON, &detail); err != nil {
		t.Fatal(err)
	}
	return detail
}

// audit.record_text が false（既定）でも true でも、トークンの形・Bearer は detail_json に入らない。
// false（既定）は文字数だけ（FR-AU03）。true は text に本文が入る（FR-S07）。
func TestControlRecordText(t *testing.T) {
	const body = "memo こんにちは"
	tests := []struct {
		name string
		who  domain.Actor
		op   func(e *ctlEnv) domain.Op
	}{
		{"mcp type", ctlAlice, func(*ctlEnv) domain.Op { return domain.TypeText{Text: body} }},
		{"mcp clipboard_write", ctlAlice, func(*ctlEnv) domain.Op { return domain.ClipboardWrite{Text: body} }},
		{"mcp clipboard_read", ctlAlice, func(e *ctlEnv) domain.Op {
			e.clipboard.SetText(body)
			return domain.ClipboardRead{}
		}},
		{"web input.text", ctlViewer, func(*ctlEnv) domain.Op { return domain.TypeText{Text: body} }},
		{"web clipboard write", ctlViewer, func(*ctlEnv) domain.Op { return domain.ClipboardWrite{Text: body} }},
	}
	for _, tt := range tests {
		t.Run(tt.name+" default", func(t *testing.T) {
			e := newCtlEnv(t)
			if tt.who == ctlViewer {
				mustAcquire(t, e, ctlViewer)
			}
			d := recordTextDetail(t, e, tt.who, tt.op(e))
			if _, ok := d["text"]; ok || d["text_len"] != float64(len([]rune(body))) {
				t.Fatalf("default detail = %v, want length only", d)
			}
		})
		t.Run(tt.name+" enabled", func(t *testing.T) {
			e := newCtlEnv(t)
			e.settings.put(settingAuditRecordText, "true")
			if tt.who == ctlViewer {
				mustAcquire(t, e, ctlViewer)
			}
			d := recordTextDetail(t, e, tt.who, tt.op(e))
			if d["text"] != body || d["text_len"] != float64(len([]rune(body))) {
				t.Fatalf("enabled detail = %v, want text and length", d)
			}
			if _, ok := d["text_truncated"]; ok {
				t.Fatalf("short text must not be marked truncated: %v", d)
			}
		})
	}
}

func TestControlRecordTextTruncatesAndNeverKeepsTokens(t *testing.T) {
	e := newCtlEnv(t)
	e.settings.put(settingAuditRecordText, "true")
	long := strings.Repeat("あ", recordedTextMaxRunes+10)
	d := recordTextDetail(t, e, ctlAlice, domain.TypeText{Text: long})
	if got := d["text"].(string); len([]rune(got)) != recordedTextMaxRunes || d["text_truncated"] != true || d["text_len"] != float64(recordedTextMaxRunes+10) {
		t.Fatalf("detail text runes=%d truncated=%v len=%v", len([]rune(got)), d["text_truncated"], d["text_len"])
	}

	const token = "prm_" + "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef0123456789_-x"
	d = recordTextDetail(t, e, ctlAlice, domain.ClipboardWrite{Text: "key " + token + " and Authorization: Bearer " + token})
	raw, _ := json.Marshal(d)
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "ABCDEFGHIJ") {
		t.Fatalf("token leaked into detail: %s", raw)
	}
}

// 切り詰めの境界をまたぐ秘密も伏せる。先に切り詰めると、残った断片が形の最小長を割って伏せられずに残る（NFR-S13）。
func TestControlRecordTextRedactsSecretsStraddlingTruncationBoundary(t *testing.T) {
	const (
		bearerToken = "FAKETOKENFAKETOKENFAKETOKENFAKETOKEN"
		hexSecret   = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	)
	tests := []struct {
		name   string
		secret string
		kept   int // 切り詰め前の本文で、境界の手前に残る秘密の文字数
		marker string
	}{
		{"bearer", "Bearer " + bearerToken, len("Bearer ") + 13, "FAKETOKEN"},
		{"hex64", hexSecret, 30, "deadbeef"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newCtlEnv(t)
			e.settings.put(settingAuditRecordText, "true")
			text := strings.Repeat("あ", recordedTextMaxRunes-tt.kept) + tt.secret + strings.Repeat("あ", 100)
			d := recordTextDetail(t, e, ctlAlice, domain.TypeText{Text: text})
			got, _ := d["text"].(string)
			if strings.Contains(got, tt.marker) {
				t.Fatalf("secret fragment leaked across the truncation boundary: %q", string([]rune(got)[recordedTextMaxRunes-60:]))
			}
			if n := len([]rune(got)); n > recordedTextMaxRunes {
				t.Fatalf("recorded text runes = %d, want <= %d", n, recordedTextMaxRunes)
			}
			if d["text_truncated"] != true || d["text_len"] != float64(len([]rune(text))) {
				t.Fatalf("text_truncated = %v, text_len = %v, want true and %d", d["text_truncated"], d["text_len"], len([]rune(text)))
			}
		})
	}
}

// 設定が真偽値でない・false のときは本文を記録しない（記録しない側に倒す）。
func TestControlRecordTextIgnoresInvalidSetting(t *testing.T) {
	e := newCtlEnv(t)
	e.settings.put(settingAuditRecordText, `"yes"`)
	d := recordTextDetail(t, e, ctlAlice, domain.TypeText{Text: "abc"})
	if _, ok := d["text"]; ok {
		t.Fatalf("detail = %v", d)
	}
}

// key の監査（FR-AU03）: 表示できる 1 文字のキーは keys に残さない（パスワードを 1 文字ずつ key で打てるため、type の本文と同じ扱い）。
// 名前つきのキー（enter・tab・escape・方向キー・f1）と修飾キーの名前は残す。本文は audit.record_text が true の mcp / web だけ text に入る。
func TestControlKeyPressAuditRedactsTypedCharacters(t *testing.T) {
	keys := func(s string) domain.Op {
		k, err := domain.ParseKeyCombo(s)
		if err != nil {
			t.Fatal(err)
		}
		return domain.KeyPress{Keys: k}
	}
	tests := []struct {
		keys    string
		want    string // detail の keys
		textLen bool   // text_len = 1 が付くか（伏せたとき）
	}{
		{"a", "[char]", true},
		{"7", "[char]", true},
		{"!", "[char]", true},
		{"あ", "[char]", true},
		{"shift+a", "shift+[char]", true},
		{"ctrl+shift+t", "ctrl+shift+[char]", true},
		{"enter", "enter", false},
		{"tab", "tab", false},
		{"escape", "escape", false},
		{"space", "space", false},
		{"left", "left", false},
		{"f5", "f5", false},
		{"ctrl+alt+tab", "ctrl+alt+tab", false},
	}
	for _, tt := range tests {
		t.Run(tt.keys+" default", func(t *testing.T) {
			e := newCtlEnv(t)
			d := recordTextDetail(t, e, ctlAlice, keys(tt.keys))
			if d["keys"] != tt.want {
				t.Fatalf("keys = %v, want %q (detail %v)", d["keys"], tt.want, d)
			}
			if _, ok := d["text"]; ok {
				t.Fatalf("the default must not record the character: %v", d)
			}
			if _, ok := d["text_len"]; ok != tt.textLen {
				t.Fatalf("text_len presence = %v, want %v (detail %v)", ok, tt.textLen, d)
			}
		})
		t.Run(tt.keys+" record_text", func(t *testing.T) {
			e := newCtlEnv(t)
			e.settings.put(settingAuditRecordText, "true")
			d := recordTextDetail(t, e, ctlAlice, keys(tt.keys))
			_, hasText := d["text"]
			if hasText != tt.textLen {
				t.Fatalf("text presence = %v, want %v (detail %v)", hasText, tt.textLen, d)
			}
			if d["keys"] != tt.want {
				t.Fatalf("keys = %v, want %q", d["keys"], tt.want)
			}
		})
	}

	t.Run("web and agent never keep the character by default", func(t *testing.T) {
		e := newCtlEnv(t)
		mustAcquire(t, e, ctlViewer)
		d := recordTextDetail(t, e, ctlViewer, keys("x"))
		if d["keys"] != "[char]" {
			t.Fatalf("web keys = %v", d["keys"])
		}
		je := newJudgeEnv(t, domain.ApprovalAuto, &stubJudge{v: domain.VerdictAllow})
		je.settings.put(settingAuditRecordText, "true")
		if _, err := je.control.Do(context.Background(), ctlAgent, keys("q")); err != nil {
			t.Fatal(err)
		}
		for _, en := range je.audit.Entries() {
			if strings.Contains(string(en.DetailJSON), `"q"`) || strings.Contains(string(en.DetailJSON), `"text"`) {
				t.Errorf("agent audit keeps the character: %s", en.DetailJSON)
			}
		}
	})
}
