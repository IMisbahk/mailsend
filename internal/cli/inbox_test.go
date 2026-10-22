package cli

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IMisbahk/mailsend/internal/auth"
	"github.com/IMisbahk/mailsend/internal/imapfake"
	"github.com/IMisbahk/mailsend/internal/smtpfake"
	"github.com/emersion/go-imap/v2"
)

func TestVersionAndIMAPHelp(t *testing.T) {
	useTestConfig(t)
	stdout, _, err := executeCLI(t, "", "version")
	if err != nil || strings.TrimSpace(stdout) != "0.2.0" {
		t.Fatalf("version %q %v", stdout, err)
	}
	stdout, _, err = executeCLI(t, "", "account", "set-imap", "--help")
	if err != nil || !strings.Contains(stdout, "keychain password") || !strings.Contains(stdout, "993") || !strings.Contains(stdout, "ssl") {
		t.Fatalf("set-imap help %v\n%s", err, stdout)
	}
	stdout, _, err = executeCLI(t, "", "inbox", "--help")
	if err != nil || !strings.Contains(stdout, "EXAMINE") || !strings.Contains(stdout, "BODY.PEEK") || !strings.Contains(stdout, "7d") {
		t.Fatalf("inbox help %v\n%s", err, stdout)
	}
	stdout, _, err = executeCLI(t, "", "read", "--help")
	if err != nil || !strings.Contains(stdout, "--mark-read") || !strings.Contains(stdout, "EXAMINE") {
		t.Fatalf("read help %v\n%s", err, stdout)
	}
	stdout, _, err = executeCLI(t, "", "account", "add", "--help")
	if err != nil || !strings.Contains(stdout, "--imap-host") || !strings.Contains(stdout, "set-imap") {
		t.Fatalf("add help %v\n%s", err, stdout)
	}
	stdout, _, err = executeCLI(t, "", "sent-check", "--help")
	if err != nil || !strings.Contains(stdout, "set-imap") || !strings.Contains(stdout, `\Sent`) {
		t.Fatalf("sent-check help %v\n%s", err, stdout)
	}
}

func TestSetIMAPKeepsPasswordAndLegacyAccount(t *testing.T) {
	dir := useTestConfig(t)
	original := []byte("{\n  \"default\": \"sunbeam\",\n  \"accounts\": [\"sunbeam\"],\n  \"emails\": {\n    \"sunbeam\": \"misbah@sunbeam.agency\"\n  }\n}\n")
	path := filepath.Join(dir, "accounts.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := executeCLI(t, "", "inbox", "--account", "sunbeam"); err == nil || !strings.Contains(err.Error(), "not an SMTP account") {
		t.Fatalf("inbox on gmail %v\n%s", err, stderr)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("legacy accounts.json changed:\n%s", got)
	}
	if _, _, err = executeCLI(t, "", "account", "set-imap", "sunbeam"); err == nil || !strings.Contains(err.Error(), "not an SMTP account") {
		t.Fatalf("set-imap on gmail %v", err)
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("set-imap rewrote the legacy file:\n%s", got)
	}

	const password = "mailbox-secret-value"
	t.Setenv("MAILSEND_SMTP_PASSWORD", password)
	stdout, _, err := executeCLI(t, "", "account", "add", "gobitsnbytes",
		"--provider", "smtp",
		"--host", "mail.gobitsnbytes.org",
		"--port", "587",
		"--tls", "starttls",
		"--username", "ada@gobitsnbytes.org",
		"--from", "ada@gobitsnbytes.org",
		"--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, password) || strings.Contains(stdout, `"imap"`) {
		t.Fatalf("add stdout %s", stdout)
	}
	t.Setenv("MAILSEND_SMTP_PASSWORD", "")
	stdout, stderr, err := executeCLI(t, "", "--debug", "account", "set-imap", "gobitsnbytes", "--json")
	if err != nil {
		t.Fatalf("set-imap %v\n%s", err, stderr)
	}
	if strings.Contains(stdout, password) || strings.Contains(stderr, password) {
		t.Fatalf("password leaked\nstdout %s\nstderr %s", stdout, stderr)
	}
	var enabled map[string]any
	if err = json.Unmarshal([]byte(stdout), &enabled); err != nil {
		t.Fatal(err)
	}
	if enabled["status"] != "enabled" || enabled["imap_host"] != "mail.gobitsnbytes.org" || enabled["imap_port"] != float64(993) || enabled["imap_tls"] != "ssl" || enabled["imap_username"] != "ada@gobitsnbytes.org" {
		t.Fatalf("set-imap json %#v", enabled)
	}
	if stored, err := auth.SMTPPassword("gobitsnbytes"); err != nil || stored != password {
		t.Fatalf("password %q %v", stored, err)
	}
	body, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(body), password) {
		t.Fatalf("config %s", body)
	}
	sunbeam, err := auth.LookupProvider("sunbeam")
	if err != nil || sunbeam.Provider != auth.ProviderGmail {
		t.Fatalf("sunbeam %+v %v", sunbeam, err)
	}
	if auth.DefaultAccount() != "sunbeam" {
		t.Fatalf("default became %s", auth.DefaultAccount())
	}

	t.Setenv("MAILSEND_SMTP_PASSWORD", password)
	stdout, _, err = executeCLI(t, "", "account", "add", "gobitsnbytes",
		"--provider", "smtp",
		"--host", "mail.gobitsnbytes.org",
		"--port", "587",
		"--tls", "starttls",
		"--username", "ada@gobitsnbytes.org",
		"--from", "ada@gobitsnbytes.org",
		"--reply-to", "replies@gobitsnbytes.org",
		"--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"status": "updated"`) || strings.Contains(stdout, password) {
		t.Fatalf("update %s", stdout)
	}
	saved, err := auth.LookupProvider("gobitsnbytes")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, ok := saved.IMAP()
	if !ok || endpoint.Host != "mail.gobitsnbytes.org" || endpoint.Port != 993 || endpoint.TLS != "ssl" || endpoint.Username != "ada@gobitsnbytes.org" {
		t.Fatalf("update dropped IMAP %+v", saved)
	}
}

func TestAccountTestReportsSMTPAndIMAPSeparately(t *testing.T) {
	useTestConfig(t)
	const password = "mailbox-secret-value"
	const username = "ada@gobitsnbytes.org"
	smtpServer, err := smtpfake.Listen(smtpfake.Options{Mode: smtpfake.ModeStartTLS, Username: username, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = smtpServer.Close() })
	imapServer := startCLIIMAP(t, imapfake.ModeSSL, username, password)
	writeCA(t, append(append([]byte{}, smtpServer.Certificate...), imapServer.Certificate...))
	t.Setenv("MAILSEND_SMTP_PASSWORD", password)
	if _, _, err = executeCLI(t, "", "account", "add", "gobitsnbytes",
		"--provider", "smtp",
		"--host", smtpServer.Host(),
		"--port", strconv.Itoa(smtpServer.Port()),
		"--tls", "starttls",
		"--username", username,
		"--from", username,
		"--imap-host", imapServer.Host(),
		"--imap-port", strconv.Itoa(imapServer.Port()),
		"--imap-tls", "ssl",
		"--json"); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeCLI(t, "", "--debug", "account", "test", "gobitsnbytes", "--json")
	if err != nil {
		t.Fatalf("test %v\n%s\n%s", err, stdout, stderr)
	}
	if strings.Contains(stdout, password) || strings.Contains(stderr, password) {
		t.Fatalf("password leaked\n%s\n%s", stdout, stderr)
	}
	var report map[string]any
	if err = json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	if report["status"] != "ok" || report["authenticated"] != true {
		t.Fatalf("report %#v", report)
	}
	smtpReport := report["smtp"].(map[string]any)
	imapReport := report["imap"].(map[string]any)
	if smtpReport["status"] != "ok" || imapReport["status"] != "ok" || imapReport["tls"] != "ssl" {
		t.Fatalf("sides %#v %#v", smtpReport, imapReport)
	}

	if _, _, err = executeCLI(t, "", "account", "set-imap", "gobitsnbytes", "--imap-username", "nobody@gobitsnbytes.org", "--json"); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = executeCLI(t, "", "--debug", "account", "test", "gobitsnbytes", "--send-to", "alex@example.com", "--json")
	if err == nil || !strings.Contains(err.Error(), "IMAP:") {
		t.Fatalf("expected IMAP failure, got %v\n%s", err, stdout)
	}
	if strings.Contains(stdout, password) || strings.Contains(stderr, password) || strings.Contains(err.Error(), password) {
		t.Fatalf("password leaked in failure\n%s\n%s\n%v", stdout, stderr, err)
	}
	if err = json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout %s: %v", stdout, err)
	}
	smtpReport = report["smtp"].(map[string]any)
	imapReport = report["imap"].(map[string]any)
	if report["status"] != "failed" || smtpReport["status"] != "ok" || imapReport["status"] != "failed" {
		t.Fatalf("partial failure %#v", report)
	}
	if _, waitErr := smtpServer.WaitSubmission(50 * time.Millisecond); waitErr == nil {
		t.Fatal("test message was sent after IMAP login failed")
	}
}

func TestInboxReadAndSentCheck(t *testing.T) {
	useTestConfig(t)
	const password = "mailbox-secret-value"
	const username = "ada@gobitsnbytes.org"
	server := startCLIIMAP(t, imapfake.ModeSSL, username, password)
	writeCA(t, server.Certificate)
	t.Setenv("MAILSEND_SMTP_PASSWORD", password)
	if _, _, err := executeCLI(t, "", "account", "add", "gobitsnbytes",
		"--provider", "smtp",
		"--host", "mail.gobitsnbytes.org",
		"--port", "587",
		"--tls", "starttls",
		"--username", username,
		"--from", username,
		"--imap-host", server.Host(),
		"--imap-port", strconv.Itoa(server.Port()),
		"--imap-tls", "ssl",
		"--json"); err != nil {
		t.Fatal(err)
	}
	oldWhen := time.Now().Add(-30 * 24 * time.Hour).UTC().Truncate(time.Second)
	recent := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	if _, err := server.Append("INBOX", plainCLIMessage("Older <older@example.com>", "Me <me@example.com>", "Old note", "old body", oldWhen), oldWhen, nil); err != nil {
		t.Fatal(err)
	}
	newUID, err := server.Append("INBOX", mixedCLIMessage(), recent, nil)
	if err != nil {
		t.Fatal(err)
	}
	seenUID, err := server.Append("INBOX", plainCLIMessage("Seen <seen@example.com>", "Me <me@example.com>", "Already read", "seen body", recent), recent, []imap.Flag{imap.FlagSeen})
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeCLI(t, "", "--debug", "--account", "gobitsnbytes", "inbox", "--json")
	if err != nil {
		t.Fatalf("inbox %v\n%s\n%s", err, stdout, stderr)
	}
	if strings.Contains(stdout, password) || strings.Contains(stderr, password) {
		t.Fatalf("password leaked\n%s\n%s", stdout, stderr)
	}
	var page struct {
		Folder   string `json:"folder"`
		Messages []struct {
			UID     uint32 `json:"uid"`
			From    string `json:"from"`
			Subject string `json:"subject"`
			Snippet string `json:"snippet"`
			Seen    bool   `json:"seen"`
		} `json:"messages"`
	}
	if err = json.Unmarshal([]byte(stdout), &page); err != nil {
		t.Fatal(err)
	}
	if page.Folder != "INBOX" || len(page.Messages) != 2 || page.Messages[0].UID != newUID || page.Messages[0].Seen {
		t.Fatalf("inbox page %#v", page)
	}
	if !strings.Contains(page.Messages[0].Snippet, "second message") || !strings.Contains(page.Messages[0].From, "ada@example.com") {
		t.Fatalf("summary %#v", page.Messages[0])
	}
	stdout, _, err = executeCLI(t, "", "--account", "gobitsnbytes", "inbox", "--unread", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(stdout), &page); err != nil {
		t.Fatal(err)
	}
	for _, message := range page.Messages {
		if message.UID == seenUID || message.Seen {
			t.Fatalf("unread included seen %#v", page.Messages)
		}
	}
	stdout, _, err = executeCLI(t, "", "--account", "gobitsnbytes", "inbox", "--since", "0s", "--from", "older@example.com", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(stdout), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Subject != "Old note" {
		t.Fatalf("from filter %#v", page.Messages)
	}

	stdout, stderr, err = executeCLI(t, "", "--debug", "--account", "gobitsnbytes", "read", strconv.FormatUint(uint64(newUID), 10), "--json")
	if err != nil {
		t.Fatalf("read %v\n%s\n%s", err, stdout, stderr)
	}
	if strings.Contains(stdout, password) || strings.Contains(stderr, password) || strings.Contains(stdout, "JVBERi0") {
		t.Fatalf("read leaked a secret or an attachment\n%s\n%s", stdout, stderr)
	}
	var message struct {
		UID         uint32 `json:"uid"`
		Seen        bool   `json:"seen"`
		MarkedRead  bool   `json:"marked_read"`
		Body        string `json:"body"`
		BodyType    string `json:"body_type"`
		Subject     string `json:"subject"`
		Attachments []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"attachments"`
	}
	if err = json.Unmarshal([]byte(stdout), &message); err != nil {
		t.Fatal(err)
	}
	if message.Seen || message.MarkedRead || message.BodyType != "text/plain" || !strings.Contains(message.Body, "second message") || message.Subject != "Newer" {
		t.Fatalf("message %#v", message)
	}
	if len(message.Attachments) != 1 || message.Attachments[0].Name != "report.pdf" || message.Attachments[0].Size == 0 {
		t.Fatalf("attachments %#v", message.Attachments)
	}
	stdout, _, err = executeCLI(t, "", "--account", "gobitsnbytes", "read", strconv.FormatUint(uint64(newUID), 10), "--mark-read", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(stdout), &message); err != nil {
		t.Fatal(err)
	}
	if !message.MarkedRead || !message.Seen {
		t.Fatalf("mark-read %#v", message)
	}

	if _, _, err = executeCLI(t, "", "--account", "gobitsnbytes", "sent-check", "--to", "ada@example.com", "--json"); err == nil || !strings.Contains(err.Error(), "Sent folder") {
		t.Fatalf("missing sent %v", err)
	}
	var status *statusExit
	if !errors.As(err, &status) || status.code != 2 {
		t.Fatalf("missing sent status %#v", status)
	}
	if err = server.CreateMailbox("Sent"); err != nil {
		t.Fatal(err)
	}
	sentWhen := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	helloUID, err := server.Append("Sent", plainCLIMessage("Me <"+username+">", "Ada <ada@example.com>", "Hello", "sent body", sentWhen), sentWhen, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.Append("Sent", plainCLIMessage("Me <"+username+">", "Ada <ada@example.com>", "Hello extra", "other", sentWhen), sentWhen, nil); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = executeCLI(t, "", "--account", "gobitsnbytes", "sent-check", "--to", "ada@example.com", "--subject", "Hello", "--json")
	if !errors.As(err, &status) || status.code != 3 {
		t.Fatalf("match status %v stdout %s", err, stdout)
	}
	var sent struct {
		Messages []struct {
			ID      string `json:"id"`
			To      string `json:"to"`
			Subject string `json:"subject"`
		} `json:"messages"`
	}
	if jsonErr := json.Unmarshal([]byte(stdout), &sent); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if len(sent.Messages) != 1 || sent.Messages[0].ID != strconv.FormatUint(uint64(helloUID), 10) || sent.Messages[0].To != "ada@example.com" || sent.Messages[0].Subject != "Hello" {
		t.Fatalf("sent %#v", sent.Messages)
	}
	stdout, _, err = executeCLI(t, "", "--account", "gobitsnbytes", "sent-check", "--to", "nobody@example.com", "--json")
	if err != nil {
		t.Fatalf("empty sent %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, `"messages": []`) {
		t.Fatalf("empty json %s", stdout)
	}
}

func TestSentCheckWithoutIMAPAndHungServer(t *testing.T) {
	useTestConfig(t)
	const password = "mailbox-secret-value"
	t.Setenv("MAILSEND_SMTP_PASSWORD", password)
	if _, _, err := executeCLI(t, "", "account", "add", "gobitsnbytes",
		"--provider", "smtp",
		"--host", "mail.gobitsnbytes.org",
		"--port", "587",
		"--tls", "starttls",
		"--username", "ada@gobitsnbytes.org",
		"--from", "ada@gobitsnbytes.org",
		"--json"); err != nil {
		t.Fatal(err)
	}
	_, _, err := executeCLI(t, "", "--account", "gobitsnbytes", "sent-check", "--to", "ada@example.com", "--json")
	var status *statusExit
	if !errors.As(err, &status) || status.code != 2 || !strings.Contains(err.Error(), "set-imap") {
		t.Fatalf("smtp without imap %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = executeCLI(t, "", "account", "set-imap", "gobitsnbytes", "--imap-host", host, "--imap-port", portText, "--imap-tls", "none"); err != nil {
		t.Fatal(err)
	}
	previous := sentCheckTimeout
	sentCheckTimeout = 250 * time.Millisecond
	t.Cleanup(func() { sentCheckTimeout = previous })
	started := time.Now()
	_, _, err = executeCLI(t, "", "--account", "gobitsnbytes", "sent-check", "--to", "ada@example.com", "--json")
	if time.Since(started) > 3*time.Second {
		t.Fatalf("sent-check waited %s", time.Since(started))
	}
	if !errors.As(err, &status) || status.code != 2 || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("hung imap %v", err)
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("timeout error included the password: %v", err)
	}
}

func startCLIIMAP(t *testing.T, mode, username, password string) *imapfake.Server {
	t.Helper()
	server, err := imapfake.Listen(imapfake.Options{Mode: mode, Username: username, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func plainCLIMessage(from, to, subject, body string, when time.Time) []byte {
	raw := "From: " + from + "\r\nTo: " + to + "\r\nSubject: " + subject + "\r\nDate: " + when.Format(time.RFC1123Z) + "\r\nMessage-ID: <" + subject + "@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n"
	return []byte(raw)
}

func mixedCLIMessage() []byte {
	return []byte(strings.Join([]string{
		"From: Ada <ada@example.com>",
		"To: me@example.com",
		"Subject: Newer",
		"Date: Thu, 22 Oct 2026 12:00:00 +0000",
		"Message-ID: <newer@example.com>",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="bnd"`,
		"",
		"--bnd",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"second message with a useful snippet",
		"--bnd",
		`Content-Type: application/pdf; name="report.pdf"`,
		`Content-Disposition: attachment; filename="report.pdf"`,
		"Content-Transfer-Encoding: base64",
		"",
		"JVBERi0x",
		"--bnd--",
		"",
	}, "\r\n"))
}
