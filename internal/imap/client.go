// Package imap reads mail from an IMAP server.
// Listing and reading use EXAMINE and BODY.PEEK, so messages stay unread
// unless the caller sets the \Seen flag itself. Passwords are never logged.
package imap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/IMisbahk/mailsend/internal/smtp"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const snippetPeekBytes = 2048

// Config is one IMAP account. Password is held in memory for the connection
// and is not written by this package.
type Config struct {
	Host      string
	Port      int
	TLSMode   string
	Username  string
	Password  string
	TLSConfig *tls.Config
}

// Client reads one configured IMAP account.
type Client struct {
	cfg Config
	// protocolTrace receives the raw IMAP conversation when set.
	// Leave it nil: the trace includes the LOGIN password.
	protocolTrace io.Writer
}

// Summary is one inbox row.
type Summary struct {
	UID     uint32    `json:"uid"`
	Date    time.Time `json:"date"`
	From    string    `json:"from"`
	Subject string    `json:"subject"`
	Snippet string    `json:"snippet"`
	Seen    bool      `json:"seen"`
}

// Attachment is a file part that was not downloaded.
type Attachment struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Message is one message prepared for display.
type Message struct {
	UID         uint32       `json:"uid"`
	Folder      string       `json:"folder"`
	Seen        bool         `json:"seen"`
	MarkedRead  bool         `json:"marked_read"`
	From        string       `json:"from"`
	To          []string     `json:"to"`
	Cc          []string     `json:"cc"`
	ReplyTo     []string     `json:"reply_to"`
	Subject     string       `json:"subject"`
	Date        time.Time    `json:"date"`
	MessageID   string       `json:"message_id"`
	Body        string       `json:"body"`
	BodyType    string       `json:"body_type"`
	Attachments []Attachment `json:"attachments"`
}

// ListQuery selects messages in one folder, newest first.
type ListQuery struct {
	Folder string
	Limit  int
	Unread bool
	Since  time.Time
	From   string
}

// New validates config. It does not connect.
func New(cfg Config) (*Client, error) {
	mode, err := normalizeMode(cfg.TLSMode)
	if err != nil {
		return nil, err
	}
	cfg.TLSMode = mode
	cfg.Host = strings.Trim(strings.TrimSpace(cfg.Host), "[]")
	if cfg.Host == "" || strings.Contains(cfg.Host, "://") || strings.ContainsAny(cfg.Host, " \t\r\n/") {
		return nil, fmt.Errorf("invalid IMAP host %q", cfg.Host)
	}
	if cfg.Port == 0 {
		cfg.Port = 993
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("invalid IMAP port %d", cfg.Port)
	}
	cfg.Username = strings.TrimSpace(cfg.Username)
	if cfg.Username == "" || strings.ContainsAny(cfg.Username, "\r\n") {
		return nil, fmt.Errorf("IMAP username is required")
	}
	if err = smtp.ValidatePassword(cfg.Password); err != nil {
		return nil, err
	}
	if cfg.TLSMode != smtp.TLSNone && cfg.TLSConfig == nil {
		cfg.TLSConfig = &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	}
	return &Client{cfg: cfg}, nil
}

func normalizeMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", smtp.TLSSSL:
		return smtp.TLSSSL, nil
	case smtp.TLSStartTLS:
		return smtp.TLSStartTLS, nil
	case smtp.TLSNone:
		return smtp.TLSNone, nil
	default:
		return "", fmt.Errorf("invalid IMAP TLS mode %q (use ssl, starttls, or none)", mode)
	}
}

// CheckLogin connects, sends LOGIN, then LOGOUT. It does not select a mailbox.
func (c *Client) CheckLogin(ctx context.Context) error {
	return c.withSession(ctx, func(*session) error { return nil })
}

// List returns messages newest first. The folder is opened with EXAMINE and
// bodies are fetched with BODY.PEEK, so the seen flag is left unchanged.
func (c *Client) List(ctx context.Context, query ListQuery) ([]Summary, error) {
	if query.Limit < 0 {
		return nil, fmt.Errorf("invalid IMAP list limit %d", query.Limit)
	}
	folder := strings.TrimSpace(query.Folder)
	if folder == "" {
		folder = "INBOX"
	}
	if strings.ContainsAny(folder, "\r\n") || strings.ContainsAny(query.From, "\r\n") {
		return nil, fmt.Errorf("invalid IMAP mailbox or from filter")
	}
	var listed []Summary
	err := c.withMailbox(ctx, folder, true, func(sess *session) error {
		var err error
		listed, err = sess.listMessages(ctx, query)
		return err
	})
	if err != nil {
		return nil, err
	}
	if listed == nil {
		listed = []Summary{}
	}
	return listed, nil
}

// Read loads headers, the text body, and attachment names for one UID.
// The body prefers text/plain and falls back to a tag-stripped text/html.
// Attachment bytes are not fetched. The seen flag is set only when markRead
// is true.
func (c *Client) Read(ctx context.Context, folder string, uid uint32, markRead bool) (Message, error) {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		folder = "INBOX"
	}
	if uid == 0 || strings.ContainsAny(folder, "\r\n") {
		return Message{}, fmt.Errorf("invalid IMAP message %d in folder %q", uid, folder)
	}
	var message Message
	err := c.withMailbox(ctx, folder, !markRead, func(sess *session) error {
		var err error
		message, err = sess.readMessage(ctx, folder, imap.UID(uid), markRead)
		return err
	})
	if err != nil {
		return Message{}, err
	}
	return message, nil
}

func (c *Client) withSession(ctx context.Context, fn func(*session) error) error {
	sess, err := c.dial(ctx)
	if err != nil {
		return c.redact(err)
	}
	defer sess.close()
	if err = sess.login(ctx); err != nil {
		return c.redact(err)
	}
	err = fn(sess)
	_ = sess.logout(ctx)
	return c.redact(err)
}

func (c *Client) withMailbox(ctx context.Context, folder string, readOnly bool, fn func(*session) error) error {
	return c.withSession(ctx, func(sess *session) error {
		if err := sess.open(ctx, folder, readOnly); err != nil {
			return err
		}
		return fn(sess)
	})
}

func (c *Client) redact(err error) error {
	if err == nil || c == nil || c.cfg.Password == "" {
		return err
	}
	if ctxErr := contextError(err); ctxErr != nil {
		return ctxErr
	}
	message := err.Error()
	if strings.Contains(message, c.cfg.Password) {
		return errors.New(strings.ReplaceAll(message, c.cfg.Password, "[redacted]"))
	}
	return err
}

func contextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func (c *Client) dial(ctx context.Context) (*session, error) {
	address := net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.cfg.Port))
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("IMAP connection to %s failed: %w", address, err)
	}
	watchCtx, stop := context.WithCancel(ctx)
	go func() {
		<-watchCtx.Done()
		_ = raw.Close()
	}()
	fail := func(err error) (*session, error) {
		stop()
		_ = raw.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	options := &imapclient.Options{TLSConfig: c.tlsConfig()}
	if c.protocolTrace != nil {
		options.DebugWriter = c.protocolTrace
	}
	var client *imapclient.Client
	switch c.cfg.TLSMode {
	case smtp.TLSSSL:
		tlsConn := tls.Client(raw, c.tlsConfig())
		if err = tlsConn.HandshakeContext(ctx); err != nil {
			return fail(fmt.Errorf("IMAP TLS handshake with %s failed: %w", c.cfg.Host, err))
		}
		client = imapclient.New(tlsConn, options)
	case smtp.TLSStartTLS:
		client, err = imapclient.NewStartTLS(raw, options)
		if err != nil {
			return fail(fmt.Errorf("IMAP STARTTLS with %s failed: %w", c.cfg.Host, err))
		}
	default:
		client = imapclient.New(raw, options)
	}
	return &session{client: client, stop: stop, account: c}, nil
}

func (c *Client) tlsConfig() *tls.Config {
	var cfg *tls.Config
	if c.cfg.TLSConfig != nil {
		cfg = c.cfg.TLSConfig.Clone()
	} else {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if cfg.ServerName == "" {
		cfg.ServerName = c.cfg.Host
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		cfg.MinVersion = tls.VersionTLS12
	}
	cfg.InsecureSkipVerify = false
	return cfg
}

type session struct {
	client  *imapclient.Client
	stop    context.CancelFunc
	account *Client
}

func (s *session) close() {
	if s == nil {
		return
	}
	if s.client != nil {
		_ = s.client.Close()
		s.client = nil
	}
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
}

func (s *session) done(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (s *session) login(ctx context.Context) error {
	err := s.client.Login(s.account.cfg.Username, s.account.cfg.Password).Wait()
	if err = s.done(ctx, err); err != nil {
		return fmt.Errorf("IMAP login failed: %w", err)
	}
	return nil
}

func (s *session) logout(ctx context.Context) error {
	if s.client == nil {
		return nil
	}
	err := s.client.Logout().Wait()
	return s.done(ctx, err)
}

func (s *session) open(ctx context.Context, folder string, readOnly bool) error {
	_, err := s.client.Select(folder, &imap.SelectOptions{ReadOnly: readOnly}).Wait()
	if err = s.done(ctx, err); err != nil {
		return fmt.Errorf("IMAP folder %q: %w", folder, err)
	}
	return nil
}

func (s *session) listMessages(ctx context.Context, query ListQuery) ([]Summary, error) {
	criteria := &imap.SearchCriteria{}
	if query.Unread {
		criteria.NotFlag = []imap.Flag{imap.FlagSeen}
	}
	if !query.Since.IsZero() {
		// IMAP SINCE is a date. Messages before the exact instant are dropped below.
		criteria.Since = dateOnlyUTC(query.Since)
	}
	if from := strings.TrimSpace(query.From); from != "" {
		criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{Key: "From", Value: from})
	}
	data, err := s.client.UIDSearch(criteria, nil).Wait()
	if err = s.done(ctx, err); err != nil {
		return nil, err
	}
	uids := data.AllUIDs()
	if len(uids) == 0 {
		return []Summary{}, nil
	}
	fetched, err := s.fetchUIDs(ctx, uids, &imap.FetchOptions{
		UID:           true,
		Flags:         true,
		Envelope:      true,
		InternalDate:  true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	})
	if err != nil {
		return nil, err
	}
	type row struct {
		summary Summary
		part    bodyPart
		hasBody bool
	}
	rows := make([]row, 0, len(fetched))
	for _, msg := range fetched {
		if msg == nil || msg.UID == 0 {
			continue
		}
		item := row{summary: summaryFrom(msg)}
		if !query.Since.IsZero() {
			when := item.summary.Date
			if when.IsZero() || when.Before(query.Since) {
				continue
			}
		}
		if part, ok := chooseBody(collectParts(msg.BodyStructure)); ok {
			item.part = part
			item.hasBody = true
		}
		rows = append(rows, item)
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].summary.Date.Equal(rows[j].summary.Date) {
			return rows[i].summary.Date.After(rows[j].summary.Date)
		}
		return rows[i].summary.UID > rows[j].summary.UID
	})
	if query.Limit > 0 && len(rows) > query.Limit {
		rows = rows[:query.Limit]
	}
	out := make([]Summary, 0, len(rows))
	for _, item := range rows {
		if item.hasBody {
			text, err := s.peekPart(ctx, imap.UID(item.summary.UID), item.part, snippetPeekBytes)
			if err != nil {
				return nil, err
			}
			item.summary.Snippet = plainSnippet(text)
		}
		out = append(out, item.summary)
	}
	return out, nil
}

func summaryFrom(msg *imapclient.FetchMessageBuffer) Summary {
	summary := Summary{UID: uint32(msg.UID), Date: messageTime(msg).UTC(), Seen: hasFlag(msg.Flags, imap.FlagSeen)}
	if msg.Envelope != nil {
		summary.From = joinAddresses(msg.Envelope.From)
		summary.Subject = strings.TrimSpace(msg.Envelope.Subject)
	}
	return summary
}

func dateOnlyUTC(when time.Time) time.Time {
	when = when.UTC()
	return time.Date(when.Year(), when.Month(), when.Day(), 0, 0, 0, 0, time.UTC)
}

func messageTime(msg *imapclient.FetchMessageBuffer) time.Time {
	if msg == nil {
		return time.Time{}
	}
	if !msg.InternalDate.IsZero() {
		return msg.InternalDate
	}
	if msg.Envelope != nil {
		return msg.Envelope.Date
	}
	return time.Time{}
}

func (s *session) peekPart(ctx context.Context, uid imap.UID, part bodyPart, limit int64) (string, error) {
	section := &imap.FetchItemBodySection{Peek: true, Part: part.path}
	if limit > 0 {
		section.Partial = &imap.SectionPartial{Offset: 0, Size: limit}
	}
	fetched, err := s.fetchUIDs(ctx, []imap.UID{uid}, &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{section},
	})
	if err != nil {
		return "", err
	}
	raw := []byte(nil)
	for _, msg := range fetched {
		if msg != nil && msg.UID == uid {
			raw = msg.FindBodySection(section)
			break
		}
	}
	text, err := partText(part.part, raw)
	if err != nil {
		if limit > 0 {
			return strings.TrimSpace(strings.ToValidUTF8(string(raw), "")), nil
		}
		return "", fmt.Errorf("IMAP message %d body could not be decoded: %w", uid, err)
	}
	return text, nil
}

func (s *session) readMessage(ctx context.Context, folder string, uid imap.UID, markRead bool) (Message, error) {
	fetched, err := s.fetchUIDs(ctx, []imap.UID{uid}, &imap.FetchOptions{
		UID:           true,
		Flags:         true,
		Envelope:      true,
		InternalDate:  true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	})
	if err != nil {
		return Message{}, err
	}
	var msg *imapclient.FetchMessageBuffer
	for _, item := range fetched {
		if item != nil && item.UID == uid {
			msg = item
			break
		}
	}
	if msg == nil {
		return Message{}, fmt.Errorf("IMAP message %d was not found in %s", uid, folder)
	}
	parts := collectParts(msg.BodyStructure)
	message := Message{
		UID:         uint32(uid),
		Folder:      folder,
		Seen:        hasFlag(msg.Flags, imap.FlagSeen),
		To:          []string{},
		Cc:          []string{},
		ReplyTo:     []string{},
		Date:        messageTime(msg).UTC(),
		Attachments: attachmentsFrom(parts),
	}
	if message.Attachments == nil {
		message.Attachments = []Attachment{}
	}
	if msg.Envelope != nil {
		message.From = joinAddresses(msg.Envelope.From)
		message.To = formatAddresses(msg.Envelope.To)
		message.Cc = formatAddresses(msg.Envelope.Cc)
		message.ReplyTo = formatAddresses(msg.Envelope.ReplyTo)
		message.Subject = strings.TrimSpace(msg.Envelope.Subject)
		message.MessageID = strings.TrimSpace(msg.Envelope.MessageID)
	}
	if part, ok := chooseBody(parts); ok {
		message.BodyType = "text/plain"
		if strings.EqualFold(part.part.MediaType(), "text/html") {
			message.BodyType = "text/html"
		}
		body, err := s.peekPart(ctx, uid, part, 0)
		if err != nil {
			return Message{}, err
		}
		message.Body = body
	}
	if markRead {
		if err = s.storeSeen(ctx, uid); err != nil {
			return Message{}, err
		}
		message.MarkedRead = true
		message.Seen = true
	}
	return message, nil
}

func (s *session) storeSeen(ctx context.Context, uid imap.UID) error {
	err := s.client.Store(imap.UIDSetNum(uid), &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Flags:  []imap.Flag{imap.FlagSeen},
		Silent: true,
	}, nil).Close()
	if err = s.done(ctx, err); err != nil {
		return fmt.Errorf("IMAP message %d could not be marked read: %w", uid, err)
	}
	return nil
}

func (s *session) fetchUIDs(ctx context.Context, uids []imap.UID, options *imap.FetchOptions) ([]*imapclient.FetchMessageBuffer, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	var set imap.UIDSet
	set.AddNum(uids...)
	fetched, err := s.client.Fetch(set, options).Collect()
	if err = s.done(ctx, err); err != nil {
		return nil, err
	}
	return fetched, nil
}
