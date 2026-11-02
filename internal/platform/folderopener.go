package platform

import "context"

// FolderOpener は OS のファイルマネージャー（Windows ではエクスプローラー）でフォルダーを開く（#54。FR-H13）。
// service には出さず、呼び出し元（ホスト GUI のログ画面の app.LogReader）が開くフォルダーを固定する。
// goroutine を持たず、呼び出し元の goroutine で同期的に動く。
type FolderOpener interface {
	// OpenFolder は dir のフォルダーをファイルマネージャーで開き、開けたら返す（ウィンドウは待たない）。
	// dir は絶対パスの実在するディレクトリでなければならず、満たさなければ、相対パス・空・NUL を含む値は
	// domain.ErrInvalidArgument、存在しない・ディレクトリでなければ ErrNotFound（パスはエラーに含めない）。
	// Windows 実装は ShellExecute の "open" を dir に対して呼ぶ（シェル・コマンドラインを経由しない）。
	// ctx が取り消し済みなら、開かずに ctx.Err()。OS が拒否したときは、パスを含まないエラー。
	OpenFolder(ctx context.Context, dir string) error
}
