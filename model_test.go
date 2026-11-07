package tollgate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestAliases_CoverModel fails when internal/model gains an exported type or
// constant that model.go does not re-export, so extension authors never need
// to import an internal package (docs/decisions/0003).
func TestAliases_CoverModel(t *testing.T) {
	want := exportedNames(t, filepath.Join("internal", "model"))
	have := exportedNames(t, ".")
	var missing []string
	for name := range want {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("model.go does not re-export: %s", strings.Join(missing, ", "))
	}
}

// exportedNames returns the exported top-level type and const names declared
// in the non-test Go files of dir.
func exportedNames(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || (gd.Tok != token.TYPE && gd.Tok != token.CONST) {
				continue
			}
			for _, s := range gd.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						names[s.Name.Name] = true
					}
				case *ast.ValueSpec:
					for _, id := range s.Names {
						if id.IsExported() {
							names[id.Name] = true
						}
					}
				}
			}
		}
	}
	return names
}
