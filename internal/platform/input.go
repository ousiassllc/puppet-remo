package platform

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// InputInjector は入力注入。InputGuard を通った後にだけ呼ばれる（service.Control が唯一の呼び出し元）。
// 座標は仮想デスクトップの物理座標。引数の不正（未知のボタン・修飾キー・方向・キー、範囲外の count・amount）は
// 何も注入せずに ErrInvalidArgument を返す。
type InputInjector interface {
	MoveTo(ctx context.Context, p domain.Point) error
	// Click は現在のカーソル位置でクリックする。count は 1〜3（ダブル・トリプルクリック）。
	// mods はクリックの間だけ押下する。
	Click(ctx context.Context, b domain.Button, count int, mods []domain.Modifier) error
	// Button は現在のカーソル位置で b を押す・離す（Web UI の input.mouse の down / up）。押したまま離さないボタンは
	// ReleaseAll が離す。未知のボタン・操作は何も注入せずに ErrInvalidArgument。
	Button(ctx context.Context, b domain.Button, a domain.ButtonAction) error
	// Drag は from で左ボタンを押し、to まで移動して離す。終了後のカーソルは to にある。
	Drag(ctx context.Context, from, to domain.Point, mods []domain.Modifier) error
	// Scroll は at へ移動してホイールをノッチ単位で回す。amount は正。
	Scroll(ctx context.Context, at domain.Point, dir domain.Direction, amount int) error
	// Wheel はノッチ単位（小数可）。× 120（WHEEL_DELTA）を丸めて注入し、端数は実装が持ち越す。
	// 縦は Win32 の正が上向きのため dy の符号を反転する。NaN・無限大は ErrInvalidArgument。
	Wheel(ctx context.Context, at domain.Point, dx, dy float64) error
	Key(ctx context.Context, k domain.KeyCombo) error
	// KeyEvent は Web UI の down / up。Code は空にできない。
	KeyEvent(ctx context.Context, e domain.KeyEvent) error
	// Text は KEYEVENTF_UNICODE で文字列を入力する。空文字列は何もしない。
	Text(ctx context.Context, s string) error
	// ReleaseAll は押下中のキー・ボタンをすべて解放する（中断・キルスイッチ時）。
	ReleaseAll() error
	CursorPos() (domain.Point, error)
}
