package service

import (
	"context"
	"errors"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// このファイルはスコープ付きの run_task（#158）が Control.Do へ ctx で渡す 2 つの部品:
//
//   - 物理座標（WithPhysicalCoords）: 座標はモデルが見た基準 S の原点・縮小率で agent が物理座標へ変換済み。Do は縮小座標の変換をしない
//     （モニターの縮小率は、グループ合成画像の座標とは別の物なので使わない）。座標がモニター上にあることの検査はそのまま行う。
//   - グループの観測（withGroupObserver）: Do の入力の注入の直前ごと（1 つの操作の複数回の注入の間を含む）に、求め直した対象グループ G を
//     agent の基準と比べる。移動・置き換えは停止（stop。halted・ErrInputBlocked）、メンバーの増減だけは中断（yield。ErrGroupChanged。
//     注入しない・停止にしない）。観測がある操作は、操作ごとの撮影の事前確認（precheckCapture）を省く: バッチの中で画像を撮らないため。
//     メンバーが増えれば観測が中断し、次の撮影（中断後の GrabGroupFrame）が撮れるかを確かめる。

type physicalCoordsKey struct{}

// WithPhysicalCoords は、op の座標が仮想デスクトップの物理座標（変換済み）であることを Do に伝える ctx を返す。
func WithPhysicalCoords(ctx context.Context) context.Context {
	return context.WithValue(ctx, physicalCoordsKey{}, true)
}

func physicalCoords(ctx context.Context) bool {
	v, _ := ctx.Value(physicalCoordsKey{}).(bool)
	return v
}

// ReasonGroupChanged は ErrGroupChanged の detail.reason（中断の理由。停止の理由ではない）。
const ReasonGroupChanged = "group_changed"

// ErrGroupChanged は、観測（withGroupObserver）が、対象グループのメンバーの増減だけを検出して入力の（残りを）注入せずに返す中断のエラー
// （ErrInputBlocked と同じコードだが、停止ではなく、reason は group_changed）。IsGroupChanged で判定する。
var ErrGroupChanged = domain.ErrInputBlocked.
	WithMsg("the target window group changed; the action was not executed").
	WithDetail("reason", ReasonGroupChanged)

// groupChangedPartial は、中断のとき、同じ操作がすでに一部を注入していたこと（キー・文字の複数回の注入の途中、クリックの移動のあと。
// モデルへは「未実行」ではなく「一部だけ実行済み」と伝える）を ErrGroupChanged の detail.partial に載せる。
func groupChangedPartial(partial bool) error {
	if !partial {
		return ErrGroupChanged
	}
	return ErrGroupChanged.WithDetail("partial", true)
}

// IsGroupChangedPartial は err が、一部を注入したあとの ErrGroupChanged（中断）かを返す。
func IsGroupChangedPartial(err error) bool {
	var de *domain.Error
	if !errors.As(err, &de) || !IsGroupChanged(err) {
		return false
	}
	partial, _ := de.Detail["partial"].(bool)
	return partial
}

// IsGroupChanged は err が ErrGroupChanged（中断）かを返す。
func IsGroupChanged(err error) bool {
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeInputBlocked {
		return false
	}
	return de.Detail["reason"] == ReasonGroupChanged
}

// groupObserver は求め直した G を受け取り、停止（stop）か中断（yield）を返す。どちらでもなければ通す。
type groupObserver func(g domain.Group) (stop *scopeStop, yield bool)

type groupObserverKey struct{}

func withGroupObserver(ctx context.Context, obs groupObserver) context.Context {
	return context.WithValue(ctx, groupObserverKey{}, obs)
}

func groupObserverFrom(ctx context.Context) groupObserver {
	obs, _ := ctx.Value(groupObserverKey{}).(groupObserver)
	return obs
}
