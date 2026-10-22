package imap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// sentSearchLimit caps how many Sent UIDs are inspected. A truncated search
// that cannot prove a match is an error.
var sentSearchLimit = 200

// SentHit is one Sent message that matched a recipient search.
type SentHit struct {
	UID       uint32
	Date      time.Time
	Recipient string
	Subject   string
}

// SentPage is a Sent search result. Truncated means the mailbox had more
// matching UIDs than were inspected. A truncated page with no matches is
// returned as an error so the caller cannot treat "not found" as proven.
type SentPage struct {
	Folder    string
	Messages  []SentHit
	Truncated bool
}

var sentFolderNames = []string{"Sent", "Sent Items", "Sent Messages", "INBOX.Sent"}

// SearchSent looks in the Sent folder for recipient, optionally limited by
// an exact subject and a time. The folder is the SPECIAL-USE \Sent mailbox
// when the server advertises one, otherwise the first existing common name.
// The mailbox is opened read-only and message bodies are not fetched.
func (c *Client) SearchSent(ctx context.Context, recipient, subject string, since time.Time) (SentPage, error) {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" || strings.ContainsAny(recipient, " \t\r\n") {
		return SentPage{}, fmt.Errorf("invalid recipient %q", recipient)
	}
	subject = strings.Join(strings.Fields(subject), " ")
	var page SentPage
	err := c.withSession(ctx, func(sess *session) error {
		folder, err := sess.findSentMailbox(ctx)
		if err != nil {
			return err
		}
		if err = sess.open(ctx, folder, true); err != nil {
			return err
		}
		found, truncated, err := sess.searchSent(ctx, recipient, subject, since)
		if err != nil {
			return err
		}
		page = SentPage{Folder: folder, Messages: found, Truncated: truncated}
		return nil
	})
	if err != nil {
		return SentPage{}, err
	}
	return page, nil
}

func (s *session) findSentMailbox(ctx context.Context) (string, error) {
	listed, err := s.listMailboxes(ctx)
	if err != nil {
		return "", err
	}
	return chooseSentMailbox(listed)
}

func (s *session) listMailboxes(ctx context.Context) ([]*imap.ListData, error) {
	caps := s.client.Caps()
	if caps != nil && caps.Has(imap.CapSpecialUse) && (caps.Has(imap.CapListExtended) || caps.Has(imap.CapIMAP4rev2)) {
		listed, err := s.client.List("", "*", &imap.ListOptions{ReturnSpecialUse: true}).Collect()
		if err = s.done(ctx, err); err == nil {
			return listed, nil
		}
	}
	listed, err := s.client.List("", "*", nil).Collect()
	if err = s.done(ctx, err); err != nil {
		return nil, err
	}
	return listed, nil
}

func chooseSentMailbox(listed []*imap.ListData) (string, error) {
	for _, item := range listed {
		if item == nil || item.Mailbox == "" || !mailboxSelectable(item) {
			continue
		}
		if mailboxHasAttr(item, imap.MailboxAttrSent) {
			return item.Mailbox, nil
		}
	}
	byName := map[string]string{}
	for _, item := range listed {
		if item == nil || item.Mailbox == "" || !mailboxSelectable(item) {
			continue
		}
		key := strings.ToLower(item.Mailbox)
		if _, ok := byName[key]; !ok {
			byName[key] = item.Mailbox
		}
	}
	for _, name := range sentFolderNames {
		if actual, ok := byName[strings.ToLower(name)]; ok {
			return actual, nil
		}
	}
	return "", fmt.Errorf("IMAP Sent folder not found (looked for \\Sent, Sent, Sent Items, Sent Messages, INBOX.Sent)")
}

func mailboxHasAttr(item *imap.ListData, want imap.MailboxAttr) bool {
	for _, attr := range item.Attrs {
		if strings.EqualFold(string(attr), string(want)) {
			return true
		}
	}
	return false
}

func mailboxSelectable(item *imap.ListData) bool {
	return !mailboxHasAttr(item, imap.MailboxAttrNoSelect)
}

func (s *session) searchSent(ctx context.Context, recipient, subject string, since time.Time) ([]SentHit, bool, error) {
	criteria := recipientCriteria(recipient)
	if !since.IsZero() {
		// IMAP SINCE is a date. The exact instant is applied after the fetch.
		criteria.Since = dateOnlyUTC(since)
	}
	if subject != "" {
		criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{Key: "Subject", Value: subject})
	}
	data, err := s.client.UIDSearch(criteria, nil).Wait()
	if err = s.done(ctx, err); err != nil {
		return nil, false, err
	}
	uids := data.AllUIDs()
	truncated := false
	if len(uids) > sentSearchLimit {
		sort.Slice(uids, func(i, j int) bool { return uids[i] > uids[j] })
		uids = uids[:sentSearchLimit]
		truncated = true
	}
	if len(uids) == 0 {
		return []SentHit{}, false, nil
	}
	fetched, err := s.fetchUIDs(ctx, uids, &imap.FetchOptions{
		UID:          true,
		Envelope:     true,
		InternalDate: true,
	})
	if err != nil {
		return nil, false, err
	}
	found := make([]SentHit, 0)
	for _, msg := range fetched {
		hit, ok, err := sentHit(msg, recipient, subject, since)
		if err != nil {
			return nil, false, err
		}
		if ok {
			found = append(found, hit)
		}
	}
	if truncated && len(found) == 0 {
		return nil, false, fmt.Errorf("IMAP Sent search was truncated before it could rule out a match")
	}
	sort.Slice(found, func(i, j int) bool {
		if !found[i].Date.Equal(found[j].Date) {
			return found[i].Date.After(found[j].Date)
		}
		return found[i].UID > found[j].UID
	})
	return found, truncated && len(found) > 0, nil
}

func recipientCriteria(recipient string) *imap.SearchCriteria {
	to := imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "To", Value: recipient}}}
	cc := imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Cc", Value: recipient}}}
	bcc := imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Bcc", Value: recipient}}}
	return &imap.SearchCriteria{Or: [][2]imap.SearchCriteria{{
		{Or: [][2]imap.SearchCriteria{{to, cc}}},
		bcc,
	}}}
}

func sentHit(msg *imapclient.FetchMessageBuffer, recipient, subject string, since time.Time) (SentHit, bool, error) {
	if msg == nil || msg.UID == 0 || msg.Envelope == nil {
		return SentHit{}, false, fmt.Errorf("IMAP Sent message metadata could not be read")
	}
	storedSubject := strings.TrimSpace(msg.Envelope.Subject)
	if subject != "" && !strings.EqualFold(storedSubject, subject) {
		return SentHit{}, false, nil
	}
	if !envelopeHasAddress(msg.Envelope, recipient) {
		return SentHit{}, false, nil
	}
	sentAt := messageTime(msg)
	if !since.IsZero() {
		if sentAt.IsZero() {
			return SentHit{}, false, fmt.Errorf("IMAP message %d has no date", msg.UID)
		}
		if sentAt.Before(since) {
			return SentHit{}, false, nil
		}
	}
	return SentHit{UID: uint32(msg.UID), Date: sentAt.UTC(), Recipient: recipient, Subject: storedSubject}, true, nil
}
