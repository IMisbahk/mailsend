package mail

import (
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAddressesAndBuildHeaders(t *testing.T) {
	to, err := ParseAddresses([]string{"alex@example.com", "Alex@example.com, bob@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(to) != 2 {
		t.Fatalf("expected duplicate removal, got %#v", to)
	}
	built, err := (Message{To: to, Cc: []string{"cc@example.com"}, Bcc: []string{"hidden@example.com"}, Subject: "Hello", Text: "text", Account: "me@example.com"}).Build()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(built.Raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed.Header.Get("To"), "alex@example.com") || !strings.Contains(parsed.Header.Get("Cc"), "cc@example.com") {
		t.Fatalf("recipient headers missing: %#v", parsed.Header)
	}
	if !strings.Contains(parsed.Header.Get("Bcc"), "hidden@example.com") {
		t.Fatal("BCC recipient is missing from API message envelope headers")
	}
	if len(built.Recipients) != 4 {
		t.Fatalf("expected envelope recipients across to/cc/bcc, got %#v", built.Recipients)
	}
}

func TestMultipartAlternativeAndAttachmentEncoding(t *testing.T) {
	dir := t.TempDir()
	attachment := filepath.Join(dir, "report.bin")
	data := []byte{0, 1, 2, 3, 4, 250, 251, 252}
	if err := os.WriteFile(attachment, data, 0600); err != nil {
		t.Fatal(err)
	}
	built, err := (Message{To: []string{"a@example.com"}, Subject: "Hello", Text: "plain hello", HTML: "<b>hello</b>", Attachments: []string{attachment}}).Build()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(built.Raw)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	media, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || media != "multipart/mixed" {
		t.Fatalf("outer MIME type %q, %v", media, err)
	}
	outer := multipart.NewReader(msg.Body, params["boundary"])
	part, err := outer.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	altType, altParams, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if err != nil || altType != "multipart/alternative" {
		t.Fatalf("alternative part type %q, %v", altType, err)
	}
	alt := multipart.NewReader(part, altParams["boundary"])
	plain, err := alt.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	plainBody, _ := io.ReadAll(plain)
	if !strings.Contains(string(plainBody), "plain hello") {
		t.Fatalf("plain part: %s", plainBody)
	}
	html, err := alt.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	htmlBody, _ := io.ReadAll(html)
	if !strings.Contains(string(htmlBody), "<b>hello</b>") {
		t.Fatalf("HTML part: %s", htmlBody)
	}
	file, err := outer.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := io.ReadAll(file)
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(data) {
		t.Fatalf("attachment changed: %v", decoded)
	}
	if got := file.FileName(); got != "report.bin" {
		t.Fatalf("attachment name %q", got)
	}
}

func TestSMTPMessageIDHidesBccAndEncodesUTF8(t *testing.T) {
	built, err := (Message{
		To: []string{"alex@example.com"}, Cc: []string{"cc@example.com"}, Bcc: []string{"hidden@example.com"},
		Subject: "Café update", Text: "plain", HTML: "<b>café</b>", FromName: "Misbah",
		Account: "misbah@sunbeam.agency", ReplyTo: "replies@sunbeam.agency",
		IncludeMessageID: true, OmitBccHeader: true,
	}).Build()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(built.MessageID, "@sunbeam.agency>") || !strings.HasPrefix(built.MessageID, "<") {
		t.Fatalf("message id %q", built.MessageID)
	}
	if built.EnvelopeFrom != "misbah@sunbeam.agency" {
		t.Fatalf("envelope from %q", built.EnvelopeFrom)
	}
	if len(built.Recipients) != 3 || built.Recipients[2] != "hidden@example.com" {
		t.Fatalf("envelope recipients %#v", built.Recipients)
	}
	raw := string(built.MIME)
	parsed, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header.Get("Message-ID") != built.MessageID {
		t.Fatalf("header message id %q", parsed.Header.Get("Message-Id"))
	}
	if parsed.Header.Get("Date") == "" {
		t.Fatal("missing Date header")
	}
	if parsed.Header.Get("Bcc") != "" {
		t.Fatalf("bcc header leaked: %q", parsed.Header.Get("Bcc"))
	}
	if !strings.Contains(parsed.Header.Get("From"), "misbah@sunbeam.agency") || !strings.Contains(strings.ToLower(parsed.Header.Get("From")), "misbah") {
		t.Fatalf("from header %q", parsed.Header.Get("From"))
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || decoded != "Café update" {
		t.Fatalf("subject %q, %v", parsed.Header.Get("Subject"), err)
	}
	if !strings.Contains(parsed.Header.Get("Reply-To"), "replies@sunbeam.agency") {
		t.Fatalf("reply-to %q", parsed.Header.Get("Reply-To"))
	}
}

func TestBuildRejectsBadInputs(t *testing.T) {
	cases := []Message{{Subject: "x", Text: "body"}, {To: []string{"bad"}, Subject: "x", Text: "body"}, {To: []string{"a@example.com"}, Subject: "x\r\nBcc:x@y.com", Text: "body"}, {To: []string{"a@example.com"}, Subject: "x", FromName: "bad\r\nBcc:x@y.com", Text: "body"}}
	for _, m := range cases {
		if _, err := m.Build(); err == nil {
			t.Fatalf("expected validation error for %#v", m)
		}
	}
}
