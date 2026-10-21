package auth

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

const (
	defaultKeychainTimeout     = 8 * time.Second
	defaultTokenRefreshTimeout = 15 * time.Second
	defaultIdentityTimeout     = 15 * time.Second
	dialTimeout                = 10 * time.Second
	tlsHandshakeTimeout        = 10 * time.Second
	responseHeaderTimeout      = 30 * time.Second
)

// IdentityTimeout bounds the Google userinfo request used by account and quota.
// send does not call that endpoint when the address is already stored.
var IdentityTimeout = defaultIdentityTimeout

var (
	keychainTimeout     = defaultKeychainTimeout
	tokenRefreshTimeout = defaultTokenRefreshTimeout
	keyringGetFn        = keyring.Get
	keyringSetFn        = keyring.Set
	keyringDeleteFn     = keyring.Delete
)

var (
	debugMu  sync.Mutex
	debugOut io.Writer
)

// SetDebug turns the stderr step trace on or off. A nil writer disables it.
func SetDebug(w io.Writer) {
	debugMu.Lock()
	debugOut = w
	debugMu.Unlock()
}

// Debugf writes one step when --debug is set. It never includes tokens or passwords.
func Debugf(format string, args ...any) {
	debugMu.Lock()
	w := debugOut
	debugMu.Unlock()
	if w == nil {
		return
	}
	fmt.Fprintf(w, "debug: "+format+"\n", args...)
}

func secretGet(key string) (string, error) {
	label := "get " + key
	Debugf("keychain: %s", label)
	type result struct {
		value string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		value, err := keyringGetFn(service, key)
		ch <- result{value, err}
	}()
	timer := time.NewTimer(keychainTimeout)
	defer timer.Stop()
	select {
	case got := <-ch:
		if got.err != nil {
			Debugf("keychain: %s failed: %v", label, got.err)
		} else {
			Debugf("keychain: %s finished", label)
		}
		return got.value, got.err
	case <-timer.C:
		Debugf("keychain: %s timed out after %s", label, keychainTimeout)
		return "", fmt.Errorf("OS keychain did not respond within %s (%s)", keychainTimeout, label)
	}
}

func secretPut(key, value string) error {
	return secretCall("set "+key, func() error { return keyringSetFn(service, key, value) })
}

func secretRemove(key string) error {
	return secretCall("delete "+key, func() error { return keyringDeleteFn(service, key) })
}

func secretCall(label string, fn func() error) error {
	Debugf("keychain: %s", label)
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	timer := time.NewTimer(keychainTimeout)
	defer timer.Stop()
	select {
	case err := <-ch:
		if err != nil {
			Debugf("keychain: %s failed: %v", label, err)
		} else {
			Debugf("keychain: %s finished", label)
		}
		return err
	case <-timer.C:
		Debugf("keychain: %s timed out after %s", label, keychainTimeout)
		return fmt.Errorf("OS keychain did not respond within %s (%s)", keychainTimeout, label)
	}
}

func oauthHTTPClient(cfg *oauth2.Config, tok *oauth2.Token) *http.Client {
	base := boundedTransport()
	baseClient := &http.Client{Transport: base}
	source := oauth2.ReuseTokenSource(tok, &refreshSource{cfg: cfg, token: tok, base: baseClient})
	return &http.Client{Transport: &oauth2.Transport{Source: source, Base: base}}
}

func boundedTransport() http.RoundTripper {
	return traceTransport{base: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout:       30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}}
}

type refreshSource struct {
	cfg   *oauth2.Config
	token *oauth2.Token
	base  *http.Client
}

func (s *refreshSource) Token() (*oauth2.Token, error) {
	Debugf("oauth: refreshing access token")
	ctx, cancel := context.WithTimeout(context.Background(), tokenRefreshTimeout)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, s.base)
	fresh, err := s.cfg.TokenSource(ctx, s.token).Token()
	if err != nil {
		Debugf("oauth: refresh failed: %v", err)
		return nil, err
	}
	s.token = fresh
	expiry := "unset"
	if !fresh.Expiry.IsZero() {
		expiry = fresh.Expiry.UTC().Format(time.RFC3339)
	}
	Debugf("oauth: refresh finished, expiry %s", expiry)
	return fresh, nil
}

type traceTransport struct{ base http.RoundTripper }

func (t traceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target := req.URL.Host + req.URL.Path
	Debugf("http: %s %s", req.Method, target)
	started := time.Now()
	resp, err := t.base.RoundTrip(req)
	elapsed := time.Since(started).Round(time.Millisecond)
	if err != nil {
		Debugf("http: %s %s failed after %s: %v", req.Method, target, elapsed, err)
		return nil, err
	}
	Debugf("http: %s %s -> %d in %s", req.Method, target, resp.StatusCode, elapsed)
	return resp, nil
}
