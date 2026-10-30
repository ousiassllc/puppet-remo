package platformtest

import (
	"context"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// RunClipboardContract は platform.Clipboard の契約を検査する。
// クリップボードを書き換えるので、終了時に元のテキストを書き戻す（実機では、テキスト以外の内容は失われる）。
func RunClipboardContract(t *testing.T, newClipboard func(t *testing.T) platform.Clipboard) {
	t.Helper()
	ctx := context.Background()

	t.Run("WriteText then ReadText round-trips text", func(t *testing.T) {
		c := newClipboard(t)
		original, err := c.ReadText(ctx)
		if err != nil {
			t.Fatalf("ReadText: %v", err)
		}
		t.Cleanup(func() { _ = c.WriteText(ctx, original) })

		for _, s := range []string{"hello", "日本語 😀 mixed", "line1\r\nline2\ttab", " leading and trailing ", ""} {
			if err := c.WriteText(ctx, s); err != nil {
				t.Fatalf("WriteText(%q): %v", s, err)
			}
			got, err := c.ReadText(ctx)
			if err != nil {
				t.Fatalf("ReadText: %v", err)
			}
			if got != s {
				t.Errorf("ReadText = %q, want %q", got, s)
			}
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		c := newClipboard(t)
		_, err := c.ReadText(canceledCtx())
		requireCanceled(t, err, "ReadText")
		requireCanceled(t, c.WriteText(canceledCtx(), "x"), "WriteText")
	})
}
