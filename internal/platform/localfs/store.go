package localfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

var _ platform.FileStore = (*Store)(nil)

// Store は platform.FileStore のローカルのフォルダ向けの実装。状態を持たない（ルートは呼び出しごとに渡される）。
type Store struct{}

// New は Store を返す。
func New() *Store { return &Store{} }

// エラーにパスは入れない。
var (
	errLink        = domain.ErrInvalidArgument.WithMsg("path goes through a symbolic link, junction or reparse point, which is not allowed")
	errUnsupported = domain.ErrInvalidArgument.WithMsg("only regular files and folders can be used")
	errChanged     = conflict("the file changed while it was being opened; try again")
)

// conflict は「いまの状態では実行できない」（domain.CodeConflict）のエラーを作る。
func conflict(msg string) error { return domain.NewError(domain.CodeConflict, msg, nil) }

// dirMode はルート・フォルダを作るときの権限（Windows では無視される。ファイルは open_*.go が 0600 で作る）。
const dirMode fs.FileMode = 0o700

// realRoot は root（絶対パス）の実体のパスを返す。root 自体はホストが選んだ場所なので、リンクを経由していてもよい（以後は実体のパスを基準にする）。
// create なら、無い root を作る。create でなくて無ければ domain.ErrFileNotFound。
func realRoot(root string, create bool) (string, error) {
	if root == "" || !filepath.IsAbs(root) || strings.ContainsRune(root, 0) {
		return "", domain.ErrInvalidArgument.WithMsg("the transfer folder must be an absolute path")
	}
	if create {
		if err := os.MkdirAll(root, dirMode); err != nil {
			return "", fmt.Errorf("create the transfer folder: %w", err)
		}
	}
	real, err := filepath.EvalSymlinks(root)
	if errors.Is(err, fs.ErrNotExist) {
		return "", domain.ErrFileNotFound
	}
	if err != nil {
		return "", fmt.Errorf("resolve the transfer folder: %w", err)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("stat the transfer folder: %w", err)
	}
	if !fi.IsDir() {
		return "", domain.ErrInvalidArgument.WithMsg("the transfer folder is not a folder")
	}
	return real, nil
}

// checkEntry は Lstat の結果が、通常のファイルかディレクトリで、リンクでないことを確かめる。
func checkEntry(fi fs.FileInfo) error {
	if fi.Mode()&fs.ModeSymlink != 0 || isReparsePoint(fi) {
		return errLink
	}
	if !fi.IsDir() && !fi.Mode().IsRegular() {
		return errUnsupported
	}
	return nil
}

// split は rel を検査して要素に分ける（呼び出し側の検査に頼らない）。
func split(rel string) ([]string, error) { return domain.SplitTransferPath(rel) }

// walkExisting は real の下の parts を 1 要素ずつ Lstat でたどり、すべてが実在し、途中がディレクトリで、どれもリンクでないことを確かめる。
// 無ければ domain.ErrFileNotFound。最後の要素の絶対パスと Lstat の結果を返す。
func walkExisting(real string, parts []string) (string, fs.FileInfo, error) {
	cur := real
	var fi fs.FileInfo
	for i, p := range parts {
		cur = filepath.Join(cur, p)
		var err error
		if fi, err = os.Lstat(cur); errors.Is(err, fs.ErrNotExist) {
			return "", nil, domain.ErrFileNotFound
		} else if err != nil {
			return "", nil, fmt.Errorf("stat: %w", err)
		}
		if err := checkEntry(fi); err != nil {
			return "", nil, err
		}
		if i < len(parts)-1 && !fi.IsDir() {
			return "", nil, domain.ErrFileNotFound
		}
	}
	return cur, fi, nil
}

// ensureDirs は real の下の dirs を 1 要素ずつたどり、無いディレクトリを作る。既存のものはリンクでなくディレクトリであること。
func ensureDirs(real string, dirs []string) (string, error) {
	cur := real
	for _, p := range dirs {
		cur = filepath.Join(cur, p)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(cur, dirMode); err != nil && !errors.Is(err, fs.ErrExist) {
				return "", fmt.Errorf("create folder: %w", err)
			}
			fi, err = os.Lstat(cur)
		}
		if err != nil {
			return "", fmt.Errorf("stat: %w", err)
		}
		if err := checkEntry(fi); err != nil {
			return "", err
		}
		if !fi.IsDir() {
			return "", conflict("a file exists where a folder is needed")
		}
	}
	return cur, nil
}

// open はリンクをたどらずにファイルを開き、開いたあとに、途中のどこもリンクでないこと・開いたものが今のパスの先と同じであることを
// もう一度確かめる（検査と使用の間のすり替えを狭める）。通らなければ閉じてエラーを返す。
func open(real string, parts []string, write, create bool) (*os.File, error) {
	abs := filepath.Join(append([]string{real}, parts...)...)
	f, err := openNoFollow(abs, write, create)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, domain.ErrFileNotFound
	}
	if err != nil {
		return nil, wrapOpenError(err)
	}
	if err := verifyOpened(f, real, parts); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func verifyOpened(f *os.File, real string, parts []string) error {
	if link, err := openedIsReparsePoint(f); err != nil {
		return fmt.Errorf("inspect the opened file: %w", err)
	} else if link {
		return errLink
	}
	_, fi, err := walkExisting(real, parts)
	if err != nil {
		return err
	}
	of, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat the opened file: %w", err)
	}
	if !of.Mode().IsRegular() {
		return errUnsupported
	}
	if !os.SameFile(of, fi) {
		return errChanged
	}
	return nil
}

// hashFile は f の先頭から size バイトの SHA-256（16 進の小文字）。
func hashFile(f *os.File, size int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, size)); err != nil {
		return "", fmt.Errorf("hash: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (*Store) Put(ctx context.Context, root string, req platform.FilePut) (platform.FileInfo, error) {
	parts, err := split(req.Rel)
	if err != nil {
		return platform.FileInfo{}, err
	}
	if req.Offset < 0 || req.MaxFileBytes < 1 || req.MaxTotalBytes < 1 || int64(len(req.Data)) > math.MaxInt64-req.Offset {
		return platform.FileInfo{}, domain.ErrInvalidArgument.WithMsg("invalid offset or limits")
	}
	newSize := req.Offset + int64(len(req.Data))
	if newSize > req.MaxFileBytes {
		return platform.FileInfo{}, domain.ErrFileTooLarge
	}
	if err := ctx.Err(); err != nil {
		return platform.FileInfo{}, err
	}
	real, err := realRoot(root, true)
	if err != nil {
		return platform.FileInfo{}, err
	}
	dir, err := ensureDirs(real, parts[:len(parts)-1])
	if err != nil {
		return platform.FileInfo{}, err
	}
	existing, created, err := existingFile(filepath.Join(dir, parts[len(parts)-1]), req.Offset)
	if err != nil {
		return platform.FileInfo{}, err
	}
	if req.Offset == 0 {
		newSize = int64(len(req.Data)) // 置き換えるので、いまの大きさは数えない
	}
	total, err := (&Store{}).TotalSize(ctx, root)
	if err != nil {
		return platform.FileInfo{}, err
	}
	if total-existing+newSize > req.MaxTotalBytes {
		return platform.FileInfo{}, domain.ErrFileStoreFull
	}

	f, err := open(real, parts, true, true)
	if err != nil {
		return platform.FileInfo{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := writeOpened(f, req)
	if err != nil {
		if created { // 新しく作ったファイルに書けなかったときは、空・途中のファイルを残さない
			_ = f.Close()
			_ = os.Remove(filepath.Join(dir, parts[len(parts)-1]))
		}
		return platform.FileInfo{}, err
	}
	info.Rel = strings.Join(parts, "/")
	return info, nil
}

// existingFile は Put の対象の今の状態を返す: 既存の通常ファイルの大きさ（無ければ 0）と、無かったか。
// offset が 0 より大きいのは、ちょうどその大きさの既存ファイルへの追記だけ。
func existingFile(abs string, offset int64) (size int64, created bool, err error) {
	fi, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if offset != 0 {
			return 0, false, conflict("offset must be 0 for a new file")
		}
		return 0, true, nil
	case err != nil:
		return 0, false, fmt.Errorf("stat: %w", err)
	}
	if err := checkEntry(fi); err != nil {
		return 0, false, err
	}
	if fi.IsDir() {
		return 0, false, conflict("a folder exists at this path")
	}
	if offset != 0 && fi.Size() != offset {
		return 0, false, conflict("offset must equal the current size of the file")
	}
	return fi.Size(), false, nil
}

// writeOpened は開いたファイルへ req.Data を書き、全体の大きさと SHA-256 を返す。
func writeOpened(f *os.File, req platform.FilePut) (platform.FileInfo, error) {
	if req.Offset == 0 {
		if err := f.Truncate(0); err != nil {
			return platform.FileInfo{}, fmt.Errorf("truncate: %w", err)
		}
	} else if fi, err := f.Stat(); err != nil {
		return platform.FileInfo{}, fmt.Errorf("stat the opened file: %w", err)
	} else if fi.Size() != req.Offset {
		return platform.FileInfo{}, conflict("offset must equal the current size of the file")
	}
	if _, err := f.WriteAt(req.Data, req.Offset); err != nil {
		return platform.FileInfo{}, fmt.Errorf("write: %w", err)
	}
	size := req.Offset + int64(len(req.Data))
	sum, err := hashFile(f, size)
	if err != nil {
		return platform.FileInfo{}, err
	}
	return platform.FileInfo{Size: size, SHA256: sum}, nil
}

func (*Store) Get(ctx context.Context, root, rel string, offset int64, length int) ([]byte, platform.FileInfo, error) {
	parts, err := split(rel)
	if err != nil {
		return nil, platform.FileInfo{}, err
	}
	if offset < 0 || length < 0 {
		return nil, platform.FileInfo{}, domain.ErrInvalidArgument.WithMsg("offset and length must not be negative")
	}
	if err := ctx.Err(); err != nil {
		return nil, platform.FileInfo{}, err
	}
	real, err := realRoot(root, false)
	if err != nil {
		return nil, platform.FileInfo{}, err
	}
	_, fi, err := walkExisting(real, parts)
	if err != nil {
		return nil, platform.FileInfo{}, err
	}
	if fi.IsDir() {
		return nil, platform.FileInfo{}, domain.ErrInvalidArgument.WithMsg("the path is a folder; use list_files")
	}
	f, err := open(real, parts, false, false)
	if err != nil {
		return nil, platform.FileInfo{}, err
	}
	defer func() { _ = f.Close() }()
	of, err := f.Stat()
	if err != nil {
		return nil, platform.FileInfo{}, fmt.Errorf("stat the opened file: %w", err)
	}
	size := of.Size()
	if offset > size {
		return nil, platform.FileInfo{}, domain.ErrInvalidArgument.WithMsg("offset is beyond the end of the file")
	}
	n := int(min(int64(length), size-offset))
	data := make([]byte, n)
	if n > 0 {
		if _, err := f.ReadAt(data, offset); err != nil { // 読んでいる間にファイルが縮んだときの EOF も失敗として扱う
			return nil, platform.FileInfo{}, fmt.Errorf("read: %w", err)
		}
	}
	sum, err := hashFile(f, size)
	if err != nil {
		return nil, platform.FileInfo{}, err
	}
	return data, platform.FileInfo{Rel: strings.Join(parts, "/"), Size: size, SHA256: sum}, nil
}

func (*Store) List(ctx context.Context, root, rel string, limit int) ([]platform.FileEntry, bool, error) {
	var parts []string
	if rel != "" {
		var err error
		if parts, err = split(rel); err != nil {
			return nil, false, err
		}
	}
	if limit < 1 {
		return nil, false, domain.ErrInvalidArgument.WithMsg("limit must be positive")
	}
	real, err := realRoot(root, false)
	if errors.Is(err, domain.ErrFileNotFound) && len(parts) == 0 {
		return nil, false, nil // まだ何も置かれていないルート
	}
	if err != nil {
		return nil, false, err
	}
	dir := real
	if len(parts) > 0 {
		var fi fs.FileInfo
		if dir, fi, err = walkExisting(real, parts); err != nil {
			return nil, false, err
		}
		if !fi.IsDir() {
			return nil, false, domain.ErrInvalidArgument.WithMsg("the path is not a folder")
		}
	}
	entries, err := os.ReadDir(dir) // 名前の昇順
	if err != nil {
		return nil, false, fmt.Errorf("read folder: %w", err)
	}
	var out []platform.FileEntry
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		fi, err := os.Lstat(filepath.Join(dir, e.Name()))
		if err != nil || checkEntry(fi) != nil {
			continue // リンク・特殊なファイルは見せない（消えたものも飛ばす）
		}
		if len(out) == limit {
			return out, true, nil
		}
		fe := platform.FileEntry{Rel: path.Join(append(slices.Clone(parts), e.Name())...), IsDir: fi.IsDir(), ModTime: fi.ModTime()}
		if !fi.IsDir() {
			fe.Size = fi.Size()
		}
		out = append(out, fe)
	}
	return out, false, nil
}

func (*Store) Delete(ctx context.Context, root, rel string) (platform.FileInfo, error) {
	parts, err := split(rel)
	if err != nil {
		return platform.FileInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return platform.FileInfo{}, err
	}
	real, err := realRoot(root, false)
	if err != nil {
		return platform.FileInfo{}, err
	}
	abs, fi, err := walkExisting(real, parts)
	if err != nil {
		return platform.FileInfo{}, err
	}
	info := platform.FileInfo{Rel: strings.Join(parts, "/")}
	if !fi.IsDir() {
		info.Size = fi.Size()
	}
	if err := os.Remove(abs); err != nil {
		if fi.IsDir() && !errors.Is(err, fs.ErrNotExist) {
			return platform.FileInfo{}, conflict("the folder is not empty")
		}
		if errors.Is(err, fs.ErrNotExist) {
			return platform.FileInfo{}, domain.ErrFileNotFound
		}
		return platform.FileInfo{}, fmt.Errorf("delete: %w", err)
	}
	return info, nil
}

func (*Store) Resolve(ctx context.Context, root, rel string) (string, error) {
	parts, err := split(rel)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	real, err := realRoot(root, false)
	if err != nil {
		return "", err
	}
	abs, fi, err := walkExisting(real, parts)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", domain.ErrInvalidArgument.WithMsg("the path is not a file")
	}
	return abs, nil
}

func (*Store) TotalSize(ctx context.Context, root string) (int64, error) {
	real, err := realRoot(root, false)
	if errors.Is(err, domain.ErrFileNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(real, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // 数えている間に消えた
			}
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.Type().IsRegular() { // リンクをたどらない（WalkDir は Lstat の種類で見る）
			if fi, err := d.Info(); err == nil && !isReparsePoint(fi) {
				total += fi.Size()
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measure the transfer folder: %w", err)
	}
	return total, nil
}

// wrapOpenError は open の失敗を写す。リンクを開こうとして O_NOFOLLOW が拒否したもの（ELOOP）は errLink。
func wrapOpenError(err error) error {
	if isSymlinkLoop(err) {
		return errLink
	}
	return fmt.Errorf("open: %w", err)
}
