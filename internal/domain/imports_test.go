package domain_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/ousiassllc/puppet-remo"

// domain は他の内部パッケージを import しない（docs/components/overview.md §2 の不変条件）。
// CI では scripts/check-imports.sh も同じ規則を go list で検査する。
func TestDomainImportsNoInternalPackage(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files: %v", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		parsed, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range parsed.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(path, modulePath+"/") || strings.HasSuffix(f, "_test.go") && path == modulePath+"/internal/domain" {
				continue
			}
			t.Errorf("%s imports %s", f, path)
		}
	}
}
