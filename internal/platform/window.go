package platform

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// WindowManager はウィンドウの列挙とフォーカス。事実を返すだけで、許可・拒否・所有関係の判定は決めない（判定は domain.ResolveGroup と service）。
// 取得できなかった事実は「不明」（零値・検証不能の印）のまま返す。推測で埋めない。
type WindowManager interface {
	// List は list_windows（MCP）用のトップレベルウィンドウの一覧を返す。タイトルのあるものだけ。ID は一意。
	List(ctx context.Context) ([]domain.WindowInfo, error)
	// ListAll は可視（非 cloaked）のすべてのトップレベルウィンドウを Z オーダー（前面が先）で返す。
	// タイトルのないもの・ツールウィンドウ・ポップアップ・メニューを含む（対象グループの判定用。domain.ResolveGroup）。
	// 所有関係の鎖（OwnerChain・MenuOwnerChain）・Class・ProcessStart・Style を埋める。ID は一意。
	ListAll(ctx context.Context) ([]domain.WindowInfo, error)
	// At は座標上の最上位ウィンドウと、その座標にある葉（GA_ROOT に引き上げる前の子ウィンドウ）を返す（InputGuard 用）。どのウィンドウにもなければ ok=false。
	At(p domain.Point) (hit domain.HitInfo, ok bool, err error)
	// Foreground は前面のウィンドウと、そのスレッドのフォーカス先の葉（hwndFocus）を返す。
	Foreground() (domain.FocusInfo, error)
	// Focus は id のウィンドウを前面にする。存在しなければ ErrWindowNotFound（ID の形式不正を含む）。
	Focus(ctx context.Context, id string) error
}
