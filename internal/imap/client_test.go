package imap

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IMisbahk/mailsend/internal/imapfake"
	"github.com/emersion/go-imap/v2"
)

func TestStripHTMLRemovesScripts(t *testing.T) {
	got := strings.TrimSpace(stripHTML("<html><body><p>Hello <b>there</b></p><script>secret()</script></body></html>"))
	if strings.Contains(got, "secret") || strings.Contains(got, "<") {
		t.Fatalf("stripped %q", got)
	}
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "there") {
		t.Fatalf("stripped %q", got)
	}
}

func TestChooseSentMailboxOrder(t *testing.T) {
	sentAttr := &imap.ListData{Mailbox: "Gesendet", Attrs: []imap.MailboxAttr{imap.MailboxAttrSent}}
	plain := &imap.ListData{Mailbox: "Sent"}
	got, err := chooseSentMailbox([]*imap.ListData{plain, sentAttr})
	if err != nil || got != "Gesendet" {
		t.Fatalf("special-use mailbox %q %v", got, err)
	}
	got, err = chooseSentMailbox([]*imap.ListData{
		{Mailbox: "Sent Items"},
		{Mailbox: "INBOX.Sent"},
		{Mailbox: "Sent"},
	})
	if err != nil || got != "Sent" {
		t.Fatalf("fallback %q %v", got, err)
	}
	got, err = chooseSentMailbox([]*imap.ListData{{Mailbox: "INBOX.Sent"}})
	if err != nil || got != "INBOX.Sent" {
		t.Fatalf("inbox.sent %q %v", got, err)
	}
	if _, err = chooseSentMailbox([]*imap.ListData{{Mailbox: "INBOX"}}); err == nil {
		t.Fatal("expected a missing Sent folder to fail")
	}
}

func TestCheckLoginModesDoNotSelect(t *testing.T) {
	for _, mode := range []string{imapfake.ModeSSL, imapfake.ModeStartTLS, imapfake.ModeNone} {
		t.Run(mode, func(t *testing.T) {
			server := startIMAP(t, mode, "user", "mailbox-secret-value")
			client := testClient(t, server, mode, "mailbox-secret-value")
			var trace bytes.Buffer
			client.protocolTrace = &trace
			if err := client.CheckLogin(context.Background()); err != nil {
				t.Fatal(err)
			}
			logged := strings.ReplaceAll(trace.String(), "mailbox-secret-value", "[redacted]")
			if strings.Contains(logged, "mailbox-secret-value") {
				t.Fatal("password remained in the redacted trace")
			}
			if !traceHasCommand(trace.String(), "LOGIN") || !traceHasCommand(trace.String(), "LOGOUT") {
				t.Fatalf("login conversation missing LOGIN/LOGOUT:\n%s", logged)
			}
			if traceHasCommand(trace.String(), "EXAMINE") || traceHasCommand(trace.String(), "SELECT") {
				t.Fatalf("login opened a mailbox:\n%s", logged)
			}
		})
	}
}

func TestCheckLoginFailureDoesNotEchoPassword(t *testing.T) {
	server := startIMAP(t, imapfake.ModeSSL, "user", "other-password")
	client := testClient(t, server, imapfake.ModeSSL, "mailbox-secret-value")
	err := client.CheckLogin(context.Background())
	if err == nil || strings.Contains(err.Error(), "mailbox-secret-value") {
		t.Fatalf("error %v", err)
	}
}

func TestListDoesNotMarkReadAndSortsByDate(t *testing.T) {
	server := startIMAP(t, imapfake.ModeSSL, "user", "mailbox-secret-value")
	older := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	newer := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	if _, err := server.Append("INBOX", plainMessage("Newer <newer@example.com>", "Ada <ada@example.com>", "First stored", "newer body", newer), newer, nil); err != nil {
		t.Fatal(err)
	}
	oldUID, err := server.Append("INBOX", plainMessage("Older <older@example.com>", "Ada <ada@example.com>", "Second stored", "older body", older), older, nil)
	if err != nil {
		t.Fatal(err)
	}
	seenWhen := newer.Add(-time.Minute)
	seenUID, err := server.Append("INBOX", plainMessage("Seen <seen@example.com>", "Ada <ada@example.com>", "Already read", "seen body", seenWhen), seenWhen, []imap.Flag{imap.FlagSeen})
	if err != nil {
		t.Fatal(err)
	}
	client := testClient(t, server, imapfake.ModeSSL, "mailbox-secret-value")
	var trace bytes.Buffer
	client.protocolTrace = &trace
	listed, err := client.List(context.Background(), ListQuery{Limit: 10, Since: time.Now().Add(-7 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	logged := strings.ReplaceAll(trace.String(), "mailbox-secret-value", "[redacted]")
	if err = assertPeekOnly(logged); err != nil {
		t.Fatalf("list protocol: %v\n%s", err, logged)
	}
	if len(listed) != 3 {
		t.Fatalf("listed %#v", listed)
	}
	if listed[0].Subject != "First stored" || listed[0].UID == oldUID {
		t.Fatalf("newest first %#v", listed)
	}
	if listed[0].Seen || !strings.Contains(listed[0].Snippet, "newer body") || !strings.Contains(listed[0].From, "newer@example.com") {
		t.Fatalf("summary %#v", listed[0])
	}
	unread, err := client.List(context.Background(), ListQuery{Limit: 10, Unread: true, Since: time.Now().Add(-7 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range unread {
		if message.UID == seenUID || message.Seen {
			t.Fatalf("unread included seen %#v", unread)
		}
	}
	fromAda, err := client.List(context.Background(), ListQuery{Limit: 10, From: "older@example.com", Since: time.Time{}})
	if err != nil || len(fromAda) != 1 || fromAda[0].Subject != "Second stored" {
		t.Fatalf("from filter %#v %v", fromAda, err)
	}
	again, err := client.Read(context.Background(), "INBOX", listed[0].UID, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Seen || again.MarkedRead {
		t.Fatalf("listing or reading marked the message read: %#v", again)
	}
}

func TestReadPlainHTMLAndAttachment(t *testing.T) {
	server := startIMAP(t, imapfake.ModeSSL, "user", "mailbox-secret-value")
	when := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	uid, err := server.Append("INBOX", mixedMessage(), when, nil)
	if err != nil {
		t.Fatal(err)
	}
	htmlUID, err := server.Append("INBOX", htmlMessage(), when, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := testClient(t, server, imapfake.ModeSSL, "mailbox-secret-value")
	var trace bytes.Buffer
	client.protocolTrace = &trace
	message, err := client.Read(context.Background(), "INBOX", uid, false)
	if err != nil {
		t.Fatal(err)
	}
	logged := strings.ReplaceAll(trace.String(), "mailbox-secret-value", "[redacted]")
	if err = assertPeekOnly(logged); err != nil {
		t.Fatalf("read protocol: %v\n%s", err, logged)
	}
	if message.Seen || strings.Contains(message.Body, "JVBERi0") || !strings.Contains(message.Body, "second message") {
		t.Fatalf("body %#v", message)
	}
	if message.BodyType != "text/plain" || message.Subject != "Newer" || len(message.Attachments) != 1 || message.Attachments[0].Name != "report.pdf" || message.Attachments[0].Size == 0 {
		t.Fatalf("message %#v", message)
	}
	htmlBody, err := client.Read(context.Background(), "INBOX", htmlUID, false)
	if err != nil {
		t.Fatal(err)
	}
	if htmlBody.BodyType != "text/html" || strings.Contains(htmlBody.Body, "secret") || strings.Contains(htmlBody.Body, "<") || !strings.Contains(htmlBody.Body, "Hello") {
		t.Fatalf("html %#v", htmlBody)
	}
	trace.Reset()
	marked, err := client.Read(context.Background(), "INBOX", uid, true)
	if err != nil {
		t.Fatal(err)
	}
	if !marked.MarkedRead || !traceHasCommand(trace.String(), "STORE") {
		t.Fatalf("mark read %#v\n%s", marked, strings.ReplaceAll(trace.String(), "mailbox-secret-value", "[redacted]"))
	}
	checked, err := client.Read(context.Background(), "INBOX", uid, false)
	if err != nil || !checked.Seen || checked.MarkedRead {
		t.Fatalf("seen after mark %#v %v", checked, err)
	}
}

func TestSearchSentSpecialUseAndFallback(t *testing.T) {
	server := startIMAP(t, imapfake.ModeSSL, "user", "mailbox-secret-value")
	if err := server.CreateMailbox("Gesendet"); err != nil {
		t.Fatal(err)
	}
	if err := server.CreateMailbox("Sent"); err != nil {
		t.Fatal(err)
	}
	if err := server.SetSpecialUse("Gesendet", imap.MailboxAttrSent); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-3 * time.Hour).UTC().Truncate(time.Second)
	uid, err := server.Append("Gesendet", plainMessage("Me <me@example.com>", "Ada <ada@example.com>", "Hello", "sent body", when), when, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.Append("Sent", plainMessage("Me <me@example.com>", "Ada <ada@example.com>", "Wrong folder", "nope", when), when, nil); err != nil {
		t.Fatal(err)
	}
	client := testClient(t, server, imapfake.ModeSSL, "mailbox-secret-value")
	page, err := client.SearchSent(context.Background(), "ada@example.com", "Hello", time.Now().Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if page.Folder != "Gesendet" || len(page.Messages) != 1 || page.Messages[0].UID != uid || page.Messages[0].Subject != "Hello" {
		t.Fatalf("special-use page %#v", page)
	}
	miss, err := client.SearchSent(context.Background(), "ada@example.com", "Missing", time.Now().Add(-30*24*time.Hour))
	if err != nil || len(miss.Messages) != 0 || miss.Truncated {
		t.Fatalf("miss %#v %v", miss, err)
	}

	fallback := startIMAP(t, imapfake.ModeSSL, "user", "mailbox-secret-value")
	if err = fallback.CreateMailbox("Sent Items"); err != nil {
		t.Fatal(err)
	}
	itemUID, err := fallback.Append("Sent Items", plainMessage("Me <me@example.com>", "Bo <bo@example.com>", "Note", "body", when), when, nil)
	if err != nil {
		t.Fatal(err)
	}
	fallbackClient := testClient(t, fallback, imapfake.ModeSSL, "mailbox-secret-value")
	page, err = fallbackClient.SearchSent(context.Background(), "bo@example.com", "", time.Time{})
	if err != nil || page.Folder != "Sent Items" || len(page.Messages) != 1 || page.Messages[0].UID != itemUID {
		t.Fatalf("fallback %#v %v", page, err)
	}
}

func TestSearchSentTruncation(t *testing.T) {
	previous := sentSearchLimit
	sentSearchLimit = 1
	t.Cleanup(func() { sentSearchLimit = previous })
	server := startIMAP(t, imapfake.ModeSSL, "user", "mailbox-secret-value")
	if err := server.CreateMailbox("Sent"); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if _, err := server.Append("Sent", plainMessage("Me <me@example.com>", "Ada <ada@example.com>", "Hello", "older", when), when, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Append("Sent", plainMessage("Me <me@example.com>", "Ada <ada@example.com>", "Hello extra", "newer", when.Add(time.Minute)), when.Add(time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	client := testClient(t, server, imapfake.ModeSSL, "mailbox-secret-value")
	_, err := client.SearchSent(context.Background(), "ada@example.com", "Hello", time.Time{})
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("expected truncation error, got %v", err)
	}
}

func traceHasCommand(trace, name string) bool {
	name = strings.ToUpper(name)
	for _, line := range strings.Split(strings.ToUpper(trace), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "T") {
			continue
		}
		command := fields[1]
		if command == "UID" && len(fields) >= 3 {
			command = fields[2]
		}
		if command == name {
			return true
		}
	}
	return false
}

// assertPeekOnly checks that the client opened the mailbox with EXAMINE and
// fetched text with BODY.PEEK. Server replies echo BODY[section] for a peek,
// and message text can contain words such as "stored", so only commands count.
func assertPeekOnly(trace string) error {
	if !traceHasCommand(trace, "EXAMINE") {
		return fmt.Errorf("missing EXAMINE")
	}
	if traceHasCommand(trace, "SELECT") || traceHasCommand(trace, "STORE") {
		return fmt.Errorf("mailbox was selected or flags were stored")
	}
	sawPeek := false
	for _, line := range strings.Split(strings.ToUpper(trace), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "T") {
			continue
		}
		if strings.Contains(line, "BODY.PEEK") {
			sawPeek = true
		}
		if strings.Contains(line, " BODY[") {
			return fmt.Errorf("fetch was not a peek: %s", strings.TrimSpace(line))
		}
	}
	if !sawPeek {
		return fmt.Errorf("missing BODY.PEEK")
	}
	return nil
}

func startIMAP(t *testing.T, mode, username, password string) *imapfake.Server {
	t.Helper()
	server, err := imapfake.Listen(imapfake.Options{Mode: mode, Username: username, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func testClient(t *testing.T, server *imapfake.Server, mode, password string) *Client {
	t.Helper()
	cfg := Config{Host: server.Host(), Port: server.Port(), TLSMode: mode, Username: "user", Password: password}
	if mode != imapfake.ModeNone {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(server.Certificate) {
			t.Fatal("test certificate was not parsed")
		}
		cfg.TLSConfig = &tls.Config{ServerName: server.Host(), RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func plainMessage(from, to, subject, body string, when time.Time) []byte {
	raw := "From: " + from + "\nTo: " + to + "\nSubject: " + subject + "\nDate: " + when.Format(time.RFC1123Z) + "\nMessage-ID: <" + subject + "@example.com>\nMIME-Version: 1.0\nContent-Type: text/plain; charset=utf-8\n\n" + body + "\n"
	return crlf(raw)
}

func htmlMessage() []byte {
	raw := "From: Ada <ada@example.com>\nTo: me@example.com\nSubject: HTML note\nDate: Thu, 22 Oct 2026 12:00:00 +0000\nMessage-ID: <html@example.com>\nMIME-Version: 1.0\nContent-Type: text/html; charset=utf-8\n\n<html><body><p>Hello <b>there</b></p><script>secret()</script></body></html>\n"
	return crlf(raw)
}

func mixedMessage() []byte {
	raw := strings.Join([]string{
		"From: Ada <ada@example.com>",
		"To: me@example.com",
		"Cc: cc@example.com",
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
	}, "\n")
	return crlf(raw)
}

func crlf(value string) []byte {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return []byte(strings.ReplaceAll(value, "\n", "\r\n"))
}
