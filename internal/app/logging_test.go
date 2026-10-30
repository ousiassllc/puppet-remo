package app

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDataDir(t *testing.T) {
	got, err := DataDir(`C:\Users\alice\AppData\Local`)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(`C:\Users\alice\AppData\Local`, "puppet-remo"); got != want {
		t.Errorf("DataDir = %q, want %q (NFR-S11)", got, want)
	}
	if _, err := DataDir(""); err == nil {
		t.Error("an empty LOCALAPPDATA must be an error")
	}
}

func TestParseLogLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"": slog.LevelInfo, "  ": slog.LevelInfo, "debug": slog.LevelDebug, "INFO": slog.LevelInfo,
		"warn": slog.LevelWarn, "Error": slog.LevelError,
	} {
		got, err := ParseLogLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLogLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseLogLevel("loud"); err == nil {
		t.Error("an unknown level must be an error")
	}
}

// NFR-O01: JSON の slog を logs\ に出す。レベル未満は出さない。
func TestNewLoggerWritesJSONUnderLogs(t *testing.T) {
	dir := t.TempDir()
	log, closer, err := NewLogger(dir, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	log.Debug("hidden")
	log.Info("hello", "k", "v")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "logs", "puppet-remo.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("log lines = %q, want only the info line", lines)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	if rec["msg"] != "hello" || rec["k"] != "v" || rec["level"] != "INFO" {
		t.Errorf("record = %v", rec)
	}
}
