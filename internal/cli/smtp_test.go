package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IMisbahk/mailsend/internal/auth"
	mailmsg "github.com/IMisbahk/mailsend/internal/mail"
	"github.com/IMisbahk/mailsend/internal/smtpfake"
	"github.com/IMisbahk/mailsend/internal/store"
	"github.com/zalando/go-keyring"
)

func useTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MAILSEND_CONFIG_DIR", dir)
	t.Setenv("MAILSEND_SMTP_PASSWORD", "")
	t.Setenv("MAILSEND_SMTP_CA_FILE", "")
	keyring.MockInit()
	return dir
}

func executeCLI(t *testing.T, in string, args ...string) (string, string, error) {
	t.Helper()
	outBuf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	previousOut := commandStdout
	previousErr := commandStderr
	commandStdout = outBuf
	commandStderr = errBuf
	t.Cleanup(func() {
		commandStdout = previousOut
		commandStderr = previousErr
	})
	root := newRoot()
	if in != "" {
		root.SetIn(strings.NewReader(in))
	}
	root.SetOut(outBuf)
	root.SetErr(errBuf)
	root.SetArgs(args)
	err := root.Execute()
	return outBuf.String(), errBuf.String(), err
}

func writeCA(t *testing.T, pemBytes []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pemBytes, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILSEND_SMTP_CA_FILE", path)
	return path
}

func addSMTPAccount(t *testing.T, server *smtpfake.Server, password string) {
	t.Helper()
	t.Setenv("MAILSEND_SMTP_PASSWORD", password)
	_, _, err := executeCLI(t, "", "account", "add", "agency",
		"--provider", "smtp",
		"--host", server.Host(),
		"--port", strconv.Itoa(server.Port()),
		"--tls", "starttls",
		"--username", "misbah@sunbeam.agency",
		"--from", "misbah@sunbeam.agency",
		"--reply-to", "replies@sunbeam.agency",
		"--json")
	if err != nil {
		t.Fatal(err)
	}
}

func TestAccountAddRejectsPasswordFlagAndStoresSecret(t *testing.T) {
	dir := useTestConfig(t)
	_, _, err := executeCLI(t, "", "account", "add", "agency", "--provider", "smtp", "--password", "secret", "--host", "mail.example.com", "--from", "misbah@sunbeam.agency", "--username", "misbah")
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --password") {
		t.Fatalf("password flag error %v", err)
	}
	_, _, err = executeCLI(t, "", "account", "add", "agency", "--provider", "smtp", "--host", "mail.example.com", "--username", "misbah", "--from", "misbah@sunbeam.agency")
	if err == nil || !strings.Contains(err.Error(), "MAILSEND_SMTP_PASSWORD") {
		t.Fatalf("missing password error %v", err)
	}
	stdout, _, err := executeCLI(t, "stdin-secret\n", "account", "add", "agency", "--provider", "smtp", "--host", "mail.example.com", "--username", "misbah@sunbeam.agency", "--from", "misbah@sunbeam.agency", "--password-stdin", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"status": "added"`) || strings.Contains(stdout, "stdin-secret") {
		t.Fatalf("stdout %s", stdout)
	}
	body, err := os.ReadFile(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "stdin-secret") || strings.Contains(string(body), "password") {
		t.Fatalf("config leaked the password: %s", body)
	}
	if got, err := auth.SMTPPassword("agency"); err != nil || got != "stdin-secret" {
		t.Fatalf("keychain %q %v", got, err)
	}
}

func TestLegacySunbeamAccountIsStillGmail(t *testing.T) {
	dir := useTestConfig(t)
	original := []byte("{\n  \"default\": \"sunbeam\",\n  \"accounts\": [\"sunbeam\"],\n  \"emails\": {\n    \"sunbeam\": \"misbah@sunbeam.agency\"\n  }\n}\n")
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), original, 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := executeCLI(t, "", "quota", "--account", "sunbeam", "--json")
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("gmail quota should still require login, got %v", err)
	}
	_, _, err = executeCLI(t, "", "account", "add", "sunbeam", "--provider", "smtp", "--host", "mail.example.com", "--username", "misbah", "--from", "misbah@sunbeam.agency")
	t.Setenv("MAILSEND_SMTP_PASSWORD", "secret")
	_, _, err = executeCLI(t, "", "account", "add", "sunbeam", "--provider", "smtp", "--host", "mail.example.com", "--username", "misbah", "--from", "misbah@sunbeam.agency")
	if err == nil || !strings.Contains(err.Error(), "Gmail") {
		t.Fatalf("overwrite error %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("sunbeam manifest changed:\n%s", got)
	}
}

func TestSMTPSendHistoryQuotaAndErrors(t *testing.T) {
	dir := useTestConfig(t)
	server, err := smtpfake.Listen(smtpfake.Options{Mode: smtpfake.ModeStartTLS, Username: "misbah@sunbeam.agency", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	writeCA(t, server.Certificate)
	addSMTPAccount(t, server, "secret")

	stdout, _, err := executeCLI(t, "", "account", "--account", "agency", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "secret") || !strings.Contains(stdout, `"password_stored": true`) || !strings.Contains(stdout, server.Host()) {
		t.Fatalf("account json %s", stdout)
	}
	stdout, _, err = executeCLI(t, "", "quota", "--account", "agency", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var quota map[string]any
	if err = json.Unmarshal([]byte(stdout), &quota); err != nil {
		t.Fatal(err)
	}
	if quota["provider"] != "smtp" || quota["quota_available"] != false || quota["remaining_sends"] != nil {
		t.Fatalf("quota %#v", quota)
	}
	if !strings.Contains(quota["message"].(string), "daily-limit") {
		t.Fatalf("quota message %v", quota["message"])
	}

	before := server.Connections.Load()
	stdout, _, err = executeCLI(t, "", "send", "--account", "agency", "--to", "alex@example.com", "--subject", "Preview", "--body", "Preview", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"status": "dry_run"`) || !strings.Contains(stdout, "Preview") {
		t.Fatalf("dry-run %s", stdout)
	}
	if server.Connections.Load() != before {
		t.Fatalf("dry-run connected to SMTP (%d -> %d)", before, server.Connections.Load())
	}

	_, _, err = executeCLI(t, "", "account", "test", "agency", "--json")
	if err != nil {
		t.Fatal(err)
	}
	stdout, _, err = executeCLI(t, "", "send", "--account", "agency", "--to", "alex@example.com", "--cc", "cc@example.com", "--bcc", "hidden@example.com", "--subject", "Hello", "--body", "Hi there", "--html-file", writeFile(t, "<b>Hi</b>"), "--from-name", "Misbah", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err = json.Unmarshal([]byte(stdout), &sent); err != nil {
		t.Fatalf("sent json %s: %v", stdout, err)
	}
	if sent["status"] != "sent" || !strings.Contains(sent["message_id"].(string), "@sunbeam.agency") {
		t.Fatalf("sent %#v", sent)
	}
	submission, err := server.WaitSubmission(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if submission.MailFrom != "misbah@sunbeam.agency" {
		t.Fatalf("envelope %q", submission.MailFrom)
	}
	body := string(submission.Data)
	if strings.Contains(body, "Bcc:") || strings.Contains(body, "hidden@example.com") {
		t.Fatalf("bcc header leaked:\n%s", body)
	}
	foundHidden := false
	for _, recipient := range submission.Recipients {
		if recipient == "hidden@example.com" {
			foundHidden = true
		}
	}
	if !foundHidden {
		t.Fatalf("bcc missing from envelope %#v", submission.Recipients)
	}
	if !strings.Contains(body, "Misbah") || !strings.Contains(body, "Hi there") || !strings.Contains(body, "<b>Hi</b>") || !strings.Contains(body, "replies@sunbeam.agency") {
		t.Fatalf("message body:\n%s", body)
	}
	db, err := store.Open(filepath.Join(dir, "mailsend.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	history, err := db.History("", 10, "agency")
	if err != nil || len(history) != 1 || history[0].Status != "sent" || history[0].MessageID != sent["message_id"] {
		t.Fatalf("history %#v %v", history, err)
	}

	bad, err := smtpfake.Listen(smtpfake.Options{Mode: smtpfake.ModeStartTLS, Username: "misbah@sunbeam.agency", Password: "secret", FailStage: "data", FailCode: 550, FailMessage: "user unknown"})
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	writeCA(t, bad.Certificate)
	t.Setenv("MAILSEND_SMTP_PASSWORD", "secret")
	if _, err = auth.SaveSMTPAccount("bounce", auth.ProviderConfig{Host: bad.Host(), Port: bad.Port(), TLS: "starttls", Username: "misbah@sunbeam.agency", From: "misbah@sunbeam.agency"}, "secret"); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := executeCLI(t, "", "--json", "send", "--account", "bounce", "--to", "alex@example.com", "--subject", "Nope", "--body", "Hi")
	if err == nil || !strings.Contains(err.Error(), "550") || !strings.Contains(err.Error(), "permanent") {
		t.Fatalf("550 error %v", err)
	}
	previous := commandStderr
	commandStderr = &bytes.Buffer{}
	global.json = true
	printCommandError(err)
	encoded := commandStderr.(*bytes.Buffer).String()
	commandStderr = previous
	global.json = false
	var payload map[string]any
	if jsonErr := json.Unmarshal([]byte(encoded), &payload); jsonErr != nil {
		t.Fatalf("json error %s: %v", encoded, jsonErr)
	}
	details := payload["error"].(map[string]any)
	if details["type"] != "smtp_error" || details["smtp_code"] != float64(550) || details["temporary"] != false || details["permanent"] != true {
		t.Fatalf("json details %#v stderr %s", details, stderr)
	}
}

func TestSMTPRetryThenSuccess(t *testing.T) {
	useTestConfig(t)
	server, err := smtpfake.Listen(smtpfake.Options{Mode: smtpfake.ModeNone, Username: "misbah@sunbeam.agency", Password: "secret", FailRcpts: 1, FailCode: 450, FailMessage: "mailbox busy"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	t.Setenv("MAILSEND_SMTP_PASSWORD", "secret")
	if _, err = auth.SaveSMTPAccount("agency", auth.ProviderConfig{Host: server.Host(), Port: server.Port(), TLS: "none", Username: "misbah@sunbeam.agency", From: "misbah@sunbeam.agency"}, "secret"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := executeCLI(t, "", "send", "--account", "agency", "--to", "alex@example.com", "--subject", "Retry", "--body", "Hi", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"status": "sent"`) {
		t.Fatalf("retry send %s", stdout)
	}
	if _, err = server.WaitSubmission(time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestGmailSenderStillPostsEncodedRaw(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		got = body["raw"]
		if _, err := w.Write([]byte(`{"id":"gmail-1"}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	sender := gmailSender{client: &auth.Client{HTTP: server.Client(), BaseURL: server.URL, Account: auth.Account{Email: "misbah@sunbeam.agency"}}}
	id, ambiguous, err := sender.Send(context.Background(), mailmsg.BuiltMessage{Raw: "encoded-body"})
	if err != nil || ambiguous || id != "gmail-1" || got != "encoded-body" {
		t.Fatalf("gmail send id %q raw %q ambiguous %v err %v", id, got, ambiguous, err)
	}
	if sender.Provider() != auth.ProviderGmail || sender.FromAddress() != "misbah@sunbeam.agency" {
		t.Fatalf("gmail sender identity %s %s", sender.Provider(), sender.FromAddress())
	}
}

func TestGmailDryRunPlainRFC822(t *testing.T) {
	dir := useTestConfig(t)
	manifest := []byte("{\n  \"default\": \"sunbeam\",\n  \"accounts\": [\"sunbeam\"],\n  \"emails\": {\n    \"sunbeam\": \"misbah@sunbeam.agency\"\n  }\n}\n")
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	built, err := composeSendMessage("sunbeam", mailmsg.Message{To: []string{"alex@example.com"}, Subject: "Hello", Text: "Hi from mailsend"})
	if err != nil {
		t.Fatal(err)
	}
	assertPlainRFC822(t, built.MIME, "Hi from mailsend")
	t.Logf("RFC822:\n%s", built.MIME)
	if strings.Contains(string(built.MIME), "\r\n\r\nContent-Type:") {
		t.Fatalf("content headers are outside the header block:\n%s", built.MIME)
	}
	stdout, stderr, err := executeCLI(t, "", "send", "--account", "sunbeam", "--to", "alex@example.com", "--subject", "Hello", "--body", "Hi from mailsend", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run error %v stderr %s", err, stderr)
	}
	if strings.Contains(stdout, "Sent to") || strings.Contains(stderr, "Sent to") {
		t.Fatalf("dry-run sent mail stdout=%q stderr=%q", stdout, stderr)
	}
	db, err := store.Open(filepath.Join(dir, "mailsend.sqlite3"))
	if err == nil {
		defer db.Close()
		history, histErr := db.History("", 10, "sunbeam")
		if histErr != nil {
			t.Fatal(histErr)
		}
		if len(history) != 0 {
			t.Fatalf("dry-run wrote history %#v", history)
		}
	}
}

func TestPlainBodyFileSendKeepsHeadersOutOfTheBody(t *testing.T) {
	useTestConfig(t)
	server, err := smtpfake.Listen(smtpfake.Options{Mode: smtpfake.ModeNone, Username: "misbah@sunbeam.agency", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	writeCA(t, server.Certificate)
	if _, err = auth.SaveSMTPAccount("agency", auth.ProviderConfig{Host: server.Host(), Port: server.Port(), TLS: "none", Username: "misbah@sunbeam.agency", From: "misbah@sunbeam.agency"}, "secret"); err != nil {
		t.Fatal(err)
	}
	bodyPath := filepath.Join(t.TempDir(), "note.txt")
	if err = os.WriteFile(bodyPath, []byte("Hello from a body file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = executeCLI(t, "", "send", "--account", "agency", "--to", "alex@example.com", "--subject", "Hello", "--body-file", bodyPath); err != nil {
		t.Fatal(err)
	}
	submission, err := server.WaitSubmission(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertPlainRFC822(t, submission.Data, "Hello from a body file")

	var posted string
	gmail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		posted = body["raw"]
		_, _ = w.Write([]byte(`{"id":"gmail-1"}`))
	}))
	defer gmail.Close()
	built, err := (mailmsg.Message{To: []string{"alex@example.com"}, Subject: "Hello", Text: "Hello from a body file\n", Account: "misbah@sunbeam.agency"}).Build()
	if err != nil {
		t.Fatal(err)
	}
	sender := gmailSender{client: &auth.Client{HTTP: gmail.Client(), BaseURL: gmail.URL, Account: auth.Account{Email: "misbah@sunbeam.agency"}}}
	if _, _, err = sender.Send(context.Background(), built); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(posted)
	if err != nil {
		t.Fatal(err)
	}
	assertPlainRFC822(t, decoded, "Hello from a body file")
}

func assertPlainRFC822(t *testing.T, raw []byte, snippet string) {
	t.Helper()
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(parsed.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("content type %q\n%s", parsed.Header.Get("Content-Type"), raw)
	}
	body, err := io.ReadAll(parsed.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "Content-Type:") || strings.Contains(text, "Content-Transfer-Encoding:") {
		t.Fatalf("header lines leaked into the body:\n%s", text)
	}
	if !strings.Contains(text, snippet) {
		t.Fatalf("body %q", text)
	}
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "body.html")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
