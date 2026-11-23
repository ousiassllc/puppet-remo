package main

import (
	"fmt"
	"strings"
)

// probe と fixture の標準入出力の行プロトコル。ソケットは開かない。
//
//	fixture → probe: READY k=v ...（起動完了と名乗り）、ACK <cmd> ...（指示の受領）、EVT <name> k=v ...（出来事）、ERR <cmd> k=v ...
//	probe → fixture: CMD <name> [k=v ...]
//
// 1 行 = 動詞 + 空白区切りの項目。項目は k=v で、v に空白は入れない。

// message は 1 行の内容。
type message struct {
	Verb   string
	Name   string            // CMD・ACK・EVT・ERR の対象名。READY は空
	Fields map[string]string // k=v の項目
}

// formatMessage は 1 行を作る。name が空なら省く。kv は k, v, k, v ...（v の空白は '_' にする）。
func formatMessage(verb, name string, kv ...string) string {
	var b strings.Builder
	b.WriteString(verb)
	if name != "" {
		b.WriteByte(' ')
		b.WriteString(name)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteByte(' ')
		b.WriteString(kv[i])
		b.WriteByte('=')
		b.WriteString(strings.NewReplacer(" ", "_", "\n", "_", "\r", "_").Replace(kv[i+1]))
	}
	return b.String()
}

// parseMessage は 1 行を読む。動詞が空・不明な形はエラー。
func parseMessage(line string) (message, error) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return message{}, fmt.Errorf("empty line")
	}
	m := message{Verb: parts[0], Fields: map[string]string{}}
	rest := parts[1:]
	if m.Verb != "READY" && len(rest) > 0 && !strings.Contains(rest[0], "=") {
		m.Name = rest[0]
		rest = rest[1:]
	}
	for _, p := range rest {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return message{}, fmt.Errorf("malformed field %q", p)
		}
		m.Fields[k] = v
	}
	switch m.Verb {
	case "READY", "ACK", "EVT", "ERR", "CMD":
		return m, nil
	}
	return message{}, fmt.Errorf("unknown verb %q", m.Verb)
}
