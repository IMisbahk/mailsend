package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestKeychainGetTimesOut(t *testing.T) {
	previousGet := keyringGetFn
	previousTimeout := keychainTimeout
	keychainTimeout = 150 * time.Millisecond
	t.Cleanup(func() {
		keyringGetFn = previousGet
		keychainTimeout = previousTimeout
	})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	keyringGetFn = func(string, string) (string, error) {
		<-release
		return "", nil
	}
	started := time.Now()
	_, err := secretGet("token:acme")
	if err == nil || !strings.Contains(err.Error(), "OS keychain did not respond") {
		t.Fatalf("error %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("keychain wait took %s", time.Since(started))
	}
}

func TestTokenRefreshTimesOut(t *testing.T) {
	previous := tokenRefreshTimeout
	tokenRefreshTimeout = 200 * time.Millisecond
	t.Cleanup(func() { tokenRefreshTimeout = previous })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer server.Close()
	cfg := &oauth2.Config{
		ClientID: "id", ClientSecret: "secret",
		Endpoint: oauth2.Endpoint{TokenURL: server.URL},
	}
	started := time.Now()
	_, err := (&refreshSource{
		cfg:   cfg,
		token: &oauth2.Token{RefreshToken: "refresh", Expiry: time.Now().Add(-time.Hour)},
		base:  server.Client(),
	}).Token()
	if err == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("refresh err %v after %s", err, time.Since(started))
	}
}

func TestProfileStopsWhenTheDeadlinePasses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), IdentityURL: server.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.Profile(ctx)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("profile err %v after %s", err, time.Since(started))
	}
}
