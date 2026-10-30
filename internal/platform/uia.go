package platform

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// UIAutomation は UI Automation の読み取り。入力は注入しない。
// windowID が空なら前面ウィンドウ。存在しない ID は ErrWindowNotFound、上限が 1 未満なら ErrInvalidArgument。
type UIAutomation interface {
	// Tree はウィンドウの UI ツリーを返す。根の深さを 0 とし、深さが maxDepth を超える節点と、
	// 総数が maxNodes を超える節点は返さない（打ち切った節点は Truncated）。
	// パスワード欄は Value を空にして ValueRedacted を立てる。
	Tree(ctx context.Context, windowID string, maxDepth, maxNodes int) (domain.UINode, error)
	// Snapshot は高速パスの候補表の入力。1 回の呼び出しで 1 つの整合したスナップショットを返す。
	// 要素の値・テキスト本文は返さない（名前と値が同一かの真偽だけ）。maxNodes を超えたら Truncated。
	Snapshot(ctx context.Context, windowID string, maxNodes int) (domain.UISnapshot, error)
}
