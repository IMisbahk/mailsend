package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SentLookup is the result of searching Gmail Sent for a recipient and subject.
// Verified is false when the token cannot read mail or the request failed.
// In that case the caller must park the item instead of sending it again.
type SentLookup struct {
	Found     bool
	MessageID string
	Verified  bool
	Detail    string
}

// SearchSent lists recent Sent messages and matches recipient, subject, and time.
// The default mailsend login only requests gmail.send, which cannot read Sent.
// A 401 or 403 is reported as unverified rather than as a hard failure.
func (c *Client) SearchSent(ctx context.Context, recipient, subject string, attemptedAt time.Time) (SentLookup, error) {
	if c == nil || c.HTTP == nil {
		return SentLookup{Verified: false, Detail: "Gmail client is not configured"}, nil
	}
	recipient = strings.TrimSpace(recipient)
	subject = strings.TrimSpace(subject)
	if recipient == "" || subject == "" {
		return SentLookup{Verified: false, Detail: "recipient and subject are required"}, nil
	}
	query := fmt.Sprintf("in:sent to:%s subject:%s", recipient, strings.ReplaceAll(subject, `"`, " "))
	listURL := c.baseURL() + "/gmail/v1/users/me/messages?maxResults=10&q=" + url.QueryEscape(query)
	body, status, err := c.gmailGET(ctx, listURL)
	if err != nil {
		return SentLookup{Verified: false, Detail: err.Error()}, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return SentLookup{Verified: false, Detail: fmt.Sprintf("Gmail Sent search returned HTTP %d; the login scope cannot read mail, so this send cannot be confirmed", status)}, nil
	}
	if status < 200 || status >= 300 {
		return SentLookup{Verified: false, Detail: fmt.Sprintf("Gmail Sent search returned HTTP %d", status)}, nil
	}
	var listed struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err = json.Unmarshal(body, &listed); err != nil {
		return SentLookup{Verified: false, Detail: "Gmail Sent search returned unreadable JSON"}, nil
	}
	if len(listed.Messages) == 0 {
		return SentLookup{Verified: true, Found: false}, nil
	}
	for _, message := range listed.Messages {
		if message.ID == "" {
			continue
		}
		match, verified, err := c.sentMessageMatches(ctx, message.ID, recipient, subject, attemptedAt)
		if err != nil {
			return SentLookup{Verified: false, Detail: err.Error()}, err
		}
		if !verified {
			return SentLookup{Verified: false, Detail: "Gmail message metadata could not be read"}, nil
		}
		if match {
			return SentLookup{Verified: true, Found: true, MessageID: message.ID}, nil
		}
	}
	return SentLookup{Verified: true, Found: false}, nil
}

func (c *Client) sentMessageMatches(ctx context.Context, id, recipient, subject string, attemptedAt time.Time) (bool, bool, error) {
	values := url.Values{}
	values.Set("format", "metadata")
	values.Add("metadataHeaders", "Subject")
	values.Add("metadataHeaders", "To")
	values.Add("metadataHeaders", "Cc")
	getURL := c.baseURL() + "/gmail/v1/users/me/messages/" + url.PathEscape(id) + "?" + values.Encode()
	body, status, err := c.gmailGET(ctx, getURL)
	if err != nil {
		return false, false, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return false, false, nil
	}
	if status == http.StatusNotFound {
		return false, true, nil
	}
	if status < 200 || status >= 300 {
		return false, false, nil
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
		return false, false, nil
	}
	if len(message.Payload.Headers) == 0 {
		return false, false, nil
	}
	var storedSubject, toHeader, ccHeader string
	for _, header := range message.Payload.Headers {
		switch strings.ToLower(header.Name) {
		case "subject":
			storedSubject = header.Value
		case "to":
			toHeader = header.Value
		case "cc":
			ccHeader = header.Value
		}
	}
	if !strings.EqualFold(strings.TrimSpace(storedSubject), subject) {
		return false, true, nil
	}
	if !headerHasAddress(toHeader, recipient) && !headerHasAddress(ccHeader, recipient) {
		return false, true, nil
	}
	if !attemptedAt.IsZero() && message.InternalDate != "" {
		millis, err := strconv.ParseInt(message.InternalDate, 10, 64)
		if err != nil {
			return false, false, nil
		}
		sentAt := time.UnixMilli(millis)
		if sentAt.Before(attemptedAt.Add(-2 * time.Minute)) {
			return false, true, nil
		}
	}
	return true, true, nil
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
