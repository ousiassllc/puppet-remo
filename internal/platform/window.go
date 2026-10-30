package platform

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// WindowManager はウィンドウの列挙とフォーカス。事実を返すだけで、許可・拒否は決めない。
type WindowManager interface {
	// List はトップレベルウィンドウの一覧を返す。ID は一意。
	List(ctx context.Context) ([]domain.WindowInfo, error)
	// At は座標上の最上位ウィンドウを返す（InputGuard 用）。どのウィンドウにもなければ ok=false。
	At(p domain.Point) (info domain.WindowInfo, ok bool, err error)
	Foreground() (domain.WindowInfo, error)
	// Focus は id のウィンドウを前面にする。存在しなければ ErrWindowNotFound（ID の形式不正を含む）。
	Focus(ctx context.Context, id string) error
}
