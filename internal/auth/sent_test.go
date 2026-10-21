package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSearchSentMatchesRecipientAndParksOnForbidden(t *testing.T) {
	attempted := time.UnixMilli(1761040800000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gmail/v1/users/me/messages":
			if !strings.Contains(r.URL.Query().Get("q"), "in:sent") || !strings.Contains(r.URL.Query().Get("q"), "to:ada@example.com") {
				t.Errorf("query %q", r.URL.Query().Get("q"))
			}
			w.Write([]byte(`{"messages":[{"id":"abc"}]}`))
		case "/gmail/v1/users/me/messages/abc":
			w.Write([]byte(`{"id":"abc","internalDate":"1761040800000","payload":{"headers":[{"name":"Subject","value":"Hello"},{"name":"To","value":"Ada <ada@example.com>"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), BaseURL: server.URL}
	found, err := client.SearchSent(context.Background(), "ada@example.com", "Hello", attempted)
	if err != nil || !found.Verified || !found.Found || found.MessageID != "abc" {
		t.Fatalf("lookup %#v %v", found, err)
	}

	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":403,"message":"Request had insufficient authentication scopes.","status":"PERMISSION_DENIED"}}`, http.StatusForbidden)
	}))
	defer denied.Close()
	client.BaseURL = denied.URL
	client.HTTP = denied.Client()
	missing, err := client.SearchSent(context.Background(), "ada@example.com", "Hello", attempted)
	if err != nil || missing.Verified || missing.Found || missing.Detail != ErrScopeMissing.Error() {
		t.Fatalf("forbidden should be unverified scope: %#v %v", missing, err)
	}
	listed, err := client.ListSent(context.Background(), "ada@example.com", "Hello", attempted)
	if !errors.Is(err, ErrScopeMissing) || len(listed.Messages) != 0 {
		t.Fatalf("list scope %#v %v", listed, err)
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"messages":[]}`))
	}))
	defer empty.Close()
	client.BaseURL = empty.URL
	client.HTTP = empty.Client()
	none, err := client.SearchSent(context.Background(), "ada@example.com", "Hello", attempted)
	if err != nil || !none.Verified || none.Found {
		t.Fatalf("empty sent should be a confirmed miss: %#v %v", none, err)
	}
}

func TestListSentIgnoresOtherRecipientsAndOldMail(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	recent := strconv.FormatInt(now.Add(-24*time.Hour).UnixMilli(), 10)
	old := strconv.FormatInt(now.Add(-10*24*time.Hour).UnixMilli(), 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gmail/v1/users/me/messages":
			w.Write([]byte(`{"messages":[{"id":"other"},{"id":"old"},{"id":"keep"},{"id":"wrong-subject"}]}`))
		case "/gmail/v1/users/me/messages/other":
			w.Write([]byte(`{"id":"other","internalDate":"` + recent + `","payload":{"headers":[{"name":"Subject","value":"Hello"},{"name":"To","value":"other@example.com"}]}}`))
		case "/gmail/v1/users/me/messages/old":
			w.Write([]byte(`{"id":"old","internalDate":"` + old + `","payload":{"headers":[{"name":"Subject","value":"Hello"},{"name":"To","value":"ada@example.com"}]}}`))
		case "/gmail/v1/users/me/messages/keep":
			w.Write([]byte(`{"id":"keep","internalDate":"` + recent + `","payload":{"headers":[{"name":"Subject","value":"Hello"},{"name":"To","value":"Ada <ada@example.com>"}]}}`))
		case "/gmail/v1/users/me/messages/wrong-subject":
			w.Write([]byte(`{"id":"wrong-subject","internalDate":"` + recent + `","payload":{"headers":[{"name":"Subject","value":"Other"},{"name":"To","value":"ada@example.com"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), BaseURL: server.URL}
	listed, err := client.ListSent(context.Background(), "ada@example.com", "", now.Add(-7*24*time.Hour))
	if err != nil || len(listed.Messages) != 2 {
		t.Fatalf("window %#v %v", listed, err)
	}
	listed, err = client.ListSent(context.Background(), "ada@example.com", "Hello", now.Add(-7*24*time.Hour))
	if err != nil || len(listed.Messages) != 1 || listed.Messages[0].ID != "keep" || listed.Messages[0].To != "ada@example.com" || listed.Messages[0].Subject != "Hello" {
		t.Fatalf("subject filter %#v %v", listed, err)
	}
}

func TestListSentRateLimitIsNotAMissingScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":403,"message":"Rate Limit Exceeded","errors":[{"reason":"rateLimitExceeded"}]}}`, http.StatusForbidden)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), BaseURL: server.URL}
	_, err := client.ListSent(context.Background(), "ada@example.com", "", time.Now().Add(-time.Hour))
	if err == nil || errors.Is(err, ErrScopeMissing) || !strings.Contains(err.Error(), "403") {
		t.Fatalf("rate limit %v", err)
	}
}

func TestLoginScopesUseReadonlyAndNotMetadata(t *testing.T) {
	joined := strings.Join(scopes, " ")
	for _, want := range []string{
		"https://www.googleapis.com/auth/gmail.send",
		"https://www.googleapis.com/auth/gmail.readonly",
		"openid",
		"email",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %v", want, scopes)
		}
	}
	for _, banned := range []string{"gmail.metadata", "gmail.modify", "mail.google.com"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("scope %s is wider than gmail.readonly: %v", banned, scopes)
		}
	}
}
