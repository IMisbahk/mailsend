package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
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
	"time"
)

type Message struct {
	To, Cc, Bcc      []string
	Subject          string
	Text             string
	HTML             string
	Attachments      []string
	ReplyTo          string
	FromName         string
	Account          string
	MessageID        string
	IncludeMessageID bool
	OmitBccHeader    bool
	Date             time.Time
}

type BuiltMessage struct {
	Raw          string
	MIME         []byte
	Recipients   []string
	MessageID    string
	EnvelopeFrom string
}

func ParseAddresses(values []string) ([]string, error) {
	result := make([]string, 0)
	seen := map[string]bool{}
	for _, value := range values {
		if strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("invalid recipient address")
		}
		list, err := mail.ParseAddressList(value)
		if err != nil || len(list) == 0 {
			return nil, fmt.Errorf("invalid recipient address %q", value)
		}
		for _, addr := range list {
			if !strings.Contains(addr.Address, "@") || strings.ContainsAny(addr.Address, "\r\n") {
				return nil, fmt.Errorf("invalid recipient address %q", addr.Address)
			}
			key := strings.ToLower(addr.Address)
			if !seen[key] {
				result = append(result, addr.Address)
				seen[key] = true
			}
		}
	}
	return result, nil
}

func (m Message) Build() (BuiltMessage, error) {
	if len(m.To)+len(m.Cc)+len(m.Bcc) == 0 {
		return BuiltMessage{}, fmt.Errorf("at least one recipient is required")
	}
	if strings.TrimSpace(m.Subject) == "" {
		return BuiltMessage{}, fmt.Errorf("subject is required")
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return BuiltMessage{}, fmt.Errorf("subject cannot contain line breaks")
	}
	if m.Text == "" && m.HTML == "" {
		return BuiltMessage{}, fmt.Errorf("provide --body, --body-file, or --html-file")
	}
	if strings.ContainsAny(m.ReplyTo, "\r\n") {
		return BuiltMessage{}, fmt.Errorf("invalid reply-to address")
	}
	if strings.ContainsAny(m.FromName, "\r\n") {
		return BuiltMessage{}, fmt.Errorf("from name cannot contain line breaks")
	}
	if m.ReplyTo != "" {
		if _, err := mail.ParseAddress(m.ReplyTo); err != nil {
			return BuiltMessage{}, fmt.Errorf("invalid reply-to address %q", m.ReplyTo)
		}
	}
	to, err := ParseAddresses(m.To)
	if err != nil {
		return BuiltMessage{}, err
	}
	cc, err := ParseAddresses(m.Cc)
	if err != nil {
		return BuiltMessage{}, err
	}
	bcc, err := ParseAddresses(m.Bcc)
	if err != nil {
		return BuiltMessage{}, err
	}
	if len(to)+len(cc)+len(bcc) > 500 {
		return BuiltMessage{}, fmt.Errorf("at most 500 recipients are allowed per message")
	}
	bareFrom := ""
	if strings.TrimSpace(m.Account) != "" {
		parsed, err := mail.ParseAddress(m.Account)
		if err != nil || parsed.Address == "" || strings.ContainsAny(parsed.Address, "\r\n<>") {
			return BuiltMessage{}, fmt.Errorf("invalid from address %q", m.Account)
		}
		bareFrom = parsed.Address
	}
	if strings.ContainsAny(m.MessageID, "\r\n") {
		return BuiltMessage{}, fmt.Errorf("invalid message id")
	}
	if m.IncludeMessageID && m.MessageID == "" {
		m.MessageID, err = newMessageID(bareFrom)
		if err != nil {
			return BuiltMessage{}, err
		}
	}
	sentAt := m.Date
	if sentAt.IsZero() {
		sentAt = time.Now()
	}
	replyTo := ""
	if m.ReplyTo != "" {
		parsed, err := mail.ParseAddress(m.ReplyTo)
		if err != nil {
			return BuiltMessage{}, fmt.Errorf("invalid reply-to address %q", m.ReplyTo)
		}
		replyTo = parsed.String()
	}
	writeHeaders := func(w io.Writer) {
		if len(to) > 0 {
			fmt.Fprintf(w, "To: %s\r\n", formatAddressList(to))
		}
		if len(cc) > 0 {
			fmt.Fprintf(w, "Cc: %s\r\n", formatAddressList(cc))
		}
		// Gmail uses the Bcc header to address the message and strips it from
		// delivered copies. SMTP omits the header and uses envelope recipients.
		if len(bcc) > 0 && !m.OmitBccHeader {
			fmt.Fprintf(w, "Bcc: %s\r\n", formatAddressList(bcc))
		}
		if m.FromName != "" && bareFrom != "" {
			fmt.Fprintf(w, "From: %s\r\n", (&mail.Address{Name: m.FromName, Address: bareFrom}).String())
		} else if bareFrom != "" {
			fmt.Fprintf(w, "From: %s\r\n", (&mail.Address{Address: bareFrom}).String())
		}
		fmt.Fprintf(w, "Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", m.Subject))
		fmt.Fprintf(w, "Date: %s\r\n", sentAt.Format(time.RFC1123Z))
		if m.MessageID != "" {
			fmt.Fprintf(w, "Message-ID: %s\r\n", m.MessageID)
		}
		fmt.Fprint(w, "MIME-Version: 1.0\r\n")
		if replyTo != "" {
			fmt.Fprintf(w, "Reply-To: %s\r\n", replyTo)
		}
	}
	var payload bytes.Buffer
	if len(m.Attachments) > 0 {
		writeHeaders(&payload)
		var body bytes.Buffer
		boundaryWriter := multipart.NewWriter(&body)
		fmt.Fprintf(&payload, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundaryWriter.Boundary())
		if err := writeBodyParts(boundaryWriter, m); err != nil {
			return BuiltMessage{}, err
		}
		if err := boundaryWriter.Close(); err != nil {
			return BuiltMessage{}, err
		}
		payload.Write(body.Bytes())
	} else {
		writeHeaders(&payload)
		fmt.Fprint(&payload, "\r\n")
		if err := writeAlternative(&payload, m); err != nil {
			return BuiltMessage{}, err
		}
	}
	all := append(append([]string{}, to...), cc...)
	all = append(all, bcc...)
	mimeBytes := append([]byte(nil), payload.Bytes()...)
	return BuiltMessage{Raw: base64.RawURLEncoding.EncodeToString(mimeBytes), MIME: mimeBytes, Recipients: all, MessageID: m.MessageID, EnvelopeFrom: bareFrom}, nil
}

func newMessageID(from string) (string, error) {
	domain := ""
	if parsed, err := mail.ParseAddress(from); err == nil {
		from = parsed.Address
	}
	if at := strings.LastIndex(from, "@"); at >= 0 && at < len(from)-1 {
		domain = strings.TrimSuffix(from[at+1:], ".")
	}
	if domain == "" || strings.ContainsAny(domain, " \t\r\n<>@") {
		return "", fmt.Errorf("from address %q has no domain for Message-ID", from)
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("<%s.%d@%s>", hex.EncodeToString(buf), time.Now().UnixNano(), domain), nil
}

func formatAddressList(addrs []string) string {
	list := make([]string, 0, len(addrs))
	for _, a := range addrs {
		list = append(list, (&mail.Address{Address: a}).String())
	}
	return strings.Join(list, ", ")
}

func writeBodyParts(w *multipart.Writer, m Message) error {
	if m.Text != "" && m.HTML != "" {
		var body bytes.Buffer
		alt := multipart.NewWriter(&body)
		if err := writeTextPart(alt, "text/plain; charset=UTF-8", m.Text); err != nil {
			return err
		}
		if err := writeTextPart(alt, "text/html; charset=UTF-8", m.HTML); err != nil {
			return err
		}
		if err := alt.Close(); err != nil {
			return err
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": alt.Boundary()}))
		part, err := w.CreatePart(h)
		if err != nil {
			return err
		}
		if _, err = part.Write(body.Bytes()); err != nil {
			return err
		}
	} else {
		contentType, content := "text/plain; charset=UTF-8", m.Text
		if m.HTML != "" {
			contentType, content = "text/html; charset=UTF-8", m.HTML
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", contentType)
		h.Set("Content-Transfer-Encoding", "quoted-printable")
		part, err := w.CreatePart(h)
		if err != nil {
			return err
		}
		qp := quotedprintable.NewWriter(part)
		if _, err = qp.Write([]byte(content)); err != nil {
			return err
		}
		if err = qp.Close(); err != nil {
			return err
		}
	}
	for _, path := range m.Attachments {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("cannot read attachment %q: %w", path, err)
		}
		name := filepath.Base(path)
		media := mime.TypeByExtension(filepath.Ext(name))
		if media == "" {
			media = "application/octet-stream"
		}
		params := map[string]string{"name": name}
		h := textproto.MIMEHeader{}
		h.Set("Content-Type", mime.FormatMediaType(media, params))
		h.Set("Content-Transfer-Encoding", "base64")
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		part, err := w.CreatePart(h)
		if err != nil {
			return err
		}
		enc := base64.NewEncoder(base64.StdEncoding, &lineWriter{w: part, width: 76})
		if _, err = enc.Write(data); err != nil {
			return err
		}
		if err = enc.Close(); err != nil {
			return err
		}
		if _, err = io.WriteString(part, "\r\n"); err != nil {
			return err
		}
	}
	return nil
}

type lineWriter struct {
	w          io.Writer
	width, col int
}

func (l *lineWriter) Write(p []byte) (int, error) {
	n := 0
	for _, b := range p {
		if l.col == l.width {
			if _, e := io.WriteString(l.w, "\r\n"); e != nil {
				return n, e
			}
			l.col = 0
		}
		if _, e := l.w.Write([]byte{b}); e != nil {
			return n, e
		}
		l.col++
		n++
	}
	return n, nil
}

func writeAlternative(w io.Writer, m Message) error {
	if m.Text != "" && m.HTML != "" {
		mw := multipart.NewWriter(w)
		fmt.Fprintf(w, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", mw.Boundary())
		if err := writeTextPart(mw, "text/plain; charset=UTF-8", m.Text); err != nil {
			return err
		}
		if err := writeTextPart(mw, "text/html; charset=UTF-8", m.HTML); err != nil {
			return err
		}
		return mw.Close()
	}
	if m.HTML != "" {
		_, err := io.WriteString(w, "Content-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		if err != nil {
			return err
		}
		q := quotedprintable.NewWriter(w)
		if _, err = q.Write([]byte(m.HTML)); err != nil {
			return err
		}
		return q.Close()
	}
	_, err := io.WriteString(w, "Content-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	if err != nil {
		return err
	}
	q := quotedprintable.NewWriter(w)
	if _, err = q.Write([]byte(m.Text)); err != nil {
		return err
	}
	return q.Close()
}
func writeTextPart(w *multipart.Writer, contentType, body string) error {
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", contentType)
	h.Set("Content-Transfer-Encoding", "quoted-printable")
	p, e := w.CreatePart(h)
	if e != nil {
		return e
	}
	q := quotedprintable.NewWriter(p)
	if _, e = q.Write([]byte(body)); e != nil {
		return e
	}
	return q.Close()
}
