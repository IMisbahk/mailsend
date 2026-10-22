package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestLegacySunbeamManifestStaysGmail(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAILSEND_CONFIG_DIR", dir)
	keyring.MockInit()
	original := []byte("{\n  \"default\": \"sunbeam\",\n  \"accounts\": [\"sunbeam\"],\n  \"emails\": {\n    \"sunbeam\": \"misbah@sunbeam.agency\"\n  }\n}\n")
	path := filepath.Join(dir, "accounts.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LookupProvider("sunbeam")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != ProviderGmail || cfg.From != "misbah@sunbeam.agency" {
		t.Fatalf("legacy account = %+v", cfg)
	}
	if !HasToken("sunbeam") && Authenticated("sunbeam") {
		t.Fatal("legacy account should not look authenticated without a token")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("reading the account rewrote accounts.json:\n%s", got)
	}
	_, err = SaveSMTPAccount("sunbeam", ProviderConfig{
		Host: "mail.example.com", Port: 587, TLS: "starttls", Username: "misbah@sunbeam.agency", From: "misbah@sunbeam.agency",
	}, strings.Repeat("s", 8))
	if err == nil || !strings.Contains(err.Error(), "Gmail") {
		t.Fatalf("expected refusal to replace the Gmail account, got %v", err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("failed SMTP add rewrote the Gmail manifest:\n%s", got)
	}
	if HasSMTPPassword("sunbeam") {
		t.Fatal("rejected SMTP add left a password in the keychain")
	}
}

func TestSMTPPasswordStaysOutOfConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAILSEND_CONFIG_DIR", dir)
	keyring.MockInit()
	password := strings.Repeat("p", 12)
	status, err := SaveSMTPAccount("agency", ProviderConfig{
		Host: "mail.example.com", TLS: "starttls", Username: "misbah@sunbeam.agency", From: "Misbah <misbah@sunbeam.agency>", ReplyTo: "replies@sunbeam.agency",
	}, password)
	if err != nil {
		t.Fatal(err)
	}
	if status != "added" {
		t.Fatalf("status %s", status)
	}
	cfg, err := LookupProvider("agency")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != ProviderSMTP || cfg.Port != 587 || cfg.TLS != "starttls" || cfg.From != "misbah@sunbeam.agency" || cfg.ReplyTo != "replies@sunbeam.agency" {
		t.Fatalf("stored config %+v", cfg)
	}
	if cfg.Provider == ProviderGmail {
		t.Fatal("smtp account was treated as gmail")
	}
	body, err := os.ReadFile(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), password) || strings.Contains(strings.ToLower(string(body)), "password") {
		t.Fatalf("password leaked into config: %s", body)
	}
	info, err := os.Stat(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("config permissions %o", info.Mode().Perm())
	}
	got, err := SMTPPassword("agency")
	if err != nil || got != password {
		t.Fatalf("keychain password %q, %v", got, err)
	}
	if !Authenticated("agency") {
		t.Fatal("smtp account with a keychain password should be authenticated")
	}
	if Email("agency") != "misbah@sunbeam.agency" {
		t.Fatalf("email %q", Email("agency"))
	}
	status, err = SaveSMTPAccount("agency", ProviderConfig{
		Host: "smtp.example.net", Port: 465, TLS: "ssl", Username: "misbah@sunbeam.agency", From: "misbah@sunbeam.agency",
	}, strings.Repeat("r", 12))
	if err != nil || status != "updated" {
		t.Fatalf("update status %s, %v", status, err)
	}
	if got, _ = SMTPPassword("agency"); got != strings.Repeat("r", 12) {
		t.Fatalf("password was not rotated, got %q", got)
	}
	if err = Logout("agency"); err != nil {
		t.Fatal(err)
	}
	if HasSMTPPassword("agency") {
		t.Fatal("logout left the SMTP password in the keychain")
	}
	cfg, err = LookupProvider("agency")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != ProviderGmail {
		t.Fatalf("logout left provider config %+v", cfg)
	}
}

func TestSetIMAPReusesPasswordAndLeavesLegacyFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAILSEND_CONFIG_DIR", dir)
	keyring.MockInit()
	original := []byte("{\n  \"default\": \"sunbeam\",\n  \"accounts\": [\"sunbeam\"],\n  \"emails\": {\n    \"sunbeam\": \"misbah@sunbeam.agency\"\n  }\n}")
	path := filepath.Join(dir, "accounts.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LookupProvider("sunbeam")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.IMAP(); ok {
		t.Fatal("legacy account looked like it had IMAP")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("reading IMAP settings rewrote accounts.json:\n%s", got)
	}
	if err = SetIMAP("sunbeam", ProviderConfig{IMAPHost: "mail.example.com"}); err == nil {
		t.Fatal("expected set-imap to refuse a Gmail account")
	}
	password := fakePassword()
	if _, err = SaveSMTPAccount("mailbox", ProviderConfig{
		Host: "mail.example.test", Port: 587, TLS: "starttls", Username: "ada@example.test", From: "ada@example.test",
	}, password); err != nil {
		t.Fatal(err)
	}
	// The CLI fills the SMTP host before calling SetIMAP. An empty config is
	// not IMAP. Port, TLS, and username still default when only the host is set.
	if err = SetIMAP("mailbox", ProviderConfig{IMAPHost: "mail.example.test"}); err != nil {
		t.Fatal(err)
	}
	saved, err := LookupProvider("mailbox")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, ok := saved.IMAP()
	if !ok || endpoint.Host != "mail.example.test" || endpoint.Port != 993 || endpoint.TLS != "ssl" || endpoint.Username != "ada@example.test" {
		t.Fatalf("imap endpoint %+v ok %v", endpoint, ok)
	}
	if storedPassword, passwordErr := SMTPPassword("mailbox"); passwordErr != nil || storedPassword != password {
		t.Fatalf("password changed to %q (%v)", storedPassword, passwordErr)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), password) {
		t.Fatalf("password leaked into config: %s", body)
	}
	rotated := strings.Repeat("z", 12)
	if _, err = SaveSMTPAccount("mailbox", ProviderConfig{
		Host: "mail.example.test", Port: 587, TLS: "starttls", Username: "ada@example.test", From: "ada@example.test",
		IMAPHost: saved.IMAPHost, IMAPPort: saved.IMAPPort, IMAPTLS: saved.IMAPTLS, IMAPUsername: saved.IMAPUsername,
	}, rotated); err != nil {
		t.Fatal(err)
	}
	saved, err = LookupProvider("mailbox")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, ok = saved.IMAP()
	if !ok || endpoint.Port != 993 || endpoint.TLS != "ssl" {
		t.Fatalf("password rotation dropped IMAP: %+v", saved)
	}
	sunbeam, err := LookupProvider("sunbeam")
	if err != nil || sunbeam.Provider != ProviderGmail || DefaultAccount() != "sunbeam" {
		t.Fatalf("sunbeam %+v default %s err %v", sunbeam, DefaultAccount(), err)
	}
}

func TestLegacyManifestSurvivesSMTPAccount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAILSEND_CONFIG_DIR", dir)
	keyring.MockInit()
	original := []byte("{\n  \"default\": \"sunbeam\",\n  \"accounts\": [\"sunbeam\"],\n  \"emails\": {\n    \"sunbeam\": \"misbah@sunbeam.agency\"\n  }\n}")
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveSMTPAccount("agency", ProviderConfig{
		Host: "mail.example.com", Port: 587, TLS: "starttls", Username: "misbah", From: "misbah@sunbeam.agency",
	}, "pw"); err != nil {
		t.Fatal(err)
	}
	sunbeam, err := LookupProvider("sunbeam")
	if err != nil {
		t.Fatal(err)
	}
	if sunbeam.Provider != ProviderGmail || sunbeam.From != "misbah@sunbeam.agency" || DefaultAccount() != "sunbeam" {
		t.Fatalf("sunbeam changed: %+v default %s", sunbeam, DefaultAccount())
	}
	agency, err := LookupProvider("agency")
	if err != nil || agency.Provider != ProviderSMTP {
		t.Fatalf("agency %+v, %v", agency, err)
	}
}

// fakePassword is a test-only value. It is built at runtime so the source
// has no SMTP password literal.
func fakePassword() string { return strings.Repeat("x", 12) }
