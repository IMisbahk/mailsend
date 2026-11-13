package mail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		Subject: "Café update", Text: "plain", HTML: "<b>café</b>", FromName: "Example Sender",
		Account: "you@example.com", ReplyTo: "replies@example.com",
		IncludeMessageID: true, OmitBccHeader: true,
	}).Build()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(built.MessageID, "@example.com>") || !strings.HasPrefix(built.MessageID, "<") {
		t.Fatalf("message id %q", built.MessageID)
	}
	if built.EnvelopeFrom != "you@example.com" {
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
	if !strings.Contains(parsed.Header.Get("From"), "you@example.com") || !strings.Contains(strings.ToLower(parsed.Header.Get("From")), "example sender") {
		t.Fatalf("from header %q", parsed.Header.Get("From"))
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || decoded != "Café update" {
		t.Fatalf("subject %q, %v", parsed.Header.Get("Subject"), err)
	}
	if !strings.Contains(parsed.Header.Get("Reply-To"), "replies@example.com") {
		t.Fatalf("reply-to %q", parsed.Header.Get("Reply-To"))
	}
}

func TestGmailPlainTextMatchesPreV010Structure(t *testing.T) {
	sentAt := time.Date(2026, 10, 15, 12, 0, 0, 0, time.FixedZone("UTC", 0))
	account := "you@example.com"
	to := []string{"alex@example.com"}
	subject := "Hello"
	for _, text := range []string{"Hi from mailsend", "Hello from a body file\nSecond line\n"} {
		legacy := legacyGmailPlain(to, subject, text, account, sentAt)
		built, err := (Message{To: to, Subject: subject, Text: text, Account: account, Date: sentAt}).Build()
		if err != nil {
			t.Fatal(err)
		}
		fixed := bytes.Replace(legacy, []byte("MIME-Version: 1.0\r\n\r\nContent-Type:"), []byte("MIME-Version: 1.0\r\nContent-Type:"), 1)
		if bytes.Equal(legacy, fixed) {
			t.Fatal("legacy fixture did not contain the premature header break")
		}
		if !bytes.Equal(built.MIME, fixed) {
			t.Fatalf("plain text structure changed beyond the header break\nlegacy:\n%s\nfixed:\n%s\ngot:\n%s", legacy, fixed, built.MIME)
		}
		assertPlainBody(t, built.MIME, text)
	}
}

func assertPlainBody(t *testing.T, raw []byte, text string) {
	t.Helper()
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header.Get("Content-Type") != "text/plain; charset=UTF-8" {
		t.Fatalf("content type %q", parsed.Header.Get("Content-Type"))
	}
	if parsed.Header.Get("Content-Transfer-Encoding") != "quoted-printable" {
		t.Fatalf("encoding %q", parsed.Header.Get("Content-Transfer-Encoding"))
	}
	body, err := io.ReadAll(parsed.Body)
	if err != nil {
		t.Fatal(err)
	}
	if bodyHasHeaderLine(body) {
		t.Fatalf("body still contains header lines:\n%s", body)
	}
	if normalizeNewlines(decodeQuotedPrintable(t, body)) != normalizeNewlines(text) {
		t.Fatalf("body %q", body)
	}
}

func TestMIMEBodiesDoNotContainHeaders(t *testing.T) {
	dir := t.TempDir()
	bodyFile := filepath.Join(dir, "body.txt")
	fileText := "Hello from a body file\nSecond line\n"
	if err := os.WriteFile(bodyFile, []byte(fileText), 0600); err != nil {
		t.Fatal(err)
	}
	fileBody, err := os.ReadFile(bodyFile)
	if err != nil {
		t.Fatal(err)
	}
	attachment := filepath.Join(dir, "notes.txt")
	attachmentBytes := []byte("attachment bytes\n")
	if err := os.WriteFile(attachment, attachmentBytes, 0600); err != nil {
		t.Fatal(err)
	}
	sentAt := time.Date(2026, 10, 16, 8, 30, 0, 0, time.FixedZone("UTC", 0))
	providers := []struct {
		name   string
		gmail  bool
		mutate func(*Message)
	}{
		{name: "gmail", gmail: true},
		{name: "smtp", mutate: func(m *Message) {
			m.IncludeMessageID = true
			m.OmitBccHeader = true
			m.MessageID = "<sender1@example.com>"
			m.ReplyTo = "replies@example.com"
		}},
	}
	cases := []struct {
		name       string
		text       string
		html       string
		attach     bool
		singlePart bool
	}{
		{name: "body", text: "Hi there", singlePart: true},
		{name: "body-file", text: string(fileBody), singlePart: true},
		{name: "html", html: "<p>Hello</p>", singlePart: true},
		{name: "body-and-html", text: "Hi there", html: "<p>Hello</p>"},
		{name: "body-and-attachment", text: "Hi there", attach: true},
		{name: "body-file-and-attachment", text: string(fileBody), attach: true},
		{name: "html-and-attachment", html: "<p>Hello</p>", attach: true},
		{name: "body-html-attachment", text: "Hi there", html: "<p>Hello</p>", attach: true},
	}
	for _, provider := range providers {
		for _, tc := range cases {
			t.Run(provider.name+"/"+tc.name, func(t *testing.T) {
				msg := Message{
					To: []string{"alex@example.com"}, Cc: []string{"cc@example.com"}, Bcc: []string{"hidden@example.com"},
					Subject: "Hello", Text: tc.text, HTML: tc.html, Account: "you@example.com", Date: sentAt,
				}
				if tc.attach {
					msg.Attachments = []string{attachment}
				}
				if provider.mutate != nil {
					provider.mutate(&msg)
				}
				built, err := msg.Build()
				if err != nil {
					t.Fatal(err)
				}
				raw, err := base64.RawURLEncoding.DecodeString(built.Raw)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(raw, built.MIME) {
					t.Fatal("Raw and MIME differ")
				}
				parsed, err := mail.ReadMessage(bytes.NewReader(built.MIME))
				if err != nil {
					t.Fatal(err)
				}
				if provider.gmail {
					if parsed.Header.Get("Bcc") == "" || parsed.Header.Get("Message-Id") != "" {
						t.Fatalf("gmail headers bcc %q message-id %q", parsed.Header.Get("Bcc"), parsed.Header.Get("Message-Id"))
					}
				} else if parsed.Header.Get("Bcc") != "" || parsed.Header.Get("Message-Id") != msg.MessageID {
					t.Fatalf("smtp headers bcc %q message-id %q", parsed.Header.Get("Bcc"), parsed.Header.Get("Message-Id"))
				}
				assertLeafBodies(t, textproto.MIMEHeader(parsed.Header), parsed.Body, tc.text, tc.html, tc.attach, attachmentBytes)
				media, _, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
				if err != nil {
					t.Fatal(err)
				}
				if tc.singlePart && strings.HasPrefix(media, "multipart/") {
					t.Fatalf("plain or html-only message is %s", media)
				}
				if !tc.singlePart && !strings.HasPrefix(media, "multipart/") {
					t.Fatalf("expected multipart, got %s", media)
				}
			})
		}
	}
}

func legacyGmailPlain(to []string, subject, text, account string, date time.Time) []byte {
	var payload bytes.Buffer
	fmt.Fprintf(&payload, "To: %s\r\n", formatAddressList(to))
	if account != "" {
		fmt.Fprintf(&payload, "From: %s\r\n", (&mail.Address{Address: account}).String())
	}
	fmt.Fprintf(&payload, "Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", subject))
	fmt.Fprintf(&payload, "Date: %s\r\n", date.Format(time.RFC1123Z))
	fmt.Fprint(&payload, "MIME-Version: 1.0\r\n\r\n")
	fmt.Fprint(&payload, "Content-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	writer := quotedprintable.NewWriter(&payload)
	if _, err := writer.Write([]byte(text)); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return payload.Bytes()
}

func assertLeafBodies(t *testing.T, header textproto.MIMEHeader, body io.Reader, text, html string, wantAttachment bool, attachment []byte) {
	t.Helper()
	media, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || media == "" {
		raw, _ := io.ReadAll(body)
		t.Fatalf("missing content type; body starts %q", raw)
	}
	if strings.HasPrefix(media, "multipart/") {
		reader := multipart.NewReader(body, params["boundary"])
		sawText, sawHTML, sawFile := false, false, false
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			partMedia, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(partMedia, "multipart/") {
				assertLeafBodies(t, part.Header, part, text, html, false, nil)
				sawText = text != ""
				sawHTML = html != ""
				continue
			}
			raw, err := io.ReadAll(part)
			if err != nil {
				t.Fatal(err)
			}
			if bodyHasHeaderLine(raw) {
				t.Fatalf("part %s body contains header lines:\n%s", partMedia, raw)
			}
			disposition, _, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if disposition == "attachment" || part.FileName() != "" {
				sawFile = true
				decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(string(raw)), "\r\n", ""))
				if err != nil {
					t.Fatal(err)
				}
				if string(decoded) != string(attachment) {
					t.Fatalf("attachment %q", decoded)
				}
				continue
			}
			switch {
			case strings.HasPrefix(partMedia, "text/plain"):
				sawText = true
				if decoded := normalizeNewlines(decodeQuotedPrintable(t, raw)); decoded != normalizeNewlines(text) {
					t.Fatalf("plain body %q", decoded)
				}
			case strings.HasPrefix(partMedia, "text/html"):
				sawHTML = true
				if decoded := normalizeNewlines(decodeQuotedPrintable(t, raw)); decoded != normalizeNewlines(html) {
					t.Fatalf("html body %q", decoded)
				}
			default:
				sawFile = true
				decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(string(raw)), "\r\n", ""))
				if err != nil {
					t.Fatal(err)
				}
				if string(decoded) != string(attachment) {
					t.Fatalf("attachment %q", decoded)
				}
			}
		}
		if (text != "") != sawText || (html != "") != sawHTML || wantAttachment != sawFile {
			t.Fatalf("parts text=%v html=%v file=%v", sawText, sawHTML, sawFile)
		}
		return
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if bodyHasHeaderLine(raw) {
		t.Fatalf("body contains header lines:\n%s", raw)
	}
	decoded := normalizeNewlines(decodeQuotedPrintable(t, raw))
	switch {
	case strings.HasPrefix(media, "text/html"):
		if decoded != normalizeNewlines(html) {
			t.Fatalf("html body %q", decoded)
		}
	default:
		if decoded != normalizeNewlines(text) {
			t.Fatalf("plain body %q", decoded)
		}
	}
}

func normalizeNewlines(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func decodeQuotedPrintable(t *testing.T, raw []byte) string {
	t.Helper()
	decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return string(decoded)
}

func bodyHasHeaderLine(raw []byte) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, "content-type:") || strings.HasPrefix(lower, "content-transfer-encoding:") || strings.HasPrefix(lower, "content-disposition:") || strings.HasPrefix(lower, "mime-version:") {
			return true
		}
	}
	return false
}

func TestBuildRejectsBadInputs(t *testing.T) {
	cases := []Message{{Subject: "x", Text: "body"}, {To: []string{"bad"}, Subject: "x", Text: "body"}, {To: []string{"a@example.com"}, Subject: "x\r\nBcc:x@y.com", Text: "body"}, {To: []string{"a@example.com"}, Subject: "x", FromName: "bad\r\nBcc:x@y.com", Text: "body"}}
	for _, m := range cases {
		if _, err := m.Build(); err == nil {
			t.Fatalf("expected validation error for %#v", m)
		}
	}
}
