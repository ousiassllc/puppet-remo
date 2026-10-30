package platform

import (
	"context"
	"fmt"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// AppLauncher はアプリの起動（M1.5。FR-M19。docs/components/overview.md §3）。service.Control が唯一の呼び出し元。
// goroutine を持たず、呼び出し元の goroutine で同期的に動く。
type AppLauncher interface {
	// Launch は spec.ExePath を新しいプロセスとして起動し、そのプロセス ID を返す。
	// Windows 実装は CreateProcess を、ExePath を lpApplicationName に明示し、コマンドラインを
	// 「引用した ExePath + Args」（Windows の引数規則）で組み立てて呼ぶ。シェルを経由せず、標準権限のまま起動し
	// （昇格が必要なアプリは elevation_required。UAC のダイアログは出さない）、環境変数は本アプリのものを引き継ぐ。
	// プロセスの作成に成功した時点で返す（ウィンドウは待たない）。同じアプリが起動中でも毎回新しいプロセスを起動する。
	//
	// 失敗は *LaunchError（Reason は file_missing / access_denied / elevation_required / other）。
	// 空の ExePath・Windows 実装では絶対パスでない ExePath・コマンドラインに組めない値（NUL など）は domain.ErrInvalidArgument。
	// ctx が取り消し済みなら、起動せずに ctx.Err() を返す。
	Launch(ctx context.Context, spec LaunchSpec) (pid uint32, err error)

	// Inspect は登録・更新時の存在確認: exePath が存在する通常ファイルか、workingDir（空でなければ）が存在するディレクトリか。
	// どちらかが無ければ ErrNotFound（パスはエラーに含めない）。ctx が取り消し済みなら ctx.Err()。
	Inspect(ctx context.Context, exePath, workingDir string) error
}

// LaunchSpec は 1 回の起動の指定。
type LaunchSpec struct {
	// ExePath は実行ファイルの絶対パス。
	ExePath string
	// Args は固定の引数（シェルを経由しないので、空白・引用符・& や % はそのまま 1 つの引数として渡る）。
	Args []string
	// WorkingDir は作業ディレクトリ。空なら ExePath のあるディレクトリ。
	WorkingDir string
}

// LaunchError は AppLauncher.Launch の失敗。理由は domain.LaunchFailure の 4 値のいずれか。
//
// Error() は理由だけを返し、実行ファイルのパス・引数を含めない（NFR-S20。ログ・監査・MCP の応答に出さない）。
// Err は OS のエラー（errors.Is / errors.As で調べるためのもの）で、実装はパス・引数を含まない値だけを入れる。
type LaunchError struct {
	Reason domain.LaunchFailure
	Err    error
}

func (e *LaunchError) Error() string { return fmt.Sprintf("platform: launch failed (%s)", e.Reason) }

func (e *LaunchError) Unwrap() error { return e.Err }

// FilePicker はネイティブのファイル選択ダイアログ（M1.5。FR-H12）。
// `.exe` のフィルターで、ホスト GUI のメインウィンドウを親にしたモーダルを開く。
// ダイアログは自プロセスのウィンドウなので、InputGuard の自己保護（NFR-S06）が AI の入力を拒む。
type FilePicker interface {
	// PickExecutable はダイアログを開き、選ばれた実行ファイルの絶対パスを返す。キャンセルは ok=false（err は nil）。
	// ctx の取り消しでダイアログを閉じて ctx.Err() を返す。取り消し済みなら開かずに ctx.Err()。
	PickExecutable(ctx context.Context) (path string, ok bool, err error)
}
