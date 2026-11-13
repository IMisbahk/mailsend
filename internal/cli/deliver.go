package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/IMisbahk/mailsend/internal/auth"
	mailmsg "github.com/IMisbahk/mailsend/internal/mail"
	"github.com/IMisbahk/mailsend/internal/smtp"
	"github.com/IMisbahk/mailsend/internal/store"
)

var (
	commandStdout io.Writer = os.Stdout
	commandStderr io.Writer = os.Stderr
)

type mailSender interface {
	Send(ctx context.Context, built mailmsg.BuiltMessage) (messageID string, ambiguous bool, err error)
	FromAddress() string
	DefaultReplyTo() string
	Provider() string
	Probe(ctx context.Context) error
}

type gmailSender struct {
	client *auth.Client
}

func (g gmailSender) Send(ctx context.Context, built mailmsg.BuiltMessage) (string, bool, error) {
	return g.client.Send(ctx, built.Raw)
}
func (g gmailSender) FromAddress() string    { return g.client.Account.Email }
func (g gmailSender) DefaultReplyTo() string { return "" }
func (g gmailSender) Provider() string       { return auth.ProviderGmail }
func (g gmailSender) Probe(ctx context.Context) error {
	_, err := g.client.Profile(ctx)
	return err
}

func openSender(ctx context.Context, name string) (mailSender, error) {
	cfg, err := auth.LookupProvider(name)
	if err != nil {
		return nil, err
	}
	switch cfg.Provider {
	case auth.ProviderSMTP:
		password, err := auth.SMTPPassword(name)
		if err != nil {
			return nil, fmt.Errorf("account %q has no SMTP password in the OS keychain; run mailsend account add --provider smtp", name)
		}
		client, err := smtp.New(smtp.Config{
			Host:     cfg.Host,
			Port:     cfg.Port,
			TLSMode:  cfg.TLS,
			Username: cfg.Username,
			Password: password,
			From:     cfg.From,
			ReplyTo:  cfg.ReplyTo,
		})
		if err != nil {
			return nil, err
		}
		return client, nil
	case auth.ProviderGmail:
		client, err := auth.Load(name)
		if err != nil {
			return nil, err
		}
		if client.Account.Email == "" {
			auth.Debugf("sender: no stored email for %s; fetching Google identity", name)
			idCtx, cancel := context.WithTimeout(ctx, auth.IdentityTimeout)
			profile, err := client.Profile(idCtx)
			cancel()
			if err != nil {
				return nil, err
			}
			client.Account.Email = profile.EmailAddress
		}
		return gmailSender{client: client}, nil
	default:
		return nil, fmt.Errorf("account %q has unsupported provider %q", name, cfg.Provider)
	}
}

func composeSendMessage(name string, msg mailmsg.Message) (mailmsg.BuiltMessage, error) {
	if cfg, err := auth.LookupProvider(name); err == nil && cfg.Provider == auth.ProviderSMTP && cfg.From != "" {
		msg.Account = cfg.From
		if msg.ReplyTo == "" {
			msg.ReplyTo = cfg.ReplyTo
		}
		msg.IncludeMessageID = true
		msg.OmitBccHeader = true
	} else if email := auth.Email(name); email != "" {
		msg.Account = email
	}
	return msg.Build()
}

func applySenderIdentity(msg *mailmsg.Message, sender mailSender) error {
	from := strings.TrimSpace(sender.FromAddress())
	if from == "" {
		return fmt.Errorf("account has no From address")
	}
	msg.Account = from
	if strings.TrimSpace(msg.ReplyTo) == "" {
		msg.ReplyTo = sender.DefaultReplyTo()
	}
	if sender.Provider() == auth.ProviderSMTP {
		msg.IncludeMessageID = true
		msg.OmitBccHeader = true
	}
	return nil
}

func sendOne(ctx context.Context, sender mailSender, accountName string, msg mailmsg.Message, kind string) error {
	if err := applySenderIdentity(&msg, sender); err != nil {
		return err
	}
	built, err := msg.Build()
	if err != nil {
		return err
	}
	db, err := database()
	if err != nil {
		return err
	}
	defer db.Close()
	id, row, err := createSendJob(db, accountName, kind, "sending", msg)
	if err != nil {
		return err
	}
	return sendSavedOne(ctx, db, sender, row, built, id)
}

func createSendJob(db *store.DB, accountName, kind, status string, msg mailmsg.Message) (string, store.Recipient, error) {
	id, err := store.NewID()
	if err != nil {
		return "", store.Recipient{}, err
	}
	record := store.Recipient{
		Email:   strings.Join(msg.To, ", "),
		ToJSON:  joinJSON(msg.To),
		CcJSON:  joinJSON(msg.Cc),
		BccJSON: joinJSON(msg.Bcc),
		Subject: msg.Subject,
		Body:    msg.Text,
		HTML:    msg.HTML,
		Status:  "pending",
	}
	if err = db.CreateJob(store.Job{ID: id, Kind: kind, Status: status, Account: accountName, Subject: msg.Subject}, "", []store.Recipient{record}); err != nil {
		return "", store.Recipient{}, err
	}
	rows, err := db.Recipients(id)
	if err != nil {
		return "", store.Recipient{}, err
	}
	if len(rows) == 0 {
		return "", store.Recipient{}, errors.New("could not load saved send job")
	}
	return id, rows[0], nil
}

func formatCommandError(err error) map[string]any {
	payload := map[string]any{"message": err.Error(), "type": "mailsend_error"}
	var smtpErr *smtp.Error
	if errors.As(err, &smtpErr) {
		payload["type"] = "smtp_error"
		payload["stage"] = smtpErr.Stage
		payload["temporary"] = smtpErr.Temporary()
		payload["permanent"] = smtpErr.Permanent()
		if smtpErr.Code > 0 {
			payload["smtp_code"] = smtpErr.Code
		}
	}
	return map[string]any{"error": payload}
}

func printCommandError(err error) {
	if global.json {
		encoded, _ := json.Marshal(formatCommandError(err))
		fmt.Fprintln(commandStderr, string(encoded))
		return
	}
	fmt.Fprintln(commandStderr, "Error:", err)
}
