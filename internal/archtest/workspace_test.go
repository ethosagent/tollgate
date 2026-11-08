package archtest

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestWorkspace checks that the committed go.work (ADR 0017) uses exactly the
// root module and every nested module, and nothing else (tools/ stays out: it
// is run with -modfile). A nested module missing from go.work would build
// against the published root release instead of the checkout.
func TestWorkspace(t *testing.T) {
	root := moduleRoot(t)
	cmd := exec.Command("go", "work", "edit", "-json")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go work edit -json in %s: %v (is go.work missing? ADR 0017)", root, err)
	}
	got, err := workspaceUses(out)
	if err != nil {
		t.Fatalf("decode go.work: %v", err)
	}
	want := append([]string{"."}, nestedModuleDirs(t, root)...)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("go.work uses %q, want %q: run `go work use` for each module (ADR 0017)", got, want)
	}
}

// workspaceUses returns the sorted, slash-separated, "./"-free use paths of a
// `go work edit -json` document.
func workspaceUses(doc []byte) ([]string, error) {
	var w struct{ Use []struct{ DiskPath string } }
	if err := json.Unmarshal(doc, &w); err != nil {
		return nil, err
	}
	var uses []string
	for _, u := range w.Use {
		uses = append(uses, strings.TrimPrefix(filepath.ToSlash(filepath.Clean(u.DiskPath)), "./"))
	}
	slices.Sort(uses)
	return uses, nil
}

func TestWorkspaceUses_Normalises(t *testing.T) {
	doc := []byte(`{"Use":[{"DiskPath":"./ext/metrics/postgres"},{"DiskPath":"."},{"DiskPath":"./cmd/tollgated/"}]}`)
	got, err := workspaceUses(doc)
	want := []string{".", "cmd/tollgated", "ext/metrics/postgres"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("workspaceUses = %q, %v; want %q", got, err, want)
	}
}

// TestNestedModules_ShellAndGoAgree pins the two copies of the "what is a
// nested module" rule together: scripts/lib.sh nested_modules (each_module,
// check-release.sh) and nestedModuleDirs (archtest, TestWorkspace). If one
// changes alone, the gates would check different module sets.
func TestNestedModules_ShellAndGoAgree(t *testing.T) {
	root := moduleRoot(t)
	cmd := exec.Command("bash", "-c", ". scripts/lib.sh && nested_modules")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("scripts/lib.sh nested_modules: %v", err)
	}
	shell := strings.Fields(string(out))
	slices.Sort(shell)
	if goDirs := nestedModuleDirs(t, root); !slices.Equal(shell, goDirs) {
		t.Errorf("scripts/lib.sh nested_modules = %q, archtest nestedModuleDirs = %q: change both rules together", shell, goDirs)
	}
}
