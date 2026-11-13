package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/example-account/mailsend/internal/smtpfake"
)

func TestQueueAddListCancelAndResolve(t *testing.T) {
	useTestConfig(t)
	file := filepath.Join(t.TempDir(), "india.json")
	body := []byte(`{"label":"India","start":"2026-10-21T09:00:00Z","interval":"5m","timezone":"Asia/Kolkata","emails":[{"to":"a@example.com","subject":"Hi","body":"Hello","step":"first"},{"to":"b@example.com","subject":"Next","body":"Hello","step":"fu-d3"}]}`)
	if err := os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeCLI(t, "", "--account", "acme", "queue", "add", "--file", file, "--json")
	if err != nil {
		t.Fatalf("add %v %s", err, stderr)
	}
	var added map[string]any
	if err = json.Unmarshal([]byte(stdout), &added); err != nil {
		t.Fatal(err)
	}
	if added["status"] != "queued" || added["count"] != float64(2) || added["account"] != "acme" {
		t.Fatalf("added %#v", added)
	}
	batchID, _ := added["batch_id"].(string)
	stdout, stderr, err = executeCLI(t, "", "--account", "acme", "queue", "list", "--json")
	if err != nil {
		t.Fatalf("list %v %s", err, stderr)
	}
	if !strings.Contains(stdout, batchID) || !strings.Contains(stdout, `"pending": 2`) {
		t.Fatalf("list %s", stdout)
	}
	stdout, stderr, err = executeCLI(t, "", "--account", "acme", "queue", "status", batchID, "--json")
	if err != nil {
		t.Fatalf("status %v %s", err, stderr)
	}
	var status struct {
		Items []struct {
			ID     int64  `json:"id"`
			Email  string `json:"email"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err = json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("status json %s", stdout)
	}
	if len(status.Items) != 2 {
		t.Fatalf("items %#v", status.Items)
	}
	stdout, stderr, err = executeCLI(t, "", "queue", "resolve", "not-a-number", "--drop")
	if err == nil {
		t.Fatal("expected a bad item id to fail")
	}
	stdout, stderr, err = executeCLI(t, "", "--account", "acme", "queue", "resolve", strconv.FormatInt(status.Items[1].ID, 10), "--drop", "--reason", "not this one", "--json")
	if err != nil {
		t.Fatalf("resolve %v %s %s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, `"status": "cancelled"`) {
		t.Fatalf("resolve %s", stdout)
	}
	stdout, stderr, err = executeCLI(t, "", "--account", "acme", "queue", "cancel", batchID, "--json")
	if err != nil {
		t.Fatalf("cancel %v %s", err, stderr)
	}
	if !strings.Contains(stdout, `"cancelled_items": 1`) {
		t.Fatalf("cancel %s", stdout)
	}
}

func TestDispatchOnceUsesHookAndRecordsHistory(t *testing.T) {
	useTestConfig(t)
	server, err := smtpfake.Listen(smtpfake.Options{Username: "you@example.com", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	writeCA(t, server.Certificate)
	addSMTPAccount(t, server, "secret")
	dir := t.TempDir()
	allow := filepath.Join(dir, "allow.sh")
	if err = os.WriteFile(allow, []byte("#!/bin/sh\ncat >/dev/null\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(dir, "block.sh")
	if err = os.WriteFile(block, []byte("#!/bin/sh\ncat >/dev/null\necho blocked by hook >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "batch.json")
	if err = os.WriteFile(file, []byte(`{"label":"India","start":"2020-01-01T00:00:00Z","interval":"0s","emails":[{"to":"a@example.com","subject":"Queued","body":"Hello from the queue","step":"first"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := executeCLI(t, "", "--account", "agency", "queue", "add", "--file", file, "--json"); err != nil {
		t.Fatalf("add %v %s", err, stderr)
	}
	stdout, stderr, err := executeCLI(t, "", "--account", "agency", "--json", "dispatch", "--once", "--interval", "0s", "--hook", block)
	if err != nil {
		t.Fatalf("blocked dispatch %v %s %s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, `"blocked_this_run": 1`) || !strings.Contains(stdout, `"sent_this_run": 0`) {
		t.Fatalf("blocked dispatch %s", stdout)
	}
	if _, err = server.WaitSubmission(200 * time.Millisecond); err == nil {
		t.Fatal("hook block still sent mail")
	}
	file = filepath.Join(dir, "second.json")
	if err = os.WriteFile(file, []byte(`{"label":"India","start":"2020-01-01T00:00:00Z","interval":"0s","emails":[{"to":"b@example.com","subject":"Queued","body":"Hello from the queue","step":"first"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err = executeCLI(t, "", "--account", "agency", "queue", "add", "--file", file, "--json"); err != nil {
		t.Fatalf("add second %v %s", err, stderr)
	}
	stdout, stderr, err = executeCLI(t, "", "--account", "agency", "--json", "dispatch", "--once", "--interval", "0s", "--daily-cap", "10", "--timezone", "Asia/Kolkata", "--hook", allow)
	if err != nil {
		t.Fatalf("dispatch %v %s %s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, `"sent_this_run": 1`) {
		t.Fatalf("dispatch %s\n%s", stdout, stderr)
	}
	submission, err := server.WaitSubmission(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(submission.Recipients) != 1 || submission.Recipients[0] != "b@example.com" {
		t.Fatalf("submission %#v", submission.Recipients)
	}
	if !strings.Contains(string(submission.Data), "Hello from the queue") {
		t.Fatalf("body %s", submission.Data)
	}
	stdout, stderr, err = executeCLI(t, "", "--account", "agency", "history", "--json")
	if err != nil {
		t.Fatalf("history %v %s", err, stderr)
	}
	if !strings.Contains(stdout, "b@example.com") || !strings.Contains(stdout, `"Status": "sent"`) {
		t.Fatalf("history %s", stdout)
	}
}
