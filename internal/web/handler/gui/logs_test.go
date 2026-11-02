package gui_test

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/handler/gui/guitest"
)

// logEntry は記録 1 件を作る（時計の ago 前）。
func logEntry(f *guitest.Fakes, ago time.Duration, level slog.Level, label, msg string, attrs ...string) service.AppLogEntry {
	e := service.AppLogEntry{Time: f.Clock.T.Add(-ago), Level: label, Severity: level, Message: msg}
	for i := 0; i+1 < len(attrs); i += 2 {
		e.Attrs = append(e.Attrs, service.AppLogAttr{Key: attrs[i], Value: attrs[i+1]})
	}
	return e
}

func seedLogs(f *guitest.Fakes) {
	f.Logs.Items = []service.AppLogEntry{ // 新しい順
		logEntry(f, time.Minute, slog.LevelError, "ERROR", "mcp tool failed", "tool", "screenshot", "error", "capture failed"),
		logEntry(f, 2*time.Minute, slog.LevelWarn, "WARN", "bearer authentication failed", "reason", "unknown", "device", "alice-laptop"),
		logEntry(f, 3*time.Minute, slog.LevelInfo, "INFO", "lease acquired", "holder", "alice-laptop"),
		logEntry(f, 4*time.Minute, slog.LevelInfo, "INFO", "tailnet listener is listening", "addrs", "[100.64.0.1:8765]"),
	}
}

func TestLogsPageShowsSuccessAndErrorEntriesNewestFirst(t *testing.T) {
	r := newRig(t, seedLogs)
	res := r.get("/logs")
	status(t, res, http.StatusOK)
	contains(t, res, "<html", "<title>ログ - puppet-remo</title>", "日時", "レベル", "内容",
		`data-log-level="ERROR"`, `data-log-level="WARN"`, `data-log-level="INFO"`,
		"mcp tool failed", "capture failed", "bearer authentication failed", "alice-laptop", "lease acquired", "tailnet listener is listening",
		"2026-10-30 11:59:00", // 時計は 2026-10-30 12:00（UTC）の 1 分前
		`data-log-path`, `puppet-remo.log`,
		"監査ログ", // 監査ログとの違いの案内
		`aria-current="page"`, `href="/logs"`)
	// サイドバーの「ログ」が現在の項目になる。
	if !strings.Contains(res.Body, `href="/logs"`) {
		t.Error("the sidebar has no link to /logs")
	}
	order := []string{"mcp tool failed", "bearer authentication failed", "lease acquired", "tailnet listener is listening"}
	last := -1
	for _, m := range order {
		i := strings.Index(res.Body, m)
		if i < last {
			t.Fatalf("log is not newest first: %q appears before the previous one", m)
		}
		last = i
	}
	// ログの閲覧は読み取り専用: 変更・消去の操作は出さない。操作はログフォルダーを開くボタン（POST /logs/open-folder）だけ（#54）。
	lacks(t, res, "data-pager", `method="post"`, "hx-put", "hx-delete", "hx-patch")
	contains(t, res, `hx-post="/logs/open-folder"`, "ログフォルダーを開く", `data-log-open-folder`, `hx-swap="none"`, `hx-disabled-elt="this"`)
	if n := strings.Count(res.Body, "hx-post="); n != 1 {
		t.Errorf("hx-post occurrences = %d, want only the open-folder button", n)
	}
}

func TestLogsLevelFilter(t *testing.T) {
	r := newRig(t, seedLogs)
	warn := r.get("/logs?level=warn")
	status(t, warn, http.StatusOK)
	contains(t, warn, "mcp tool failed", "bearer authentication failed", `data-log-filter="warn"`)
	lacks(t, warn, "lease acquired", "tailnet listener is listening")

	errOnly := r.get("/logs?level=error")
	contains(t, errOnly, "mcp tool failed", `data-log-filter="error"`)
	lacks(t, errOnly, "bearer authentication failed", "lease acquired")

	// 無い・未知の値は all。大文字小文字は区別しない。
	for _, q := range []string{"", "?level=all", "?level=bogus"} {
		res := r.get("/logs" + q)
		status(t, res, http.StatusOK)
		contains(t, res, `data-log-filter="all"`, "lease acquired")
	}
	contains(t, r.get("/logs?level=ERROR"), `data-log-filter="error"`)

	// 絞り込みのリンクは 3 つ。今の絞り込みだけが aria-current。
	contains(t, errOnly, `data-log-filter-link="all"`, `data-log-filter-link="warn"`, `data-log-filter-link="error"`,
		`hx-get="/logs?level=warn"`, `hx-get="/logs?level=error"`, `hx-get="/logs"`, `hx-target="#app-log"`, `hx-push-url="true"`)
	if n := strings.Count(errOnly.Body, `aria-current="true"`); n != 1 {
		t.Errorf("aria-current filters = %d, want 1", n)
	}
}

func TestLogsEmptyState(t *testing.T) {
	r := newRig(t)
	res := r.get("/logs")
	status(t, res, http.StatusOK)
	contains(t, res, `data-empty="app-log"`, "表示できるログはありません")
	lacks(t, res, "<tbody", "data-pager")

	r2 := newRig(t, seedLogs)
	none := r2.get("/logs?level=error&page=9")
	status(t, none, http.StatusOK)
	contains(t, none, `data-empty="app-log"`, "data-pager-prev")
}

func TestLogsPagingKeepsTheFilterInLinks(t *testing.T) {
	size := service.AppLogPageSize
	total := size*2 + 3
	r := newRig(t, func(f *guitest.Fakes) {
		for i := range total {
			f.Logs.Items = append(f.Logs.Items, logEntry(f, time.Duration(i+1)*time.Minute, slog.LevelError, "ERROR", fmt.Sprintf("failure-%03d", i)))
		}
		f.Logs.Items = append(f.Logs.Items, logEntry(f, 1000*time.Minute, slog.LevelInfo, "INFO", "ancient"))
	})
	p1 := r.get("/logs?level=error")
	if n := strings.Count(p1.Body, "data-log-level="); n != size {
		t.Errorf("page 1 rows = %d, want %d", n, size)
	}
	contains(t, p1, "failure-000", `href="/logs?level=error&amp;page=2"`, "data-pager-next", "1 ページ目")
	lacks(t, p1, "data-pager-prev", "ancient")

	p3 := r.get("/logs?level=error&page=3")
	if n := strings.Count(p3.Body, "data-log-level="); n != 3 {
		t.Errorf("page 3 rows = %d, want 3", n)
	}
	contains(t, p3, fmt.Sprintf("failure-%03d", total-1), `href="/logs?level=error&amp;page=2"`, "data-pager-prev", "3 ページ目")
	lacks(t, p3, "data-pager-next")

	// 不正なページ番号は 1。フィルターの戻りリンク（ページ 1）は page を付けない。
	for _, q := range []string{"&page=0", "&page=-1", "&page=abc"} {
		contains(t, r.get("/logs?level=error"+q), "failure-000", "1 ページ目")
	}
	p2 := r.get("/logs?level=error&page=2")
	contains(t, p2, `href="/logs?level=error"`, "data-pager-prev")
}

func TestLogsFragmentVariants(t *testing.T) {
	r := newRig(t, seedLogs)
	frag := r.get("/logs?level=warn", hx...)
	status(t, frag, http.StatusOK)
	contains(t, frag, `id="app-log"`, "bearer authentication failed", `data-log-filter="warn"`)
	lacks(t, frag, "<html", "<body", "<h1", "lease acquired")

	boosted := r.get("/logs", "HX-Request", "true", "HX-Boosted", "true")
	status(t, boosted, http.StatusOK)
	contains(t, boosted, "<title>ログ - puppet-remo</title>", `id="app-log"`, `id="sidebar-nav"`)
	lacks(t, boosted, "<html")

	restore := r.get("/logs", "HX-Request", "true", "HX-History-Restore-Request", "true")
	contains(t, restore, "<html", `id="app-log"`)
}

// ログの内容は HTML としてエスケープされる（ログの文字列がタグとして解釈されない）。
func TestLogsEscapesEntryText(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) {
		f.Logs.Items = []service.AppLogEntry{logEntry(f, time.Minute, slog.LevelWarn, "WARN", `<script>alert(1)</script>`, "k", `"><img src=x>`)}
	})
	res := r.get("/logs")
	contains(t, res, "&lt;script&gt;alert(1)&lt;/script&gt;")
	lacks(t, res, "<script>alert(1)", "<img src=x>")
}

func TestLogsErrorRendersErrorPage(t *testing.T) {
	r := newRig(t, func(f *guitest.Fakes) { f.Logs.Err = errors.New("disk boom") })
	res := r.get("/logs")
	status(t, res, http.StatusInternalServerError)
	contains(t, res, "<html", `data-error-code="internal"`)
	lacks(t, res, "disk boom")
}

// ---- ログフォルダーを開く（#54） ----

const logFolderPath = `C:\Users\demo\AppData\Local\puppet-remo\logs`

func TestLogsPageHasTheOpenFolderButtonInEveryFullRender(t *testing.T) {
	r := newRig(t, seedLogs)
	for _, hdr := range [][]string{nil, {"HX-Request", "true", "HX-Boosted", "true"}} {
		contains(t, r.get("/logs", hdr...), `hx-post="/logs/open-folder"`, "ログフォルダーを開く")
	}
	// 絞り込み・ページ送りのフラグメント（#app-log だけ）にはボタンを含めない（差し替えで消えない場所に置く）。
	lacks(t, r.get("/logs", hx...), "/logs/open-folder")
}

func TestOpenLogFolderOpensTheFolderAndShowsAToast(t *testing.T) {
	r := newRig(t)
	res := r.post("/logs/open-folder", nil, hx...)
	status(t, res, http.StatusOK)
	must(t, r.f.Logs.OpenFolderCalls == 1, "OpenFolder calls = %d, want 1", r.f.Logs.OpenFolderCalls)
	contains(t, res, `hx-swap-oob="beforeend:#toast-root"`, `data-toast-level="info"`, "ログフォルダーを開きました。")
	lacks(t, res, "<html", "puppet-remo.log", logFolderPath)
}

// 開く場所はリクエストで決まらない。フォーム・クエリのパスらしい値は読まず、応答にも出さない。
func TestOpenLogFolderIgnoresAnyRequestedPath(t *testing.T) {
	r := newRig(t)
	const evil = `C:\Windows\System32`
	res := r.post("/logs/open-folder?path="+url.QueryEscape(evil)+"&dir="+url.QueryEscape(evil),
		url.Values{"path": {evil}, "dir": {evil}, "folder": {evil}}, hx...)
	status(t, res, http.StatusOK)
	must(t, r.f.Logs.OpenFolderCalls == 1, "OpenFolder calls = %d, want 1", r.f.Logs.OpenFolderCalls)
	lacks(t, res, evil, "System32")
}

// GET では動かない（POST だけの経路。ページ遷移・プリフェッチ・<img> などで開かせない）。
func TestOpenLogFolderIsPostOnly(t *testing.T) {
	r := newRig(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		status(t, r.do(method, "/logs/open-folder", nil, hx...), http.StatusNotFound)
	}
	must(t, r.f.Logs.OpenFolderCalls == 0, "OpenFolder calls = %d, want 0", r.f.Logs.OpenFolderCalls)
}

func TestOpenLogFolderFailureIsAFixedToastWithoutDetails(t *testing.T) {
	const osErr = "ShellExecute failed at " + logFolderPath
	for _, tt := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{"missing folder", fmt.Errorf("%w: %s", service.ErrNotFound, logFolderPath), http.StatusNotFound, "not_found", "ログフォルダーが見つかりません。"},
		{"os error", errors.New(osErr), http.StatusInternalServerError, "internal", "ログフォルダーを開けませんでした。"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, func(f *guitest.Fakes) { f.Logs.OpenFolderErr = tt.err })
			res := r.post("/logs/open-folder", nil, hx...)
			status(t, res, tt.wantStatus)
			contains(t, res, `data-error-code="`+tt.wantCode+`"`, tt.wantMsg, `data-toast-level="error"`)
			lacks(t, res, logFolderPath, "ShellExecute", `C:\`)
			if got := res.Header.Get("HX-Retarget"); got != "#toast-root" {
				t.Errorf("HX-Retarget = %q, want #toast-root", got)
			}
			if strings.Contains(fmt.Sprint(res.Header), logFolderPath) {
				t.Errorf("headers leak the path: %v", res.Header)
			}
			must(t, r.f.Logs.OpenFolderCalls == 1, "OpenFolder calls = %d, want 1", r.f.Logs.OpenFolderCalls)
		})
	}
}
