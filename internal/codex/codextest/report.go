package codextest

import (
	"encoding/json"
	"os"
	"strings"
)

// Report は HelperMain が残す記録。
type Report struct {
	Args     []string `json:"args"`
	EnvNames []string `json:"envNames"`
	CodexDir string   `json:"codexHome"`
	Cwd      string   `json:"cwd"`
}

func writeReport(path string) {
	r := Report{Args: os.Args[1:], CodexDir: os.Getenv("CODEX_HOME")}
	r.Cwd, _ = os.Getwd()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		r.EnvNames = append(r.EnvNames, name)
	}
	b, _ := json.Marshal(r)
	_ = os.WriteFile(path, b, 0o600)
}

// ReadReport は HelperMain の記録を読む。
func ReadReport(path string) (Report, error) {
	var r Report
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(b, &r)
}
