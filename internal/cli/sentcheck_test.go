package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IMisbahk/mailsend/internal/auth"
)

func TestParseSpanAcceptsDays(t *testing.T) {
	parsed, err := parseSpan("30d")
	if err != nil || parsed != 30*24*time.Hour {
		t.Fatalf("30d %v %v", parsed, err)
	}
	parsed, err = parseSpan("7d")
	if err != nil || parsed != 7*24*time.Hour {
		t.Fatalf("7d %v %v", parsed, err)
	}
	parsed, err = parseSpan("168h")
	if err != nil || parsed != 7*24*time.Hour {
		t.Fatalf("168h %v %v", parsed, err)
	}
}

func TestSentCheckExitCodes(t *testing.T) {
	useTestConfig(t)
	recent := time.Now().Add(-24 * time.Hour).UnixMilli()
	var mode string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "scope":
			http.Error(w, `{"error":{"status":"PERMISSION_DENIED","message":"Request had insufficient authentication scopes."}}`, http.StatusForbidden)
		case "found":
			if r.URL.Path == "/gmail/v1/users/me/messages" {
				if !strings.Contains(r.URL.Query().Get("q"), "in:sent to:ada@example.com") {
					t.Errorf("query %q", r.URL.Query().Get("q"))
				}
				w.Write([]byte(`{"messages":[{"id":"abc"}]}`))
				return
			}
			w.Write([]byte(`{"id":"abc","internalDate":"` + strconv.FormatInt(recent, 10) + `","payload":{"headers":[{"name":"Subject","value":"Hello"},{"name":"To","value":"ada@example.com"}]}}`))
		default:
			w.Write([]byte(`{"messages":[]}`))
		}
	}))
	defer server.Close()
	previous := openGmailClient
	openGmailClient = func(context.Context, string) (*auth.Client, error) {
		return &auth.Client{HTTP: server.Client(), BaseURL: server.URL, Account: auth.Account{Name: "sunbeam", Email: "misbah@sunbeam.agency"}}, nil
	}
	t.Cleanup(func() { openGmailClient = previous })

	stdout, stderr, err := executeCLI(t, "", "--account", "sunbeam", "sent-check", "--to", "ada@example.com", "--since", "30d", "--json")
	if err != nil {
		t.Fatalf("empty %v %s %s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, `"messages": []`) || !strings.Contains(stdout, `"to": "ada@example.com"`) {
		t.Fatalf("empty json %s", stdout)
	}

	mode = "found"
	stdout, stderr, err = executeCLI(t, "", "--account", "sunbeam", "sent-check", "--to", "ada@example.com", "--subject", "Hello", "--since", "7d", "--json")
	var status *statusExit
	if !errors.As(err, &status) || status.code != 3 || status.err != nil {
		t.Fatalf("found exit %#v %v stdout %s stderr %s", status, err, stdout, stderr)
	}
	var payload struct {
		Messages []struct {
			ID      string `json:"id"`
			To      string `json:"to"`
			Subject string `json:"subject"`
		} `json:"messages"`
	}
	if jsonErr := json.Unmarshal([]byte(stdout), &payload); jsonErr != nil || len(payload.Messages) != 1 || payload.Messages[0].ID != "abc" || payload.Messages[0].Subject != "Hello" {
		t.Fatalf("found json %s %v", stdout, jsonErr)
	}

	mode = "scope"
	stdout, stderr, err = executeCLI(t, "", "--account", "sunbeam", "sent-check", "--to", "ada@example.com", "--json")
	status = nil
	if !errors.As(err, &status) || status.code != 2 || status.err == nil || status.err.Error() != auth.ErrScopeMissing.Error() {
		t.Fatalf("scope exit %#v %v stdout %s stderr %s", status, err, stdout, stderr)
	}
}
