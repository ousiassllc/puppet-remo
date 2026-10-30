package platformtest

import (
	"context"
	"errors"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// canceledCtx は取り消し済みの ctx を返す。
func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// requireCode は err が domain の code のエラーであることを検査する。
func requireCode(t *testing.T, err error, want *domain.Error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("%s: error = %v, want %s", what, err, want.Code)
	}
}

// requireCanceled は err が context.Canceled であることを検査する。
func requireCanceled(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, context.Canceled) {
		t.Errorf("%s with a canceled context: error = %v, want context.Canceled", what, err)
	}
}
