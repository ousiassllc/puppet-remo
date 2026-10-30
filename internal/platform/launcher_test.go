package platform_test

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
)

func TestLaunchErrorMessageHasOnlyTheReason(t *testing.T) {
	cause := &fs.PathError{Op: "CreateProcess", Path: `C:\secret dir\app.exe`, Err: errors.New("denied by --token=abc")}
	err := error(&platform.LaunchError{Reason: domain.LaunchAccessDenied, Err: cause})

	for _, text := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err)} {
		if text != "platform: launch failed (access_denied)" {
			t.Errorf("message = %q, want only the reason", text)
		}
		if strings.Contains(text, "secret") || strings.Contains(text, "token") {
			t.Errorf("message %q leaks the cause", text)
		}
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is cannot reach the cause")
	}
}

func TestLaunchErrorIsFoundByErrorsAs(t *testing.T) {
	wrapped := fmt.Errorf("launch: %w", &platform.LaunchError{Reason: domain.LaunchElevationRequired})
	var le *platform.LaunchError
	if !errors.As(wrapped, &le) || le.Reason != domain.LaunchElevationRequired {
		t.Errorf("errors.As = %v, %+v; want reason elevation_required", errors.As(wrapped, &le), le)
	}
}
