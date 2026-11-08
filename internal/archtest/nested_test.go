package archtest

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// nestedThirdParty is the D7 allowlist for nested modules under ext/ and cmd/
// (ADR 0011, ADR 0010), keyed by the module's directory. The root module's `go list ./...`
// excludes nested modules, so TestNestedModules lists each one on its own.
// Adding an entry needs an ADR.
var nestedThirdParty = map[string][]string{
	"ext/metrics/postgres": {"github.com/jackc/pgx/v5"},
	"cmd/tollgated":        {"go.yaml.in/yaml/v3"},
}

// checkNested applies the ext/cmd-layer law to one import of a nested-module
// package: stdlib, the root public API, its own module's packages (rel is
// under the module path) and allowlisted third-party only. Never internal/,
// and testkit only from test files (inTest), as D5 says for ext and cmd.
//
// This check is the only enforcement of D5 for nested modules: Go's internal
// rule is import-path based, and a nested module's path is under the root
// module's path, so `go build` would accept the import (ADR 0011).
func checkNested(dir, rel, imp string, inTest bool) string {
	switch {
	case !inTest && isTestkit(imp):
		return fmt.Sprintf("D5 %s imports %s: testkit is for tests only; non-test code never imports it", rel, imp)
	case isInternal(imp):
		return fmt.Sprintf("D5 %s imports %s: extensions use the public API only: never internal/", rel, imp)
	case isStd(imp), isRoot(imp), strings.HasPrefix(imp, module+"/"):
		return ""
	case underAny(imp, nestedThirdParty[dir]):
		return ""
	}
	return fmt.Sprintf("D7 %s imports %s: third-party import not on the nested-module allowlist for %s (needs an ADR)", rel, imp, dir)
}

// nestedViolations checks every import edge the compiler sees for one
// nested-module package, test files included: a nested module's tests are
// held to the same law as its code (D5, D7).
func nestedViolations(dir string, p goPackage) []string {
	rel := strings.TrimPrefix(p.ImportPath, module+"/")
	var out []string
	if p.Doc == "" {
		out = append(out, fmt.Sprintf("P1 %s: package has no doc comment", rel))
	}
	for _, imp := range p.Imports {
		if v := checkNested(dir, rel, imp, false); v != "" {
			out = append(out, v)
		}
	}
	for _, imp := range slices.Concat(p.TestImports, p.XTestImports) {
		if v := checkNested(dir, rel, imp, true); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func TestCheckNested_Edges(t *testing.T) {
	const dir = "ext/metrics/postgres"
	bad := map[string]string{
		module + "/internal/model":   "D5",
		module + "/testkit":          "D5",
		"github.com/lib/pq":          "D7",
		"golang.org/x/sync/errgroup": "D7",
	}
	for imp, rule := range bad {
		if got := checkNested(dir, dir, imp, false); !strings.HasPrefix(got, rule+" ") {
			t.Errorf("checkNested(%s) = %q, want a %s violation", imp, got, rule)
		}
	}
	for _, imp := range []string{"context", module, "github.com/jackc/pgx/v5", "github.com/jackc/pgx/v5/pgxpool"} {
		if got := checkNested(dir, dir, imp, false); got != "" {
			t.Errorf("checkNested(%s) = %q, want allowed", imp, got)
		}
	}
	if got := checkNested(dir, dir, module+"/testkit", true); got != "" {
		t.Errorf("checkNested(testkit, test file) = %q, want allowed", got)
	}
}

func TestNestedViolations_CoverTestImports(t *testing.T) {
	const dir = "ext/metrics/postgres"
	cases := []struct {
		name string
		p    goPackage
		want string
	}{
		{"code", goPackage{Imports: []string{module + "/internal/model"}}, "D5 "},
		{"in-package test", goPackage{TestImports: []string{module + "/internal/chain"}}, "D5 "},
		{"external test", goPackage{XTestImports: []string{"github.com/lib/pq"}}, "D7 "},
		{"testkit in code", goPackage{Imports: []string{module + "/testkit"}}, "D5 "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.p.ImportPath, c.p.Doc = module+"/"+dir, "doc"
			got := nestedViolations(dir, c.p)
			if len(got) != 1 || !strings.HasPrefix(got[0], c.want) {
				t.Errorf("nestedViolations = %q, want one %q violation", got, c.want)
			}
		})
	}
}

func TestNestedModules(t *testing.T) {
	root := moduleRoot(t)
	dirs := nestedModuleDirs(t, root)
	for dir := range nestedThirdParty {
		if !slices.Contains(dirs, dir) {
			t.Errorf("allowlist entry %s has no go.mod: remove it or restore the module", dir)
		}
	}
	for _, dir := range dirs {
		if _, ok := nestedThirdParty[dir]; !ok {
			t.Errorf("nested module %s is not in nestedThirdParty: register it (with an empty list if it has no deps)", dir)
		}
		for _, p := range listIn(t, filepath.Join(root, dir)) {
			for _, v := range nestedViolations(dir, p) {
				t.Error(v)
			}
		}
	}
}

// nestedModuleDirs finds the go.mod files under ext/ and cmd/ with the same
// rule as scripts/lib.sh nested_modules: directories named testdata or
// starting with "." or "_" are skipped, as the go command skips them.
func nestedModuleDirs(t *testing.T, root string) []string {
	t.Helper()
	var dirs []string
	for _, top := range []string{"ext", "cmd"} {
		dirs = append(dirs, nestedModuleDirsUnder(t, root, top)...)
	}
	slices.Sort(dirs)
	return dirs
}

func nestedModuleDirsUnder(t *testing.T, root, top string) []string {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && skippedDir(d.Name()):
			return filepath.SkipDir
		case !d.IsDir() && d.Name() == "go.mod":
			rel, err := filepath.Rel(root, filepath.Dir(path))
			dirs = append(dirs, filepath.ToSlash(rel))
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s/: %v", top, err)
	}
	return dirs
}

func skippedDir(name string) bool {
	return name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}
