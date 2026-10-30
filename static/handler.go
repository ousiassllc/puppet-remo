package static

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// Handler は dist を配信する http.Handler を返す。呼び出し側が StripPrefix で /static を外して渡す。
func Handler() (http.Handler, error) {
	fsys, immutable, err := distFS()
	if err != nil {
		return nil, err
	}
	return NewHandler(fsys, immutable), nil
}

// NewHandler は fsys を配信する。immutable が true（go:embed）なら、ビルド ID は最初の 1 回だけ計算する。
// false（開発のディスク配信）はリクエストごとに計算し直す。
func NewHandler(fsys fs.FS, immutable bool) http.Handler {
	h := &handler{fsys: fsys}
	if immutable {
		var (
			once sync.Once
			id   string
			err  error
		)
		h.buildID = func() (string, error) {
			once.Do(func() { id, err = computeBuildID(fsys) })
			return id, err
		}
	} else {
		h.buildID = func() (string, error) { return computeBuildID(fsys) }
	}
	return h
}

type handler struct {
	fsys    fs.FS
	buildID func() (string, error)
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	f, err := h.fsys.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	id, err := h.buildID()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	hd := w.Header()
	hd.Set("Cache-Control", "no-cache")
	hd.Set("ETag", `"`+id+`"`)
	hd.Set("X-Content-Type-Options", "nosniff")
	// 更新時刻は返さない（検証は ETag だけで行う）。ServeContent が If-None-Match を見て 304 を返す。
	http.ServeContent(w, r, name, time.Time{}, rs)
}

// computeBuildID は fsys の全ファイルの名前と内容から決まるビルド ID を返す。
func computeBuildID(fsys fs.FS) (string, error) {
	var names []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", errors.New("static: dist is empty (run `task frontend:build`)")
	}
	sort.Strings(names)
	sum := sha256.New()
	for _, n := range names {
		f, err := fsys.Open(n)
		if err != nil {
			return "", err
		}
		sum.Write([]byte(n))
		sum.Write([]byte{0})
		_, err = io.Copy(sum, f)
		_ = f.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(sum.Sum(nil))[:16], nil
}
