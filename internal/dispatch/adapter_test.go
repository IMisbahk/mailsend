package dispatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func envWithout(keys ...string) []string {
	skip := map[string]bool{}
	for _, key := range keys {
		skip[key] = true
	}
	var out []string
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if skip[name] {
			continue
		}
		out = append(out, value)
	}
	return out
}

func TestPresendHookAdapter(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "mailsend-presend-hook")
	if _, err := os.Stat(script); err != nil {
		script = filepath.Join("scripts", "mailsend-presend-hook")
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}
	precheck := filepath.Join(t.TempDir(), "precheck.py")
	logPath := filepath.Join(t.TempDir(), "precheck.log")
	program := "import os, sys\nopen(os.environ['PRECHECK_LOG'], 'a').write(' '.join(sys.argv)+'\\n')\nsys.exit(1 if 'block@' in ' '.join(sys.argv) else 0)\n"
	if err := os.WriteFile(precheck, []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	run := func(env []string, payload string) (int, string) {
		t.Helper()
		cmd := exec.Command(script)
		cmd.Env = env
		cmd.Stdin = strings.NewReader(payload)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return 0, string(out)
		}
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		return exitErr.ExitCode(), string(out)
	}
	code, output := run(envWithout("MAILSEND_PRECHECK"), `{"email":"a@example.com","step":"first"}`)
	if code != 1 || !strings.Contains(output, "MAILSEND_PRECHECK") {
		t.Fatalf("missing checker code %d output %s", code, output)
	}
	base := append(envWithout("MAILSEND_PRECHECK", "PRECHECK_LOG", "MAILSEND_SLOT", "MAILSEND_PRECHECK_NO_GMAIL"), "MAILSEND_PRECHECK="+precheck, "PRECHECK_LOG="+logPath, "MAILSEND_SLOT=india")
	code, output = run(base, `{"email":"block@example.com","subject":"Hi","step":"first","label":"India"}`)
	if code != 1 {
		t.Fatalf("block code %d output %s", code, output)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "check block@example.com") || !strings.Contains(string(logged), "--reserve") || !strings.Contains(string(logged), "--kind first") {
		t.Fatalf("check invocation %s", logged)
	}
	code, output = run(base, `{"email":"a@example.com","subject":"Hi","step":"fu-d3","message_id":"mid-1","label":"India"}`)
	if code != 0 {
		t.Fatalf("confirm code %d output %s", code, output)
	}
	logged, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "confirm a@example.com") || !strings.Contains(string(logged), "--message-id mid-1") {
		t.Fatalf("confirm invocation %s", logged)
	}
	noGmailLog := filepath.Join(t.TempDir(), "nogmail.log")
	noGmail := append(envWithout("MAILSEND_PRECHECK", "PRECHECK_LOG", "MAILSEND_SLOT", "MAILSEND_PRECHECK_NO_GMAIL"), "MAILSEND_PRECHECK="+precheck, "PRECHECK_LOG="+noGmailLog, "MAILSEND_PRECHECK_NO_GMAIL=1")
	code, output = run(noGmail, `{"email":"a@example.com","subject":"Hi","step":"first","label":"India"}`)
	if code != 0 {
		t.Fatalf("no-gmail check code %d output %s", code, output)
	}
	code, output = run(noGmail, `{"email":"a@example.com","subject":"Hi","step":"fu","message_id":"mid-2","label":"India"}`)
	if code != 0 {
		t.Fatalf("no-gmail confirm code %d output %s", code, output)
	}
	logged, err = os.ReadFile(noGmailLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], " check ") || !strings.Contains(lines[0], "--no-gmail") {
		t.Fatalf("check should pass --no-gmail: %s", logged)
	}
	if !strings.Contains(lines[1], "confirm a@example.com") || strings.Contains(lines[1], "--no-gmail") {
		t.Fatalf("confirm must not pass --no-gmail: %s", logged)
	}
}
