package smtp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mailmsg "github.com/IMisbahk/mailsend/internal/mail"
	"github.com/IMisbahk/mailsend/internal/smtpfake"
)

func testClient(t *testing.T, server *smtpfake.Server, mode, password string) *Client {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(server.Certificate) {
		t.Fatal("test certificate was not added")
	}
	client, err := New(Config{
		Host: server.Host(), Port: server.Port(), TLSMode: mode,
		Username: "you@example.com", Password: password, From: "you@example.com",
		TLSConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func sampleMessage(t *testing.T) mailmsg.BuiltMessage {
	t.Helper()
	dir := t.TempDir()
	attachment := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(attachment, []byte("hello attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	built, err := (mailmsg.Message{
		To: []string{"alex@example.com"}, Cc: []string{"cc@example.com"}, Bcc: []string{"hidden@example.com"},
		Subject: "Café", Text: "plain line\r\n.dot line\r\nend", HTML: "<b>café</b>",
		FromName: "Example Sender", Account: "you@example.com", ReplyTo: "replies@example.com",
		Attachments: []string{attachment}, IncludeMessageID: true, OmitBccHeader: true,
	}).Build()
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func TestSTARTTLSAuthAndSend(t *testing.T) {
	server, err := smtpfake.Listen(smtpfake.Options{Mode: smtpfake.ModeStartTLS, Username: "you@example.com", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := testClient(t, server, TLSStartTLS, "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = client.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if !server.TLSStarted.Load() {
		t.Fatal("STARTTLS did not negotiate TLS")
	}
	built := sampleMessage(t)
	id, ambiguous, err := client.Send(ctx, built)
	if err != nil || ambiguous || id != built.MessageID {
		t.Fatalf("send id %q ambiguous %v err %v", id, ambiguous, err)
	}
	got, err := server.WaitSubmission(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got.MailFrom != "you@example.com" {
		t.Fatalf("envelope from %q", got.MailFrom)
	}
	if strings.Join(got.Recipients, ",") != "alex@example.com,cc@example.com,hidden@example.com" {
		t.Fatalf("recipients %#v", got.Recipients)
	}
	body := string(got.Data)
	if !strings.Contains(body, built.MessageID) || !strings.Contains(body, "Date:") || !strings.Contains(body, "note.txt") {
		t.Fatalf("message missing required headers:\n%s", body)
	}
	if strings.Contains(body, "Bcc:") {
		t.Fatalf("bcc header leaked:\n%s", body)
	}
	if !strings.Contains(body, ".dot line") {
		t.Fatalf("dot-stuffed line was not restored:\n%s", body)
	}
}

func TestAuthFailureIsPermanent(t *testing.T) {
	server, err := smtpfake.Listen(smtpfake.Options{Mode: smtpfake.ModeStartTLS, Username: "you@example.com", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := testClient(t, server, TLSStartTLS, "wrong-password")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = client.Probe(ctx)
	var smtpErr *Error
	if err == nil || !errors.As(err, &smtpErr) || smtpErr.Code != 535 || smtpErr.Temporary() || !smtpErr.IsAuth() || smtpErr.Retryable() {
		t.Fatalf("auth error %#v %v", smtpErr, err)
	}
}

func TestImplicitTLSAndLogin(t *testing.T) {
	server, err := smtpfake.Listen(smtpfake.Options{Mode: smtpfake.ModeSSL, Username: "you@example.com", Password: "secret", LoginOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := testClient(t, server, TLSSSL, "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	built := sampleMessage(t)
	if _, _, err = client.Send(ctx, built); err != nil {
		t.Fatal(err)
	}
	if !server.TLSStarted.Load() {
		t.Fatal("implicit TLS was not used")
	}
}

func TestCleartextModeDoesNotUseTLS(t *testing.T) {
	server, err := smtpfake.Listen(smtpfake.Options{Mode: smtpfake.ModeNone, Username: "you@example.com", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := New(Config{
		Host: server.Host(), Port: server.Port(), TLSMode: TLSNone,
		Username: "you@example.com", Password: "secret", From: "you@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = client.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if server.TLSStarted.Load() {
		t.Fatal("tls=none negotiated TLS")
	}
}

func TestTemporaryAndPermanentReplies(t *testing.T) {
	temporary, err := smtpfake.Listen(smtpfake.Options{
		Mode: smtpfake.ModeNone, Username: "you@example.com", Password: "secret",
		FailStage: "rcpt", FailCode: 450, FailMessage: "mailbox busy",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer temporary.Close()
	client, err := New(Config{Host: temporary.Host(), Port: temporary.Port(), TLSMode: TLSNone, Username: "you@example.com", Password: "secret", From: "you@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, ambiguous, err := client.Send(ctx, sampleMessage(t))
	var smtpErr *Error
	if err == nil || ambiguous || !errors.As(err, &smtpErr) || !smtpErr.Temporary() || smtpErr.Permanent() || !smtpErr.Retryable() || smtpErr.Code != 450 {
		t.Fatalf("temporary err ambiguous=%v %#v %v", ambiguous, smtpErr, err)
	}
	permanent, err := smtpfake.Listen(smtpfake.Options{
		Mode: smtpfake.ModeNone, Username: "you@example.com", Password: "secret",
		FailStage: "data", FailCode: 550, FailMessage: "user unknown",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer permanent.Close()
	client, err = New(Config{Host: permanent.Host(), Port: permanent.Port(), TLSMode: TLSNone, Username: "you@example.com", Password: "secret", From: "you@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	_, ambiguous, err = client.Send(ctx, sampleMessage(t))
	smtpErr = nil
	if err == nil || ambiguous || !errors.As(err, &smtpErr) || smtpErr.Temporary() || !smtpErr.Permanent() || smtpErr.Retryable() || smtpErr.Code != 550 || smtpErr.IsQuota() {
		t.Fatalf("permanent err ambiguous=%v %#v %v", ambiguous, smtpErr, err)
	}
	limited := &Error{Code: 450, Stage: "rcpt", Message: "rate limit exceeded"}
	if !limited.IsQuota() || !limited.Retryable() {
		t.Fatal("rate limit was not classified")
	}
}

func TestLostDataResponseIsUncertain(t *testing.T) {
	server, err := smtpfake.Listen(smtpfake.Options{Mode: smtpfake.ModeNone, Username: "you@example.com", Password: "secret", DropAfterData: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := New(Config{Host: server.Host(), Port: server.Port(), TLSMode: TLSNone, Username: "you@example.com", Password: "secret", From: "you@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, ambiguous, err := client.Send(ctx, sampleMessage(t))
	var retry interface{ Retryable() bool }
	if err == nil || !ambiguous || (errors.As(err, &retry) && retry.Retryable()) {
		t.Fatalf("ambiguous=%v err=%v", ambiguous, err)
	}
}

func TestTLSVerificationRejectsUnknownCertificate(t *testing.T) {
	server, err := smtpfake.Listen(smtpfake.Options{Mode: smtpfake.ModeStartTLS, Username: "you@example.com", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	t.Setenv("MAILSEND_SMTP_CA_FILE", "")
	client, err := New(Config{Host: server.Host(), Port: server.Port(), TLSMode: TLSStartTLS, Username: "you@example.com", Password: "secret", From: "you@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = client.Probe(ctx); err == nil || !strings.Contains(strings.ToLower(err.Error()), "tls") {
		t.Fatalf("expected TLS verification failure, got %v", err)
	}
}

func TestNormalizeRejectsHardcodedOmissions(t *testing.T) {
	if _, err := Normalize(Config{TLSMode: "starttls", Username: "sender1", From: "you@example.com"}); err == nil {
		t.Fatal("missing host was accepted")
	}
	cfg, err := Normalize(Config{Host: "mail.example.com", TLSMode: "", Username: "sender1", From: "Example Sender <you@example.com>"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 587 || cfg.TLSMode != TLSStartTLS || cfg.From != "you@example.com" {
		t.Fatalf("normalized %#v", cfg)
	}
	if _, err = NormalizeTLS("smtp.gmail.com"); err == nil {
		t.Fatal("accepted an invalid tls mode")
	}
}
