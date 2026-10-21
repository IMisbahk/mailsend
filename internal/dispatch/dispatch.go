package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IMisbahk/mailsend/internal/auth"
	mailmsg "github.com/IMisbahk/mailsend/internal/mail"
	"github.com/IMisbahk/mailsend/internal/store"
	"github.com/gofrs/flock"
)

// ErrDispatcherBusy means another dispatcher process already holds the account lock.
var ErrDispatcherBusy = errors.New("dispatcher is already running for this account")

const defaultPoll = 15 * time.Second
const sendTimeout = 90 * time.Second
const defaultNotSentRetries = 3

// Clock is the time source. Tests advance it inside Sleep.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, delay time.Duration) error
}

// WallClock is the process clock.
type WallClock struct{}

func (WallClock) Now() time.Time { return time.Now() }

func (WallClock) Sleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Sender is the mail provider. Gmail and SMTP clients both satisfy it.
type Sender interface {
	Send(ctx context.Context, built mailmsg.BuiltMessage) (messageID string, ambiguous bool, err error)
	FromAddress() string
	DefaultReplyTo() string
	Provider() string
}

// Result is the machine-readable summary printed when dispatch stops.
type Result struct {
	Status         string       `json:"status"`
	Account        string       `json:"account"`
	SentThisRun    int          `json:"sent_this_run"`
	BlockedThisRun int          `json:"blocked_this_run"`
	FailedThisRun  int          `json:"failed_this_run"`
	Parked         []ParkNotice `json:"parked"`
	Warnings       []string     `json:"warnings,omitempty"`
	Detail         string       `json:"detail,omitempty"`
}

// ParkNotice is one item a person needs to look at before it can be sent or closed.
type ParkNotice struct {
	ID      int64  `json:"id"`
	BatchID string `json:"batch_id"`
	Email   string `json:"email"`
	Subject string `json:"subject"`
	Reason  string `json:"reason"`
}

// Dispatcher sends queued mail for one account.
// Interval is the account-wide minimum gap. Each batch may require a longer gap.
type Dispatcher struct {
	DB                *store.DB
	Account           string
	Interval          time.Duration
	DailyCap          int
	Timezone          *time.Location
	Holds             []store.HoldFile
	Hook              string
	AfterSend         string
	Once              bool
	Poll              time.Duration
	Clock             Clock
	Sender            Sender
	Probe             SentProbe
	HookRunner        HookRunner
	Logf              func(string, ...any)
	LockPath          string
	MaxNotSentRetries int

	sentThisRun    int
	blockedThisRun int
	failedThisRun  int
	warnings       []string
	announced      string
	detail         string
}

// LockPath is the flock file that keeps two dispatcher processes from sending.
func LockPath(dir, account string) (string, error) {
	if err := auth.ValidateAccountName(account); err != nil {
		return "", err
	}
	return filepath.Join(dir, "locks", "dispatch-"+account+".lock"), nil
}

// Run holds the account lock and sends until the queue is idle, the process is asked to stop, or --once finishes one item.
func (d *Dispatcher) Run(ctx context.Context) (Result, error) {
	if d.DB == nil {
		return Result{}, fmt.Errorf("dispatcher has no database")
	}
	if strings.TrimSpace(d.Account) == "" {
		return Result{}, fmt.Errorf("dispatcher requires an account")
	}
	if d.Sender == nil {
		return Result{}, fmt.Errorf("dispatcher has no sender")
	}
	if d.Clock == nil {
		d.Clock = WallClock{}
	}
	if d.Poll <= 0 {
		d.Poll = defaultPoll
	}
	if d.Timezone == nil {
		d.Timezone = time.Local
	}
	if d.MaxNotSentRetries <= 0 {
		d.MaxNotSentRetries = defaultNotSentRetries
	}
	if d.HookRunner == nil {
		d.HookRunner = RunCommandHook
	}
	if d.LockPath != "" {
		lock, err := acquireDispatchLock(d.LockPath)
		if err != nil {
			return Result{}, err
		}
		defer lock.Unlock()
	}
	if err := d.reconcileSending(ctx); err != nil {
		return Result{}, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return d.stopped(err)
		}
		if err := d.DB.RefreshQueueBatches(d.Account, d.now()); err != nil {
			return Result{}, err
		}
		pending, err := d.DB.PendingQueueSends(d.Account)
		if err != nil {
			return Result{}, err
		}
		if len(pending) == 0 {
			return d.finish("completed")
		}
		chosen, holdPath, allHeld := d.pick(pending)
		if allHeld {
			result, done, err := d.wait(ctx, d.now().Add(d.Poll), "hold: waiting because "+holdPath+" exists")
			if done || err != nil {
				return result, err
			}
			continue
		}
		over, until, err := d.capReached()
		if err != nil {
			return Result{}, err
		}
		if over {
			reason := fmt.Sprintf("daily cap %d reached in %s; next window %s", d.DailyCap, d.Timezone.String(), until.In(d.Timezone).Format(time.RFC3339))
			result, done, err := d.wait(ctx, until, reason)
			if done || err != nil {
				return result, err
			}
			continue
		}
		readyAt, err := d.readyAt(chosen)
		if err != nil {
			return Result{}, err
		}
		if d.now().Before(readyAt) {
			reason := fmt.Sprintf("waiting until %s to send %s", readyAt.In(d.Timezone).Format(time.RFC3339), chosen.Item.Email)
			result, done, err := d.wait(ctx, readyAt, reason)
			if done || err != nil {
				return result, err
			}
			continue
		}
		acted, result, done, err := d.deliver(ctx, chosen)
		if done || err != nil {
			return result, err
		}
		if !acted {
			continue
		}
		d.announced = ""
		if d.Once {
			return d.finish("ok")
		}
	}
}

func (d *Dispatcher) deliver(ctx context.Context, send store.QueueSend) (bool, Result, bool, error) {
	if path, held := d.itemHeld(send.Item, send.Batch); held {
		result, done, err := d.wait(ctx, d.now().Add(d.Poll), "hold: waiting because "+path+" exists")
		return false, result, done, err
	}
	over, until, err := d.capReached()
	if err != nil {
		return false, Result{}, true, err
	}
	if over {
		reason := fmt.Sprintf("daily cap %d reached in %s; next window %s", d.DailyCap, d.Timezone.String(), until.In(d.Timezone).Format(time.RFC3339))
		result, done, err := d.wait(ctx, until, reason)
		return false, result, done, err
	}
	if command := d.hookCommand(send.Batch); command != "" {
		code, reason, hookErr := d.HookRunner(ctx, command, payloadFor(send))
		if hookErr != nil && ctx.Err() != nil {
			result, err := d.stopped(ctx.Err())
			return false, result, true, err
		}
		if hookErr != nil || code != 0 {
			if reason == "" && hookErr != nil {
				reason = hookErr.Error()
			}
			if reason == "" {
				reason = fmt.Sprintf("pre-send hook exited %d", code)
			}
			if err = d.DB.SetQueueItemStatus(send.Item.ID, "blocked", reason, d.now()); err != nil {
				return false, Result{}, true, err
			}
			d.blockedThisRun++
			d.logf("blocked %s: %s", send.Item.Email, reason)
			return true, Result{}, false, nil
		}
	}
	if path, held := d.itemHeld(send.Item, send.Batch); held {
		result, done, err := d.wait(ctx, d.now().Add(d.Poll), "hold: waiting because "+path+" exists")
		return false, result, done, err
	}
	over, until, err = d.capReached()
	if err != nil {
		return false, Result{}, true, err
	}
	if over {
		reason := fmt.Sprintf("daily cap %d reached in %s; next window %s", d.DailyCap, d.Timezone.String(), until.In(d.Timezone).Format(time.RFC3339))
		result, done, err := d.wait(ctx, until, reason)
		return false, result, done, err
	}
	claimed, err := d.DB.ClaimQueueItem(send.Item.ID, d.now())
	if err != nil {
		return false, Result{}, true, err
	}
	send.Item = claimed
	built, err := d.build(send)
	if err != nil {
		if finishErr := d.DB.FinishQueueItem(send.Item.ID, "failed", "", err.Error(), d.now()); finishErr != nil {
			return false, Result{}, true, finishErr
		}
		d.failedThisRun++
		d.logf("failed %s before send: %s", send.Item.Email, err)
		return true, Result{}, false, nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	messageID, ambiguous, sendErr := d.Sender.Send(sendCtx, built)
	cancel()
	if ctx.Err() != nil && sendErr != nil && !ambiguous {
		result, err := d.stopped(ctx.Err())
		return false, result, true, err
	}
	sentAt := d.now()
	if sendErr == nil && !ambiguous && messageID != "" {
		if err = d.DB.FinishQueueItem(send.Item.ID, "sent", messageID, "", sentAt); err != nil {
			return false, Result{}, true, err
		}
		if err = d.noteSent(sentAt); err != nil {
			return false, Result{}, true, err
		}
		d.sentThisRun++
		d.logf("sent %s (message %s)", send.Item.Email, messageID)
		d.runAfterSend(ctx, send, messageID)
		return true, Result{}, false, nil
	}
	if ambiguous || (sendErr == nil && messageID == "") {
		return d.finishAmbiguous(ctx, send, sentAt, sendErr)
	}
	if auth.IsQuotaError(sendErr) || auth.IsAuthError(sendErr) {
		if err = d.DB.FinishQueueItem(send.Item.ID, "pending", "", sendErr.Error(), d.now()); err != nil {
			return false, Result{}, true, err
		}
		delay := auth.RetryDelay(sendErr, time.Minute)
		if delay < time.Minute {
			delay = time.Minute
		}
		d.logf("%s stayed queued: %s", send.Item.Email, sendErr)
		result, done, err := d.wait(ctx, d.now().Add(delay), sendErr.Error())
		return false, result, done, err
	}
	if err = d.DB.FinishQueueItem(send.Item.ID, "failed", "", sendErr.Error(), d.now()); err != nil {
		return false, Result{}, true, err
	}
	d.failedThisRun++
	d.logf("failed %s: %s", send.Item.Email, sendErr)
	return true, Result{}, false, nil
}

func (d *Dispatcher) finishAmbiguous(ctx context.Context, send store.QueueSend, attempted time.Time, sendErr error) (bool, Result, bool, error) {
	evidence, probeErr := d.lookup(ctx, send, attempted)
	if probeErr != nil && ctx.Err() != nil {
		result, err := d.stopped(ctx.Err())
		return false, result, true, err
	}
	if evidence.Verified && evidence.Found && evidence.MessageID != "" {
		if err := d.markReconciledSent(send, evidence.MessageID, attempted, "provider response was ambiguous; sent mail matched"); err != nil {
			return false, Result{}, true, err
		}
		d.sentThisRun++
		d.runAfterSend(ctx, send, evidence.MessageID)
		return true, Result{}, false, nil
	}
	if evidence.Verified && !evidence.Found {
		if send.Item.Attempts >= d.MaxNotSentRetries {
			if err := d.park(send, "provider response was ambiguous and the message was confirmed not sent"); err != nil {
				return false, Result{}, true, err
			}
			return true, Result{}, false, nil
		}
		if err := d.DB.FinishQueueItem(send.Item.ID, "pending", "", "provider response was ambiguous and sent mail was not found", d.now()); err != nil {
			return false, Result{}, true, err
		}
		d.logf("%s was not accepted; it stays queued", send.Item.Email)
		return true, Result{}, false, nil
	}
	reason := "provider response was ambiguous and the send could not be confirmed"
	if sendErr != nil {
		reason = reason + ": " + sendErr.Error()
	}
	if evidence.Detail != "" {
		reason = reason + ": " + evidence.Detail
	}
	if err := d.park(send, reason); err != nil {
		return false, Result{}, true, err
	}
	return true, Result{}, false, nil
}

func (d *Dispatcher) reconcileSending(ctx context.Context) error {
	sending, err := d.DB.SendingQueueSends(d.Account)
	if err != nil {
		return err
	}
	for _, send := range sending {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = d.reconcileOne(ctx, send); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) reconcileOne(ctx context.Context, send store.QueueSend) error {
	attempted := d.now()
	if send.Item.AttemptStartedAt != "" {
		if parsed, err := store.ParseStamp(send.Item.AttemptStartedAt); err == nil {
			attempted = parsed
		}
	}
	messageID, found, err := d.DB.FindConfirmedSend(d.Account, send.Item.Email, send.Item.Subject, attempted.Add(-2*time.Minute), send.Item.HistoryRecipientID)
	if err != nil {
		return err
	}
	if found {
		return d.markReconciledSent(send, messageID, attempted, "matched send history")
	}
	evidence, probeErr := d.lookup(ctx, send, attempted)
	if probeErr != nil && ctx.Err() != nil {
		return probeErr
	}
	if evidence.Verified && evidence.Found && evidence.MessageID != "" {
		return d.markReconciledSent(send, evidence.MessageID, attempted, "matched sent mail")
	}
	if evidence.Verified && !evidence.Found {
		if send.Item.Attempts >= d.MaxNotSentRetries {
			return d.park(send, fmt.Sprintf("confirmed not sent after %d attempts", send.Item.Attempts))
		}
		d.logf("reconciled %s as not sent; it will be tried again", send.Item.Email)
		return d.DB.FinishQueueItem(send.Item.ID, "pending", "", "reconciled: provider did not accept the message", d.now())
	}
	reason := "could not confirm whether the send was accepted"
	if evidence.Detail != "" {
		reason += ": " + evidence.Detail
	}
	if probeErr != nil {
		reason += ": " + probeErr.Error()
	}
	return d.park(send, reason)
}

func (d *Dispatcher) lookup(ctx context.Context, send store.QueueSend, attempted time.Time) (SentEvidence, error) {
	if d.Probe == nil {
		return SentEvidence{Verified: false, Detail: "no sent-mail check is configured"}, nil
	}
	return d.Probe.ProbeSent(ctx, SentQuery{
		Account: d.Account, Email: send.Item.Email, Subject: send.Item.Subject,
		AttemptedAt: attempted, HistoryJobID: send.Item.HistoryJobID,
	})
}

func (d *Dispatcher) markReconciledSent(send store.QueueSend, messageID string, at time.Time, how string) error {
	if err := d.DB.FinishQueueItem(send.Item.ID, "sent", messageID, "", d.now()); err != nil {
		return err
	}
	if err := d.noteSent(at); err != nil {
		return err
	}
	d.logf("reconciled %s as sent (%s, message %s)", send.Item.Email, how, messageID)
	return nil
}

func (d *Dispatcher) park(send store.QueueSend, reason string) error {
	reason = truncateText(reason, 400)
	if err := d.DB.FinishQueueItem(send.Item.ID, "parked", "", reason, d.now()); err != nil {
		return err
	}
	d.logf("parked %s for review: %s", send.Item.Email, reason)
	return nil
}

func (d *Dispatcher) runAfterSend(ctx context.Context, send store.QueueSend, messageID string) {
	command := d.afterCommand(send.Batch)
	if command == "" {
		return
	}
	payload := payloadFor(send)
	payload.MessageID = messageID
	payload.Status = "sent"
	code, reason, err := d.HookRunner(ctx, command, payload)
	if err != nil || code != 0 {
		if reason == "" && err != nil {
			reason = err.Error()
		}
		if reason == "" {
			reason = fmt.Sprintf("after-send hook exited %d", code)
		}
		warning := fmt.Sprintf("after-send hook for %s: %s", send.Item.Email, reason)
		d.warnings = append(d.warnings, warning)
		d.logf("%s", warning)
	}
}

func (d *Dispatcher) readyAt(send store.QueueSend) (time.Time, error) {
	scheduled, err := store.ParseStamp(send.Item.ScheduledAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("queue item %d has an invalid schedule: %w", send.Item.ID, err)
	}
	earliest := scheduled
	last, ok, err := d.DB.DispatchCursor(d.Account)
	if err != nil {
		return time.Time{}, err
	}
	if ok {
		notBefore := last.Add(gapBetween(send.Batch.Interval(), d.Interval))
		if notBefore.After(earliest) {
			earliest = notBefore
		}
	}
	return earliest, nil
}

func (d *Dispatcher) capReached() (bool, time.Time, error) {
	if d.DailyCap <= 0 {
		return false, time.Time{}, nil
	}
	start := dayStart(d.now(), d.Timezone)
	count, err := d.DB.SentCountSince(d.Account, start)
	if err != nil {
		return false, time.Time{}, err
	}
	if count >= d.DailyCap {
		return true, nextMidnight(d.now(), d.Timezone), nil
	}
	return false, time.Time{}, nil
}

func (d *Dispatcher) pick(pending []store.QueueSend) (store.QueueSend, string, bool) {
	holdPath := ""
	for _, send := range pending {
		path, held := d.itemHeld(send.Item, send.Batch)
		if held {
			if holdPath == "" {
				holdPath = path
			}
			continue
		}
		return send, "", false
	}
	if holdPath == "" {
		holdPath = "a hold file"
	}
	return store.QueueSend{}, holdPath, true
}

func (d *Dispatcher) itemHeld(item store.QueueItem, batch store.QueueBatch) (string, bool) {
	path, held, err := holdMatch(item, d.rulesFor(batch))
	if err != nil {
		d.logf("cannot read hold file %s: %v", path, err)
		if path == "" {
			path = "a hold file"
		}
		return path, true
	}
	return path, held
}

func (d *Dispatcher) rulesFor(batch store.QueueBatch) []store.HoldFile {
	rules := make([]store.HoldFile, 0, len(d.Holds)+len(batch.Holds))
	rules = append(rules, d.Holds...)
	rules = append(rules, batch.Holds...)
	return rules
}

func (d *Dispatcher) hookCommand(batch store.QueueBatch) string {
	if strings.TrimSpace(batch.HookCommand) != "" {
		return batch.HookCommand
	}
	return strings.TrimSpace(d.Hook)
}

func (d *Dispatcher) afterCommand(batch store.QueueBatch) string {
	if strings.TrimSpace(batch.AfterCommand) != "" {
		return batch.AfterCommand
	}
	return strings.TrimSpace(d.AfterSend)
}

func (d *Dispatcher) build(send store.QueueSend) (mailmsg.BuiltMessage, error) {
	msg := mailmsg.Message{
		To: []string{send.Item.Email}, Cc: send.Item.Cc, Bcc: send.Item.Bcc,
		Subject: send.Item.Subject, Text: send.Item.Body, HTML: send.Item.HTML,
		ReplyTo: send.Batch.ReplyTo, FromName: send.Batch.FromName, Account: d.Sender.FromAddress(),
	}
	if strings.TrimSpace(msg.ReplyTo) == "" {
		msg.ReplyTo = d.Sender.DefaultReplyTo()
	}
	if d.Sender.Provider() == auth.ProviderSMTP {
		msg.IncludeMessageID = true
		msg.OmitBccHeader = true
	}
	return msg.Build()
}

func (d *Dispatcher) noteSent(at time.Time) error {
	if at.IsZero() {
		at = d.now()
	}
	return d.DB.SetDispatchCursor(d.Account, at)
}

func (d *Dispatcher) wait(ctx context.Context, until time.Time, reason string) (Result, bool, error) {
	if d.Once {
		d.detail = reason
		result, err := d.finish("idle")
		return result, true, err
	}
	now := d.now()
	if !until.After(now) {
		until = now.Add(d.Poll)
	}
	slice := until.Sub(now)
	if slice > d.Poll {
		slice = d.Poll
	}
	if slice <= 0 {
		slice = d.Poll
	}
	if reason != d.announced {
		d.logf("%s", reason)
		d.announced = reason
	}
	if err := d.Clock.Sleep(ctx, slice); err != nil {
		result, finErr := d.stopped(err)
		if finErr != nil {
			return Result{}, true, finErr
		}
		return result, true, err
	}
	return Result{}, false, nil
}

func (d *Dispatcher) stopped(cause error) (Result, error) {
	result, err := d.finish("stopped")
	if err != nil {
		return Result{}, err
	}
	return result, cause
}

func (d *Dispatcher) finish(status string) (Result, error) {
	if err := d.DB.RefreshQueueBatches(d.Account, d.now()); err != nil {
		return Result{}, err
	}
	parked, err := d.DB.ParkedQueueItems(d.Account)
	if err != nil {
		return Result{}, err
	}
	notices := make([]ParkNotice, 0, len(parked))
	for _, item := range parked {
		notices = append(notices, ParkNotice{ID: item.ID, BatchID: item.BatchID, Email: item.Email, Subject: item.Subject, Reason: item.Error})
	}
	if status == "ok" || status == "completed" {
		pending, err := d.DB.PendingQueueSends(d.Account)
		if err != nil {
			return Result{}, err
		}
		if len(pending) == 0 {
			status = "completed"
			if len(notices) > 0 {
				status = "needs_attention"
			}
		}
	}
	if len(notices) > 0 && status != "idle" {
		d.logf("%d queued item(s) are parked for review; use mailsend queue status and mailsend queue resolve", len(notices))
	}
	return Result{
		Status: status, Account: d.Account, SentThisRun: d.sentThisRun, BlockedThisRun: d.blockedThisRun,
		FailedThisRun: d.failedThisRun, Parked: notices, Warnings: d.warnings, Detail: d.detail,
	}, nil
}

func (d *Dispatcher) now() time.Time {
	if d.Clock == nil {
		return time.Now()
	}
	return d.Clock.Now()
}

func (d *Dispatcher) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

func payloadFor(send store.QueueSend) HookPayload {
	return HookPayload{
		Account: send.Batch.Account, BatchID: send.Batch.ID, Label: send.Batch.Label,
		Email: send.Item.Email, Subject: send.Item.Subject, Step: send.Item.Step, Tag: send.Item.Tag,
		ScheduledAt: send.Item.ScheduledAt, Position: send.Item.Position,
	}
}

func gapBetween(batchInterval, floor time.Duration) time.Duration {
	if batchInterval < 0 {
		batchInterval = 0
	}
	if floor < 0 {
		floor = 0
	}
	if floor > batchInterval {
		return floor
	}
	return batchInterval
}

func holdMatch(item store.QueueItem, rules []store.HoldFile) (string, bool, error) {
	for _, rule := range rules {
		path := strings.TrimSpace(rule.Path)
		if path == "" {
			continue
		}
		present, err := holdPresent(path)
		if err != nil {
			return path, true, err
		}
		if !present {
			continue
		}
		if len(rule.Steps) == 0 {
			return path, true, nil
		}
		step := strings.TrimSpace(item.Step)
		if step == "" {
			continue
		}
		for _, candidate := range rule.Steps {
			if strings.EqualFold(strings.TrimSpace(candidate), step) {
				return path, true, nil
			}
		}
	}
	return "", false, nil
}

func holdPresent(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func dayStart(now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}

func nextMidnight(now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
}

func acquireDispatchLock(path string) (*flock.Flock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(path), 0700)
	lock := flock.New(path)
	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("cannot lock dispatcher: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("%w", ErrDispatcherBusy)
	}
	return lock, nil
}
