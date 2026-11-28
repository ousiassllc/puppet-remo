package service

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// FileTransfer は、ホストが決めたフォルダ（置き場所。設定 file_transfer.root。既定は %LOCALAPPDATA%\PuppetRemo\inbox）の中だけで
// ファイルを置く・取り出す・一覧する・消す窓口（#264。MCP の put_file / get_file / list_files / delete_file。FR-M27）。
// platform.FileStore を呼ぶのはここだけ（ResolveInRoot を除く）。ステートレスに近く（書き込みだけ直列化する）、どの goroutine からも呼べる。
//
// Put・Get・List・Delete の検査の順序は固定で、1 つでも失敗したら何もしない（Control.Do の入力系と同じ規則）:
//
//  1. KillSwitch → ErrKillSwitchActive
//  2. LeaseManager.Check（リース未保持の mcp は暗黙に Acquire を試みる。承認が要るクライアントは承認を待つ。FR-L04）。他者が保持中は
//     ErrLeaseHeldByOther、mcp 以外で未保持は ErrLeaseRequired。ウィンドウに束縛されたリース（#156）では使えない（ErrInputBlocked、
//     reason file_out_of_scope。ホストのファイルはウィンドウのスコープの外）
//  3. 設定（置き場所・上限）の読み取りと、置き場所がアプリのデータフォルダと重ならないことの確認
//  4. 引数の検査（domain.CleanTransferPath。1 回の転送の大きさは domain.MaxFileChunkBytes まで）
//  5. KillSwitch の再確認（書き込み・削除の直前）と platform.FileStore の実行（書き込み・削除は直列化）
//  6. 監査（tool.<ツール名>。パス・大きさ・SHA-256・結果・所要時間。ファイルの中身は記録しない。FR-AU03）
//
// 置き場所・上限は設定画面（HostSettings）だけが変える。呼び出し側（MCP）からは変えられない。
type FileTransfer interface {
	// Put は req.Path へ req.Data を書く。Offset が 0 なら新規作成（同名のファイルは置き換える）、0 より大きければ、ちょうどその
	// 大きさのファイルへの追記（大きなファイルを分けて運ぶ）。ファイル・置き場所全体の大きさの上限を超えると何も書かず
	// ErrFileTooLarge・ErrFileStoreFull。結果はファイル全体の大きさと SHA-256。
	Put(ctx context.Context, who domain.Actor, req FilePutRequest) (FileResult, error)
	// Get は req.Path のファイルの [Offset, Offset+Length) を返す（Length が 0 なら domain.MaxFileChunkBytes。それを超える分は切り詰める）。
	// 結果にはファイル全体の大きさと SHA-256 が付く。無ければ ErrFileNotFound。
	Get(ctx context.Context, who domain.Actor, req FileGetRequest) (FileChunk, error)
	// List は Path のフォルダ（空ならルート）の直下を、名前の昇順で最大 domain.MaxFileListEntries 件返す。
	List(ctx context.Context, who domain.Actor, path string) (FileListing, error)
	// Delete は Path のファイルまたは空のフォルダを消す。
	Delete(ctx context.Context, who domain.Actor, path string) (FileResult, error)
	// ResolveInRoot は置き場所の中の実在する通常ファイル rel の、実体の絶対パスを返す（リンクを経由せず、置き場所の外に出ないことを
	// 確かめたもの）。ホストの上で置いたファイルを参照する他の機能（#265）のための窓口で、リースは見ない（呼び出し側が、その機能に
	// 必要な検査をすること）。キルスイッチの発動中は ErrKillSwitchActive。パスの形が不正なら ErrInvalidArgument、無ければ ErrFileNotFound。
	ResolveInRoot(ctx context.Context, rel string) (string, error)
}

// FilePutRequest は FileTransfer.Put の指定。
type FilePutRequest struct {
	Path   string
	Offset int64
	Data   []byte
}

// FileGetRequest は FileTransfer.Get の指定。
type FileGetRequest struct {
	Path   string
	Offset int64
	Length int
}

// FileResult はファイル 1 つの結果。Path は置き場所からの相対パス（"/" 区切り）。SHA256 は 16 進の小文字（Delete では空）。
type FileResult struct {
	Path   string
	Size   int64
	SHA256 string
}

// FileChunk は Get の結果。Size・SHA256 はファイル全体のもの。
type FileChunk struct {
	FileResult
	Offset int64
	Data   []byte
	EOF    bool // Data の末尾がファイルの末尾
}

// FileEntry は List の 1 項目。
type FileEntry struct {
	Path     string
	IsDir    bool
	Size     int64 // フォルダは 0
	Modified time.Time
}

// FileListing は List の結果。Truncated は件数の上限で切り捨てたこと。
type FileListing struct {
	Entries   []FileEntry
	Truncated bool
}

// FileTransferConfig は NewFileTransfer の設定。Files・Leases・Kill・Clock・DefaultRoot は必須。
type FileTransferConfig struct {
	Files  platform.FileStore
	Leases LeaseManager
	Kill   KillSwitchState
	// Settings は置き場所と上限の読み取り（file_transfer.*）。nil なら既定の置き場所・上限。
	Settings repository.Settings
	// DefaultRoot は設定が無いときの置き場所（%LOCALAPPDATA%\PuppetRemo\inbox）。domain.ValidateTransferRoot を満たすこと。
	DefaultRoot string
	// ProtectedDirs は置き場所と重ねてはならないフォルダ（アプリのデータフォルダ。DB・ログがある）。重なる設定は、使うたびに ErrInvalidArgument で拒否する。
	ProtectedDirs []string
	Audit         AuditRecorder // nil なら記録しない
	Clock         domain.Clock
	Logger        *slog.Logger // nil なら slog.Default()
}

type fileTransfer struct {
	cfg FileTransferConfig
	log *slog.Logger
	// mu は書き込み・削除を直列化する（置き場所全体の上限の計算と書き込みの間に、別の書き込みが入らないように）。
	mu sync.Mutex
}

// NewFileTransfer は FileTransfer を返す。
func NewFileTransfer(cfg FileTransferConfig) (FileTransfer, error) {
	if cfg.Files == nil || cfg.Leases == nil || cfg.Kill == nil || cfg.Clock == nil {
		return nil, domain.ErrInvalidArgument.WithMsg("file transfer requires files, leases, kill switch and clock")
	}
	if err := domain.ValidateTransferRoot(cfg.DefaultRoot); err != nil {
		return nil, err
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &fileTransfer{cfg: cfg, log: log}, nil
}

// fileCall は 1 回の呼び出しの、検査を通ったあとの文脈。
type fileCall struct {
	leaseID string
	root    string
	set     fileTransferSettings
}

// begin は検査の 1〜3（キルスイッチ・リース・設定）を行う。leaseID は取れたら（失敗しても）返す（監査用）。
func (f *fileTransfer) begin(ctx context.Context, who domain.Actor) (fileCall, error) {
	var c fileCall
	if f.cfg.Kill.Active() {
		return c, domain.ErrKillSwitchActive
	}
	leaseID, scope, err := ensureLease(ctx, f.cfg.Leases, who)
	c.leaseID = leaseID
	if err != nil {
		return c, err
	}
	if scope != nil {
		return c, scopeBlocked(domain.ReasonFileOutOfScope, domain.BlockedBy{})
	}
	c.root, c.set, err = f.settings(ctx)
	return c, err
}

// settings は有効な置き場所と上限を返す。
func (f *fileTransfer) settings(ctx context.Context) (string, fileTransferSettings, error) {
	set, invalid, err := loadFileTransferSettings(ctx, f.cfg.Settings)
	if err != nil {
		return "", fileTransferSettings{}, err
	}
	if invalid {
		f.log.Warn("invalid file transfer size limit setting; using the default")
	}
	root := set.Root
	if root == "" {
		root = f.cfg.DefaultRoot
	}
	for _, p := range f.cfg.ProtectedDirs {
		if overlaps(root, p) || resolvedOverlaps(root, p) {
			return "", fileTransferSettings{}, domain.ErrInvalidArgument.WithMsg("the host's transfer folder overlaps the app's data folder, so file transfer is disabled")
		}
	}
	return root, set, nil
}

// overlaps は a と b の一方が他方と同じか、その中にあるか（文字列の比較。大文字・小文字は区別しない。リンクは見ない）。
func overlaps(a, b string) bool { return within(a, b) || within(b, a) }

// resolvedOverlaps は a と b の実体のパス（リンクを解決したもの）が重なるか。解決できなければ重なるとみなす（fail closed）。
func resolvedOverlaps(a, b string) bool {
	ra, errA := resolveLoose(a)
	rb, errB := resolveLoose(b)
	return errA != nil || errB != nil || overlaps(ra, rb)
}

// resolveLoose は p のシンボリックリンク・ジャンクションを解決した絶対パスを返す。存在しない末尾の要素は、存在する最も近い親を解決して付け直す
// （まだ作られていない置き場所でも比べられる）。リンク先が無いリンクなど、解決できないものは error。
func resolveLoose(p string) (string, error) {
	cur := filepath.Clean(p)
	var tail []string
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			slices.Reverse(tail)
			return filepath.Join(append([]string{real}, tail...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if _, lerr := os.Lstat(cur); lerr == nil {
			return "", err // リンク先が無いリンク
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// inboxScope はファイル転送の置き場所（リモートが書ける場所）。lexical は設定の文字列のまま、resolved はリンクを解決した実体のパス。
type inboxScope struct{ lexical, resolved string }

func newInboxScope(root string) (inboxScope, error) {
	resolved, err := resolveLoose(root)
	return inboxScope{lexical: root, resolved: resolved}, err
}

// contains は p が置き場所の中（または置き場所そのもの）か。文字列の上でも、実体のパスの上でも調べる。p が解決できなければ（無い場合を除く）中とみなす。
func (s inboxScope) contains(p string) bool {
	if within(s.lexical, p) {
		return true
	}
	real, err := resolveLoose(p)
	return err != nil || within(s.resolved, real)
}

func within(parent, child string) bool {
	p, c := strings.ToLower(filepath.Clean(parent)), strings.ToLower(filepath.Clean(child))
	return c == p || strings.HasPrefix(c, strings.TrimRight(p, `\/`)+string(filepath.Separator))
}

// maxAuditFilePathRunes は監査に記録する、呼び出し側が渡したパスの最大文字数。
const maxAuditFilePathRunes = 256

func (f *fileTransfer) record(ctx context.Context, who domain.Actor, tool string, detail map[string]any, leaseID string, start time.Time, err error) {
	if f.cfg.Audit == nil {
		return
	}
	entry := ToolAuditEntry(who, tool, detail, leaseID, err, f.cfg.Clock.Now().Sub(start))
	if domain.CodeOf(err) == domain.CodeInvalidArgument {
		entry.Result = auditDenied // 形の不正なパス・置き場所の外に出る指定は、launch_app の指定の不備と同じく拒否として残す
	}
	f.cfg.Audit.Record(ctx, entry)
}

// rejectedPath は検査の前の（形が不正かもしれない）パスを、監査に残せる長さにする。
func rejectedPath(p string) string { return domain.TruncateRunes(p, maxAuditFilePathRunes) }

func (f *fileTransfer) Put(ctx context.Context, who domain.Actor, req FilePutRequest) (res FileResult, err error) {
	start := f.cfg.Clock.Now()
	detail := map[string]any{"path": rejectedPath(req.Path), "offset": req.Offset, "length": len(req.Data)}
	var leaseID string
	defer func() {
		if err == nil {
			detail["path"], detail["size"], detail["sha256"] = res.Path, res.Size, res.SHA256
		}
		f.record(ctx, who, "put_file", detail, leaseID, start, err)
	}()
	c, err := f.begin(ctx, who)
	leaseID = c.leaseID
	if err != nil {
		return FileResult{}, err
	}
	if _, err := domain.CleanTransferPath(req.Path); err != nil {
		return FileResult{}, err
	}
	switch {
	case req.Offset < 0:
		return FileResult{}, domain.ErrInvalidArgument.WithMsg("offset must not be negative")
	case len(req.Data) > domain.MaxFileChunkBytes:
		return FileResult{}, domain.ErrInvalidArgument.WithMsg("content is too large for one call: send the file in chunks of at most 524288 bytes, using offset")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cfg.Kill.Active() {
		return FileResult{}, domain.ErrKillSwitchActive
	}
	info, err := f.cfg.Files.Put(ctx, c.root, platform.FilePut{
		Rel: req.Path, Offset: req.Offset, Data: req.Data, MaxFileBytes: c.set.MaxFileBytes, MaxTotalBytes: c.set.MaxTotalBytes,
	})
	if err != nil {
		return FileResult{}, err
	}
	return FileResult{Path: info.Rel, Size: info.Size, SHA256: info.SHA256}, nil
}

func (f *fileTransfer) Get(ctx context.Context, who domain.Actor, req FileGetRequest) (chunk FileChunk, err error) {
	start := f.cfg.Clock.Now()
	detail := map[string]any{"path": rejectedPath(req.Path), "offset": req.Offset}
	var leaseID string
	defer func() {
		if err == nil {
			detail["path"], detail["size"], detail["sha256"], detail["length"] = chunk.Path, chunk.Size, chunk.SHA256, len(chunk.Data)
		}
		f.record(ctx, who, "get_file", detail, leaseID, start, err)
	}()
	c, err := f.begin(ctx, who)
	leaseID = c.leaseID
	if err != nil {
		return FileChunk{}, err
	}
	if _, err := domain.CleanTransferPath(req.Path); err != nil {
		return FileChunk{}, err
	}
	switch {
	case req.Offset < 0 || req.Length < 0:
		return FileChunk{}, domain.ErrInvalidArgument.WithMsg("offset and length must not be negative")
	case req.Length == 0 || req.Length > domain.MaxFileChunkBytes:
		req.Length = domain.MaxFileChunkBytes
	}
	data, info, err := f.cfg.Files.Get(ctx, c.root, req.Path, req.Offset, req.Length)
	if err != nil {
		return FileChunk{}, err
	}
	return FileChunk{
		FileResult: FileResult{Path: info.Rel, Size: info.Size, SHA256: info.SHA256},
		Offset:     req.Offset, Data: data, EOF: req.Offset+int64(len(data)) >= info.Size,
	}, nil
}

func (f *fileTransfer) List(ctx context.Context, who domain.Actor, path string) (listing FileListing, err error) {
	start := f.cfg.Clock.Now()
	detail := map[string]any{"path": rejectedPath(path)}
	var leaseID string
	defer func() {
		if err == nil {
			detail["count"], detail["truncated"] = len(listing.Entries), listing.Truncated
		}
		f.record(ctx, who, "list_files", detail, leaseID, start, err)
	}()
	c, err := f.begin(ctx, who)
	leaseID = c.leaseID
	if err != nil {
		return FileListing{}, err
	}
	if path != "" {
		if _, err := domain.CleanTransferPath(path); err != nil {
			return FileListing{}, err
		}
	}
	entries, truncated, err := f.cfg.Files.List(ctx, c.root, path, domain.MaxFileListEntries)
	if err != nil {
		return FileListing{}, err
	}
	listing = FileListing{Entries: make([]FileEntry, 0, len(entries)), Truncated: truncated}
	for _, e := range entries {
		listing.Entries = append(listing.Entries, FileEntry{Path: e.Rel, IsDir: e.IsDir, Size: e.Size, Modified: e.ModTime})
	}
	return listing, nil
}

func (f *fileTransfer) Delete(ctx context.Context, who domain.Actor, path string) (res FileResult, err error) {
	start := f.cfg.Clock.Now()
	detail := map[string]any{"path": rejectedPath(path)}
	var leaseID string
	defer func() {
		if err == nil {
			detail["path"], detail["size"] = res.Path, res.Size
		}
		f.record(ctx, who, "delete_file", detail, leaseID, start, err)
	}()
	c, err := f.begin(ctx, who)
	leaseID = c.leaseID
	if err != nil {
		return FileResult{}, err
	}
	if _, err := domain.CleanTransferPath(path); err != nil {
		return FileResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cfg.Kill.Active() {
		return FileResult{}, domain.ErrKillSwitchActive
	}
	info, err := f.cfg.Files.Delete(ctx, c.root, path)
	if err != nil {
		return FileResult{}, err
	}
	return FileResult{Path: info.Rel, Size: info.Size}, nil
}

func (f *fileTransfer) ResolveInRoot(ctx context.Context, rel string) (string, error) {
	if f.cfg.Kill.Active() {
		return "", domain.ErrKillSwitchActive
	}
	if _, err := domain.CleanTransferPath(rel); err != nil {
		return "", err
	}
	root, _, err := f.settings(ctx)
	if err != nil {
		return "", err
	}
	return f.cfg.Files.Resolve(ctx, root, rel)
}
