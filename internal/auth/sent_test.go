package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSearchSentMatchesRecipientAndParksOnForbidden(t *testing.T) {
	attempted := time.UnixMilli(1761040800000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gmail/v1/users/me/messages":
			if r.URL.Query().Get("q") == "" {
				t.Errorf("missing query")
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
		http.Error(w, "insufficient authentication scopes", http.StatusForbidden)
	}))
	defer denied.Close()
	client.BaseURL = denied.URL
	client.HTTP = denied.Client()
	missing, err := client.SearchSent(context.Background(), "ada@example.com", "Hello", attempted)
	if err != nil || missing.Verified || missing.Found {
		t.Fatalf("forbidden should be unverified: %#v %v", missing, err)
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
