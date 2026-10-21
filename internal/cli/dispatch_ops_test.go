package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDispatchHelpAndServiceContract(t *testing.T) {
	useTestConfig(t)
	stdout, stderr, err := executeCLI(t, "", "dispatch", "--help")
	if err != nil {
		t.Fatalf("help %v\n%s%s", err, stdout, stderr)
	}
	help := stdout + stderr
	for _, want := range []string{
		"fu-d3,fu-d7,ooo,fu=",
		"--lock-file",
		"--gap-from-history",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %s\n%s", want, help)
		}
	}
	root := filepath.Join("..", "..")
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "fu-d3,fu-d7,ooo,fu=") || !strings.Contains(string(readme), `"fu-d3", "fu-d7", "ooo", "fu"`) {
		t.Fatal("README follow-up hold steps omit fu")
	}
	service, err := os.ReadFile(filepath.Join(root, "scripts", "mailsend-dispatch.service"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(service), "MAILSEND_SLOT=") {
		t.Fatalf("unit sets MAILSEND_SLOT:\n%s", service)
	}
	if !strings.Contains(string(service), "fu-d3,fu-d7,ooo,fu=") {
		t.Fatalf("unit hold-step:\n%s", service)
	}
}
