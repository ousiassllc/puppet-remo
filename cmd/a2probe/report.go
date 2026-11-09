package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"
)

// 出力（report.txt・result.json）の型。記録するのは probe が起動して照合した fixture の窓だけ。
// 他アプリのタイトル・文書・窓の数は、型にも持たない（書く場所がない）。

// Status は検査 1 件の結果。
type Status string

const (
	StatusPass Status = "PASS" // 合格条件を満たした
	StatusFail Status = "FAIL" // 満たさなかった（ゲートの根拠。原因は Detail）
	StatusInfo Status = "INFO" // 合否の閾値を持たない記録
	StatusSkip Status = "SKIP" // 前提が成り立たず測れなかった（合格・不合格どちらにも数えない）
)

// Check は検査 1 件。ID は M1〜M8 と枝番（plan の表の項目）。
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// LinkFact は所有関係の鎖の 1 段。
type LinkFact struct {
	ID           string `json:"id"`
	PID          uint32 `json:"pid"`
	ProcessStart uint64 `json:"process_start"`
	Class        string `json:"class"`
	Unverifiable bool   `json:"unverifiable,omitempty"`
}

// WindowFact は fixture の窓 1 枚の事実と、ResolveGroup の判定。
type WindowFact struct {
	Stage          string     `json:"stage"`
	Role           string     `json:"role"` // どの fixture の窓か（main / interferer / foreign）
	ID             string     `json:"id"`
	PID            uint32     `json:"pid"`
	ProcessStart   uint64     `json:"process_start"`
	Class          string     `json:"class"`
	Rect           [4]int     `json:"rect"` // x, y, w, h（ListAll の見た目の枠）
	Style          string     `json:"style,omitempty"`
	OwnerChain     []LinkFact `json:"owner_chain,omitempty"`
	MenuOwnerID    string     `json:"menu_owner_id,omitempty"`
	MenuOwnerChain []LinkFact `json:"menu_owner_chain,omitempty"`
	Member         bool       `json:"member"`
	Relation       string     `json:"relation,omitempty"` // メンバーのとき
	Reason         string     `json:"reason,omitempty"`   // 非メンバーのとき
	Cause          string     `json:"cause,omitempty"`
}

// CaptureFact は PrintWindow 1 回の結果。
type CaptureFact struct {
	Stage        string         `json:"stage"`
	Role         string         `json:"role"`
	Class        string         `json:"class"`
	ID           string         `json:"id"`
	Flags        string         `json:"flags"`
	Returned     bool           `json:"returned"` // PrintWindow の戻り値
	Win32Error   string         `json:"win32_error,omitempty"`
	ElapsedMs    float64        `json:"elapsed_ms"`
	Size         [2]int         `json:"size"`          // 画像の大きさ（GetWindowRect の大きさ）
	WindowRect   [4]int         `json:"window_rect"`   // GetWindowRect: x, y, w, h
	FrameRect    [4]int         `json:"frame_rect"`    // DWM の拡張フレーム: x, y, w, h
	ClientRect   [4]int         `json:"client_rect"`   // 画像上のクライアント領域: x, y, w, h（verdict・untouched・black はこの範囲）
	Verdict      string         `json:"verdict"`       // content / untouched / all_black / uniform
	UntouchedPct float64        `json:"untouched_pct"` // PrintWindow が触らなかった画素の割合
	BlackPct     float64        `json:"black_pct"`
	Magenta      int            `json:"magenta_pixels"`
	Distinct     int            `json:"distinct_colors"`
	Watched      map[string]int `json:"watched_colors,omitempty"`
	PNG          string         `json:"png,omitempty"`
}

// Report は 1 回の実行の全記録。
type Report struct {
	Version string `json:"version"`
	// Gate は、この結果が成立性ゲート（S1〜S3）の最終合格ではないことを機械可読に示す（fixture だけの部分測定。製品のスコープ付き入力と
	// 実アプリでの確認は別の手順）。checks の PASS をゲートの合格と読まないための印で、常に GateFinal=false。
	GateFinal bool              `json:"gate_final"`
	GateNote  string            `json:"gate_note"`
	Env       map[string]string `json:"env"`
	Checks    []Check           `json:"checks"`
	Windows   []WindowFact      `json:"windows"`
	Captures  []CaptureFact     `json:"captures"`
	Notes     []string          `json:"notes,omitempty"`
}

func newReport() *Report {
	return &Report{Version: probeVersion, GateFinal: false, GateNote: gateNote, Env: map[string]string{}}
}

// Add は検査を 1 件足す。
func (r *Report) Add(id, title string, st Status, detailf string, args ...any) {
	d := detailf
	if len(args) > 0 {
		d = fmt.Sprintf(detailf, args...)
	}
	r.Checks = append(r.Checks, Check{ID: id, Title: title, Status: st, Detail: d})
}

// Note は検査にならない記録（手順の途中の事実）を足す。
func (r *Report) Note(f string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(f, args...))
}

// Counts は結果ごとの件数を返す。
func (r *Report) Counts() map[Status]int {
	c := map[Status]int{}
	for _, k := range r.Checks {
		c[k.Status]++
	}
	return c
}

// Failed は FAIL が 1 件でもあるかを返す。
func (r *Report) Failed() bool { return r.Counts()[StatusFail] > 0 }

// JSON は result.json の中身。
func (r *Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// Text は report.txt の中身。
func (r *Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "a2probe %s — Issue #156 A-2 成立性プローブ（fixture の窓だけを測定。S1〜S3 の最終合格ではない部分結果）\n\n", r.Version)
	b.WriteString("[環境]\n")
	for _, k := range sortedKeys(r.Env) {
		fmt.Fprintf(&b, "  %s = %s\n", k, r.Env[k])
	}
	b.WriteString("\n[検査]\n")
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "  %-5s %-6s %s\n", c.Status, c.ID, c.Title)
		if c.Detail != "" {
			for line := range strings.SplitSeq(c.Detail, "\n") {
				fmt.Fprintf(&b, "                %s\n", line)
			}
		}
	}
	cnt := r.Counts()
	fmt.Fprintf(&b, "\n[集計] PASS=%d FAIL=%d INFO=%d SKIP=%d\n", cnt[StatusPass], cnt[StatusFail], cnt[StatusInfo], cnt[StatusSkip])
	if len(r.Notes) > 0 {
		b.WriteString("\n[記録]\n")
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "  - %s\n", n)
		}
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

// gateNote は result.json の gate_note。
const gateNote = "fixture だけの部分測定。S1〜S3（製品のスコープ付き入力＋実アプリでの表示と操作の完了）の最終合格ではない。checks の PASS をゲートの合格として扱わない"

// errText は err の文字列から、ファイルのパス（ユーザー名を含みうる）を除く。report.txt・result.json に利用者の環境のパスを書かないため。
func errText(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Op + ": " + pe.Err.Error()
	}
	return err.Error()
}
