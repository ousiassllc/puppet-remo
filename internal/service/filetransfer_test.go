package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/localfs"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// ftEnv はファイル転送のテストの部品: 本物のリース・キルスイッチ・監査の偽物と、一時フォルダの中の置き場所（localfs の実ファイルシステム）。
type ftEnv struct {
	*ctlEnv
	ft      FileTransfer
	base    string // 一時フォルダ（置き場所の親）
	root    string // 既定の置き場所
	dataDir string
}

func newFtEnv(t *testing.T) *ftEnv {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &ftEnv{ctlEnv: newCtlEnv(t), base: base, root: filepath.Join(base, "inbox"), dataDir: filepath.Join(base, "data")}
	e.ft, err = NewFileTransfer(FileTransferConfig{
		Files: localfs.New(), Leases: e.leases, Kill: e.kill, Settings: e.settings, DefaultRoot: e.root,
		ProtectedDirs: []string{e.dataDir}, Audit: e.audit, Clock: e.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func ftSum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (e *ftEnv) put(who domain.Actor, path, data string) (FileResult, error) {
	e.t.Helper()
	return e.ft.Put(context.Background(), who, FilePutRequest{Path: path, Data: []byte(data)})
}

func TestFileTransferRoundTrip(t *testing.T) {
	e := newFtEnv(t)
	ctx := context.Background()

	res, err := e.put(ctlAlice, "build/app.txt", "hello")
	if err != nil || res.Size != 5 || res.SHA256 != ftSum("hello") || res.Path != "build/app.txt" {
		t.Fatalf("Put = %+v, %v", res, err)
	}
	// 既定の置き場所（無ければ作る）の中に置かれる。
	if b, err := os.ReadFile(filepath.Join(e.root, "build", "app.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("file on disk = %q, %v", b, err)
	}

	chunk, err := e.ft.Get(ctx, ctlAlice, FileGetRequest{Path: "build/app.txt"})
	if err != nil || string(chunk.Data) != "hello" || chunk.Size != 5 || chunk.SHA256 != ftSum("hello") || !chunk.EOF || chunk.Offset != 0 {
		t.Fatalf("Get = %+v, %v", chunk, err)
	}
	chunk, err = e.ft.Get(ctx, ctlAlice, FileGetRequest{Path: "build/app.txt", Offset: 1, Length: 2})
	if err != nil || string(chunk.Data) != "el" || chunk.EOF || chunk.SHA256 != ftSum("hello") {
		t.Fatalf("ranged Get = %+v, %v", chunk, err)
	}

	list, err := e.ft.List(ctx, ctlAlice, "")
	if err != nil || len(list.Entries) != 1 || list.Entries[0].Path != "build" || !list.Entries[0].IsDir {
		t.Fatalf("List = %+v, %v", list, err)
	}
	list, err = e.ft.List(ctx, ctlAlice, "build")
	if err != nil || len(list.Entries) != 1 || list.Entries[0].Path != "build/app.txt" || list.Entries[0].Size != 5 {
		t.Fatalf("List(build) = %+v, %v", list, err)
	}

	del, err := e.ft.Delete(ctx, ctlAlice, "build/app.txt")
	if err != nil || del.Size != 5 {
		t.Fatalf("Delete = %+v, %v", del, err)
	}
	if _, err := e.ft.Get(ctx, ctlAlice, FileGetRequest{Path: "build/app.txt"}); !errors.Is(err, domain.ErrFileNotFound) {
		t.Fatalf("Get after Delete: %v", err)
	}
}

func TestFileTransferChunkedPut(t *testing.T) {
	e := newFtEnv(t)
	ctx := context.Background()
	if _, err := e.ft.Put(ctx, ctlAlice, FilePutRequest{Path: "big.bin", Data: []byte("abc")}); err != nil {
		t.Fatal(err)
	}
	res, err := e.ft.Put(ctx, ctlAlice, FilePutRequest{Path: "big.bin", Offset: 3, Data: []byte("def")})
	if err != nil || res.Size != 6 || res.SHA256 != ftSum("abcdef") {
		t.Fatalf("append = %+v, %v", res, err)
	}
	if _, err := e.ft.Put(ctx, ctlAlice, FilePutRequest{Path: "big.bin", Offset: 1, Data: []byte("x")}); domain.CodeOf(err) != domain.CodeConflict {
		t.Fatalf("append at a wrong offset: %v", err)
	}
	if _, err := e.ft.Put(ctx, ctlAlice, FilePutRequest{Path: "big.bin", Offset: -1, Data: []byte("x")}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("negative offset: %v", err)
	}
	tooBig := make([]byte, domain.MaxFileChunkBytes+1)
	if _, err := e.ft.Put(ctx, ctlAlice, FilePutRequest{Path: "huge.bin", Data: tooBig}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("one call over the chunk size: %v", err)
	}
	// Get は 1 回の上限で切って、続きを offset で取れる。
	big := bytes.Repeat([]byte("z"), domain.MaxFileChunkBytes)
	if _, err := e.ft.Put(ctx, ctlAlice, FilePutRequest{Path: "chunk.bin", Data: big}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ft.Put(ctx, ctlAlice, FilePutRequest{Path: "chunk.bin", Offset: int64(len(big)), Data: []byte("tail")}); err != nil {
		t.Fatal(err)
	}
	first, err := e.ft.Get(ctx, ctlAlice, FileGetRequest{Path: "chunk.bin", Length: 10 * domain.MaxFileChunkBytes})
	if err != nil || len(first.Data) != domain.MaxFileChunkBytes || first.EOF {
		t.Fatalf("first chunk = %d bytes, eof %v, %v", len(first.Data), first.EOF, err)
	}
	rest, err := e.ft.Get(ctx, ctlAlice, FileGetRequest{Path: "chunk.bin", Offset: int64(len(first.Data))})
	if err != nil || string(rest.Data) != "tail" || !rest.EOF || rest.SHA256 != first.SHA256 {
		t.Fatalf("rest = %+v, %v", rest, err)
	}
}

func TestFileTransferRejectsEscapes(t *testing.T) {
	e := newFtEnv(t)
	ctx := context.Background()
	secret := filepath.Join(e.base, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.put(ctlAlice, "ok.txt", "ok"); err != nil { // 置き場所を作る
		t.Fatal(err)
	}
	outside := filepath.Join(e.base, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.root, "link")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(e.root, "secret-link.txt")); err != nil {
		t.Fatal(err)
	}

	bad := []string{"../secret.txt", `..\secret.txt`, "a/../../secret.txt", secret, "/etc/passwd", `C:\Windows\win.ini`, "C:win.ini",
		`\\host\share\x`, `\\?\C:\x`, "a.txt:stream", "NUL", "link/new.txt", "link/secret.txt", "secret-link.txt"}
	for _, p := range bad {
		if _, err := e.put(ctlAlice, p, "pwned"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("Put(%q) = %v", p, err)
		}
		if _, err := e.ft.Get(ctx, ctlAlice, FileGetRequest{Path: p}); err == nil {
			t.Errorf("Get(%q) succeeded", p)
		}
		if _, err := e.ft.Delete(ctx, ctlAlice, p); err == nil {
			t.Errorf("Delete(%q) succeeded", p)
		}
		if _, err := e.ft.ResolveInRoot(ctx, p); err == nil {
			t.Errorf("ResolveInRoot(%q) succeeded", p)
		}
	}
	for _, p := range []string{"../", "..", "link", "/"} {
		if _, err := e.ft.List(ctx, ctlAlice, p); err == nil {
			t.Errorf("List(%q) succeeded", p)
		}
	}
	if b, _ := os.ReadFile(secret); string(b) != "secret" {
		t.Errorf("secret modified: %q", b)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("wrote outside the root: %v", entries)
	}
	// 拒否も監査に残る（denied）。
	last := e.audit.Entries()[len(e.audit.Entries())-1]
	if last.Result != auditDenied && last.Result != auditError {
		t.Errorf("last audit result = %q", last.Result)
	}
}

func TestFileTransferSizeLimits(t *testing.T) {
	e := newFtEnv(t)
	e.settings.put(settingFileTransferMaxFile, "10")
	e.settings.put(settingFileTransferMaxTotal, "15")

	if _, err := e.put(ctlAlice, "a", strings.Repeat("x", 10)); err != nil {
		t.Fatalf("exactly the per-file limit: %v", err)
	}
	if _, err := e.put(ctlAlice, "b", strings.Repeat("x", 11)); !errors.Is(err, domain.ErrFileTooLarge) {
		t.Fatalf("over the per-file limit: %v", err)
	}
	if _, err := e.put(ctlAlice, "c", strings.Repeat("x", 6)); !errors.Is(err, domain.ErrFileStoreFull) {
		t.Fatalf("over the total limit: %v", err)
	}
	if _, err := e.put(ctlAlice, "c", strings.Repeat("x", 5)); err != nil {
		t.Fatalf("exactly the total limit: %v", err)
	}
	for _, name := range []string{"b"} {
		if _, err := os.Stat(filepath.Join(e.root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after a rejected put: %v", name, err)
		}
	}
	// 既定値: 1 ファイル 100 MiB・全体 1 GiB。
	set, invalid, err := loadFileTransferSettings(context.Background(), newCtlSettings())
	if err != nil || invalid || set.MaxFileBytes != 100<<20 || set.MaxTotalBytes != 1<<30 || set.Root != "" {
		t.Fatalf("defaults = %+v, %v, %v", set, invalid, err)
	}
}

// 同時に put しても、置き場所全体の大きさの上限を超えない（合計の確認から書き込みの完了までが 1 つの排他区間。#275）。
func TestFileTransferConcurrentPutsRespectTotalLimit(t *testing.T) {
	const (
		writers = 16
		size    = 10
		limit   = 35 // 3 つ（30）までは入り、4 つ目（40）は入らない
	)
	e := newFtEnv(t)
	e.settings.put(settingFileTransferMaxFile, "100")
	e.settings.put(settingFileTransferMaxTotal, strconv.Itoa(limit))

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = e.put(ctlAlice, "f"+strconv.Itoa(i), strings.Repeat("x", size))
		}()
	}
	close(start)
	wg.Wait()

	stored := 0
	for i, err := range errs {
		switch {
		case err == nil:
			stored++
		case !errors.Is(err, domain.ErrFileStoreFull):
			t.Errorf("put %d: %v", i, err)
		}
	}
	if stored != limit/size {
		t.Errorf("stored %d files, want %d", stored, limit/size)
	}
	total := 0
	entries, err := os.ReadDir(e.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range entries {
		fi, err := ent.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += int(fi.Size())
	}
	if total > limit || total != stored*size {
		t.Errorf("total on disk = %d bytes (limit %d, stored %d files)", total, limit, stored)
	}
}

func TestFileTransferRequiresTheLease(t *testing.T) {
	e := newFtEnv(t)
	ctx := context.Background()
	if _, err := e.put(ctlAlice, "a.txt", "a"); err != nil {
		t.Fatal(err)
	}
	// 他のセッションが保持しているあいだは、4 つとも使えない（暗黙の取得も通らない）。
	calls := map[string]func() error{
		"put":    func() error { _, err := e.put(ctlBob, "b.txt", "b"); return err },
		"get":    func() error { _, err := e.ft.Get(ctx, ctlBob, FileGetRequest{Path: "a.txt"}); return err },
		"list":   func() error { _, err := e.ft.List(ctx, ctlBob, ""); return err },
		"delete": func() error { _, err := e.ft.Delete(ctx, ctlBob, "a.txt"); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, domain.ErrLeaseHeldByOther) {
			t.Errorf("%s while another session holds the lease: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(e.root, "b.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("b.txt was written without the lease: %v", err)
	}
	// リースを持たない mcp 以外（web・agent）は暗黙に取得できない。
	e.leases.Release(ctlAlice)
	web := domain.Actor{Kind: domain.ActorWeb, SessionID: "viewer"}
	if _, err := e.ft.Put(ctx, web, FilePutRequest{Path: "w.txt", Data: []byte("w")}); !errors.Is(err, domain.ErrLeaseRequired) {
		t.Errorf("web without the lease: %v", err)
	}
	// 未保持の mcp は暗黙に取得する（入力系ツールと同じ。FR-L04）。
	if _, err := e.ft.List(ctx, ctlBob, ""); err != nil {
		t.Fatalf("implicit acquire: %v", err)
	}
	if h, ok := e.leases.Holder(); !ok || !h.Actor.SameSession(ctlBob) {
		t.Fatalf("Holder = %+v, %v", h, ok)
	}
}

func TestFileTransferIsUnavailableWhileTheKillSwitchIsActive(t *testing.T) {
	e := newFtEnv(t)
	ctx := context.Background()
	if _, err := e.put(ctlAlice, "a.txt", "a"); err != nil {
		t.Fatal(err)
	}
	if err := e.kill.Trigger("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.put(ctlAlice, "b.txt", "b"); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Errorf("Put: %v", err)
	}
	if _, err := e.ft.Get(ctx, ctlAlice, FileGetRequest{Path: "a.txt"}); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Errorf("Get: %v", err)
	}
	if _, err := e.ft.List(ctx, ctlAlice, ""); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Errorf("List: %v", err)
	}
	if _, err := e.ft.Delete(ctx, ctlAlice, "a.txt"); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Errorf("Delete: %v", err)
	}
	if _, err := e.ft.ResolveInRoot(ctx, "a.txt"); !errors.Is(err, domain.ErrKillSwitchActive) {
		t.Errorf("ResolveInRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.root, "a.txt")); err != nil {
		t.Errorf("a.txt was removed while the kill switch was active: %v", err)
	}
}

func TestFileTransferIsUnavailableToScopedLeases(t *testing.T) {
	te := newTgtEnv(t)
	root := filepath.Join(t.TempDir(), "inbox")
	ft, err := NewFileTransfer(FileTransferConfig{Files: localfs.New(), Leases: te.leases, Kill: te.kill, DefaultRoot: root, Audit: te.audit, Clock: te.clock})
	if err != nil {
		t.Fatal(err)
	}
	te.acquire(ctlAlice)
	_, err = ft.Put(context.Background(), ctlAlice, FilePutRequest{Path: "a.txt", Data: []byte("a")})
	var de *domain.Error
	if !errors.Is(err, domain.ErrInputBlocked) || !errors.As(err, &de) || de.Detail["reason"] != string(domain.ReasonFileOutOfScope) {
		t.Fatalf("Put with a scoped lease: %v", err)
	}
	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the folder was created for a scoped lease: %v", statErr)
	}
}

func TestFileTransferAudit(t *testing.T) {
	e := newFtEnv(t)
	ctx := context.Background()
	if _, err := e.put(ctlAlice, "dir/a.txt", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ft.Get(ctx, ctlAlice, FileGetRequest{Path: "dir/a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ft.List(ctx, ctlAlice, "dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ft.Delete(ctx, ctlAlice, "dir/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.put(ctlAlice, "../escape.txt", "secret-content"); err == nil {
		t.Fatal("escape was accepted")
	}

	// 暗黙の取得で lease.acquire などの行も入る。tool.* の行だけを見る。
	var tools []repository.AuditEntry
	for _, en := range e.audit.Entries() {
		if en.Kind == "tool" {
			tools = append(tools, en)
		}
	}
	wantTools := []string{"put_file", "get_file", "list_files", "delete_file", "put_file"}
	if len(tools) != len(wantTools) {
		t.Fatalf("tool entries = %d, want %d", len(tools), len(wantTools))
	}
	for i, en := range tools {
		if en.Tool != wantTools[i] || en.Event != "tool."+wantTools[i] || en.ClientID != ctlAlice.ClientID || en.LeaseID == "" {
			t.Errorf("entry %d = %+v", i, en)
		}
	}
	detail := func(en repository.AuditEntry) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(en.DetailJSON, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if d := detail(tools[0]); tools[0].Result != auditOK || d["path"] != "dir/a.txt" || d["size"] != float64(5) || d["sha256"] != ftSum("hello") {
		t.Errorf("put audit = %s (%s)", tools[0].DetailJSON, tools[0].Result)
	}
	if d := detail(tools[1]); d["path"] != "dir/a.txt" || d["size"] != float64(5) || d["sha256"] != ftSum("hello") || d["length"] != float64(5) {
		t.Errorf("get audit = %s", tools[1].DetailJSON)
	}
	if d := detail(tools[2]); d["count"] != float64(1) {
		t.Errorf("list audit = %s", tools[2].DetailJSON)
	}
	if d := detail(tools[3]); d["size"] != float64(5) {
		t.Errorf("delete audit = %s", tools[3].DetailJSON)
	}
	rej := tools[4]
	if rej.Result != auditDenied || rej.ErrorCode != string(domain.CodeInvalidArgument) || detail(rej)["path"] != "../escape.txt" {
		t.Errorf("rejected audit = %+v %s", rej, rej.DetailJSON)
	}
	// ファイルの中身は、どの行にも入らない。
	for _, en := range e.audit.All() {
		if strings.Contains(string(en.DetailJSON), "secret-content") || strings.Contains(string(en.DetailJSON), "hello") {
			t.Errorf("the audit row contains file content: %s", en.DetailJSON)
		}
	}
}

func TestFileTransferHostSettings(t *testing.T) {
	e := newFtEnv(t)
	custom := filepath.Join(e.base, "qa", "files")
	raw, _ := json.Marshal(custom)
	e.settings.put(settingFileTransferRoot, string(raw))
	if _, err := e.put(ctlAlice, "a.txt", "a"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(custom, "a.txt")); err != nil || string(b) != "a" {
		t.Fatalf("file in the custom root = %q, %v", b, err)
	}
	if _, err := os.Stat(e.root); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the default root was used: %v", err)
	}
	abs, err := e.ft.ResolveInRoot(context.Background(), "a.txt")
	if err != nil || abs != filepath.Join(custom, "a.txt") {
		t.Fatalf("ResolveInRoot = %q, %v", abs, err)
	}

	// 壊れた設定・アプリのデータフォルダと重なる設定は、既定の場所に切り替えず、使えない（fail closed）。
	for name, v := range map[string]string{
		"not a string": `123`,
		"relative":     `"inbox"`,
		"unc":          `"\\\\host\\share\\x"`,
		"data dir":     mustJSON(t, e.dataDir),
		"in data dir":  mustJSON(t, filepath.Join(e.dataDir, "logs")),
		"parent":       mustJSON(t, e.base),
	} {
		e.settings.put(settingFileTransferRoot, v)
		if _, err := e.put(ctlAlice, "b.txt", "b"); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: Put = %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(e.base, "b.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("b.txt was written outside a valid root: %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNewFileTransferValidatesConfig(t *testing.T) {
	e := newFtEnv(t)
	base := FileTransferConfig{Files: localfs.New(), Leases: e.leases, Kill: e.kill, DefaultRoot: e.root, Clock: e.clock}
	if _, err := NewFileTransfer(base); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*FileTransferConfig){
		"files":        func(c *FileTransferConfig) { c.Files = nil },
		"leases":       func(c *FileTransferConfig) { c.Leases = nil },
		"kill":         func(c *FileTransferConfig) { c.Kill = nil },
		"clock":        func(c *FileTransferConfig) { c.Clock = nil },
		"default root": func(c *FileTransferConfig) { c.DefaultRoot = "relative" },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := NewFileTransfer(cfg); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
