package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestMockGmailSendAndIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/userinfo":
			fmt.Fprint(w, `{"email":"sender@example.com"}`)
		case "/gmail/v1/users/me/messages/send":
			var body map[string]string
			if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
				t.Error(e)
			}
			if body["raw"] != "encoded" {
				t.Errorf("raw payload = %q", body["raw"])
			}
			fmt.Fprint(w, `{"id":"gmail-message-1"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := &Client{HTTP: server.Client(), BaseURL: server.URL, IdentityURL: server.URL}
	p, e := c.Profile(context.Background())
	if e != nil || p.EmailAddress != "sender@example.com" {
		t.Fatalf("profile %#v, %v", p, e)
	}
	id, ambiguous, e := c.Send(context.Background(), "encoded")
	if e != nil || ambiguous || id != "gmail-message-1" {
		t.Fatalf("send %q ambiguous=%v err=%v", id, ambiguous, e)
	}
}
func TestSendErrorsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		status           int
		retry, ambiguous bool
	}{{429, true, false}, {401, false, false}, {403, false, false}, {500, false, true}} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status == 429 {
					w.Header().Set("Retry-After", "7")
				}
				http.Error(w, "quota test", tc.status)
			}))
			defer s.Close()
			c := &Client{HTTP: s.Client(), BaseURL: s.URL}
			_, ambiguous, e := c.Send(context.Background(), "x")
			if e == nil {
				t.Fatal("expected API error")
			}
			if ambiguous != tc.ambiguous {
				t.Fatalf("ambiguous=%v want %v (%v)", ambiguous, tc.ambiguous, e)
			}
			if Retryable(e) != tc.retry {
				t.Fatalf("retryable=%v want %v", Retryable(e), tc.retry)
			}
			if tc.status == 429 && RetryDelay(e, time.Second) != 7*time.Second {
				t.Fatalf("Retry-After not respected: %v", RetryDelay(e, time.Second))
			}
			if tc.status == 401 && !strings.Contains(e.Error(), "run mailsend login") {
				t.Fatalf("unclear auth error: %v", e)
			}
		})
	}
}

type errorTransport struct{ err error }

func (t errorTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

func TestRefreshFailureIsSafeAndActionable(t *testing.T) {
	c := &Client{HTTP: &http.Client{Transport: errorTransport{err: &oauth2.RetrieveError{ErrorCode: "invalid_grant"}}}}
	_, ambiguous, err := c.Send(context.Background(), "raw")
	if err == nil || ambiguous || !strings.Contains(err.Error(), "run mailsend login") {
		t.Fatalf("refresh failure classification ambiguous=%v err=%v", ambiguous, err)
	}
}
type classifiedError struct {
	retry bool
	quota bool
	authn bool
}

func (e classifiedError) Error() string   { return "classified" }
func (e classifiedError) Retryable() bool { return e.retry }
func (e classifiedError) IsQuota() bool   { return e.quota }
func (e classifiedError) IsAuth() bool    { return e.authn }

func TestInterfaceErrorsAreClassified(t *testing.T) {
	temporary := classifiedError{retry: true, quota: true}
	if !Retryable(temporary) || !IsQuotaError(temporary) || IsAuthError(temporary) {
		t.Fatal("temporary quota error was misclassified")
	}
	permanent := classifiedError{authn: true}
	if Retryable(permanent) || IsQuotaError(permanent) || !IsAuthError(permanent) {
		t.Fatal("permanent auth error was misclassified")
	}
}

func TestLostSendResponseIsUncertain(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("server does not support hijacking")
		}
		conn, _, e := h.Hijack()
		if e != nil {
			t.Fatal(e)
		}
		_ = conn.Close()
	}))
	defer s.Close()
	c := &Client{HTTP: s.Client(), BaseURL: s.URL}
	_, ambiguous, e := c.Send(context.Background(), "raw")
	if e == nil || !ambiguous {
		t.Fatalf("expected uncertain transport result, got ambiguous=%v err=%v", ambiguous, e)
	}
}
