package cli

import (
	"context"
	"errors"
	"fmt"
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
		Short: "Search Gmail Sent for messages to one recipient",
		Long: `Search the account's Gmail Sent folder. Exit 0 means nothing matched. Exit 3 means at least one message matched. Exit 2 means the search failed, including a token that does not have gmail.readonly.

Existing send-only tokens keep working for mailsend send. This command does not. Re-run mailsend login after upgrading so Google grants the read scope. A 403 from a missing scope is reported as "scope missing, re-run mailsend login" and is not treated as an empty Sent folder.`,
		Example: `  mailsend sent-check --account sunbeam --to ada@example.com --since 30d --json
  mailsend sent-check --account sunbeam --to ada@example.com --subject "Hello" --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			to = strings.TrimSpace(to)
			if to == "" {
				return &statusExit{code: 2, err: fmt.Errorf("--to is required")}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), sentCheckTimeout)
			defer cancel()
			account := selectedAccount()
			auth.Debugf("sent-check: account %s to %s since %s", account, to, since.Duration())
			auth.Debugf("sent-check: opening Gmail client")
			client, err := openGmailClient(ctx, account)
			if err != nil {
				return &statusExit{code: 2, err: sentCheckFailure(err)}
			}
			var sinceAt time.Time
			if since.Duration() > 0 {
				sinceAt = time.Now().Add(-since.Duration())
			}
			auth.Debugf("sent-check: searching Gmail Sent")
			listed, err := client.ListSent(ctx, to, subject, sinceAt)
			if err != nil {
				return &statusExit{code: 2, err: sentCheckFailure(err)}
			}
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
			if err = output(payload, func() {
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
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "Recipient address (required)")
	cmd.Flags().StringVar(&subject, "subject", "", "Only messages with this subject")
	cmd.Flags().Var(&since, "since", "How far back to search (30d, 12h); 0s searches without a time limit")
	return cmd
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
