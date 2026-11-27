package codex

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/codex/appserver"
	"github.com/ousiassllc/puppet-remo/internal/codex/protocol"
	"github.com/ousiassllc/puppet-remo/internal/codex/rpc"
	"github.com/ousiassllc/puppet-remo/internal/openai"
)

const maxMessageRunes = 300

// sanitize は app-server の文を表示に向く形にする: 資格情報らしいものを取り除き、短くする。
func sanitize(msg string) string {
	msg = appserver.Redact(strings.TrimSpace(msg))
	if r := []rune(msg); len(r) > maxMessageRunes {
		msg = string(r[:maxMessageRunes]) + "…"
	}
	return msg
}

// turnError は失敗したターンのエラーを、openai の分類（errors.Is で判定する）にする。
// 使用量の上限 → ErrUsageLimit、認可 → ErrPlanUnauthorized。それ以外は分類なしの失敗（タスクは codex_error）。
func turnError(e *protocol.TurnError) error {
	if e == nil {
		return errors.New("codex: the turn failed (no reason given)")
	}
	msg := sanitize(e.Message)
	if msg == "" {
		msg = e.CodexErrorInfo.Kind
	}
	switch e.CodexErrorInfo.Kind {
	case protocol.ErrUsageLimitExceeded:
		return fmt.Errorf("%w: %s", openai.ErrUsageLimit, msg)
	case protocol.ErrUnauthorized:
		return fmt.Errorf("%w: %s", openai.ErrPlanUnauthorized, msg)
	}
	return fmt.Errorf("codex: the turn failed: %s", msg)
}

// callError は要求の失敗: ctx の終わりはそれ自身、接続の終了は分類なしの失敗、app-server のエラー応答は内容を持つ失敗。
func callError(ctx context.Context, se *appserver.Session, what string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var rerr *rpc.Error
	if errors.As(err, &rerr) {
		return fmt.Errorf("codex: %s: %s", what, sanitize(rerr.Message))
	}
	if errors.Is(err, rpc.ErrClosed) {
		return fmt.Errorf("codex: %s: the app-server ended: %s", what, sanitize(endMessage(se)))
	}
	return fmt.Errorf("codex: %s: %s", what, sanitize(err.Error()))
}

// endMessage はプロセスが終了したことと、その終わり方。
func endMessage(se *appserver.Session) string {
	if r := se.EndReason(); r != "" {
		return r
	}
	return "the app-server ended"
}
