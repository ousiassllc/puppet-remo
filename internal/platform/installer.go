package platform

import "context"

// Installer は署名検証済みのインストーラの実行（M3。FR-H09、NFR-S10。docs/components/overview.md §5.10）。
// service.Updater が唯一の呼び出し元で、署名の検証・版の比較・適用の可否の判定は呼び出し側が済ませてから呼ぶ（ここは判定しない）。
// goroutine を持たず、呼び出し元の goroutine で同期的に動く。
type Installer interface {
	// Run は path のインストーラをサイレント（`/S`）で起動し、起動できたら返す（インストーラの終了は待たない）。
	// アプリ自身の終了は呼び出し側が行う（インストーラが実行中のアプリのファイルを置き換えるため）。
	// path は絶対パスの実在する通常ファイル（拡張子 .exe）でなければならず、満たさなければ、相対パス・空・拡張子違いは
	// domain.ErrInvalidArgument、存在しなければ ErrNotFound（パスはエラーに含めない）。ctx が取り消し済みなら、起動せずに ctx.Err()。
	// OS が起動を拒否した（昇格の確認をユーザーが取り消した場合を含む）ときは、パスを含まないエラー。
	Run(ctx context.Context, path string) error
}
