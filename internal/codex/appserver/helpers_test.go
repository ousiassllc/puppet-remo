package appserver_test

import (
	"os"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/codex/codextest"
)

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

func testExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func readReport(p string) (codextest.Report, error) { return codextest.ReadReport(p) }
