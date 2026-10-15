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
	}, "secret-value")
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
	const password = "sunbeam-mailbox-secret"
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
	}, "rotated-secret")
	if err != nil || status != "updated" {
		t.Fatalf("update status %s, %v", status, err)
	}
	if got, _ = SMTPPassword("agency"); got != "rotated-secret" {
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
