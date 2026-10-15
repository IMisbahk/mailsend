// Package smtp sends mail through a caller-configured SMTP server.
// The host always comes from account configuration. This package does not
// fall back to a built-in server.
package smtp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"

	mailmsg "github.com/IMisbahk/mailsend/internal/mail"
)

const (
	TLSStartTLS = "starttls"
	TLSSSL      = "ssl"
	TLSNone     = "none"
)

// Config is the non-secret server identity plus the password held in memory
// for the duration of a connection. The password is not written by this package.
type Config struct {
	Host      string
	Port      int
	TLSMode   string
	Username  string
	Password  string
	From      string
	ReplyTo   string
	LocalName string
	TLSConfig *tls.Config
}

// Client is one configured SMTP account.
type Client struct {
	cfg Config
}

// Error is an SMTP reply or protocol failure with a stage and status code.
// Code 0 means the failure happened before an SMTP status was available.
type Error struct {
	Code    int
	Stage   string
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return "SMTP error"
	}
	summary := cleanSMTPText(e.Message)
	if e.Code == 0 {
		if summary == "" {
			return fmt.Sprintf("SMTP %s failed", e.Stage)
		}
		return fmt.Sprintf("SMTP %s failed: %s", e.Stage, summary)
	}
	kind := "permanent"
	if e.Temporary() {
		kind = "temporary"
	}
	if e.IsAuth() {
		return fmt.Sprintf("SMTP authentication %s failure %d at %s: %s", kind, e.Code, e.Stage, summary)
	}
	return fmt.Sprintf("SMTP %s failure %d at %s: %s", kind, e.Code, e.Stage, summary)
}

func (e *Error) Temporary() bool {
	return e != nil && e.Code >= 400 && e.Code < 500
}

func (e *Error) Permanent() bool {
	return e != nil && e.Code >= 500 && e.Code < 600
}

// Retryable reports temporary 4xx failures. Permanent 5xx failures are not retried.
func (e *Error) Retryable() bool { return e.Temporary() }

// IsAuth reports authentication failures, including a later 530 from MAIL/RCPT.
func (e *Error) IsAuth() bool {
	if e == nil {
		return false
	}
	if e.Stage == "auth" && e.Code >= 400 {
		return true
	}
	switch e.Code {
	case 530, 534, 535, 538:
		return true
	default:
		return false
	}
}

// IsQuota reports temporary server limit responses so bulk sending can stop
// the same way a Gmail rate-limit response stops a campaign.
func (e *Error) IsQuota() bool {
	if e == nil || !e.Temporary() {
		return false
	}
	text := strings.ToLower(e.Message)
	for _, word := range []string{"rate", "quota", "too many", "limit", "throttl", "insufficient"} {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

func cleanSMTPText(message string) string {
	message = strings.ReplaceAll(message, "\r", " ")
	message = strings.ReplaceAll(message, "\n", " ")
	return strings.Join(strings.Fields(message), " ")
}

type lostResponseError struct{ err error }

func (e *lostResponseError) Error() string { return e.err.Error() }
func (e *lostResponseError) Unwrap() error { return e.err }

// NormalizeTLS accepts starttls, ssl, and none. An empty value is STARTTLS.
func NormalizeTLS(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", TLSStartTLS:
		return TLSStartTLS, nil
	case TLSSSL:
		return TLSSSL, nil
	case TLSNone:
		return TLSNone, nil
	default:
		return "", fmt.Errorf("invalid TLS mode %q (use starttls, ssl, or none)", mode)
	}
}

// DefaultPort is the protocol default for a TLS mode, not a mail provider.
func DefaultPort(mode string) int {
	switch mode {
	case TLSSSL:
		return 465
	case TLSNone:
		return 25
	default:
		return 587
	}
}

// Normalize checks host, port, TLS mode, username, and addresses.
// It does not require a password and does not contact the server.
func Normalize(cfg Config) (Config, error) {
	mode, err := NormalizeTLS(cfg.TLSMode)
	if err != nil {
		return Config{}, err
	}
	cfg.TLSMode = mode
	cfg.Host = strings.Trim(strings.TrimSpace(cfg.Host), "[]")
	if cfg.Host == "" || strings.Contains(cfg.Host, "://") || strings.ContainsAny(cfg.Host, " \t\r\n/") {
		return Config{}, fmt.Errorf("invalid SMTP host %q", cfg.Host)
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort(mode)
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return Config{}, fmt.Errorf("invalid SMTP port %d", cfg.Port)
	}
	cfg.Username = strings.TrimSpace(cfg.Username)
	if cfg.Username == "" || strings.ContainsAny(cfg.Username, "\r\n") {
		return Config{}, fmt.Errorf("SMTP username is required")
	}
	from, err := bareAddress(cfg.From, "from")
	if err != nil {
		return Config{}, err
	}
	cfg.From = from
	if strings.TrimSpace(cfg.ReplyTo) != "" {
		replyTo, err := bareAddress(cfg.ReplyTo, "reply-to")
		if err != nil {
			return Config{}, err
		}
		cfg.ReplyTo = replyTo
	} else {
		cfg.ReplyTo = ""
	}
	return cfg, nil
}

// ValidatePassword rejects empty, multiline, and oversized secrets.
// 2048 bytes stays under the smallest OS keychain item limit used here.
func ValidatePassword(password string) error {
	if password == "" {
		return fmt.Errorf("SMTP password is empty")
	}
	if strings.ContainsAny(password, "\r\n\x00") {
		return fmt.Errorf("SMTP password cannot contain line breaks")
	}
	if len(password) > 2048 {
		return fmt.Errorf("SMTP password is too long for the OS keychain")
	}
	return nil
}

func bareAddress(value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("SMTP %s address is required", label)
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("invalid SMTP %s address", label)
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || !strings.Contains(parsed.Address, "@") || strings.ContainsAny(parsed.Address, "\r\n<>") {
		return "", fmt.Errorf("invalid SMTP %s address %q", label, value)
	}
	return parsed.Address, nil
}

// New validates config and loads TLS roots. It does not connect.
func New(cfg Config) (*Client, error) {
	normalized, err := Normalize(cfg)
	if err != nil {
		return nil, err
	}
	if err = ValidatePassword(cfg.Password); err != nil {
		return nil, err
	}
	normalized.Password = cfg.Password
	normalized.LocalName = cfg.LocalName
	if cfg.TLSConfig != nil {
		normalized.TLSConfig = cfg.TLSConfig
	} else if normalized.TLSMode != TLSNone {
		normalized.TLSConfig, err = loadTLSConfig(normalized.Host)
		if err != nil {
			return nil, err
		}
	}
	return &Client{cfg: normalized}, nil
}

func loadTLSConfig(host string) (*tls.Config, error) {
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	path := strings.TrimSpace(os.Getenv("MAILSEND_SMTP_CA_FILE"))
	if path == "" {
		return cfg, nil
	}
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read SMTP CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("SMTP CA file %s contains no certificates", path)
	}
	cfg.RootCAs = pool
	return cfg, nil
}

func (c *Client) Provider() string { return "smtp" }
func (c *Client) FromAddress() string {
	if c == nil {
		return ""
	}
	return c.cfg.From
}
func (c *Client) DefaultReplyTo() string {
	if c == nil {
		return ""
	}
	return c.cfg.ReplyTo
}

// Probe connects, negotiates the configured TLS mode, and authenticates.
// It does not send a message.
func (c *Client) Probe(ctx context.Context) error {
	session, err := c.connect(ctx)
	if err != nil {
		return err
	}
	session.quit()
	return nil
}

// Send submits one MIME message. The returned id is the Message-ID header.
// ambiguous is true only when the message was fully submitted and the final
// server reply was not received.
func (c *Client) Send(ctx context.Context, built mailmsg.BuiltMessage) (string, bool, error) {
	if len(built.Recipients) == 0 {
		return "", false, fmt.Errorf("SMTP send requires at least one recipient")
	}
	envelope := built.EnvelopeFrom
	if envelope == "" {
		envelope = c.cfg.From
	}
	envelope, err := bareAddress(envelope, "envelope from")
	if err != nil {
		return "", false, err
	}
	recipients := make([]string, 0, len(built.Recipients))
	for _, recipient := range built.Recipients {
		address, err := bareAddress(recipient, "recipient")
		if err != nil {
			return "", false, err
		}
		recipients = append(recipients, address)
	}
	if len(built.MIME) == 0 {
		return "", false, fmt.Errorf("SMTP send requires a MIME message")
	}
	session, err := c.connect(ctx)
	if err != nil {
		return "", false, err
	}
	defer session.quit()
	if err = session.deliver(ctx, envelope, recipients, built.MIME); err != nil {
		var lost *lostResponseError
		if errors.As(err, &lost) {
			return built.MessageID, true, lost
		}
		return "", false, err
	}
	messageID := built.MessageID
	if messageID == "" {
		messageID = "accepted"
	}
	return messageID, false, nil
}

func (c *Client) address() string {
	return net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.cfg.Port))
}

func (c *Client) localName() string {
	if strings.TrimSpace(c.cfg.LocalName) != "" && validEhloName(c.cfg.LocalName) {
		return c.cfg.LocalName
	}
	name, err := os.Hostname()
	if err != nil || !validEhloName(name) {
		return "localhost"
	}
	return name
}

func validEhloName(name string) bool {
	if name == "" || len(name) > 253 || strings.ContainsAny(name, " \t\r\n") {
		return false
	}
	for _, r := range name {
		if r > 127 || !(r == '.' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func (c *Client) tlsConfig() *tls.Config {
	base := c.cfg.TLSConfig
	if base == nil {
		base = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	cfg := base.Clone()
	if cfg.ServerName == "" {
		cfg.ServerName = c.cfg.Host
	}
	if cfg.MinVersion == 0 {
		cfg.MinVersion = tls.VersionTLS12
	}
	return cfg
}

type session struct {
	conn      net.Conn
	proto     *textproto.Conn
	client    *Client
	localName string
}

func (c *Client) connect(ctx context.Context) (*session, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", c.address())
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("SMTP connection to %s failed: %w", c.address(), err)
	}
	watchContext(ctx, conn)
	if c.cfg.TLSMode == TLSSSL {
		tlsConn, err := c.handshake(ctx, conn)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		conn = tlsConn
	}
	current := &session{conn: conn, proto: textproto.NewConn(conn), client: c, localName: c.localName()}
	if err = current.startup(ctx); err != nil {
		current.quit()
		return nil, err
	}
	return current, nil
}

func (c *Client) handshake(ctx context.Context, conn net.Conn) (net.Conn, error) {
	tlsConn := tls.Client(conn, c.tlsConfig())
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("SMTP TLS handshake with %s failed: %w", c.cfg.Host, err)
	}
	watchContext(ctx, tlsConn)
	return tlsConn, nil
}

func watchContext(ctx context.Context, conn net.Conn) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	go func() {
		<-ctx.Done()
		_ = conn.SetDeadline(time.Now())
	}()
}

func (s *session) startup(ctx context.Context) error {
	code, message, err := s.readReply(ctx, "connect")
	if err != nil {
		return err
	}
	if err = replyError("connect", code, message); err != nil {
		return err
	}
	extensions, err := s.hello(ctx)
	if err != nil {
		return err
	}
	if s.client.cfg.TLSMode == TLSStartTLS {
		if !hasExtension(extensions, "STARTTLS") {
			return &Error{Stage: "starttls", Message: "server did not advertise STARTTLS"}
		}
		if _, _, err = s.roundTrip(ctx, "starttls", "STARTTLS"); err != nil {
			return err
		}
		if s.proto.R.Buffered() != 0 {
			return &Error{Stage: "starttls", Message: "server sent unexpected data before the TLS handshake"}
		}
		tlsConn, err := s.client.handshake(ctx, s.conn)
		if err != nil {
			return err
		}
		s.conn = tlsConn
		s.proto = textproto.NewConn(tlsConn)
		extensions, err = s.hello(ctx)
		if err != nil {
			return err
		}
	}
	return s.authenticate(ctx, extensions)
}

func (s *session) hello(ctx context.Context) (map[string]string, error) {
	_, message, err := s.roundTrip(ctx, "ehlo", "EHLO %s", s.localName)
	if err == nil {
		return parseExtensions(message), nil
	}
	var smtpErr *Error
	if errors.As(err, &smtpErr) && smtpErr.Permanent() {
		if _, _, helErr := s.roundTrip(ctx, "helo", "HELO %s", s.localName); helErr != nil {
			return nil, helErr
		}
		return map[string]string{}, nil
	}
	return nil, err
}

func (s *session) authenticate(ctx context.Context, extensions map[string]string) error {
	mechanisms := map[string]bool{}
	for _, name := range strings.Fields(extensionValue(extensions, "AUTH")) {
		mechanisms[strings.ToUpper(name)] = true
	}
	if strings.ContainsAny(s.client.cfg.Username, "\r\n") || strings.ContainsAny(s.client.cfg.Password, "\r\n") {
		return fmt.Errorf("SMTP username and password cannot contain line breaks")
	}
	switch {
	case mechanisms["PLAIN"]:
		payload := base64.StdEncoding.EncodeToString([]byte("\x00" + s.client.cfg.Username + "\x00" + s.client.cfg.Password))
		_, _, err := s.roundTrip(ctx, "auth", "AUTH PLAIN %s", payload)
		return err
	case mechanisms["LOGIN"]:
		return s.authLogin(ctx)
	default:
		return &Error{Stage: "auth", Message: "server does not advertise AUTH PLAIN or AUTH LOGIN"}
	}
}

func (s *session) authLogin(ctx context.Context) error {
	if err := s.writeLine(ctx, "AUTH LOGIN"); err != nil {
		return err
	}
	code, message, err := s.readReply(ctx, "auth")
	if err != nil {
		return err
	}
	if code != 334 {
		return replyError("auth", code, message)
	}
	if err = s.writeLine(ctx, "%s", base64.StdEncoding.EncodeToString([]byte(s.client.cfg.Username))); err != nil {
		return err
	}
	code, message, err = s.readReply(ctx, "auth")
	if err != nil {
		return err
	}
	if code != 334 {
		return replyError("auth", code, message)
	}
	if err = s.writeLine(ctx, "%s", base64.StdEncoding.EncodeToString([]byte(s.client.cfg.Password))); err != nil {
		return err
	}
	code, message, err = s.readReply(ctx, "auth")
	if err != nil {
		return err
	}
	return replyError("auth", code, message)
}

func (s *session) deliver(ctx context.Context, envelope string, recipients []string, mime []byte) error {
	if _, _, err := s.roundTrip(ctx, "mail", "MAIL FROM:<%s>", envelope); err != nil {
		return err
	}
	for _, recipient := range recipients {
		if _, _, err := s.roundTrip(ctx, "rcpt", "RCPT TO:<%s>", recipient); err != nil {
			return err
		}
	}
	if err := s.writeLine(ctx, "DATA"); err != nil {
		return err
	}
	code, message, err := s.readReply(ctx, "data")
	if err != nil {
		return err
	}
	if code != 354 {
		return replyError("data", code, message)
	}
	writer := s.proto.DotWriter()
	if _, err = writer.Write(mime); err != nil {
		_ = writer.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("SMTP data upload failed: %w", err)
	}
	if err = writer.Close(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("SMTP data upload failed: %w", err)
	}
	code, message, err = s.readReply(ctx, "data")
	if err != nil {
		if smtpErr := (*Error)(nil); errors.As(err, &smtpErr) && smtpErr.Code > 0 {
			return err
		}
		return &lostResponseError{err: fmt.Errorf("SMTP response was not received after DATA; the server may have accepted the message: %w", err)}
	}
	return replyError("data", code, message)
}

func (s *session) roundTrip(ctx context.Context, stage, format string, args ...any) (int, string, error) {
	if err := s.writeLine(ctx, format, args...); err != nil {
		return 0, "", err
	}
	code, message, err := s.readReply(ctx, stage)
	if err != nil {
		return code, message, err
	}
	if err = replyError(stage, code, message); err != nil {
		return code, message, err
	}
	return code, message, nil
}

func (s *session) writeLine(ctx context.Context, format string, args ...any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.proto.PrintfLine(format, args...); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("SMTP write failed: %w", err)
	}
	return nil
}

func (s *session) readReply(ctx context.Context, stage string) (int, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	code, message, err := s.proto.ReadResponse(0)
	if err != nil {
		if ctx.Err() != nil {
			return 0, "", ctx.Err()
		}
		var protoErr *textproto.Error
		if errors.As(err, &protoErr) {
			return protoErr.Code, protoErr.Msg, &Error{Code: protoErr.Code, Stage: stage, Message: protoErr.Msg}
		}
		return 0, "", fmt.Errorf("SMTP %s failed: %w", stage, err)
	}
	return code, message, nil
}

func replyError(stage string, code int, message string) error {
	if code >= 200 && code < 300 {
		return nil
	}
	return &Error{Code: code, Stage: stage, Message: message}
}

func (s *session) quit() {
	if s == nil {
		return
	}
	if s.proto != nil {
		_ = s.proto.PrintfLine("QUIT")
		_ = s.proto.Close()
		s.proto = nil
	} else if s.conn != nil {
		_ = s.conn.Close()
	}
}

func parseExtensions(message string) map[string]string {
	extensions := map[string]string{}
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		key = strings.ToUpper(key)
		if strings.Contains(key, "=") {
			parts := strings.SplitN(key, "=", 2)
			key = parts[0]
			if value == "" {
				value = parts[1]
			} else if parts[1] != "" {
				value = parts[1] + " " + value
			}
		}
		if key == "AUTH" {
			if existing := extensions[key]; existing != "" && value != "" {
				value = existing + " " + value
			}
		}
		extensions[key] = value
	}
	return extensions
}

func hasExtension(extensions map[string]string, name string) bool {
	_, ok := extensions[strings.ToUpper(name)]
	return ok
}

func extensionValue(extensions map[string]string, name string) string {
	return extensions[strings.ToUpper(name)]
}
