package mail

import (
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
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

func TestBuildRejectsBadInputs(t *testing.T) {
	cases := []Message{{Subject: "x", Text: "body"}, {To: []string{"bad"}, Subject: "x", Text: "body"}, {To: []string{"a@example.com"}, Subject: "x\r\nBcc:x@y.com", Text: "body"}, {To: []string{"a@example.com"}, Subject: "x", FromName: "bad\r\nBcc:x@y.com", Text: "body"}}
	for _, m := range cases {
		if _, err := m.Build(); err == nil {
			t.Fatalf("expected validation error for %#v", m)
		}
	}
}

func TestPlainTextBodyRoundTripNoHeadersInBody(t *testing.T) {
	note := "Thanks for the intro.\n\nBest,\nAlex\nsunbeam.agency"
	built, err := (Message{To: []string{"a@example.com"}, Subject: "Intro note", Text: note}).Build()
	if err != nil {
		t.Fatal(err)
	}
	msg, body := decodeBuiltMessage(t, built.Raw)
	contentType := msg.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("Content-Type should be a header, got %q", contentType)
	}
	if msg.Header.Get("Content-Transfer-Encoding") == "" {
		t.Fatal("Content-Transfer-Encoding should be a header, not body text")
	}
	if strings.Contains(body, "Content-Type:") || strings.Contains(body, "Content-Transfer-Encoding:") {
		t.Fatalf("MIME headers leaked into body: %q", body)
	}
	if normalizeNewlines(body) != normalizeNewlines(note) {
		t.Fatalf("decoded body %q != input %q", body, note)
	}
	if !strings.HasSuffix(normalizeNewlines(body), "\nsunbeam.agency") && normalizeNewlines(body) != "sunbeam.agency" {
		t.Fatalf("final line sunbeam.agency was altered: %q", body)
	}
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") || strings.Contains(body, "utm_") {
		t.Fatalf("unexpected tracking or redirect link inserted: %q", body)
	}
}

func TestLongLineRoundTripWithoutVisibleSoftBreak(t *testing.T) {
	longLine := strings.Repeat("abcdefghijklmnopqrstuvwxyz", 4) // 104 chars
	built, err := (Message{To: []string{"a@example.com"}, Subject: "Long", Text: longLine}).Build()
	if err != nil {
		t.Fatal(err)
	}
	_, body := decodeBuiltMessage(t, built.Raw)
	if normalizeNewlines(body) != longLine {
		t.Fatalf("decoded body %q != input %q", body, longLine)
	}
	if strings.Contains(body, "=\r\n") || strings.Contains(body, "=\n") || strings.HasSuffix(strings.TrimSpace(body), "=") {
		t.Fatalf("quoted-printable soft break visible after decode: %q", body)
	}
}

func TestUtf8SubjectAndBodyRoundTrip(t *testing.T) {
	subject := "Réunion café — 日本語"
	text := "Café résumé: 日本語の本文です。\n\nsunbeam.agency"
	built, err := (Message{To: []string{"a@example.com"}, Subject: subject, Text: text}).Build()
	if err != nil {
		t.Fatal(err)
	}
	msg, body := decodeBuiltMessage(t, built.Raw)
	decodedSubject, err := (&mime.WordDecoder{}).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	if decodedSubject != subject {
		t.Fatalf("subject %q != %q", decodedSubject, subject)
	}
	if normalizeNewlines(body) != normalizeNewlines(text) {
		t.Fatalf("decoded body %q != input %q", body, text)
	}
}

func TestHtmlOnlyAndAlternativeAreRealMime(t *testing.T) {
	t.Run("htmlOnly", func(t *testing.T) {
		html := "<p>Hello from sunbeam.agency</p>"
		built, err := (Message{To: []string{"a@example.com"}, Subject: "HTML", HTML: html}).Build()
		if err != nil {
			t.Fatal(err)
		}
		msg, body := decodeBuiltMessage(t, built.Raw)
		if !strings.HasPrefix(msg.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("Content-Type header missing/wrong: %q", msg.Header.Get("Content-Type"))
		}
		if msg.Header.Get("Content-Transfer-Encoding") == "" {
			t.Fatal("Content-Transfer-Encoding should be a header")
		}
		if strings.Contains(body, "Content-Type:") || strings.Contains(body, "Content-Transfer-Encoding:") {
			t.Fatalf("MIME headers leaked into HTML body: %q", body)
		}
		if normalizeNewlines(body) != html {
			t.Fatalf("decoded HTML body %q != input %q", body, html)
		}
	})
	t.Run("textPlusHtml", func(t *testing.T) {
		text := "plain note\n\nsunbeam.agency"
		html := "<p>plain note</p><p>sunbeam.agency</p>"
		built, err := (Message{To: []string{"a@example.com"}, Subject: "Both", Text: text, HTML: html}).Build()
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
		if err != nil || media != "multipart/alternative" {
			t.Fatalf("expected multipart/alternative header, got %q (%v)", msg.Header.Get("Content-Type"), err)
		}
		reader := multipart.NewReader(msg.Body, params["boundary"])
		plainPart, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		plainBody := decodePartBody(t, plainPart)
		if strings.Contains(plainBody, "Content-Type:") {
			t.Fatalf("headers leaked into alternative plain part: %q", plainBody)
		}
		if normalizeNewlines(plainBody) != normalizeNewlines(text) {
			t.Fatalf("plain part %q != %q", plainBody, text)
		}
		htmlPart, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		htmlBody := decodePartBody(t, htmlPart)
		if normalizeNewlines(htmlBody) != html {
			t.Fatalf("html part %q != %q", htmlBody, html)
		}
	})
}

func decodeBuiltMessage(t *testing.T, rawURL string) (*mail.Message, string) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := io.ReadAll(msg.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeTransferEncoding(t, msg.Header.Get("Content-Transfer-Encoding"), encoded)
	return msg, body
}

func decodePartBody(t *testing.T, part *multipart.Part) string {
	t.Helper()
	encoded, err := io.ReadAll(part)
	if err != nil {
		t.Fatal(err)
	}
	return decodeTransferEncoding(t, part.Header.Get("Content-Transfer-Encoding"), encoded)
}

func decodeTransferEncoding(t *testing.T, transferEncoding string, encoded []byte) string {
	t.Helper()
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "", "7bit", "8bit", "binary":
		return string(encoded)
	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(string(encoded))))
		if err != nil {
			t.Fatal(err)
		}
		return string(decoded)
	case "base64":
		decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, strings.NewReader(string(encoded))))
		if err != nil {
			t.Fatal(err)
		}
		return string(decoded)
	default:
		t.Fatalf("unsupported Content-Transfer-Encoding %q", transferEncoding)
		return ""
	}
}

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}
