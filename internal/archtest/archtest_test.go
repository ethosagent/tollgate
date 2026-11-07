package archtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/ethosagent/tollgate"

// thirdParty is the allowlist of non-stdlib import path prefixes, by layer
// that may use them (rule D7). The core (root, internal) has none by design:
// docs/decisions/0002. Adding an entry needs an ADR.
var thirdParty = map[string][]string{
	"ext": {},
	"cmd": {},
}

// layer classifies a package by its path relative to the module root.
func layer(rel string) string {
	switch {
	case rel == ".":
		return "root"
	case rel == "internal/model":
		return "model"
	case rel == "internal/archtest":
		return "archtest"
	case strings.HasPrefix(rel, "internal/"):
		return "internal"
	case rel == "testkit" || strings.HasPrefix(rel, "testkit/"):
		return "testkit"
	case rel == "ext" || strings.HasPrefix(rel, "ext/"):
		return "ext"
	case strings.HasPrefix(rel, "cmd/"):
		return "cmd"
	}
	return ""
}

// rule is one dependency-law check: may a package in layer `from` import `imp`?
type rule struct {
	id    string
	from  []string
	allow func(imp string) bool
	why   string
}

var rules = []rule{
	{"D2", []string{"model"}, isStd, "internal/model is the neutral model: standard library only"},
	{"D3", []string{"internal"}, either(isStd, isInternal), "internal engine packages never import the root, ext, testkit or cmd"},
	{"D4", []string{"root"}, either(isStd, isInternal), "the core has no third-party dependencies and never imports ext, testkit or cmd"},
	{"D5", []string{"ext", "cmd"}, publicOnly, "extensions and the binary use the public API only: never internal/"},
	{"D6", []string{"testkit"}, either(isStd, isRoot), "testkit depends on the public API and the standard library only"},
	{"D6", []string{"archtest"}, isStd, "the architecture gate depends on the standard library only"},
}

func TestArchitecture(t *testing.T) {
	pkgs := listPackages(t)
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages")
	}
	var violations []string
	for _, p := range pkgs {
		rel := relPath(p.ImportPath)
		l := layer(rel)
		if l == "" {
			violations = append(violations, fmt.Sprintf("D1 %s: package is in no layer; add it to ARCHITECTURE.md and layer() in internal/archtest", rel))
			continue
		}
		if p.Doc == "" {
			violations = append(violations, fmt.Sprintf("P1 %s: package has no doc comment; add doc.go saying what it does and what it may import", rel))
		}
		for _, imp := range p.Imports {
			if v := check(l, rel, imp); v != "" {
				violations = append(violations, v)
			}
		}
	}
	slices.Sort(violations)
	for _, v := range violations {
		t.Error(v)
	}
}

func check(l, rel, imp string) string {
	if !isStd(imp) && !strings.HasPrefix(imp, module) && !allowedThirdParty(l, imp) {
		return fmt.Sprintf("D7 %s imports %s: third-party import not on the allowlist for layer %q (needs an ADR)", rel, imp, l)
	}
	for _, r := range rules {
		if !slices.Contains(r.from, l) {
			continue
		}
		if !r.allow(imp) && !allowedThirdParty(l, imp) {
			return fmt.Sprintf("%s %s imports %s: %s", r.id, rel, imp, r.why)
		}
	}
	return ""
}

func TestCheck_RejectsKnownBadEdges(t *testing.T) {
	bad := []struct{ layer, rel, imp, rule string }{
		{"model", "internal/model", module + "/internal/forward", "D2"},
		{"internal", "internal/forward", module, "D3"},
		{"internal", "internal/chain", module + "/ext/monitors", "D3"},
		{"root", ".", module + "/testkit", "D4"},
		{"root", ".", "github.com/lib/pq", "D7"},
		{"ext", "ext/monitors", module + "/internal/chain", "D5"},
		{"cmd", "cmd/tollgated", module + "/internal/forward", "D5"},
		{"testkit", "testkit", module + "/internal/model", "D6"},
	}
	for _, b := range bad {
		got := check(b.layer, b.rel, b.imp)
		if !strings.HasPrefix(got, b.rule+" ") {
			t.Errorf("check(%s, %s) = %q, want a %s violation", b.rel, b.imp, got, b.rule)
		}
	}
	good := []struct{ layer, rel, imp string }{
		{"root", ".", module + "/internal/model"},
		{"internal", "internal/chain", module + "/internal/model"},
		{"ext", "ext/monitors", module},
		{"cmd", "cmd/tollgated", module + "/ext/monitors"},
		{"testkit", "testkit", "net/http/httptest"},
	}
	for _, g := range good {
		if got := check(g.layer, g.rel, g.imp); got != "" {
			t.Errorf("check(%s, %s) = %q, want allowed", g.rel, g.imp, got)
		}
	}
}

type goPackage struct {
	ImportPath   string
	Doc          string   // package doc synopsis; empty means no package comment
	Imports      []string // non-test imports
	TestImports  []string // imports of the package's in-package _test.go files
	XTestImports []string // imports of its external (package x_test) test files
}

// listPackages lists the root module's packages (nested modules excluded).
func listPackages(t *testing.T) []goPackage { return listIn(t, moduleRoot(t)) }

// listIn runs `go list -json ./...` in dir: one module's packages.
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

func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	return filepath.Dir(strings.TrimSpace(string(out)))
}

func relPath(importPath string) string {
	if importPath == module {
		return "."
	}
	return strings.TrimPrefix(importPath, module+"/")
}

// isStd reports a standard-library import: its first element has no dot.
func isStd(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

func isRoot(imp string) bool     { return imp == module }
func isInternal(imp string) bool { return strings.HasPrefix(imp, module+"/internal/") }

func publicOnly(imp string) bool {
	return isStd(imp) || (strings.HasPrefix(imp, module) && !isInternal(imp))
}

func either(fs ...func(string) bool) func(string) bool {
	return func(imp string) bool {
		for _, f := range fs {
			if f(imp) {
				return true
			}
		}
		return false
	}
}

func allowedThirdParty(l, imp string) bool { return underAny(imp, thirdParty[l]) }

// underAny reports whether imp is one of prefixes or a package below one.
func underAny(imp string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
			return true
		}
	}
	return false
}
