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
	cmd.AddCommand(accountAddCmd(), accountSetIMAPCmd(), accountTestCmd())
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
			"imap_configured": false,
		}
		imapLine := "IMAP: not configured"
		if endpoint, ok := cfg.IMAP(); ok {
			value["imap_configured"] = true
			value["imap"] = map[string]any{
				"host": endpoint.Host, "port": endpoint.Port, "tls": endpoint.TLS, "username": endpoint.Username,
			}
			imapLine = fmt.Sprintf("IMAP: %s:%d %s as %s", endpoint.Host, endpoint.Port, endpoint.TLS, endpoint.Username)
		}
		return output(value, func() {
			fmt.Printf("Account: %s\nProvider: smtp\nFrom: %s\nHost: %s\nPort: %d\nTLS: %s\nUsername: %s\nReply-To: %s\n%s\nPassword: stored in the OS keychain\n", name, cfg.From, cfg.Host, cfg.Port, cfg.TLS, cfg.Username, replyTo, imapLine)
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
	var imapFlags imapCLIFlags
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or update an SMTP account",
		Long: `Add an SMTP account. The password is stored in the OS keychain and is never written to the config file or accepted as a command-line flag.

Read the password from an interactive prompt, from stdin with --password-stdin, or from the MAILSEND_SMTP_PASSWORD environment variable.

Optional IMAP flags store the mailbox server in the same account. --imap-host defaults to the SMTP host, --imap-port to 993, --imap-tls to ssl, and --imap-username to the SMTP username. The SMTP password is reused. To enable IMAP on an account that already has a password, use mailsend account set-imap.

Example:
  mailsend account add acme-smtp --provider smtp --host mail.example.com --port 587 --tls starttls --username you@example.com --from you@example.com
  mailsend account set-imap example-account`,
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
			var existing auth.ProviderConfig
			if looked, lookupErr := auth.LookupProvider(args[0]); lookupErr == nil && looked.Provider == auth.ProviderSMTP {
				existing = looked
			}
			imapCfg := resolveIMAPFlags(cmd, imapFlags, cfg.Host, cfg.Username, existing, false)
			cfg.IMAPHost = imapCfg.IMAPHost
			cfg.IMAPPort = imapCfg.IMAPPort
			cfg.IMAPTLS = imapCfg.IMAPTLS
			cfg.IMAPUsername = imapCfg.IMAPUsername
			password, err := readSMTPPassword(cmd, passwordStdin)
			if err != nil {
				return err
			}
			status, err := auth.SaveSMTPAccount(args[0], cfg, password)
			if err != nil {
				return err
			}
			result := map[string]any{
				"status": status, "account": args[0], "provider": auth.ProviderSMTP,
				"host": cfg.Host, "port": cfg.Port, "tls": cfg.TLS, "username": cfg.Username, "from": cfg.From, "reply_to": cfg.ReplyTo,
			}
			if endpoint, ok := cfg.IMAP(); ok {
				result["imap"] = map[string]any{"host": endpoint.Host, "port": endpoint.Port, "tls": endpoint.TLS, "username": endpoint.Username}
			}
			return output(result, func() {
				fmt.Printf("%s SMTP account %s (%s) via %s:%d %s\n", strings.ToUpper(status[:1])+status[1:], args[0], cfg.From, cfg.Host, cfg.Port, cfg.TLS)
				if endpoint, ok := cfg.IMAP(); ok {
					fmt.Printf("IMAP: %s:%d %s as %s\n", endpoint.Host, endpoint.Port, endpoint.TLS, endpoint.Username)
				}
			})
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "Account provider (smtp)")
	cmd.Flags().StringVar(&host, "host", "", "SMTP server hostname")
	cmd.Flags().IntVar(&port, "port", 0, "SMTP port (default 587 for starttls, 465 for ssl, 25 for none)")
	cmd.Flags().StringVar(&tlsMode, "tls", smtp.TLSStartTLS, "TLS mode: starttls, ssl, or none")
	cmd.Flags().StringVar(&username, "username", "", "SMTP username")
	cmd.Flags().StringVar(&from, "from", "", "From address, for example you@example.com")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "Optional default Reply-To address")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read the SMTP password from stdin")
	addIMAPFlags(cmd, &imapFlags)
	return cmd
}

func accountSetIMAPCmd() *cobra.Command {
	var imapFlags imapCLIFlags
	cmd := &cobra.Command{
		Use:   "set-imap <name>",
		Short: "Enable or update IMAP on an SMTP account",
		Long: `Store IMAP settings on an existing SMTP account without changing the keychain password.

The password already stored for SMTP is reused. Omit a flag to keep the current value, or to use the default when IMAP is not configured yet. Defaults are the SMTP host, port 993, TLS mode ssl, and the SMTP username.

Example:
  mailsend account set-imap example-account
  mailsend account set-imap example-account --imap-host mail.example.com --imap-port 993 --imap-tls ssl`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := auth.ValidateAccountName(args[0]); err != nil {
				return err
			}
			existing, err := auth.LookupProvider(args[0])
			if err != nil {
				return err
			}
			if existing.Provider != auth.ProviderSMTP {
				return fmt.Errorf("account %q is not an SMTP account; add it with mailsend account add --provider smtp", args[0])
			}
			imapCfg := resolveIMAPFlags(cmd, imapFlags, existing.Host, existing.Username, existing, true)
			if err = auth.SetIMAP(args[0], imapCfg); err != nil {
				return err
			}
			saved, err := auth.LookupProvider(args[0])
			if err != nil {
				return err
			}
			endpoint, ok := saved.IMAP()
			if !ok {
				return fmt.Errorf("IMAP settings were not saved")
			}
			status := "enabled"
			if existing.IMAPHost != "" {
				status = "updated"
			}
			return output(map[string]any{
				"status": status, "account": args[0], "provider": auth.ProviderSMTP,
				"imap_host": endpoint.Host, "imap_port": endpoint.Port, "imap_tls": endpoint.TLS, "imap_username": endpoint.Username,
			}, func() {
				fmt.Printf("IMAP %s for %s at %s:%d %s as %s\nPassword: reused from the OS keychain\n", status, args[0], endpoint.Host, endpoint.Port, endpoint.TLS, endpoint.Username)
			})
		},
	}
	addIMAPFlags(cmd, &imapFlags)
	return cmd
}

func accountTestCmd() *cobra.Command {
	var sendTo string
	cmd := &cobra.Command{
		Use:   "test [name]",
		Short: "Check that an account can authenticate",
		Long:  "For an SMTP account, connects, negotiates TLS, and authenticates without sending. When that account has IMAP settings, the same command also logs in over IMAP and logs out without opening a mailbox. SMTP and IMAP results are reported separately. For a Gmail account, checks the OAuth token with Google. --send-to delivers one test message and records it in history.",
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
			if sender.Provider() == auth.ProviderSMTP {
				cfg, lookupErr := auth.LookupProvider(name)
				if lookupErr == nil {
					if _, ok := cfg.IMAP(); ok {
						return accountTestSMTPAndIMAP(ctx, name, sender, cfg, sendTo, recipients)
					}
				}
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

type imapCLIFlags struct {
	host, tlsMode, username string
	port                    int
}

func addIMAPFlags(cmd *cobra.Command, flags *imapCLIFlags) {
	cmd.Flags().StringVar(&flags.host, "imap-host", "", "IMAP server hostname (default: the SMTP host)")
	cmd.Flags().IntVar(&flags.port, "imap-port", 0, "IMAP port (default 993)")
	cmd.Flags().StringVar(&flags.tlsMode, "imap-tls", "", "IMAP TLS mode: ssl, starttls, or none (default ssl)")
	cmd.Flags().StringVar(&flags.username, "imap-username", "", "IMAP username (default: the SMTP username)")
}

func imapFlagsRequested(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("imap-host") || cmd.Flags().Changed("imap-port") || cmd.Flags().Changed("imap-tls") || cmd.Flags().Changed("imap-username")
}

func resolveIMAPFlags(cmd *cobra.Command, flags imapCLIFlags, smtpHost, smtpUser string, existing auth.ProviderConfig, force bool) auth.ProviderConfig {
	if !force && !imapFlagsRequested(cmd) {
		return auth.ProviderConfig{
			IMAPHost: existing.IMAPHost, IMAPPort: existing.IMAPPort, IMAPTLS: existing.IMAPTLS, IMAPUsername: existing.IMAPUsername,
		}
	}
	host := existing.IMAPHost
	if cmd.Flags().Changed("imap-host") {
		host = strings.TrimSpace(flags.host)
	}
	if host == "" {
		host = smtpHost
	}
	port := existing.IMAPPort
	if cmd.Flags().Changed("imap-port") {
		port = flags.port
	}
	if port == 0 {
		port = 993
	}
	tlsMode := existing.IMAPTLS
	if cmd.Flags().Changed("imap-tls") {
		tlsMode = strings.TrimSpace(flags.tlsMode)
	}
	if tlsMode == "" {
		tlsMode = smtp.TLSSSL
	}
	username := existing.IMAPUsername
	if cmd.Flags().Changed("imap-username") {
		username = strings.TrimSpace(flags.username)
	}
	if username == "" {
		username = smtpUser
	}
	return auth.ProviderConfig{IMAPHost: host, IMAPPort: port, IMAPTLS: tlsMode, IMAPUsername: username}
}

func accountTestSMTPAndIMAP(ctx context.Context, name string, sender mailSender, cfg auth.ProviderConfig, sendTo string, recipients []string) error {
	endpoint, _ := cfg.IMAP()
	smtpErr := sender.Probe(ctx)
	imapClient, imapErr := openIMAPClient(name)
	if imapErr == nil {
		imapErr = imapClient.CheckLogin(ctx)
	}
	smtpStatus := "ok"
	imapStatus := "ok"
	if smtpErr != nil {
		smtpStatus = "failed"
	}
	if imapErr != nil {
		imapStatus = "failed"
	}
	status := "ok"
	if smtpErr != nil || imapErr != nil {
		status = "failed"
	}
	smtpResult := map[string]any{"status": smtpStatus, "host": cfg.Host, "port": cfg.Port, "tls": cfg.TLS}
	imapResult := map[string]any{"status": imapStatus, "host": endpoint.Host, "port": endpoint.Port, "tls": endpoint.TLS, "username": endpoint.Username}
	var warnings []string
	if cfg.TLS == smtp.TLSNone {
		warning := "Connected without TLS. The password was sent in cleartext because this account uses --tls none."
		smtpResult["warning"] = warning
		warnings = append(warnings, warning)
	}
	if endpoint.TLS == smtp.TLSNone {
		warning := "IMAP connected without TLS. The password was sent in cleartext because this account uses --imap-tls none."
		imapResult["warning"] = warning
		warnings = append(warnings, warning)
	}
	if smtpErr != nil {
		smtpResult["error"] = smtpErr.Error()
	}
	if imapErr != nil {
		imapResult["error"] = imapErr.Error()
	}
	value := map[string]any{
		"status": status, "account": name, "provider": auth.ProviderSMTP, "from": sender.FromAddress(),
		"authenticated": smtpErr == nil && imapErr == nil, "smtp": smtpResult, "imap": imapResult,
	}
	if len(warnings) > 0 {
		value["warning"] = strings.Join(warnings, " ")
	}
	if err := output(value, func() {
		fmt.Printf("SMTP: %s\n", checkLine(smtpStatus, fmt.Sprintf("%s:%d %s", cfg.Host, cfg.Port, cfg.TLS), smtpErr))
		fmt.Printf("IMAP: %s\n", checkLine(imapStatus, fmt.Sprintf("%s:%d %s as %s", endpoint.Host, endpoint.Port, endpoint.TLS, endpoint.Username), imapErr))
		for _, warning := range warnings {
			fmt.Println(warning)
		}
		if status == "ok" && sendTo == "" {
			fmt.Println("No message was sent.")
		}
	}); err != nil {
		return err
	}
	if smtpErr != nil && imapErr != nil {
		return fmt.Errorf("SMTP: %v; IMAP: %v", smtpErr, imapErr)
	}
	if smtpErr != nil {
		return fmt.Errorf("SMTP: %v", smtpErr)
	}
	if imapErr != nil {
		return fmt.Errorf("IMAP: %v", imapErr)
	}
	if sendTo == "" {
		return nil
	}
	msg := mailmsg.Message{To: recipients, Subject: "mailsend connectivity test", Text: "This is a mailsend connectivity test. No action is required."}
	return sendOne(ctx, sender, name, msg, "test")
}

func checkLine(status, detail string, err error) string {
	if err != nil {
		return fmt.Sprintf("failed (%s): %s", detail, err.Error())
	}
	if status == "ok" {
		return "authenticated " + detail
	}
	return status + " " + detail
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
