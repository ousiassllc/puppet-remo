package mcp

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

type fileBody struct {
	OK            bool   `json:"ok"`
	Path          string `json:"path"`
	Offset        int64  `json:"offset"`
	Length        int    `json:"length"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	EOF           bool   `json:"eof"`
	ContentBase64 string `json:"content_base64"`
}

func TestFileToolsRoundTrip(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")

	res := callTool(t, cs, "put_file", map[string]any{"path": "logs/app.log", "content_base64": b64("hello")})
	if res.IsError {
		t.Fatal(res.text())
	}
	var put fileBody
	res.decode(t, &put)
	if !put.OK || put.Path != "logs/app.log" || put.Size != 5 || put.SHA256 != fakeSum([]byte("hello")) {
		t.Fatalf("put = %+v", put)
	}
	// 出力にホストの絶対パスの入る余地が無い（キーは ok・path・size・sha256 だけ）。
	if got := res.keys(t); !slices.Equal(got, []string{"ok", "path", "sha256", "size"}) {
		t.Errorf("put keys = %v", got)
	}

	res = callTool(t, cs, "get_file", map[string]any{"path": "logs/app.log"})
	if res.IsError {
		t.Fatal(res.text())
	}
	var got fileBody
	res.decode(t, &got)
	raw, err := base64.StdEncoding.DecodeString(got.ContentBase64)
	if err != nil || string(raw) != "hello" || got.Length != 5 || got.Size != 5 || got.SHA256 != put.SHA256 || !got.EOF {
		t.Fatalf("get = %+v (%q, %v)", got, raw, err)
	}

	// 範囲の読み取りと続き。
	res = callTool(t, cs, "get_file", map[string]any{"path": "logs/app.log", "offset": 1, "length": 2})
	res.decode(t, &got)
	if raw, _ := base64.StdEncoding.DecodeString(got.ContentBase64); string(raw) != "el" || got.EOF || got.Size != 5 || got.SHA256 != put.SHA256 || got.Offset != 1 {
		t.Fatalf("ranged get = %+v (%q)", got, raw)
	}
	if r := e.files.gets[len(e.files.gets)-1]; r.Offset != 1 || r.Length != 2 {
		t.Errorf("get request = %+v", r)
	}

	// 分割して送る。
	res = callTool(t, cs, "put_file", map[string]any{"path": "logs/app.log", "content_base64": b64(" world"), "offset": 5})
	res.decode(t, &put)
	if res.IsError || put.Size != 11 || put.SHA256 != fakeSum([]byte("hello world")) {
		t.Fatalf("append = %+v, %s", put, res.text())
	}

	res = callTool(t, cs, "list_files", nil)
	if res.IsError {
		t.Fatal(res.text())
	}
	var list struct {
		Path      string `json:"path"`
		Truncated bool   `json:"truncated"`
		Entries   []struct {
			Path     string `json:"path"`
			Type     string `json:"type"`
			Size     int64  `json:"size"`
			Modified string `json:"modified"`
		} `json:"entries"`
	}
	res.decode(t, &list)
	if len(list.Entries) != 1 || list.Entries[0].Path != "logs/app.log" || list.Entries[0].Type != "file" || list.Entries[0].Size != 11 ||
		list.Entries[0].Modified != "2026-11-28T09:00:00Z" || list.Truncated {
		t.Fatalf("list = %+v", list)
	}

	res = callTool(t, cs, "delete_file", map[string]any{"path": "logs/app.log"})
	var del fileBody
	res.decode(t, &del)
	if res.IsError || !del.OK || del.Size != 11 {
		t.Fatalf("delete = %+v, %s", del, res.text())
	}
	if code := callTool(t, cs, "get_file", map[string]any{"path": "logs/app.log"}).errCode(t); code != domain.CodeFileNotFound {
		t.Errorf("get after delete = %s", code)
	}
}

func TestListFilesEmptyIsAnEmptyArray(t *testing.T) {
	cs := newEnv(t).connect("c1")
	res := callTool(t, cs, "list_files", nil)
	if res.IsError {
		t.Fatal(res.text())
	}
	if entries, ok := res.StructuredContent.(map[string]any)["entries"].([]any); !ok || len(entries) != 0 {
		t.Errorf("entries = %#v, want []", res.StructuredContent.(map[string]any)["entries"])
	}
}

func TestPutFileRejectsBadContent(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	for name, content := range map[string]string{
		"not base64":      "!!!not base64!!!",
		"url alphabet":    "_-_-",
		"over one call":   strings.Repeat("A", maxBase64Len+4),
		"missing padding": "aGVsbG8",
	} {
		res := callTool(t, cs, "put_file", map[string]any{"path": "a.txt", "content_base64": content})
		if code := res.errCode(t); code != domain.CodeInvalidArgument {
			t.Errorf("%s: code = %s", name, code)
		}
	}
	if len(e.files.puts) != 0 {
		t.Errorf("FileTransfer.Put was called with invalid content: %+v", e.files.puts)
	}
	// 空のファイルは作れる（content_base64 は空でもよい）。
	res := callTool(t, cs, "put_file", map[string]any{"path": "empty.txt", "content_base64": ""})
	if res.IsError {
		t.Fatalf("empty file: %s", res.text())
	}
	// 1 回の上限ちょうどは通る。
	full := strings.Repeat("x", domain.MaxFileChunkBytes)
	if res := callTool(t, cs, "put_file", map[string]any{"path": "full.bin", "content_base64": b64(full)}); res.IsError {
		t.Fatalf("a full chunk: %s", res.text())
	}
	if len(base64.StdEncoding.EncodeToString([]byte(full))) > maxBase64Len {
		t.Errorf("maxBase64Len %d is below a full chunk's base64", maxBase64Len)
	}
	// 必須の引数が無ければスキーマ違反（SDK が拒否する）。
	if res := callTool(t, cs, "put_file", map[string]any{"path": "a.txt"}); !res.IsError {
		t.Error("put_file without content_base64 succeeded")
	}
}

func TestFileToolErrorsAreMapped(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	for err, want := range map[*domain.Error]domain.Code{
		domain.ErrFileTooLarge:  domain.CodeFileTooLarge,
		domain.ErrFileStoreFull: domain.CodeFileStoreFull,
		domain.ErrInvalidArgument.WithMsg("path must be relative to the transfer folder"): domain.CodeInvalidArgument,
	} {
		e.files.err = err
		res := callTool(t, cs, "put_file", map[string]any{"path": "a.txt", "content_base64": b64("x")})
		if code := res.errCode(t); code != want {
			t.Errorf("code = %s, want %s", code, want)
		}
	}
	// 想定外の失敗は internal で、内容を出さない。
	e.files.err = domain.NewError(domain.CodeInternal, "open /secret/host/path: boom", nil)
	res := callTool(t, cs, "get_file", map[string]any{"path": "a.txt"})
	if code := res.errCode(t); code != domain.CodeInternal || strings.Contains(res.text(), "/secret/host/path") {
		t.Errorf("internal error = %s %q", code, res.text())
	}
}

func TestFileToolsNeedTheLease(t *testing.T) {
	e := newEnv(t)
	holder := e.connect("c1")
	if res := callTool(t, holder, "acquire_control", nil); res.IsError {
		t.Fatal(res.text())
	}
	other := e.connect("c2")
	calls := map[string]map[string]any{
		"put_file":    {"path": "a.txt", "content_base64": b64("x")},
		"get_file":    {"path": "a.txt"},
		"list_files":  nil,
		"delete_file": {"path": "a.txt"},
	}
	for tool, args := range calls {
		if code := callTool(t, other, tool, args).errCode(t); code != domain.CodeLeaseHeldByOther {
			t.Errorf("%s while another session holds the lease: %s", tool, code)
		}
	}
	if len(e.files.files) != 0 {
		t.Errorf("a file was written without the lease: %v", e.files.files)
	}
}

func TestFileToolsAcquireTheLeaseImplicitly(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	if res := callTool(t, cs, "put_file", map[string]any{"path": "a.txt", "content_base64": b64("x")}); res.IsError {
		t.Fatal(res.text())
	}
	if h, ok := e.leases.Holder(); !ok || h.Actor.ClientID != "c1" {
		t.Fatalf("Holder = %+v, %v; want c1 to hold the lease after put_file", h, ok)
	}
	// 暗黙のリース取得は承認待ちでブロックしうるので、長いタイムアウトの対象。
	for _, tool := range []string{"put_file", "get_file", "list_files", "delete_file"} {
		if !leaseAcquiringTools[tool] {
			t.Errorf("%s is not in leaseAcquiringTools", tool)
		}
	}
}

func TestFileToolsKillSwitch(t *testing.T) {
	e := newEnv(t)
	cs := e.connect("c1")
	e.kill.active.Store(true)
	calls := map[string]map[string]any{
		"put_file":    {"path": "a.txt", "content_base64": b64("x")},
		"get_file":    {"path": "a.txt"},
		"list_files":  nil,
		"delete_file": {"path": "a.txt"},
	}
	for tool, args := range calls {
		if code := callTool(t, cs, tool, args).errCode(t); code != domain.CodeKillSwitchActive {
			t.Errorf("%s: code = %s", tool, code)
		}
	}
}

func TestNewRequiresFiles(t *testing.T) {
	e := newEnv(t)
	if _, err := New(Config{Control: e.ctl, Leases: e.leases, Principal: testPrincipal}); err == nil {
		t.Error("New without Files succeeded")
	}
}
