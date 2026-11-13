package auth

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/IMisbahk/mailsend/internal/smtp"
	"github.com/zalando/go-keyring"
)

const (
	ProviderGmail = "gmail"
	ProviderSMTP  = "smtp"
)

// ProviderConfig is the non-secret portion of an account. Existing Gmail
// accounts have no entry; an absent provider means Gmail.
// IMAP fields are optional. Accounts saved before IMAP support have none,
// and those files still load.
type ProviderConfig struct {
	Name     string `json:"-"`
	Provider string `json:"provider,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	TLS      string `json:"tls,omitempty"`
	Username string `json:"username,omitempty"`
	From     string `json:"from,omitempty"`
	ReplyTo  string `json:"replyTo,omitempty"`

	IMAPHost     string `json:"imapHost,omitempty"`
	IMAPPort     int    `json:"imapPort,omitempty"`
	IMAPTLS      string `json:"imapTls,omitempty"`
	IMAPUsername string `json:"imapUsername,omitempty"`
}

// IMAPEndpoint is the resolved IMAP server for an account. The password is
// the SMTP password already stored in the keychain.
type IMAPEndpoint struct {
	Host     string
	Port     int
	TLS      string
	Username string
}

func smtpPasswordKey(name string) string { return "smtp-password:" + name }

// LookupProvider reports how an account sends. Accounts saved before SMTP
// support, including a manifest that only lists "acme", stay Gmail.
func LookupProvider(name string) (ProviderConfig, error) {
	name = accountName(name)
	if err := ValidateAccountName(name); err != nil {
		return ProviderConfig{}, err
	}
	current, err := readManifest()
	if err != nil {
		return ProviderConfig{}, err
	}
	cfg := ProviderConfig{Provider: ProviderGmail, Name: name, From: current.Emails[name]}
	if current.Providers == nil {
		return cfg, nil
	}
	stored, ok := current.Providers[name]
	if !ok || stored.Provider == "" {
		return cfg, nil
	}
	stored.Name = name
	if stored.From == "" {
		stored.From = current.Emails[name]
	}
	stored.Provider = strings.ToLower(stored.Provider)
	return stored, nil
}

// NormalizeSMTP checks SMTP account fields and fills the default port.
// It does not read or store a password.
func NormalizeSMTP(cfg ProviderConfig) (ProviderConfig, error) {
	prepared, err := smtp.Normalize(smtp.Config{
		Host:     cfg.Host,
		Port:     cfg.Port,
		TLSMode:  cfg.TLS,
		Username: cfg.Username,
		From:     cfg.From,
		ReplyTo:  cfg.ReplyTo,
	})
	if err != nil {
		return ProviderConfig{}, err
	}
	cfg.Username = prepared.Username
	imapCfg, err := normalizeIMAP(cfg)
	if err != nil {
		return ProviderConfig{}, err
	}
	return ProviderConfig{
		Provider:     ProviderSMTP,
		Host:         prepared.Host,
		Port:         prepared.Port,
		TLS:          prepared.TLSMode,
		Username:     prepared.Username,
		From:         prepared.From,
		ReplyTo:      prepared.ReplyTo,
		IMAPHost:     imapCfg.IMAPHost,
		IMAPPort:     imapCfg.IMAPPort,
		IMAPTLS:      imapCfg.IMAPTLS,
		IMAPUsername: imapCfg.IMAPUsername,
	}, nil
}

// IMAP reports the mailbox server when this account has IMAP settings.
// An empty host means IMAP is not configured. A missing port uses 993, a
// missing TLS mode uses implicit TLS, and a missing username uses the SMTP
// username.
func (cfg ProviderConfig) IMAP() (IMAPEndpoint, bool) {
	host := strings.TrimSpace(cfg.IMAPHost)
	if host == "" {
		return IMAPEndpoint{}, false
	}
	port := cfg.IMAPPort
	if port == 0 {
		port = 993
	}
	mode := strings.TrimSpace(cfg.IMAPTLS)
	if mode == "" {
		mode = smtp.TLSSSL
	}
	username := strings.TrimSpace(cfg.IMAPUsername)
	if username == "" {
		username = strings.TrimSpace(cfg.Username)
	}
	return IMAPEndpoint{Host: host, Port: port, TLS: mode, Username: username}, true
}

// normalizeIMAP checks optional IMAP fields. All empty means IMAP is off.
// When a host is set, the default port is 993 and the default TLS mode is
// implicit TLS (ssl). The username defaults to the SMTP username.
func normalizeIMAP(cfg ProviderConfig) (ProviderConfig, error) {
	host := strings.Trim(strings.TrimSpace(cfg.IMAPHost), "[]")
	mode := strings.TrimSpace(cfg.IMAPTLS)
	username := strings.TrimSpace(cfg.IMAPUsername)
	port := cfg.IMAPPort
	if host == "" && mode == "" && username == "" && port == 0 {
		return ProviderConfig{}, nil
	}
	if host == "" || strings.Contains(host, "://") || strings.ContainsAny(host, " \t\r\n/") {
		return ProviderConfig{}, fmt.Errorf("invalid IMAP host %q", cfg.IMAPHost)
	}
	if mode == "" {
		mode = smtp.TLSSSL
	}
	normalized, err := smtp.NormalizeTLS(mode)
	if err != nil {
		return ProviderConfig{}, fmt.Errorf("invalid IMAP TLS mode %q (use ssl, starttls, or none)", cfg.IMAPTLS)
	}
	if port == 0 {
		port = 993
	}
	if port < 1 || port > 65535 {
		return ProviderConfig{}, fmt.Errorf("invalid IMAP port %d", port)
	}
	if username == "" {
		username = strings.TrimSpace(cfg.Username)
	}
	if username == "" || strings.ContainsAny(username, "\r\n") {
		return ProviderConfig{}, fmt.Errorf("IMAP username is required")
	}
	return ProviderConfig{IMAPHost: host, IMAPPort: port, IMAPTLS: normalized, IMAPUsername: username}, nil
}

// SetIMAP stores IMAP settings on an existing SMTP account. It does not read
// or replace the keychain password.
func SetIMAP(name string, cfg ProviderConfig) error {
	name = accountName(name)
	if err := ValidateAccountName(name); err != nil {
		return err
	}
	current, err := readManifest()
	if err != nil {
		return err
	}
	if !accountListed(current, name) || providerOf(current, name) != ProviderSMTP {
		return fmt.Errorf("account %q is not an SMTP account; add it with mailsend account add --provider smtp", name)
	}
	stored := current.Providers[name]
	stored.IMAPHost = cfg.IMAPHost
	stored.IMAPPort = cfg.IMAPPort
	stored.IMAPTLS = cfg.IMAPTLS
	stored.IMAPUsername = cfg.IMAPUsername
	normalized, err := NormalizeSMTP(stored)
	if err != nil {
		return err
	}
	current.Providers[name] = normalized
	return writeManifest(current)
}

// SaveSMTPAccount stores SMTP settings in accounts.json and the password in
// the OS keychain. The password is never written to the config file.
// Adding an existing SMTP account updates it. A Gmail account name is refused.
func SaveSMTPAccount(name string, cfg ProviderConfig, password string) (string, error) {
	name = accountName(name)
	if err := ValidateAccountName(name); err != nil {
		return "", err
	}
	normalized, err := NormalizeSMTP(cfg)
	if err != nil {
		return "", err
	}
	if err = smtp.ValidatePassword(password); err != nil {
		return "", err
	}
	current, err := readManifest()
	if err != nil {
		return "", err
	}
	if accountListed(current, name) && providerOf(current, name) != ProviderSMTP {
		return "", fmt.Errorf("account %q already exists as a Gmail account; choose another name or run mailsend logout --account %s", name, name)
	}
	status := "added"
	if accountListed(current, name) {
		status = "updated"
	}
	previousPassword, previousErr := secretGet(smtpPasswordKey(name))
	if err = secretPut(smtpPasswordKey(name), password); err != nil {
		return "", fmt.Errorf("secure password storage failed in the OS keychain: %w; the password was not written to the config file", err)
	}
	if current.Providers == nil {
		current.Providers = map[string]ProviderConfig{}
	}
	current.Providers[name] = normalized
	if current.Emails == nil {
		current.Emails = map[string]string{}
	}
	current.Emails[name] = normalized.From
	if !accountListed(current, name) {
		current.Accounts = append(current.Accounts, name)
	}
	if current.Default == "" {
		current.Default = name
	}
	if err = writeManifest(current); err != nil {
		if previousErr == nil {
			_ = secretPut(smtpPasswordKey(name), previousPassword)
		} else {
			_ = secretRemove(smtpPasswordKey(name))
		}
		return "", err
	}
	return status, nil
}

func SMTPPassword(name string) (string, error) {
	password, err := secretGet(smtpPasswordKey(accountName(name)))
	if err != nil {
		return "", err
	}
	return password, nil
}

func HasSMTPPassword(name string) bool {
	_, err := secretGet(smtpPasswordKey(accountName(name)))
	return err == nil
}

// Authenticated reports whether the account has the credential it needs:
// an OAuth token for Gmail, or a keychain password for SMTP.
func Authenticated(name string) bool {
	cfg, err := LookupProvider(name)
	if err != nil {
		return false
	}
	if cfg.Provider == ProviderSMTP {
		return HasSMTPPassword(name)
	}
	return HasToken(name)
}

func providerOf(current manifest, name string) string {
	if current.Providers != nil {
		if cfg, ok := current.Providers[name]; ok && cfg.Provider != "" {
			return strings.ToLower(cfg.Provider)
		}
	}
	return ProviderGmail
}

func accountListed(current manifest, name string) bool {
	for _, account := range current.Accounts {
		if account == name {
			return true
		}
	}
	return false
}

func deleteSecret(key string) error {
	err := secretRemove(key)
	if err == nil || errors.Is(err, keyring.ErrNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found") {
		return nil
	}
	return err
}

// ParseBareAddress returns the mailbox from an address header value.
func ParseBareAddress(value, label string) (string, error) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil || parsed.Address == "" || strings.ContainsAny(parsed.Address, "\r\n<>") {
		return "", fmt.Errorf("invalid %s address %q", label, value)
	}
	return parsed.Address, nil
}
