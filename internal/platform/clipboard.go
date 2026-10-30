package platform

import "context"

// Clipboard はクリップボードのテキスト。テキスト以外が入っているときの ReadText は空文字列。
type Clipboard interface {
	ReadText(ctx context.Context) (string, error)
	WriteText(ctx context.Context, s string) error
}
