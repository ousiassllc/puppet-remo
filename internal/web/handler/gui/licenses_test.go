package gui_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/licenses"
)

// ライセンス表記のページは、go:embed した全文（OFL・MIT）をそのまま HTML エスケープして出し、設定の画面から開ける。
func TestLicensesPageShowsFullNotices(t *testing.T) {
	r := newRig(t)
	res := r.get("/licenses")
	status(t, res, http.StatusOK)
	for _, want := range []string{"<html", "data-licenses-text", "SIL OPEN FONT LICENSE Version 1.1", "Noto Sans JP", "Sora", "Iconoir", "Permission is hereby granted"} {
		if !strings.Contains(res.Body, want) {
			t.Errorf("licenses page does not contain %q", want)
		}
	}
	// 全文が欠けずに出る（& < > ' " は HTML エスケープされる。全文の長さで足りない場合を拾う）。
	if len(res.Body) < len(licenses.ThirdPartyNotices) {
		t.Errorf("page body = %d bytes, shorter than the embedded notices (%d bytes)", len(res.Body), len(licenses.ThirdPartyNotices))
	}
	if !strings.Contains(res.Body, `aria-current="page"`) {
		t.Error("the settings item in the sidebar must be current")
	}
}

func TestLicensesPageBoostedIsFragment(t *testing.T) {
	res := newRig(t).get("/licenses", "HX-Request", "true", "HX-Boosted", "true")
	status(t, res, http.StatusOK)
	if !strings.Contains(res.Body, "data-licenses-text") || strings.Contains(res.Body, "<html") {
		t.Errorf("boosted /licenses must be a fragment with the notices (has <html> = %v)", strings.Contains(res.Body, "<html"))
	}
}

func TestSettingsPageLinksToLicenses(t *testing.T) {
	res := newRig(t).get("/settings")
	contains(t, res, `href="/licenses"`)
}
