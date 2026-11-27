// Package engine は Codex の app-server（実行ファイル）をディスク上でどう扱うか: 置き場所、導入済みの版、
// 取得（固定したアーカイブのダウンロード・SHA-256 とサイズの検証・安全な展開・展開後の検証・置き換え）と削除（#247）。
//
// 受けるのはカタログに固定した公式のアーカイブだけで、利用者が指定したバイナリは扱わない。
package engine

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/codex/catalog"
)

var (
	// ErrMismatch はアーカイブ・展開後の実行ファイルの SHA-256 かサイズが、カタログの値と違う。
	ErrMismatch = errors.New("codex engine: the download does not match the pinned checksum or size")
	// ErrUnsafeArchive はアーカイブが「実行ファイルと同名の通常ファイル 1 本だけ」ではない
	// （パス区切り・..・絶対パス・ドライブ・リンク・ディレクトリ・2 つ目のエントリ）。
	ErrUnsafeArchive = errors.New("codex engine: the archive is not a single regular file named as pinned")
	// ErrInsecureURL はダウンロード先（リダイレクト先を含む）が https ではない。
	ErrInsecureURL = errors.New("codex engine: the download url is not https")
)

// Layout は Root の下の置き場所（Root は呼び出し側が渡す `<実行ファイルのディレクトリ>\codex`）。
type Layout struct{ Root string }

// EngineDir は導入済みの実行ファイルの置き場所。
func (l Layout) EngineDir() string { return filepath.Join(l.Root, "engine") }

// EngineNewDir は新しいエンジンを展開する先。
func (l Layout) EngineNewDir() string { return filepath.Join(l.Root, "engine.new") }

// EngineOldDir は置き換えの間、前のエンジンが待つ場所。
func (l Layout) EngineOldDir() string { return filepath.Join(l.Root, "engine.old") }

// DownloadsDir はダウンロード中のアーカイブ（.part）の置き場所。
func (l Layout) DownloadsDir() string { return filepath.Join(l.Root, "downloads") }

// RecordPath は導入の記録（installed.json）。
func (l Layout) RecordPath() string { return filepath.Join(l.Root, "installed.json") }

// Phase は Install が今していること。
type Phase string

// Install の段階（この順）。
const (
	PhaseDownload Phase = "download"
	PhaseExtract  Phase = "extract"
)

// Progress は段階と、その段階の完了バイト数・全体のバイト数。ブロックしてはならない。
type Progress func(p Phase, done, total int64)

// Installer は 1 つのプラットフォームの固定した app-server を導入する。
type Installer struct {
	Layout  Layout
	Version string // カタログの版（例: 0.164.0）
	Asset   catalog.Asset
	// Client はダウンロードに使う HTTP クライアント（nil は既定）。https 以外へのリダイレクトは常に拒否する。
	Client *http.Client
	// Now は記録の時刻（nil は time.Now）。
	Now func() time.Time
}

// Record は導入が残した記録（検証済みの実行ファイルの値）。
type Record struct {
	Version     string    `json:"version"`
	Exe         string    `json:"exe"`
	ExeSize     int64     `json:"exe_size"`
	ExeSHA256   string    `json:"exe_sha256"`
	InstalledAt time.Time `json:"installed_at"`
}

// Status は導入済みのものと固定した版の比較。
type Status struct {
	Installed bool
	Version   string // 導入済みの版
	// PinnedVersion は固定した版。Outdated は導入済みの版がそれと違う（その実行ファイルは使わず、取り直す）。
	PinnedVersion string
	Outdated      bool
	// Broken は記録はあるのに実行ファイルが無い・サイズが違う（手で消された・書き換えられた）。
	Broken bool
}

func (i *Installer) now() time.Time {
	if i.Now != nil {
		return i.Now().UTC()
	}
	return time.Now().UTC()
}

func (i *Installer) record() (Record, bool) {
	b, err := os.ReadFile(i.Layout.RecordPath())
	if err != nil {
		return Record{}, false
	}
	var r Record
	if json.Unmarshal(b, &r) != nil || r.Version == "" || r.Exe == "" {
		return Record{}, false
	}
	return r, true
}

// Status は記録を読み、ファイルの有無とサイズを確かめる（SHA-256 は導入時に確かめ済み）。
func (i *Installer) Status() Status {
	st := Status{PinnedVersion: i.Version}
	rec, ok := i.record()
	if !ok {
		return st
	}
	st.Version = rec.Version
	// 記録の Exe は固定したアセットの名前でなければならない（記録を書き換えて別のファイルを起動させない）。
	info, err := os.Stat(filepath.Join(i.Layout.EngineDir(), rec.Exe))
	if rec.Exe != i.Asset.Exe || err != nil || !info.Mode().IsRegular() || info.Size() != rec.ExeSize {
		st.Broken = true
		return st
	}
	st.Installed = true
	st.Outdated = rec.Version != i.Version
	return st
}

// ExePath は導入済みで固定した版の実行ファイル。未導入・壊れている・別の版なら ok = false。
func (i *Installer) ExePath() (string, bool) {
	st := i.Status()
	if !st.Installed || st.Outdated {
		return "", false
	}
	return filepath.Join(i.Layout.EngineDir(), i.Asset.Exe), true
}

// Install は固定したアーカイブをダウンロードして SHA-256 とサイズを確かめ、実行ファイルを展開してそれも確かめ、
// 導入済みのものと置き換える。合わないファイルは残さない（ダウンロードの .part も、途中まで展開した engine.new も消す）。
// 実行中の app-server は呼び出し側が先に止めておくこと（Windows は動いている実行ファイルのディレクトリを置き換えられない）。
func (i *Installer) Install(ctx context.Context, progress Progress) error {
	if err := os.MkdirAll(i.Layout.DownloadsDir(), 0o700); err != nil {
		return fmt.Errorf("engine: prepare: %w", err)
	}
	part := filepath.Join(i.Layout.DownloadsDir(), path.Base(i.Asset.URL)+".part")
	defer func() {
		_ = os.Remove(part)
		_ = os.Remove(i.Layout.DownloadsDir()) // 空のときだけ消える
	}()
	if err := i.download(ctx, part, progress); err != nil {
		return err
	}
	tmp := i.Layout.EngineNewDir()
	_ = os.RemoveAll(tmp)
	if err := i.extract(ctx, part, tmp, progress); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := i.swap(tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := i.writeRecord(); err != nil {
		return err
	}
	return nil
}

// httpClient はダウンロード用のクライアント。https 以外へのリダイレクトを拒む。
func (i *Installer) httpClient() *http.Client {
	var c http.Client
	if i.Client != nil {
		c = *i.Client
	}
	prev := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return ErrInsecureURL
		}
		if prev != nil {
			return prev(req, via)
		}
		if len(via) >= 10 {
			return errors.New("engine: too many redirects")
		}
		return nil
	}
	return &c
}

// download はアーカイブを part へ取得し、サイズ（Content-Length ではなく実際のバイト数）と SHA-256 を確かめる。
func (i *Installer) download(ctx context.Context, part string, progress Progress) error {
	u, err := url.Parse(i.Asset.URL)
	if err != nil || u.Scheme != "https" {
		return ErrInsecureURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.Asset.URL, nil)
	if err != nil {
		return fmt.Errorf("engine: download: %w", err)
	}
	resp, err := i.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("engine: download: %w", scrubURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("engine: download: http %d", resp.StatusCode)
	}
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("engine: download: %w", err)
	}
	h := sha256.New()
	// 固定したサイズより 1 バイト多く読めたら、そこで止めて不一致にする（際限なく書かない）。
	n, copyErr := copyProgress(ctx, io.MultiWriter(f, h), io.LimitReader(resp.Body, i.Asset.Size+1), func(done int64) {
		if progress != nil {
			progress(PhaseDownload, done, i.Asset.Size)
		}
	})
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(part)
		return fmt.Errorf("engine: download: %w", scrubURL(copyErr))
	}
	if closeErr != nil {
		_ = os.Remove(part)
		return fmt.Errorf("engine: download: %w", closeErr)
	}
	if n != i.Asset.Size || hex.EncodeToString(h.Sum(nil)) != i.Asset.SHA256 {
		_ = os.Remove(part)
		return fmt.Errorf("%w (archive)", ErrMismatch)
	}
	return nil
}

// scrubURL は *url.Error から URL を外す（取得先の URL をログ・画面に出さない。クエリに署名が付くことがある）。
func scrubURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// copyProgress は ctx が終わったら止まるコピー。進みは 64 KiB ごとに通知する。
func copyProgress(ctx context.Context, dst io.Writer, src io.Reader, progress func(done int64)) (int64, error) {
	buf := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
			progress(total)
		}
		if rerr == io.EOF {
			return total, nil
		}
		if rerr != nil {
			if err := ctx.Err(); err != nil {
				return total, err
			}
			return total, rerr
		}
	}
}

// extract は zip から実行ファイル 1 本だけを dir へ展開する。受けるのは「Asset.Exe と同名の通常ファイル 1 本だけ」で、
// 書き出す先は dir/<Asset.Exe> に固定する（アーカイブの中の名前で外のファイルを上書きできない）。
func (i *Installer) extract(ctx context.Context, archive, dir string, progress Progress) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnsafeArchive, err)
	}
	defer func() { _ = zr.Close() }()
	a := i.Asset
	if len(zr.File) != 1 {
		return fmt.Errorf("%w: %d entries", ErrUnsafeArchive, len(zr.File))
	}
	zf := zr.File[0]
	if zf.Name != a.Exe || strings.ContainsAny(zf.Name, `/\:`) || zf.Name == "." || zf.Name == ".." || !zf.Mode().IsRegular() || zf.FileInfo().IsDir() {
		return ErrUnsafeArchive
	}
	if zf.UncompressedSize64 != uint64(a.ExeSize) {
		return fmt.Errorf("%w (executable size)", ErrMismatch)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("engine: extract: %w", err)
	}
	rc, err := zf.Open()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnsafeArchive, err)
	}
	defer func() { _ = rc.Close() }()
	out, err := os.OpenFile(filepath.Join(dir, a.Exe), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return fmt.Errorf("engine: extract: %w", err)
	}
	h := sha256.New()
	n, copyErr := copyProgress(ctx, io.MultiWriter(out, h), io.LimitReader(rc, a.ExeSize+1), func(done int64) {
		if progress != nil {
			progress(PhaseExtract, done, a.ExeSize)
		}
	})
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		return fmt.Errorf("engine: extract: %w", copyErr)
	case closeErr != nil:
		return fmt.Errorf("engine: extract: %w", closeErr)
	case n != a.ExeSize || hex.EncodeToString(h.Sum(nil)) != a.ExeSHA256:
		return fmt.Errorf("%w (executable)", ErrMismatch)
	}
	return nil
}

// swap は tmp を engine にする。前のエンジンは新しいものが入ってから消す。入れ替えに失敗したら前のものへ戻す。
func (i *Installer) swap(tmp string) error {
	dir, old := i.Layout.EngineDir(), i.Layout.EngineOldDir()
	_ = os.RemoveAll(old)
	hadOld := false
	if _, err := os.Stat(dir); err == nil {
		if err := os.Rename(dir, old); err != nil {
			return fmt.Errorf("engine: replace: %w", err)
		}
		hadOld = true
	}
	if err := os.Rename(tmp, dir); err != nil {
		if hadOld {
			_ = os.Rename(old, dir)
		}
		return fmt.Errorf("engine: replace: %w", err)
	}
	_ = os.RemoveAll(old)
	return nil
}

func (i *Installer) writeRecord() error {
	b, err := json.MarshalIndent(Record{
		Version: i.Version, Exe: i.Asset.Exe, ExeSize: i.Asset.ExeSize, ExeSHA256: i.Asset.ExeSHA256, InstalledAt: i.now(),
	}, "", " ")
	if err != nil {
		return err
	}
	tmp := i.Layout.RecordPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("engine: write the record: %w", err)
	}
	if err := os.Rename(tmp, i.Layout.RecordPath()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("engine: write the record: %w", err)
	}
	return nil
}

// Remove はエンジン・記録・ダウンロードの残りを消す。
func (i *Installer) Remove() error {
	if err := os.RemoveAll(i.Layout.EngineDir()); err != nil {
		return fmt.Errorf("engine: remove: %w", err)
	}
	_ = os.RemoveAll(i.Layout.EngineNewDir())
	_ = os.RemoveAll(i.Layout.EngineOldDir())
	_ = os.RemoveAll(i.Layout.DownloadsDir())
	if err := os.Remove(i.Layout.RecordPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("engine: remove the record: %w", err)
	}
	_ = os.Remove(i.Layout.RecordPath() + ".tmp")
	_ = os.Remove(i.Layout.Root) // 空のときだけ消える
	return nil
}

// CleanLeftovers は中断した導入の残り（途中まで展開した engine.new、置き換えの途中の engine.old）を片付ける。
// 置き換えが 2 回の rename の間で止まっていたら、engine.old を engine へ戻す。
func (i *Installer) CleanLeftovers() {
	_ = os.RemoveAll(i.Layout.EngineNewDir())
	if _, err := os.Stat(i.Layout.EngineDir()); err != nil {
		if _, oerr := os.Stat(i.Layout.EngineOldDir()); oerr == nil {
			_ = os.Rename(i.Layout.EngineOldDir(), i.Layout.EngineDir())
			return
		}
	}
	_ = os.RemoveAll(i.Layout.EngineOldDir())
	_ = os.RemoveAll(i.Layout.DownloadsDir())
}
