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
type ProviderConfig struct {
	Name     string `json:"-"`
	Provider string `json:"provider,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	TLS      string `json:"tls,omitempty"`
	Username string `json:"username,omitempty"`
	From     string `json:"from,omitempty"`
	ReplyTo  string `json:"replyTo,omitempty"`
}

func smtpPasswordKey(name string) string { return "smtp-password:" + name }

// LookupProvider reports how an account sends. Accounts saved before SMTP
// support, including a manifest that only lists "sunbeam", stay Gmail.
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
	return ProviderConfig{
		Provider: ProviderSMTP,
		Host:     prepared.Host,
		Port:     prepared.Port,
		TLS:      prepared.TLSMode,
		Username: prepared.Username,
		From:     prepared.From,
		ReplyTo:  prepared.ReplyTo,
	}, nil
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
