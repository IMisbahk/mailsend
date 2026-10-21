package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	sentPageSize = 25
	sentMaxPages = 8
)

// ErrScopeMissing means the stored token cannot search Gmail Sent.
// Sending still works. The operator re-runs login to grant gmail.readonly.
var ErrScopeMissing = errors.New("scope missing, re-run mailsend login")

// SentMessage is one Gmail Sent message that matched a recipient search.
type SentMessage struct {
	ID      string    `json:"id"`
	Date    time.Time `json:"date"`
	To      string    `json:"to"`
	Subject string    `json:"subject"`
}

// SentList is a page of Sent matches. Truncated means more pages existed
// after the ones that were read. A truncated empty list is returned as an error
// so a caller cannot treat "not found" as proven.
type SentList struct {
	Messages  []SentMessage
	Truncated bool
}

// SentLookup is the result of searching Gmail Sent for a recipient and subject.
// Verified is false when the token cannot read mail or the request failed.
// In that case the caller must park the item instead of sending it again.
type SentLookup struct {
	Found     bool
	MessageID string
	Verified  bool
	Detail    string
}

// ListSent searches Gmail Sent for recipient, optionally limited by subject and
// since. Matches are confirmed from message headers, not from the query alone.
func (c *Client) ListSent(ctx context.Context, recipient, subject string, since time.Time) (SentList, error) {
	if c == nil || c.HTTP == nil {
		return SentList{}, fmt.Errorf("Gmail client is not configured")
	}
	recipient = strings.TrimSpace(recipient)
	if recipient == "" || strings.ContainsAny(recipient, " \t\r\n") {
		return SentList{}, fmt.Errorf("invalid recipient %q", recipient)
	}
	subject = strings.Join(strings.Fields(subject), " ")
	query := sentQuery(recipient, subject, since)
	pageToken := ""
	found := make([]SentMessage, 0)
	for page := 0; page < sentMaxPages; page++ {
		values := url.Values{}
		values.Set("maxResults", strconv.Itoa(sentPageSize))
		values.Set("q", query)
		if pageToken != "" {
			values.Set("pageToken", pageToken)
		}
		listURL := c.baseURL() + "/gmail/v1/users/me/messages?" + values.Encode()
		body, status, err := c.gmailGET(ctx, listURL)
		if err != nil {
			return SentList{}, err
		}
		if scopeDenied(status, body) {
			return SentList{}, ErrScopeMissing
		}
		if status < 200 || status >= 300 {
			return SentList{}, fmt.Errorf("Gmail Sent search returned HTTP %d", status)
		}
		var listed struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err = json.Unmarshal(body, &listed); err != nil {
			return SentList{}, fmt.Errorf("Gmail Sent search returned unreadable JSON")
		}
		for _, message := range listed.Messages {
			if message.ID == "" {
				continue
			}
			hit, ok, err := c.readSentMessage(ctx, message.ID, recipient, subject, since)
			if err != nil {
				return SentList{}, err
			}
			if ok {
				found = append(found, hit)
			}
		}
		if listed.NextPageToken == "" {
			return SentList{Messages: found}, nil
		}
		pageToken = listed.NextPageToken
	}
	if len(found) == 0 {
		return SentList{}, fmt.Errorf("Gmail Sent search was truncated before it could rule out a match")
	}
	return SentList{Messages: found, Truncated: true}, nil
}

// SearchSent looks for one Sent message to recipient with subject at or after
// attemptedAt minus two minutes. A missing read scope is unverified, with
// ErrScopeMissing's text, so a crashed send is parked instead of retried.
func (c *Client) SearchSent(ctx context.Context, recipient, subject string, attemptedAt time.Time) (SentLookup, error) {
	recipient = strings.TrimSpace(recipient)
	subject = strings.TrimSpace(subject)
	if recipient == "" || subject == "" {
		return SentLookup{Verified: false, Detail: "recipient and subject are required"}, nil
	}
	var since time.Time
	if !attemptedAt.IsZero() {
		since = attemptedAt.Add(-2 * time.Minute)
	}
	listed, err := c.ListSent(ctx, recipient, subject, since)
	if err != nil {
		if errors.Is(err, ErrScopeMissing) {
			return SentLookup{Verified: false, Detail: ErrScopeMissing.Error()}, nil
		}
		return SentLookup{Verified: false, Detail: err.Error()}, err
	}
	if len(listed.Messages) == 0 {
		if listed.Truncated {
			return SentLookup{Verified: false, Detail: "Gmail Sent search was truncated before it could rule out a match"}, nil
		}
		return SentLookup{Verified: true, Found: false}, nil
	}
	return SentLookup{Verified: true, Found: true, MessageID: listed.Messages[0].ID}, nil
}

func (c *Client) readSentMessage(ctx context.Context, id, recipient, subject string, since time.Time) (SentMessage, bool, error) {
	values := url.Values{}
	values.Set("format", "metadata")
	values.Add("metadataHeaders", "Subject")
	values.Add("metadataHeaders", "To")
	values.Add("metadataHeaders", "Cc")
	values.Add("metadataHeaders", "Bcc")
	getURL := c.baseURL() + "/gmail/v1/users/me/messages/" + url.PathEscape(id) + "?" + values.Encode()
	body, status, err := c.gmailGET(ctx, getURL)
	if err != nil {
		return SentMessage{}, false, err
	}
	if scopeDenied(status, body) {
		return SentMessage{}, false, ErrScopeMissing
	}
	if status == http.StatusNotFound {
		return SentMessage{}, false, nil
	}
	if status < 200 || status >= 300 {
		return SentMessage{}, false, fmt.Errorf("Gmail message %s returned HTTP %d", id, status)
	}
	var message struct {
		InternalDate string `json:"internalDate"`
		Payload      struct {
			Headers []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
		} `json:"payload"`
	}
	if err = json.Unmarshal(body, &message); err != nil {
		return SentMessage{}, false, fmt.Errorf("Gmail message %s metadata could not be read", id)
	}
	if len(message.Payload.Headers) == 0 {
		return SentMessage{}, false, fmt.Errorf("Gmail message %s metadata could not be read", id)
	}
	var storedSubject, toHeader, ccHeader, bccHeader string
	for _, header := range message.Payload.Headers {
		switch strings.ToLower(header.Name) {
		case "subject":
			storedSubject = strings.TrimSpace(header.Value)
		case "to":
			toHeader = header.Value
		case "cc":
			ccHeader = header.Value
		case "bcc":
			bccHeader = header.Value
		}
	}
	if subject != "" && !strings.EqualFold(storedSubject, subject) {
		return SentMessage{}, false, nil
	}
	if !headerHasAddress(toHeader, recipient) && !headerHasAddress(ccHeader, recipient) && !headerHasAddress(bccHeader, recipient) {
		return SentMessage{}, false, nil
	}
	sentAt, dated, err := sentInstant(message.InternalDate)
	if err != nil {
		return SentMessage{}, false, fmt.Errorf("Gmail message %s has an unreadable date", id)
	}
	if !since.IsZero() {
		if !dated {
			return SentMessage{}, false, fmt.Errorf("Gmail message %s has no date", id)
		}
		if sentAt.Before(since) {
			return SentMessage{}, false, nil
		}
	}
	return SentMessage{ID: id, Date: sentAt.UTC(), To: recipient, Subject: storedSubject}, true, nil
}

func sentInstant(internalDate string) (time.Time, bool, error) {
	internalDate = strings.TrimSpace(internalDate)
	if internalDate == "" {
		return time.Time{}, false, nil
	}
	millis, err := strconv.ParseInt(internalDate, 10, 64)
	if err != nil {
		return time.Time{}, false, err
	}
	return time.UnixMilli(millis), true, nil
}

func sentQuery(recipient, subject string, since time.Time) string {
	parts := []string{"in:sent", "to:" + recipient}
	if !since.IsZero() {
		parts = append(parts, fmt.Sprintf("after:%d", since.Add(-time.Second).Unix()))
	}
	if subject != "" {
		parts = append(parts, "subject:"+quoteGmailTerm(subject))
	}
	return strings.Join(parts, " ")
}

func quoteGmailTerm(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func scopeDenied(status int, body []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	text := strings.ToLower(string(body))
	if strings.Contains(text, "access_token_scope_insufficient") || strings.Contains(text, "insufficientpermissions") {
		return true
	}
	if strings.Contains(text, "metadata scope") {
		return true
	}
	if strings.Contains(text, "insufficient") && (strings.Contains(text, "scope") || strings.Contains(text, "permission")) {
		return true
	}
	return false
}

func (c *Client) gmailGET(ctx context.Context, target string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func headerHasAddress(header, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || strings.TrimSpace(header) == "" {
		return false
	}
	list, err := mail.ParseAddressList(header)
	if err != nil {
		return strings.Contains(strings.ToLower(header), email)
	}
	for _, addr := range list {
		if strings.EqualFold(addr.Address, email) {
			return true
		}
	}
	return false
}
