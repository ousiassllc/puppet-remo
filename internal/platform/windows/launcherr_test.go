package windows

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestClassifyLaunchError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want domain.LaunchFailure
	}{
		{"file not found", syscall.Errno(2), domain.LaunchFileMissing},
		{"path not found", syscall.Errno(3), domain.LaunchFileMissing},
		{"working directory is invalid", syscall.Errno(267), domain.LaunchFileMissing},
		{"access denied", syscall.Errno(5), domain.LaunchAccessDenied},
		{"privilege not held", syscall.Errno(1314), domain.LaunchAccessDenied},
		{"blocked by policy", syscall.Errno(1260), domain.LaunchAccessDenied},
		{"elevation required", syscall.Errno(740), domain.LaunchElevationRequired},
		{"bad exe format", syscall.Errno(193), domain.LaunchOther},
		{"command line too long", syscall.Errno(206), domain.LaunchOther},
		{"not an errno", errors.New("boom"), domain.LaunchOther},
		{"wrapped errno", fmt.Errorf("wrap: %w", syscall.Errno(740)), domain.LaunchElevationRequired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyLaunchError(tc.err); got != tc.want {
				t.Errorf("classifyLaunchError = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewLaunchErrorKeepsOnlyTheErrnoAsCause(t *testing.T) {
	cause := &fs.PathError{Op: "CreateProcess", Path: `C:\secret\app.exe`, Err: syscall.Errno(740)}
	le := newLaunchError(cause)
	if le.Reason != domain.LaunchElevationRequired {
		t.Errorf("Reason = %q, want elevation_required", le.Reason)
	}
	if !errors.Is(le, syscall.Errno(740)) {
		t.Error("the errno is not reachable through errors.Is")
	}
	if strings.Contains(le.Error(), "secret") || (le.Err != nil && strings.Contains(le.Err.Error(), "secret")) {
		t.Errorf("launch error leaks the path: %q / %v", le.Error(), le.Err)
	}

	other := newLaunchError(errors.New(`open C:\secret\app.exe: boom`))
	if other.Reason != domain.LaunchOther || other.Err != nil {
		t.Errorf("non-errno error = %+v, want reason other and no cause", other)
	}
}
