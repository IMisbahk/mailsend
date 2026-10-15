package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

const service = "mailsend"

var scopes = []string{"https://www.googleapis.com/auth/gmail.send", "openid", "email"}

type credentials struct {
	Installed *oauthClient `json:"installed"`
	Web       *oauthClient `json:"web"`
}
type oauthClient struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthURI      string `json:"auth_uri"`
	TokenURI     string `json:"token_uri"`
}
type manifest struct {
	Default   string                    `json:"default"`
	Accounts  []string                  `json:"accounts"`
	Emails    map[string]string         `json:"emails,omitempty"`
	Providers map[string]ProviderConfig `json:"providers,omitempty"`
}
type Account struct {
	Name   string         `json:"name"`
	Email  string         `json:"email"`
	Token  *oauth2.Token  `json:"-"`
	Config *oauth2.Config `json:"-"`
}
type Profile struct {
	EmailAddress string `json:"email"`
}

type Client struct {
	Account     Account
	HTTP        *http.Client
	BaseURL     string
	IdentityURL string
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return "https://gmail.googleapis.com"
}
func (c *Client) identityURL() string {
	if c.IdentityURL != "" {
		return strings.TrimRight(c.IdentityURL, "/")
	}
	return "https://openidconnect.googleapis.com"
}

func ConfigDir() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("MAILSEND_CONFIG_DIR")); custom != "" {
		if e := os.MkdirAll(custom, 0700); e != nil {
			return "", e
		}
		_ = os.Chmod(custom, 0700)
		return custom, nil
	}
	root, e := os.UserConfigDir()
	if e != nil {
		return "", e
	}
	p := filepath.Join(root, "mailsend")
	if e = os.MkdirAll(p, 0700); e != nil {
		return "", e
	}
	_ = os.Chmod(p, 0700)
	return p, nil
}
func ClientCredentialsPath() (string, error) {
	if p := os.Getenv("MAILSEND_OAUTH_CLIENT_FILE"); p != "" {
		return p, nil
	}
	d, e := ConfigDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(d, "client_secret.json"), nil
}
func parseClient() (*oauth2.Config, error) {
	p, e := ClientCredentialsPath()
	if e != nil {
		return nil, e
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return nil, fmt.Errorf("OAuth client credentials not found at %s; see README setup", p)
	}
	var c credentials
	if e = json.Unmarshal(b, &c); e != nil {
		return nil, fmt.Errorf("invalid OAuth client JSON: %w", e)
	}
	v := c.Installed
	if v == nil || v.ClientID == "" || v.ClientSecret == "" {
		return nil, fmt.Errorf("OAuth JSON must contain an installed-app client with client_id and client_secret")
	}
	if v.AuthURI == "" {
		v.AuthURI = "https://accounts.google.com/o/oauth2/auth"
	}
	if v.TokenURI == "" {
		v.TokenURI = "https://oauth2.googleapis.com/token"
	}
	return &oauth2.Config{ClientID: v.ClientID, ClientSecret: v.ClientSecret, Endpoint: oauth2.Endpoint{AuthURL: v.AuthURI, TokenURL: v.TokenURI}, Scopes: scopes}, nil
}
func tokenKey(name string) string { return "token:" + name }
func accountName(name string) string {
	if name == "" {
		return "default"
	}
	return name
}
func ValidateAccountName(name string) error {
	name = accountName(name)
	if name == "." || name == ".." || len(name) > 40 {
		return fmt.Errorf("account names must be 1 to 40 letters, numbers, dots, dashes, or underscores")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return fmt.Errorf("account names must be 1 to 40 letters, numbers, dots, dashes, or underscores")
		}
	}
	return nil
}
func Load(name string) (*Client, error) {
	name = accountName(name)
	raw, e := keyring.Get(service, tokenKey(name))
	if e != nil {
		return nil, fmt.Errorf("account %q is not logged in; run mailsend login --account %s", name, name)
	}
	var tok oauth2.Token
	if e = json.Unmarshal([]byte(raw), &tok); e != nil {
		return nil, fmt.Errorf("stored credentials for account %q are invalid; run mailsend login again", name)
	}
	cfg, e := parseClient()
	if e != nil {
		return nil, e
	}
	httpClient := cfg.Client(context.Background(), &tok)
	return &Client{Account: Account{Name: name, Email: Email(name), Token: &tok, Config: cfg}, HTTP: httpClient}, nil
}
func (c *Client) Profile(ctx context.Context) (Profile, error) {
	var p Profile
	req, e := http.NewRequestWithContext(ctx, "GET", c.identityURL()+"/v1/userinfo", nil)
	if e != nil {
		return p, e
	}
	resp, e := c.HTTP.Do(req)
	if e != nil {
		if isRefreshError(e) {
			return p, fmt.Errorf("Google authorization expired or was revoked; run mailsend login again: %w", e)
		}
		return p, fmt.Errorf("Google account identity request failed: %w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return p, apiError(resp)
	}
	e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&p)
	if e != nil {
		return p, e
	}
	if p.EmailAddress == "" {
		return p, fmt.Errorf("Google user-info response did not contain an email address")
	}
	return p, nil
}
func (c *Client) Send(ctx context.Context, raw string) (id string, ambiguous bool, err error) {
	body, _ := json.Marshal(map[string]string{"raw": raw})
	req, e := http.NewRequestWithContext(ctx, "POST", c.baseURL()+"/gmail/v1/users/me/messages/send", strings.NewReader(string(body)))
	if e != nil {
		return "", false, e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.HTTP.Do(req)
	if e != nil {
		if isRefreshError(e) {
			return "", false, fmt.Errorf("Google authorization expired or was revoked; run mailsend login again: %w", e)
		}
		return "", true, fmt.Errorf("send response was not received; Gmail may have accepted the message, so it was marked uncertain to prevent duplicates: %w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode >= 500 {
			return "", true, fmt.Errorf("Gmail returned HTTP %d; acceptance is uncertain; not retrying to avoid duplicate delivery: %s", resp.StatusCode, readError(resp))
		}
		return "", false, apiError(resp)
	}
	var result struct {
		ID string `json:"id"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); e != nil {
		return "", true, fmt.Errorf("Gmail may have accepted the message but returned an unreadable response; marked uncertain: %w", e)
	}
	if result.ID == "" {
		return "", true, fmt.Errorf("Gmail response omitted the message ID; marked uncertain to prevent duplicate delivery")
	}
	return result.ID, false, nil
}
func isRefreshError(err error) bool {
	var retrieve *oauth2.RetrieveError
	return errors.As(err, &retrieve)
}
func readError(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return strings.TrimSpace(string(b))
}

type APIError struct {
	Status     int
	Details    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Status == 401 {
		return "Google authorization expired or was revoked; run mailsend login again (HTTP 401)"
	}
	if e.Status == 403 {
		return fmt.Sprintf("Gmail rejected the request (HTTP 403), possibly because sending limits were reached: %s", e.Details)
	}
	return fmt.Sprintf("Gmail API returned HTTP %d: %s", e.Status, e.Details)
}
func (e *APIError) Retryable() bool {
	details := strings.ToLower(e.Details)
	return e.Status == 429 || (e.Status == 403 && (strings.Contains(details, "ratelimitexceeded") || strings.Contains(details, "userratelimitexceeded")))
}
func apiError(resp *http.Response) error {
	delay := time.Duration(0)
	if raw := resp.Header.Get("Retry-After"); raw != "" {
		if seconds, err := time.ParseDuration(raw + "s"); err == nil {
			delay = seconds
		} else if when, err := http.ParseTime(raw); err == nil && when.After(time.Now()) {
			delay = time.Until(when)
		}
	}
	return &APIError{Status: resp.StatusCode, Details: readError(resp), RetryAfter: delay}
}
func Retryable(err error) bool {
	var retry interface{ Retryable() bool }
	return errors.As(err, &retry) && retry.Retryable()
}
func RetryDelay(err error, fallback time.Duration) time.Duration {
	var api *APIError
	if errors.As(err, &api) && api.RetryAfter > 0 {
		return api.RetryAfter
	}
	return fallback
}
func IsQuotaError(err error) bool {
	var api *APIError
	if errors.As(err, &api) {
		details := strings.ToLower(api.Details)
		return api.Status == 429 || (api.Status == 403 && (strings.Contains(details, "limit") || strings.Contains(details, "quota") || strings.Contains(details, "rate")))
	}
	var quota interface{ IsQuota() bool }
	return errors.As(err, &quota) && quota.IsQuota()
}
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	var api *APIError
	if errors.As(err, &api) && (api.Status == 401 || (api.Status == 403 && !api.Retryable())) {
		return true
	}
	var authn interface{ IsAuth() bool }
	if errors.As(err, &authn) && authn.IsAuth() {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "run mailsend login")
}

func Login(ctx context.Context, name string, output io.Writer) (string, error) {
	name = accountName(name)
	cfg, e := parseClient()
	if e != nil {
		return "", e
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return "", fmt.Errorf("cannot start local OAuth callback: %w", e)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	cfg.RedirectURL = redirect
	stateBytes := make([]byte, 24)
	if _, e = rand.Read(stateBytes); e != nil {
		return "", e
	}
	state := hex.EncodeToString(stateBytes)
	verifier := oauth2.GenerateVerifier()
	type callback struct {
		code string
		err  error
	}
	ch := make(chan callback, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "OAuth state mismatch", http.StatusBadRequest)
			select {
			case ch <- callback{err: errors.New("OAuth state validation failed")}:
			default:
			}
			return
		}
		if e := r.URL.Query().Get("error"); e != "" {
			http.Error(w, "Authorization failed. You may close this tab.", http.StatusBadRequest)
			select {
			case ch <- callback{err: fmt.Errorf("Google authorization failed: %s", e)}:
			default:
			}
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Authorization code missing", http.StatusBadRequest)
			select {
			case ch <- callback{err: errors.New("OAuth callback did not include a code")}:
			default:
			}
			return
		}
		fmt.Fprint(w, "mailsend is connected. You may close this tab and return to the terminal.")
		select {
		case ch <- callback{code: code}:
		default:
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(ln)
	defer server.Shutdown(context.Background())
	authURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"), oauth2.S256ChallengeOption(verifier))
	fmt.Fprintf(output, "Opening Google authorization in your browser.\nIf it does not open, visit:\n%s\n", authURL)
	_ = openBrowser(authURL)
	select {
	case cb := <-ch:
		if cb.err != nil {
			return "", cb.err
		}
		tok, e := cfg.Exchange(ctx, cb.code, oauth2.VerifierOption(verifier))
		if e != nil {
			return "", fmt.Errorf("OAuth token exchange failed: %w", e)
		}
		if tok.RefreshToken == "" {
			return "", fmt.Errorf("Google did not return an offline refresh token; run login again and approve offline access")
		}
		raw, _ := json.Marshal(tok)
		if e = keyring.Set(service, tokenKey(name), string(raw)); e != nil {
			return "", fmt.Errorf("secure token storage failed in the OS keychain: %w; no credentials were written to disk", e)
		}
		if e = addAccount(name); e != nil {
			_ = keyring.Delete(service, tokenKey(name))
			return "", e
		}
		client := &Client{Account: Account{Name: name, Token: tok, Config: cfg}, HTTP: cfg.Client(context.Background(), tok)}
		p, e := client.Profile(ctx)
		if e != nil {
			return "", fmt.Errorf("login succeeded but Google account lookup failed: %w", e)
		}
		if e = setAccountEmail(name, p.EmailAddress); e != nil {
			return "", e
		}
		return p.EmailAddress, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(5 * time.Minute):
		return "", fmt.Errorf("OAuth authorization timed out after 5 minutes")
	}
}
func openBrowser(raw string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", raw)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", raw)
	default:
		cmd = exec.Command("xdg-open", raw)
	}
	return cmd.Start()
}
func manifestPath() (string, error) {
	d, e := ConfigDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(d, "accounts.json"), nil
}
func readManifest() (manifest, error) {
	p, e := manifestPath()
	if e != nil {
		return manifest{}, e
	}
	b, e := os.ReadFile(p)
	if os.IsNotExist(e) {
		return manifest{}, nil
	}
	if e != nil {
		return manifest{}, e
	}
	var m manifest
	e = json.Unmarshal(b, &m)
	return m, e
}
func writeManifest(m manifest) error {
	p, e := manifestPath()
	if e != nil {
		return e
	}
	b, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Chmod(p, 0600)
}
func addAccount(name string) error {
	m, e := readManifest()
	if e != nil {
		return e
	}
	for _, a := range m.Accounts {
		if a == name {
			return nil
		}
	}
	m.Accounts = append(m.Accounts, name)
	if m.Default == "" {
		m.Default = name
	}
	return writeManifest(m)
}
func setAccountEmail(name, email string) error {
	m, err := readManifest()
	if err != nil {
		return err
	}
	if m.Emails == nil {
		m.Emails = map[string]string{}
	}
	m.Emails[name] = email
	return writeManifest(m)
}
func Email(name string) string {
	m, err := readManifest()
	if err != nil {
		return ""
	}
	return m.Emails[accountName(name)]
}
func Accounts() (manifest, error) { return readManifest() }
func Logout(name string) error {
	name = accountName(name)
	if err := deleteSecret(tokenKey(name)); err != nil {
		return err
	}
	if err := deleteSecret(smtpPasswordKey(name)); err != nil {
		return err
	}
	m, err := readManifest()
	if err != nil {
		return err
	}
	list := m.Accounts[:0]
	for _, a := range m.Accounts {
		if a != name {
			list = append(list, a)
		}
	}
	m.Accounts = list
	delete(m.Emails, name)
	if m.Providers != nil {
		delete(m.Providers, name)
		if len(m.Providers) == 0 {
			m.Providers = nil
		}
	}
	if m.Default == name {
		m.Default = ""
		if len(list) > 0 {
			m.Default = list[0]
		}
	}
	return writeManifest(m)
}
func DefaultAccount() string { m, _ := readManifest(); return m.Default }
func HasToken(name string) bool {
	_, e := keyring.Get(service, tokenKey(accountName(name)))
	return e == nil
}
func SetDefault(name string) error {
	m, e := readManifest()
	if e != nil {
		return e
	}
	found := false
	for _, a := range m.Accounts {
		if a == name {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("account %q is not configured", name)
	}
	m.Default = name
	return writeManifest(m)
}
