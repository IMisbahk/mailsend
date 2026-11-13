package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/example-account/mailsend/internal/auth"
	"github.com/example-account/mailsend/internal/imap"
	"github.com/example-account/mailsend/internal/smtp"
	"github.com/spf13/cobra"
)

func inboxCmd() *cobra.Command {
	var folder, from string
	var limit int
	var unread bool
	since := spanValue(7 * 24 * time.Hour)
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "List messages in an SMTP account's IMAP mailbox",
		Long: `List messages newest first. The mailbox is opened read-only with EXAMINE and bodies are fetched with BODY.PEEK, so listing does not mark mail as read.

The account must be SMTP with IMAP configured. Run mailsend account set-imap to enable it. The SMTP password in the OS keychain is reused.

--since defaults to 7d. 0s lists without a time limit.`,
		Example: `  mailsend inbox --account example-account
  mailsend inbox --account example-account --unread --since 7d --from ada@example.com --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 1 || limit > 1000 {
				return fmt.Errorf("--limit must be between 1 and 1000")
			}
			if since.Duration() < 0 {
				return fmt.Errorf("--since cannot be negative")
			}
			name := selectedAccount()
			client, err := openIMAPClient(name)
			if err != nil {
				return err
			}
			var sinceAt time.Time
			if since.Duration() > 0 {
				sinceAt = time.Now().Add(-since.Duration())
			}
			auth.Debugf("inbox: account %s folder %s limit %d", name, folder, limit)
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()
			messages, err := client.List(ctx, imap.ListQuery{
				Folder: folder, Limit: limit, Unread: unread, Since: sinceAt, From: from,
			})
			if err != nil {
				return err
			}
			if messages == nil {
				messages = []imap.Summary{}
			}
			payload := map[string]any{"account": name, "folder": folderName(folder), "messages": messages}
			return output(payload, func() {
				if len(messages) == 0 {
					fmt.Fprintln(commandStdout, "No messages.")
					return
				}
				for _, message := range messages {
					when := ""
					if !message.Date.IsZero() {
						when = message.Date.UTC().Format(time.RFC3339)
					}
					seen := "unread"
					if message.Seen {
						seen = "seen"
					}
					fmt.Fprintf(commandStdout, "%d\t%s\t%s\t%s\t%s\n%s\n", message.UID, when, seen, message.From, message.Subject, message.Snippet)
				}
			})
		},
	}
	cmd.Flags().StringVar(&folder, "folder", "INBOX", "Mailbox to list")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum messages to show, newest first")
	cmd.Flags().BoolVar(&unread, "unread", false, "Only messages without the seen flag")
	cmd.Flags().Var(&since, "since", "How far back to list (7d, 12h); 0s lists without a time limit")
	cmd.Flags().StringVar(&from, "from", "", "Only messages whose From header contains this text")
	return cmd
}

func readCmd() *cobra.Command {
	var folder string
	var markRead bool
	cmd := &cobra.Command{
		Use:   "read <uid>",
		Short: "Show one IMAP message",
		Long: `Show headers and the text/plain body of one message. If there is no plain part, the text/html part is shown with tags removed. Attachment names and sizes are listed; attachment bytes are not downloaded.

The message stays unread unless --mark-read is set. The mailbox is opened with EXAMINE unless --mark-read selects it and sets the \Seen flag.`,
		Example: `  mailsend read 42 --account example-account
  mailsend read 42 --folder INBOX --mark-read --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			uid, err := parseUID(args[0])
			if err != nil {
				return err
			}
			name := selectedAccount()
			client, err := openIMAPClient(name)
			if err != nil {
				return err
			}
			auth.Debugf("read: account %s folder %s uid %d mark-read %v", name, folder, uid, markRead)
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()
			message, err := client.Read(ctx, folder, uid, markRead)
			if err != nil {
				return err
			}
			view := struct {
				Account string `json:"account"`
				imap.Message
			}{Account: name, Message: message}
			return output(view, func() {
				when := ""
				if !message.Date.IsZero() {
					when = message.Date.UTC().Format(time.RFC3339)
				}
				seen := "unread"
				if message.Seen {
					seen = "seen"
				}
				fmt.Fprintf(commandStdout, "Folder: %s\nUID: %d\nFrom: %s\nTo: %s\nCc: %s\nReply-To: %s\nDate: %s\nSubject: %s\nMessage-ID: %s\nSeen: %s\n", folderName(message.Folder), message.UID, message.From, strings.Join(message.To, ", "), strings.Join(message.Cc, ", "), strings.Join(message.ReplyTo, ", "), when, message.Subject, message.MessageID, seen)
				if message.BodyType == "text/html" {
					fmt.Fprintln(commandStdout, "Body (from HTML):")
				} else {
					fmt.Fprintln(commandStdout, "Body:")
				}
				fmt.Fprintf(commandStdout, "%s\n", message.Body)
				if len(message.Attachments) == 0 {
					fmt.Fprintln(commandStdout, "Attachments: none")
				} else {
					fmt.Fprintln(commandStdout, "Attachments:")
					for _, attachment := range message.Attachments {
						fmt.Fprintf(commandStdout, "  %s (%d bytes)\n", attachment.Name, attachment.Size)
					}
				}
				if message.MarkedRead {
					fmt.Fprintln(commandStdout, "Marked read.")
				}
			})
		},
	}
	cmd.Flags().StringVar(&folder, "folder", "INBOX", "Mailbox containing the message")
	cmd.Flags().BoolVar(&markRead, "mark-read", false, "Set the seen flag after the message is loaded")
	return cmd
}

func folderName(folder string) string {
	if strings.TrimSpace(folder) == "" {
		return "INBOX"
	}
	return folder
}

func parseUID(value string) (uint32, error) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
	if err != nil || parsed == 0 {
		return 0, fmt.Errorf("invalid message UID %q", value)
	}
	return uint32(parsed), nil
}

func openIMAPClient(name string) (*imap.Client, error) {
	cfg, err := auth.LookupProvider(name)
	if err != nil {
		return nil, err
	}
	if cfg.Provider != auth.ProviderSMTP {
		return nil, fmt.Errorf("account %q is not an SMTP account", name)
	}
	endpoint, ok := cfg.IMAP()
	if !ok {
		return nil, fmt.Errorf("account %q has no IMAP settings; run mailsend account set-imap %s", name, name)
	}
	password, err := auth.SMTPPassword(name)
	if err != nil {
		return nil, fmt.Errorf("account %q has no SMTP password in the OS keychain; run mailsend account add --provider smtp", name)
	}
	clientCfg := imap.Config{
		Host: endpoint.Host, Port: endpoint.Port, TLSMode: endpoint.TLS, Username: endpoint.Username, Password: password,
	}
	if endpoint.TLS != smtp.TLSNone {
		tlsConfig, tlsErr := smtp.ClientTLSConfig(endpoint.Host)
		if tlsErr != nil {
			return nil, tlsErr
		}
		clientCfg.TLSConfig = tlsConfig
	}
	auth.Debugf("imap: %s:%d %s as %s", endpoint.Host, endpoint.Port, endpoint.TLS, endpoint.Username)
	return imap.New(clientCfg)
}
