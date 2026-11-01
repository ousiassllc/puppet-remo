package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// ThumbsDirName は監査のサムネイルの置き場（データのディレクトリの下。%LOCALAPPDATA%\puppet-remo\thumbs\）。
const ThumbsDirName = "thumbs"

const (
	thumbsDirMode = 0o700 // 現在のユーザーだけが読める（NFR-S11。Windows では親の ACL を継承する）
	thumbFileMode = 0o600
	tmpPrefix     = ".tmp-"
	// orphanGrace は SweepOrphans が「DB に行が無い」ファイルを消さずに残す猶予。
	// サムネイルは「ファイルを保存 → DB に紐づけ」の順に作るため、保存の直後のファイルを孤児と取り違えない。
	orphanGrace = time.Minute
)

// ThumbStore は thumbs/ 配下のサムネイル（JPEG）のファイル置き場。
// パスはすべて thumbs/ からの相対パス（区切りは `/`。audit_thumbnails.rel_path と同じ）で、
// 外へ出るもの（`..`・絶対パス・ドライブ指定・`\`・NUL・シンボリックリンク経由など）は ErrInvalidArgument で拒否する。
type ThumbStore interface {
	// Save は jpeg を relPath へ保存する。一時ファイルへ書いてからリネームするので、失敗しても途中のファイルは残らない。
	// 必要なディレクトリは 0700 で作る。同じパスのファイルは置き換える。
	Save(ctx context.Context, relPath string, jpeg []byte) error
	// Open は relPath のファイルを開く。無ければ ErrNotFound。
	Open(relPath string) (io.ReadCloser, error)
	// Remove は relPath のファイルを削除する。無いファイルでも成功する。
	Remove(relPath string) error
	// SweepOrphans は thumbs/ にあって known が false のファイル（DB に行が無いファイル・中断した Save の一時ファイル）を削除する。
	// 直近（orphanGrace）に更新されたファイルは消さない。
	SweepOrphans(ctx context.Context, known func(rel string) bool) error
}

type thumbStore struct {
	dir   string
	clock domain.Clock
}

func newThumbStore(dir string, clock domain.Clock) thumbStore {
	return thumbStore{dir: dir, clock: clock}
}

// cleanThumbPath は rel が thumbs/ 配下の相対パス（区切りは `/`）として安全かを検査し、そのまま返す。
// 書き換えて救済はしない（正規形だけを受け付ける）。拒否: 空、NUL を含む制御文字、`\`、`:`（ドライブ指定・代替データストリーム）、
// Windows でパスに使えない文字（`<>"|?*`）、先頭の `/`、空・`.`・`..` の要素、末尾が `.` か空白の要素（Windows が削る）、
// Windows の予約名（CON・NUL・COM1 など）。
func cleanThumbPath(rel string) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("%w: thumbnail path %q: %s", domain.ErrInvalidArgument, rel, why)
	}
	if rel == "" {
		return bad("empty")
	}
	if strings.HasPrefix(rel, "/") {
		return bad("absolute")
	}
	for _, r := range rel {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`\:<>"|?*`, r) {
			return bad("forbidden character")
		}
	}
	for _, seg := range strings.Split(rel, "/") {
		switch {
		case seg == "" || seg == "." || seg == "..":
			return bad("empty or relative element")
		case strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " "):
			return bad("element ends with a dot or space")
		case isReservedName(seg):
			return bad("reserved device name")
		}
	}
	return rel, nil
}

// isReservedName は Windows の予約デバイス名（拡張子付きでも予約される。上付き数字の COM¹²³・LPT¹²³ を含む。domain.isReservedDeviceName と同じ範囲）か。
func isReservedName(seg string) bool {
	stem, _, _ := strings.Cut(seg, ".")
	s := strings.ToUpper(strings.TrimRight(stem, " "))
	switch s {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(s) > 3 && (strings.HasPrefix(s, "COM") || strings.HasPrefix(s, "LPT")) {
		suffix := s[3:]
		return utf8.RuneCountInString(suffix) == 1 && strings.Contains("0123456789¹²³", suffix)
	}
	return false
}

// openRoot は thumbs/ を os.Root（外へ出る操作を OS の層で拒否する）として開く。create なら無いときに作る。
// ディレクトリが無く create でなければ fs.ErrNotExist。
func (s thumbStore) openRoot(create bool) (*os.Root, error) {
	if create {
		if err := os.MkdirAll(s.dir, thumbsDirMode); err != nil {
			return nil, fmt.Errorf("create thumbnail directory: %w", err)
		}
	}
	return os.OpenRoot(s.dir)
}

// rejectLinks は rel の途中・末尾にシンボリックリンク（Windows ではジャンクションなどの再解析ポイント）が
// あれば ErrInvalidArgument にする。この置き場はリンクを作らないので、あれば外部の細工とみなす。
func rejectLinks(root *os.Root, rel string) error {
	prefix := ""
	for _, seg := range strings.Split(rel, "/") {
		prefix = path.Join(prefix, seg)
		fi, err := root.Lstat(prefix)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // 以降も無い
		}
		if err != nil {
			return err
		}
		if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return fmt.Errorf("%w: thumbnail path %q goes through a link", domain.ErrInvalidArgument, rel)
		}
	}
	return nil
}

func (s thumbStore) Save(ctx context.Context, relPath string, jpeg []byte) (err error) {
	rel, err := cleanThumbPath(relPath)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := s.openRoot(true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := rejectLinks(root, rel); err != nil {
		return err
	}
	dir := path.Dir(rel)
	if dir != "." {
		if err := root.MkdirAll(dir, thumbsDirMode); err != nil {
			return fmt.Errorf("create thumbnail directory: %w", err)
		}
	}
	tmp := path.Join(dir, tmpPrefix+strconv.FormatUint(rand.Uint64(), 36))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, thumbFileMode)
	if err != nil {
		return fmt.Errorf("create thumbnail: %w", err)
	}
	defer func() {
		if err != nil {
			_ = root.Remove(tmp) // 失敗したら途中のファイルを残さない
		}
	}()
	_, err = f.Write(jpeg)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write thumbnail: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = root.Rename(tmp, rel); err != nil {
		return fmt.Errorf("save thumbnail: %w", err)
	}
	return nil
}

func (s thumbStore) Open(relPath string) (io.ReadCloser, error) {
	rel, err := cleanThumbPath(relPath)
	if err != nil {
		return nil, err
	}
	root, err := s.openRoot(false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: thumbnail %q", ErrNotFound, rel)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := rejectLinks(root, rel); err != nil {
		return nil, err
	}
	f, err := root.Open(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: thumbnail %q", ErrNotFound, rel)
	}
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: thumbnail %q is not a regular file", ErrNotFound, rel)
	}
	return f, nil // *os.File は root を閉じたあとも使える
}

func (s thumbStore) Remove(relPath string) error {
	rel, err := cleanThumbPath(relPath)
	if err != nil {
		return err
	}
	root, err := s.openRoot(false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := rejectLinks(root, rel); err != nil {
		return err
	}
	if err := root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s thumbStore) SweepOrphans(ctx context.Context, known func(rel string) bool) error {
	root, err := s.openRoot(false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	now := s.clock.Now()
	var errs []error
	walkErr := fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		if d.IsDir() || known(rel) {
			return nil
		}
		// シンボリックリンクは辿らず、リンク自体を（外部の細工として）消す。
		if info, ierr := d.Info(); ierr == nil && now.Sub(info.ModTime()) < orphanGrace {
			return nil
		}
		if rerr := root.Remove(rel); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			errs = append(errs, rerr)
		}
		return nil
	})
	return errors.Join(append(errs, walkErr)...)
}
