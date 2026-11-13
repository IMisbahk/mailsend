package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/IMisbahk/mailsend/internal/auth"
	"github.com/spf13/cobra"
)

// sentCheckTimeout is the whole-command budget. Any failure, including a
// missing scope or a stalled keychain or network call, returns before this.
var sentCheckTimeout = 20 * time.Second

func sentCheckCmd() *cobra.Command {
	var to, subject string
	since := spanValue(30 * 24 * time.Hour)
	cmd := &cobra.Command{
		Use:   "sent-check",
		Short: "Search Sent for messages to one recipient",
		Long: `Search the account's Sent folder. Gmail accounts use the Gmail API. SMTP accounts with IMAP search the Sent mailbox (SPECIAL-USE \Sent, or Sent, Sent Items, Sent Messages, INBOX.Sent). Exit 0 means nothing matched. Exit 3 means at least one message matched. Exit 2 means the search failed, including a Gmail token that does not have gmail.readonly or an SMTP account without IMAP.

Existing send-only Gmail tokens keep working for mailsend send. This command does not. Re-run mailsend login after upgrading so Google grants the read scope. A 403 from a missing scope is reported as "scope missing, re-run mailsend login" and is not treated as an empty Sent folder. An SMTP account needs mailsend account set-imap before this command can search.`,
		Example: `  mailsend sent-check --account acme --to ada@example.com --since 30d --json
  mailsend sent-check --account example-account --to ada@example.com --subject "Hello" --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			to = strings.TrimSpace(to)
			if to == "" {
				return &statusExit{code: 2, err: fmt.Errorf("--to is required")}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), sentCheckTimeout)
			defer cancel()
			account := selectedAccount()
			var sinceAt time.Time
			if since.Duration() > 0 {
				sinceAt = time.Now().Add(-since.Duration())
			}
			auth.Debugf("sent-check: account %s to %s since %s", account, to, since.Duration())
			listed, err := searchSent(ctx, account, to, subject, sinceAt)
			if err != nil {
				return &statusExit{code: 2, err: sentCheckFailure(err)}
			}
			return finishSentCheck(account, to, subject, sinceAt, listed)
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "Recipient address (required)")
	cmd.Flags().StringVar(&subject, "subject", "", "Only messages with this subject")
	cmd.Flags().Var(&since, "since", "How far back to search (30d, 12h); 0s searches without a time limit")
	return cmd
}

func searchSent(ctx context.Context, account, to, subject string, sinceAt time.Time) (auth.SentList, error) {
	cfg, err := auth.LookupProvider(account)
	if err != nil {
		return auth.SentList{}, err
	}
	if cfg.Provider == auth.ProviderSMTP {
		auth.Debugf("sent-check: searching IMAP Sent")
		if _, ok := cfg.IMAP(); !ok {
			return auth.SentList{}, fmt.Errorf("account %q has no IMAP settings; run mailsend account set-imap %s", account, account)
		}
		client, err := openIMAPClient(account)
		if err != nil {
			return auth.SentList{}, err
		}
		page, err := client.SearchSent(ctx, to, subject, sinceAt)
		if err != nil {
			return auth.SentList{}, err
		}
		messages := make([]auth.SentMessage, 0, len(page.Messages))
		for _, hit := range page.Messages {
			messages = append(messages, auth.SentMessage{
				ID: strconv.FormatUint(uint64(hit.UID), 10), Date: hit.Date.UTC(), To: hit.Recipient, Subject: hit.Subject,
			})
		}
		return auth.SentList{Messages: messages, Truncated: page.Truncated}, nil
	}
	auth.Debugf("sent-check: opening Gmail client")
	client, err := openGmailClient(ctx, account)
	if err != nil {
		return auth.SentList{}, err
	}
	auth.Debugf("sent-check: searching Gmail Sent")
	return client.ListSent(ctx, to, subject, sinceAt)
}

func finishSentCheck(account, to, subject string, sinceAt time.Time, listed auth.SentList) error {
	messages := listed.Messages
	if messages == nil {
		messages = []auth.SentMessage{}
	}
	payload := map[string]any{
		"account":  account,
		"to":       to,
		"messages": messages,
	}
	if !sinceAt.IsZero() {
		payload["since"] = sinceAt.UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(subject) != "" {
		payload["subject"] = strings.TrimSpace(subject)
	}
	if listed.Truncated {
		payload["truncated"] = true
	}
	if err := output(payload, func() {
		if len(messages) == 0 {
			fmt.Fprintln(commandStdout, "No matching Sent messages.")
			return
		}
		for _, message := range messages {
			when := ""
			if !message.Date.IsZero() {
				when = message.Date.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(commandStdout, "%s\t%s\t%s\t%s\n", message.ID, when, message.To, message.Subject)
		}
	}); err != nil {
		return &statusExit{code: 2, err: err}
	}
	if len(messages) > 0 {
		return &statusExit{code: 3}
	}
	return nil
}

func sentCheckFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, auth.ErrScopeMissing) {
		return auth.ErrScopeMissing
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("sent-check timed out after %s", sentCheckTimeout)
	}
	return err
}

var openGmailClient = func(ctx context.Context, name string) (*auth.Client, error) {
	sender, err := openSender(ctx, name)
	if err != nil {
		return nil, err
	}
	gmail, ok := sender.(gmailSender)
	if !ok || gmail.client == nil {
		return nil, fmt.Errorf("sent-check requires a Gmail account")
	}
	return gmail.client, nil
}
