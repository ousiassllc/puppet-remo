package remote_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

func settingsForm(monitor, quality, fps string) url.Values {
	return url.Values{"monitor": {monitor}, "quality": {quality}, "fps": {fps}}
}

func TestSettingsAppliesAndRedrawsTheToolbar(t *testing.T) {
	r := newRig(t)
	_, h := r.viewer()
	res := r.post("/viewer/settings", settingsForm("1", "55", "20"), h...)
	status(t, res, http.StatusOK)
	contains(t, res, `id="viewer-toolbar"`, `value="55"`, `value="20"`, `<option value="1" selected>`)
	lacks(t, res, "<html", "data-field-error", "data-error-code")
	want := service.ViewerConfig{Monitor: 1, Quality: 55, FPS: 20}
	if got := r.f.Sessions.Configured; len(got) != 1 || got[0] != want {
		t.Errorf("Configured = %+v, want [%+v]", got, want)
	}
}

func TestSettingsAcceptsTheBoundaries(t *testing.T) {
	r := newRig(t)
	_, h := r.viewer()
	for _, f := range []url.Values{settingsForm("0", "1", "1"), settingsForm("0", "100", "30"), settingsForm(" 0 ", " 70 ", " 10 ")} {
		status(t, r.post("/viewer/settings", f, h...), http.StatusOK)
	}
}

// 検証エラー（422）は、入力値とメッセージ付きでツールバーを再描画する。何も変えない。
func TestSettingsValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
		want []string
		not  []string
	}{
		{"画質が下限未満", settingsForm("0", "0", "10"), []string{`data-field-error="viewer-quality"`, `value="0"`}, []string{`data-field-error="viewer-fps"`, `data-field-error="viewer-monitor"`}},
		{"画質が上限超過", settingsForm("0", "101", "10"), []string{`data-field-error="viewer-quality"`, `value="101"`}, nil},
		{"画質が整数でない", settingsForm("0", "abc", "10"), []string{`data-field-error="viewer-quality"`}, nil},
		{"FPS が下限未満", settingsForm("0", "70", "0"), []string{`data-field-error="viewer-fps"`}, []string{`data-field-error="viewer-quality"`}},
		{"FPS が上限超過", settingsForm("0", "70", "31"), []string{`data-field-error="viewer-fps"`, `value="31"`}, nil},
		{"FPS が空", settingsForm("0", "70", ""), []string{`data-field-error="viewer-fps"`}, nil},
		{"存在しないモニター", settingsForm("9", "70", "10"), []string{`data-field-error="viewer-monitor"`, "存在しないモニターです"}, []string{`data-field-error="viewer-quality"`}},
		{"モニターが負", settingsForm("-1", "70", "10"), []string{`data-field-error="viewer-monitor"`}, nil},
		{"モニターが整数でない", settingsForm("main", "70", "10"), []string{`data-field-error="viewer-monitor"`}, nil},
		{"項目が無い", url.Values{}, []string{`data-field-error="viewer-monitor"`, `data-field-error="viewer-quality"`, `data-field-error="viewer-fps"`}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			_, h := r.viewer()
			res := r.post("/viewer/settings", tt.form, h...)
			status(t, res, http.StatusUnprocessableEntity)
			contains(t, res, append([]string{`id="viewer-toolbar"`, `data-error-code="invalid_argument"`, `role="alert"`}, tt.want...)...)
			lacks(t, res, append([]string{"<html"}, tt.not...)...)
			if n := len(r.f.Sessions.Configured); n != 0 {
				t.Errorf("Configure was called %d times for invalid input", n)
			}
		})
	}
}

func TestSettingsWithoutAConnectedViewerIs400(t *testing.T) {
	r := newRig(t)
	_, h := r.viewer()
	r.f.Sessions.ConfigureErr = domain.ErrInvalidArgument.WithMsg("no viewer is connected")
	failed(t, r.post("/viewer/settings", settingsForm("0", "70", "10"), h...), http.StatusBadRequest, "invalid_argument")
}

func TestSettingsNeedsAValidViewerID(t *testing.T) {
	r := newRig(t)
	failed(t, r.post("/viewer/settings", settingsForm("0", "70", "10")), http.StatusBadRequest, "invalid_argument")
	if n := len(r.f.Sessions.Configured); n != 0 {
		t.Errorf("Configure was called %d times without a viewer_id", n)
	}
}

func TestSettingsIgnoresTheQueryString(t *testing.T) {
	r := newRig(t)
	_, h := r.viewer()
	res := r.post("/viewer/settings?quality=55&fps=20&monitor=1", nil, h...)
	status(t, res, http.StatusUnprocessableEntity)
}
