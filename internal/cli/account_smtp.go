package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/IMisbahk/mailsend/internal/auth"
	mailmsg "github.com/IMisbahk/mailsend/internal/mail"
	"github.com/IMisbahk/mailsend/internal/smtp"
	"github.com/spf13/cobra"
)

func accountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Show the selected account",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return accountShow(cmd)
		},
	}
	cmd.AddCommand(accountAddCmd(), accountTestCmd())
	return cmd
}

func accountShow(cmd *cobra.Command) error {
	name := selectedAccount()
	cfg, err := auth.LookupProvider(name)
	if err != nil {
		return err
	}
	if cfg.Provider == auth.ProviderSMTP {
		if !auth.HasSMTPPassword(name) {
			return fmt.Errorf("account %q has no SMTP password in the OS keychain; run mailsend account add --provider smtp", name)
		}
		replyTo := cfg.ReplyTo
		if replyTo == "" {
			replyTo = "(none)"
		}
		value := map[string]any{
			"account":         name,
			"provider":        auth.ProviderSMTP,
			"email":           cfg.From,
			"from":            cfg.From,
			"host":            cfg.Host,
			"port":            cfg.Port,
			"tls":             cfg.TLS,
			"username":        cfg.Username,
			"reply_to":        cfg.ReplyTo,
			"authenticated":   true,
			"password_stored": true,
		}
		return output(value, func() {
			fmt.Printf("Account: %s\nProvider: smtp\nFrom: %s\nHost: %s\nPort: %d\nTLS: %s\nUsername: %s\nReply-To: %s\nPassword: stored in the OS keychain\n", name, cfg.From, cfg.Host, cfg.Port, cfg.TLS, cfg.Username, replyTo)
		})
	}
	auth.Debugf("account: loading %s", name)
	client, err := loadGmail(name)
	if err != nil {
		return err
	}
	email, warning, err := gmailIdentity(cmd.Context(), client)
	if err != nil {
		return err
	}
	value := map[string]any{
		"account":       name,
		"provider":      auth.ProviderGmail,
		"email":         email,
		"authenticated": true,
		"aliases":       []string{},
		"aliases_note":  "Alias listing requires Gmail settings scope, which mailsend does not request. The From address remains the authenticated account.",
	}
	if warning != "" {
		value["identity_error"] = warning
	}
	return output(value, func() {
		fmt.Printf("Account: %s\nProvider: gmail\nEmail: %s\nAuthentication: active\nAliases: not queried (no settings scope)\n", name, email)
		if warning != "" {
			fmt.Printf("Identity check failed: %s\n", warning)
		}
	})
}

var loadGmail = auth.Load

func gmailIdentity(ctx context.Context, client *auth.Client) (string, string, error) {
	auth.Debugf("account: fetching Google identity for %s", client.Account.Name)
	ctx, cancel := context.WithTimeout(ctx, auth.IdentityTimeout)
	defer cancel()
	profile, err := client.Profile(ctx)
	if err == nil && profile.EmailAddress != "" {
		auth.Debugf("account: identity is %s", profile.EmailAddress)
		return profile.EmailAddress, "", nil
	}
	if err == nil {
		err = fmt.Errorf("Google user-info response did not contain an email address")
	}
	stored := strings.TrimSpace(client.Account.Email)
	if stored != "" {
		auth.Debugf("account: identity check failed (%v); using stored email %s", err, stored)
		return stored, err.Error(), nil
	}
	return "", "", err
}

func accountAddCmd() *cobra.Command {
	var provider, host, tlsMode, username, from, replyTo string
	var port int
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or update an SMTP account",
		Long: `Add an SMTP account. The password is stored in the OS keychain and is never written to the config file or accepted as a command-line flag.

Read the password from an interactive prompt, from stdin with --password-stdin, or from the MAILSEND_SMTP_PASSWORD environment variable.

Example:
  mailsend account add sunbeam-smtp --provider smtp --host mail.example.com --port 587 --tls starttls --username misbah@sunbeam.agency --from misbah@sunbeam.agency`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch strings.ToLower(strings.TrimSpace(provider)) {
			case auth.ProviderSMTP:
			case auth.ProviderGmail:
				return fmt.Errorf("Gmail accounts are added with mailsend login")
			default:
				return fmt.Errorf("use --provider smtp to add an SMTP account")
			}
			cfg, err := auth.NormalizeSMTP(auth.ProviderConfig{
				Host: host, Port: port, TLS: tlsMode, Username: username, From: from, ReplyTo: replyTo,
			})
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("port") && (port < 1 || port > 65535) {
				return fmt.Errorf("invalid SMTP port %d", port)
			}
			password, err := readSMTPPassword(cmd, passwordStdin)
			if err != nil {
				return err
			}
			status, err := auth.SaveSMTPAccount(args[0], cfg, password)
			if err != nil {
				return err
			}
			return output(map[string]any{
				"status": status, "account": args[0], "provider": auth.ProviderSMTP,
				"host": cfg.Host, "port": cfg.Port, "tls": cfg.TLS, "username": cfg.Username, "from": cfg.From, "reply_to": cfg.ReplyTo,
			}, func() {
				fmt.Printf("%s SMTP account %s (%s) via %s:%d %s\n", strings.ToUpper(status[:1])+status[1:], args[0], cfg.From, cfg.Host, cfg.Port, cfg.TLS)
			})
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "Account provider (smtp)")
	cmd.Flags().StringVar(&host, "host", "", "SMTP server hostname")
	cmd.Flags().IntVar(&port, "port", 0, "SMTP port (default 587 for starttls, 465 for ssl, 25 for none)")
	cmd.Flags().StringVar(&tlsMode, "tls", smtp.TLSStartTLS, "TLS mode: starttls, ssl, or none")
	cmd.Flags().StringVar(&username, "username", "", "SMTP username")
	cmd.Flags().StringVar(&from, "from", "", "From address, for example misbah@sunbeam.agency")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "Optional default Reply-To address")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read the SMTP password from stdin")
	return cmd
}

func accountTestCmd() *cobra.Command {
	var sendTo string
	cmd := &cobra.Command{
		Use:   "test [name]",
		Short: "Check that an account can authenticate",
		Long:  "For an SMTP account, connects, negotiates TLS, and authenticates without sending. For a Gmail account, checks the OAuth token with Google. --send-to delivers one test message and records it in history.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := selectedAccount()
			if len(args) == 1 {
				name = args[0]
				if err := auth.ValidateAccountName(name); err != nil {
					return err
				}
			}
			var recipients []string
			if sendTo != "" {
				parsed, err := mailmsg.ParseAddresses([]string{sendTo})
				if err != nil {
					return err
				}
				recipients = parsed
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()
			sender, err := openSender(ctx, name)
			if err != nil {
				return err
			}
			if err = sender.Probe(ctx); err != nil {
				return err
			}
			if sendTo == "" {
				value := map[string]any{"status": "ok", "account": name, "provider": sender.Provider(), "from": sender.FromAddress(), "authenticated": true}
				warning := ""
				if sender.Provider() == auth.ProviderSMTP {
					cfg, lookupErr := auth.LookupProvider(name)
					if lookupErr == nil && cfg.TLS == smtp.TLSNone {
						warning = "Connected without TLS. The password was sent in cleartext because this account uses --tls none."
						value["warning"] = warning
					}
				}
				return output(value, func() {
					fmt.Printf("Authenticated %s (%s) as %s\n", name, sender.Provider(), sender.FromAddress())
					if warning != "" {
						fmt.Println(warning)
					}
					fmt.Println("No message was sent.")
				})
			}
			msg := mailmsg.Message{To: recipients, Subject: "mailsend connectivity test", Text: "This is a mailsend connectivity test. No action is required."}
			return sendOne(ctx, sender, name, msg, "test")
		},
	}
	cmd.Flags().StringVar(&sendTo, "send-to", "", "After authentication succeeds, send one test message to this address")
	return cmd
}

func quotaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "quota",
		Short: "Show available sending information",
		Long:  "Neither the Gmail API nor a generic SMTP server exposes a reliable remaining-send quota. This command explains that limit. The local bulk --daily-limit still applies to both providers.",
		RunE: func(cmd *cobra.Command, args []string) error {
			name := selectedAccount()
			cfg, err := auth.LookupProvider(name)
			if err != nil {
				return err
			}
			if cfg.Provider == auth.ProviderSMTP {
				if !auth.HasSMTPPassword(name) {
					return fmt.Errorf("account %q has no SMTP password in the OS keychain; run mailsend account add --provider smtp", name)
				}
				message := "SMTP does not provide a remaining-send quota. mailsend's local --daily-limit still applies to bulk sends. The SMTP server may enforce its own limits."
				value := map[string]any{
					"account": name, "provider": auth.ProviderSMTP, "email": cfg.From,
					"remaining_sends": nil, "quota_available": false, "message": message,
				}
				return output(value, func() {
					fmt.Printf("Account: %s (%s)\nProvider: smtp\nRemaining sends: unavailable from SMTP\n%s\n", name, cfg.From, message)
				})
			}
			auth.Debugf("quota: loading %s", name)
			client, err := loadGmail(name)
			if err != nil {
				return err
			}
			email, warning, err := gmailIdentity(cmd.Context(), client)
			if err != nil {
				return err
			}
			value := map[string]any{
				"account": name, "provider": auth.ProviderGmail, "email": email,
				"remaining_sends": nil, "quota_available": false,
				"message": "Gmail API does not provide a reliable remaining-send count. Google sending limits still apply; mailsend cannot bypass them.",
			}
			if warning != "" {
				value["identity_error"] = warning
			}
			return output(value, func() {
				fmt.Printf("Account: %s (%s)\nProvider: gmail\nRemaining sends: unavailable from Gmail API\nGmail sending limits apply and cannot be bypassed.\n", name, email)
				if warning != "" {
					fmt.Printf("Identity check failed: %s\n", warning)
				}
			})
		},
	}
}
