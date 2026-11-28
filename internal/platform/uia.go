package platform

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// UIAutomation は UI Automation。Tree・Snapshot・Find・Resolve は読み取りだけで、入力は注入しない。Act（#268）だけが要素の状態を変える操作で、
// 呼べるのは service.Control だけ（リース・InputGuard・入力スコープ・監査を通すため）。
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
	// Act は要素 ID（domain.ElementRef）の要素を、UI Automation のパターンで操作する（#268。ui_action）。カーソルもキーボードフォーカスも動かさず、
	// SendInput・キー入力にも切り替えない。act は Control が domain.UIAction.Validate で検査済み。
	// 要素の特定は Resolve と同じ（ウィンドウが無い・HWND が別のプロセスのものになった・要素が無い・操作の途中で消えた、のいずれも ErrStaleElement）。特定したあと、次の順に拒否する:
	//   - 要素が無効 → ErrElementDisabled
	//   - 操作に必要なパターンが無い → ErrPatternUnsupported（detail: action・pattern・supported〔要素の対応パターン〕）
	//   - set_value の対象が読み取り専用 → ErrElementReadOnly
	//   - UI Automation が拒否した → アクセス拒否（UIPI・昇格）は ErrInputBlocked（detail.reason = access_denied）、それ以外は ErrUIActionFailed（detail.reason）
	// すでにその状態の toggle（State 指定）・expand・collapse は、パターンの操作を呼ばず Changed を false にして成功とする。
	// 結果の State は操作後の状態。値（テキスト本文）は返さない。要素を特定したあとの失敗も、UIActionResult の Window・Element・Password を入れて返す。
	Act(ctx context.Context, ref domain.ElementRef, act domain.UIAction) (domain.UIActionResult, error)
}
