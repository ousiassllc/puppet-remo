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
	// Find はウィンドウの UI を浅い階層から走査し（最大 maxNodes 節点）、match が true を返した要素を最大 maxResults 件返す（#267）。
	// match には ID を空にした要素を渡す（結果の要素には ID を付ける。RuntimeId を取れない要素は ID が空）。要素の値・テキスト本文は渡さない。
	// 上限が 1 未満なら ErrInvalidArgument、存在しない windowID は ErrWindowNotFound。
	Find(ctx context.Context, windowID string, match func(domain.UIElementInfo) bool, maxResults, maxNodes int) (domain.UIFindResult, error)
	// Resolve は要素 ID（domain.ElementRef）を生きた要素へ引き直す（#267。#268 の操作が、操作の直前に使う）。
	// ウィンドウが無い・HWND が別のプロセスのものになった・要素が無い、のいずれも domain.ErrStaleElement（古い ID が別の要素に当たってはならない）。
	Resolve(ctx context.Context, ref domain.ElementRef) (domain.UIElementInfo, error)
}
