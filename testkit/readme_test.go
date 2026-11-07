package testkit_test

import (
	"os"
	"strings"
	"testing"
)

// readmeExample returns the first go code block after heading in README.md.
func readmeExample(t *testing.T, heading string) string {
	t.Helper()
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(readme), "\n"+heading+"\n")
	if !ok {
		t.Fatalf("README.md has no %q section", heading)
	}
	_, code, ok := strings.Cut(section, "```go\n")
	if !ok {
		t.Fatalf("README.md %q has no go code block", heading)
	}
	code, _, ok = strings.Cut(code, "```\n")
	if !ok {
		t.Fatalf("README.md %q: unterminated code block", heading)
	}
	return code
}

// TestReadme_ExampleIsThisFile ties the README's example to
// readme_example_test.go, where it compiles and TestAuditHook runs it.
func TestReadme_ExampleIsThisFile(t *testing.T) {
	block := readmeExample(t, "## Testing your extension")
	src, err := os.ReadFile("readme_example_test.go")
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := strings.Cut(string(src), "package testkit_test\n\n")
	if body != block {
		t.Fatalf("README.md's example and readme_example_test.go differ: make them equal\nREADME:\n%s\nfile:\n%s", block, body)
	}
	if !strings.Contains(block, "func TestAuditHook(t *testing.T)") {
		t.Fatal("the README example no longer holds TestAuditHook")
	}
}
