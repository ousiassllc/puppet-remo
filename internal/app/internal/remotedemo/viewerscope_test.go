package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
)

func doViewerScope(t *testing.T, s *viewerScopeScene, do string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__demo/viewer-scope?do="+do, nil))
	return rec.Code
}

// -viewer-scope の制御エンドポイント: 別プロセスのウィンドウが前面に出て、退かすと T が前面に戻り、reset で背景が置き直される。
func TestViewerScopeSceneControls(t *testing.T) {
	s := newViewerScopeScene(platformtest.NewFakeClipboard())
	if code := doViewerScope(t, s, "foreign"); code != http.StatusNoContent {
		t.Fatalf("foreign = %d", code)
	}
	fg, err := s.windows.Foreground()
	if err != nil || fg.Foreground.ID != viewerScopeForeignID {
		t.Errorf("foreground = %+v, %v", fg.Foreground, err)
	}
	doViewerScope(t, s, "foreign-off")
	if fg, _ = s.windows.Foreground(); fg.Foreground.ID != platformtest.FakeAppMainID {
		t.Errorf("after foreign-off foreground = %+v", fg.Foreground)
	}
	doViewerScope(t, s, "cross-process")
	if !s.app.DialogCrossProcess {
		t.Error("cross-process: DialogCrossProcess = false")
	}
	doViewerScope(t, s, "reset")
	if s.app.DialogCrossProcess {
		t.Error("reset: DialogCrossProcess = true")
	}
	if code := doViewerScope(t, s, "nope"); code != http.StatusBadRequest {
		t.Errorf("nope = %d, want 400", code)
	}
	list, err := s.windows.ListAll(t.Context())
	if err != nil || len(list) != 4 {
		t.Errorf("windows after reset = %d (%v), want 4（T と背景 3）", len(list), err)
	}
}
