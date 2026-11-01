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

// 設定が真偽値でない・false のときは本文を記録しない（記録しない側に倒す）。
func TestControlRecordTextIgnoresInvalidSetting(t *testing.T) {
	e := newCtlEnv(t)
	e.settings.put(settingAuditRecordText, `"yes"`)
	d := recordTextDetail(t, e, ctlAlice, domain.TypeText{Text: "abc"})
	if _, ok := d["text"]; ok {
		t.Fatalf("detail = %v", d)
	}
}
