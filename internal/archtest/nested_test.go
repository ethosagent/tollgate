package archtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// nestedThirdParty is the D7 allowlist for nested modules under ext/ (ADR
// 0011), keyed by the module's directory. The root module's `go list ./...`
// excludes nested modules, so TestNestedModules lists each one on its own.
// Adding an entry needs an ADR.
var nestedThirdParty = map[string][]string{
	"ext/metrics/postgres": {"github.com/jackc/pgx/v5"},
}

// checkNested applies the ext-layer law to one import of a nested-module
// package: stdlib, the root public API, its own module's packages (rel is
// under the module path) and allowlisted third-party only. Never internal/.
func checkNested(dir, rel, imp string) string {
	switch {
	case isInternal(imp):
		return fmt.Sprintf("D5 %s imports %s: extensions use the public API only: never internal/", rel, imp)
	case isStd(imp), isRoot(imp), strings.HasPrefix(imp, module+"/"):
		return ""
	}
	for _, prefix := range nestedThirdParty[dir] {
		if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
			return ""
		}
	}
	return fmt.Sprintf("D7 %s imports %s: third-party import not on the nested-module allowlist for %s (needs an ADR)", rel, imp, dir)
}

func TestCheckNested_Edges(t *testing.T) {
	const dir = "ext/metrics/postgres"
	bad := map[string]string{
		module + "/internal/model":   "D5",
		"github.com/lib/pq":          "D7",
		"golang.org/x/sync/errgroup": "D7",
	}
	for imp, rule := range bad {
		if got := checkNested(dir, dir, imp); !strings.HasPrefix(got, rule+" ") {
			t.Errorf("checkNested(%s) = %q, want a %s violation", imp, got, rule)
		}
	}
	for _, imp := range []string{"context", module, module + "/testkit", "github.com/jackc/pgx/v5", "github.com/jackc/pgx/v5/pgxpool"} {
		if got := checkNested(dir, dir, imp); got != "" {
			t.Errorf("checkNested(%s) = %q, want allowed", imp, got)
		}
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
			rel := strings.TrimPrefix(p.ImportPath, module+"/")
			if p.Doc == "" {
				t.Errorf("P1 %s: package has no doc comment", rel)
			}
			for _, imp := range p.Imports {
				if v := checkNested(dir, rel, imp); v != "" {
					t.Error(v)
				}
			}
		}
	}
}

// TestNestedModule_CannotImportInternal proves Go itself rejects a nested
// module importing the root module's internal/ packages, so D5 holds across
// the module boundary even without this gate.
func TestNestedModule_CannotImportInternal(t *testing.T) {
	root := moduleRoot(t)
	tmp := t.TempDir()
	gomod := fmt.Sprintf("module example.com/probe\n\ngo 1.26\n\nrequire %s v0.0.0\n\nreplace %s => %s\n", module, module, root)
	src := "package probe\n\nimport _ \"" + module + "/internal/model\"\n"
	for name, body := range map[string]string{"go.mod": gomod, "probe.go": src} {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("use of internal package")) {
		t.Fatalf("expected a 'use of internal package' error, got err=%v\n%s", err, out)
	}
}

func nestedModuleDirs(t *testing.T, root string) []string {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(filepath.Join(root, "ext"), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "go.mod" {
			rel, rerr := filepath.Rel(root, filepath.Dir(path))
			dirs = append(dirs, filepath.ToSlash(rel))
			return rerr
		}
		return err
	})
	if err != nil {
		t.Fatalf("walk ext/: %v", err)
	}
	slices.Sort(dirs)
	return dirs
}

func listIn(t *testing.T, dir string) []goPackage {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list in %s: %v\n%s", dir, err, stderr.String())
	}
	var pkgs []goPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p goPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			return pkgs
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
}
