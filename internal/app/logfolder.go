package app

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// ログフォルダーを開く操作（#54。FR-H13）。開く場所の安全性は「パスを引数にしない」ことで保つ:
// service.AppLog.OpenFolder には引数が無く、開くのは常にログファイルのあるフォルダー（filepath.Dir(r.path)）だけ。
// 呼び出し元（HTTP のリクエスト・画面の入力）が決められる値は無い。

// errNoFolderOpener は LogReader にフォルダーを開く手段（WithFolderOpener）が渡されていないとき。パスを含まない。
var errNoFolderOpener = errors.New("app: log folder opener is not configured")

// LogReaderOption は NewLogReader の任意の設定。
type LogReaderOption func(*LogReader)

// WithFolderOpener はログフォルダーを開く手段（OpenFolder）を設定する。無ければ OpenFolder は errNoFolderOpener を返す。
func WithFolderOpener(o platform.FolderOpener) LogReaderOption {
	return func(r *LogReader) { r.opener = o }
}

// OpenFolder はログフォルダー（今のログファイルとローテーション済みのファイルがあるフォルダー）を、OS のファイルマネージャーで開く。
// 開くのは常に filepath.Dir(Path()) だけで、パスは受け取らない。フォルダーが無ければ service.ErrNotFound（作らない）。
// エラーにパスは含まれない（platform.FolderOpener の契約）。
func (r *LogReader) OpenFolder(ctx context.Context) error {
	if r.opener == nil {
		return errNoFolderOpener
	}
	err := r.opener.OpenFolder(ctx, filepath.Dir(r.path))
	if errors.Is(err, platform.ErrNotFound) {
		return service.ErrNotFound
	}
	return err
}
