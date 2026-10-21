package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IMisbahk/mailsend/internal/auth"
	mailmsg "github.com/IMisbahk/mailsend/internal/mail"
	"github.com/IMisbahk/mailsend/internal/store"
)

func TestSpacingAcrossTwoBatches(t *testing.T) {
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	seedBatch(t, db, "batch-a", start, []queued{{email: "a1@example.com", at: start}, {email: "a2@example.com", at: start.Add(5 * time.Minute)}})
	seedBatch(t, db, "batch-b", start, []queued{{email: "b1@example.com", at: start}, {email: "b2@example.com", at: start.Add(5 * time.Minute)}})
	clock := &fakeClock{now: start}
	sender := &fakeSender{clock: clock}
	result, err := runDispatch(t, db, clock, sender, nil, 5*time.Minute, 0, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.SentThisRun != 4 {
		t.Fatalf("result %#v", result)
	}
	want := []string{"a1@example.com", "b1@example.com", "a2@example.com", "b2@example.com"}
	if strings.Join(sender.calls(), ",") != strings.Join(want, ",") {
		t.Fatalf("order %v", sender.calls())
	}
	times := sender.times()
	wantTimes := []time.Time{start, start.Add(5 * time.Minute), start.Add(10 * time.Minute), start.Add(15 * time.Minute)}
	if len(times) != len(wantTimes) {
		t.Fatalf("times %v", times)
	}
	for i := range wantTimes {
		if !times[i].Equal(wantTimes[i]) {
			t.Fatalf("send %d at %s, want %s", i, times[i], wantTimes[i])
		}
	}
}

func TestPastDueCatchUpDoesNotBurst(t *testing.T) {
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	now := start.Add(7 * time.Minute)
	seedBatch(t, db, "batch-a", start, []queued{
		{email: "one@example.com", at: start},
		{email: "two@example.com", at: start.Add(5 * time.Minute)},
		{email: "three@example.com", at: start.Add(10 * time.Minute)},
	})
	clock := &fakeClock{now: now}
	sender := &fakeSender{clock: clock}
	guardSleeps(t, clock, 80)
	if _, err := runDispatch(t, db, clock, sender, nil, 5*time.Minute, 0, time.UTC); err != nil {
		t.Fatal(err)
	}
	times := sender.times()
	want := []time.Time{now, now.Add(5 * time.Minute), now.Add(10 * time.Minute)}
	if len(times) != len(want) {
		t.Fatalf("times %v", times)
	}
	for i := range want {
		if !times[i].Equal(want[i]) {
			t.Fatalf("send %d at %s, want %s", i, times[i], want[i])
		}
		if i > 0 && times[i].Sub(times[i-1]) < 5*time.Minute {
			t.Fatalf("burst %s then %s", times[i-1], times[i])
		}
	}
}

func TestCrashBetweenSendingAndSentResumesWithoutDuplicate(t *testing.T) {
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	seedBatch(t, db, "batch-a", start, []queued{{email: "a@example.com", at: start, subject: "Hello"}})
	items, err := db.ListQueueItems("batch-a")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimQueueItem(items[0].ID, start)
	if err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{fn: func(string) (string, bool, error) {
		t.Errorf("resumed dispatcher sent the claimed email again")
		return "again", false, nil
	}}
	probe := staticProbe{evidence: SentEvidence{Found: true, Verified: true, MessageID: "gmail-123"}}
	clock := &fakeClock{now: start.Add(time.Minute)}
	result, err := runDispatch(t, db, clock, sender, probe, 5*time.Minute, 0, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if result.SentThisRun != 0 || len(sender.calls()) != 0 {
		t.Fatalf("result %#v calls %v", result, sender.calls())
	}
	item, err := db.QueueItem(claimed.ID)
	if err != nil || item.Status != "sent" || item.MessageID != "gmail-123" {
		t.Fatalf("item %#v %v", item, err)
	}
	rows, err := db.Recipients(claimed.HistoryJobID)
	if err != nil || len(rows) != 1 || rows[0].Status != "sent" || rows[0].MessageID != "gmail-123" {
		t.Fatalf("history %#v %v", rows, err)
	}
}

func TestUnverifiedSendingIsParkedAndNotRetried(t *testing.T) {
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	seedBatch(t, db, "batch-a", start, []queued{{email: "a@example.com", at: start}})
	items, err := db.ListQueueItems("batch-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ClaimQueueItem(items[0].ID, start); err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{fn: func(string) (string, bool, error) {
		t.Errorf("unverified email was sent")
		return "nope", false, nil
	}}
	probe := staticProbe{evidence: SentEvidence{Verified: false, Detail: "no fresh sent mail"}}
	clock := &fakeClock{now: start.Add(time.Minute)}
	result, err := runDispatch(t, db, clock, sender, probe, 5*time.Minute, 0, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "needs_attention" || len(result.Parked) != 1 || len(sender.calls()) != 0 {
		t.Fatalf("result %#v calls %v", result, sender.calls())
	}
	again, err := runDispatch(t, db, clock, sender, probe, 5*time.Minute, 0, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(sender.calls()) != 0 || again.Status != "needs_attention" {
		t.Fatalf("second run sent or cleared the park: %#v calls %v", again, sender.calls())
	}
}

func TestConfirmedMissRetriesOnce(t *testing.T) {
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	seedBatch(t, db, "batch-a", start, []queued{{email: "a@example.com", at: start}})
	items, err := db.ListQueueItems("batch-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ClaimQueueItem(items[0].ID, start.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{}
	probe := staticProbe{evidence: SentEvidence{Verified: true, Found: false}}
	clock := &fakeClock{now: start}
	if _, err = runDispatch(t, db, clock, sender, probe, 0, 0, time.UTC); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls()) != 1 || sender.calls()[0] != "a@example.com" {
		t.Fatalf("calls %v", sender.calls())
	}
	item, err := db.QueueItem(items[0].ID)
	if err != nil || item.Status != "sent" {
		t.Fatalf("item %#v %v", item, err)
	}
}

func TestHoldFileWaitsAndStepHoldSkipsFollowups(t *testing.T) {
	db := openDispatchDB(t)
	dir := t.TempDir()
	sendHold := filepath.Join(dir, "SEND_HOLD")
	followHold := filepath.Join(dir, "FOLLOWUP_HOLD")
	if err := os.WriteFile(sendHold, []byte("pause"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(followHold, []byte("pause"), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	seedBatch(t, db, "batch-a", start, []queued{{email: "first@example.com", at: start, step: "first"}, {email: "fu@example.com", at: start, step: "fu-d3"}})
	clock := &fakeClock{now: start}
	sender := &fakeSender{clock: clock}
	ctx, cancel := context.WithCancel(context.Background())
	clock.onSleep = func(n int, _ time.Duration) {
		if n == 1 {
			cancel()
		}
	}
	dispatcher := testDispatcher(db, clock, sender, nil, 0, 0, time.UTC)
	dispatcher.Holds = []store.HoldFile{{Path: sendHold}, {Path: followHold, Steps: []string{"fu-d3", "fu-d7", "ooo"}}}
	_, err := dispatcher.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancel while held, %v", err)
	}
	if len(sender.calls()) != 0 {
		t.Fatalf("sent while held: %v", sender.calls())
	}
	for _, email := range []string{"first@example.com", "fu@example.com"} {
		if status := itemStatus(t, db, email); status != "pending" {
			t.Fatalf("%s status %s", email, status)
		}
	}
	if err = os.Remove(sendHold); err != nil {
		t.Fatal(err)
	}
	clock = &fakeClock{now: start}
	sender = &fakeSender{clock: clock}
	dispatcher = testDispatcher(db, clock, sender, nil, 0, 0, time.UTC)
	dispatcher.Once = true
	dispatcher.Holds = []store.HoldFile{{Path: sendHold}, {Path: followHold, Steps: []string{"fu-d3", "fu-d7", "ooo"}}}
	result, err := dispatcher.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SentThisRun != 1 || strings.Join(sender.calls(), ",") != "first@example.com" {
		t.Fatalf("result %#v calls %v", result, sender.calls())
	}
	if itemStatus(t, db, "fu@example.com") != "pending" {
		t.Fatal("follow-up was not left pending behind FOLLOWUP_HOLD")
	}
	if err = os.Remove(followHold); err != nil {
		t.Fatal(err)
	}
	sender = &fakeSender{clock: clock}
	dispatcher = testDispatcher(db, clock, sender, nil, 0, 0, time.UTC)
	guardSleeps(t, clock, 40)
	if _, err = dispatcher.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sender.calls(), ",") != "fu@example.com" {
		t.Fatalf("calls %v", sender.calls())
	}
	if len(sender.times()) != 1 || !sender.times()[0].Equal(start.Add(5*time.Minute)) {
		t.Fatalf("follow-up send times %v", sender.times())
	}
}

func TestDailyCapRollsOverAtISTMidnight(t *testing.T) {
	db := openDispatchDB(t)
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 21, 22, 0, 0, 0, loc)
	if err = db.CreateJob(store.Job{ID: "outside", Kind: "send", Status: "sent", Account: "sunbeam", Subject: "earlier"}, "", []store.Recipient{{
		Email: "other@example.com", ToJSON: `["other@example.com"]`, Subject: "earlier", Body: "already sent", Status: "pending",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE recipients SET status='sent', message_id='outside-1', updated_at=? WHERE job_id=?`, store.FormatStamp(day), "outside"); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 21, 23, 0, 0, 0, loc)
	seedBatch(t, db, "batch-a", start, []queued{{email: "next@example.com", at: start}})
	clock := &fakeClock{now: time.Date(2026, 10, 21, 23, 30, 0, 0, loc)}
	guardSleeps(t, clock, 40)
	counted, err := db.SentCountSince("sunbeam", dayStart(clock.Now(), loc))
	if err != nil || counted != 1 {
		t.Fatalf("seeded send count %d %v", counted, err)
	}
	sender := &fakeSender{clock: clock}
	dispatcher := testDispatcher(db, clock, sender, nil, 0, 1, loc)
	dispatcher.Poll = time.Minute
	result, err := dispatcher.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SentThisRun != 1 || len(sender.times()) != 1 {
		t.Fatalf("result %#v times %v", result, sender.times())
	}
	midnight := time.Date(2026, 10, 22, 0, 0, 0, 0, loc)
	if sender.times()[0].Before(midnight) {
		t.Fatalf("sent at %s, before IST midnight %s", sender.times()[0], midnight)
	}
	if !sender.times()[0].Equal(midnight) {
		t.Fatalf("sent at %s, want %s", sender.times()[0], midnight)
	}
}

func TestPreSendHookBlocksAndBatchContinues(t *testing.T) {
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	seedBatch(t, db, "batch-a", start, []queued{{email: "block@example.com", at: start, step: "first"}, {email: "keep@example.com", at: start, step: "first"}})
	clock := &fakeClock{now: start}
	sender := &fakeSender{clock: clock}
	dispatcher := testDispatcher(db, clock, sender, nil, 5*time.Minute, 0, time.UTC)
	dispatcher.Hook = "precheck"
	dispatcher.HookRunner = func(_ context.Context, command string, payload HookPayload) (int, string, error) {
		if command != "precheck" {
			t.Errorf("command %s", command)
		}
		if payload.Step != "first" {
			t.Errorf("step %s", payload.Step)
		}
		if payload.Email == "block@example.com" {
			return 1, "already emailed", nil
		}
		return 0, "", nil
	}
	result, err := dispatcher.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.BlockedThisRun != 1 || result.SentThisRun != 1 {
		t.Fatalf("result %#v", result)
	}
	if strings.Join(sender.calls(), ",") != "keep@example.com" {
		t.Fatalf("calls %v", sender.calls())
	}
	if status := itemStatus(t, db, "block@example.com"); status != "blocked" {
		t.Fatalf("blocked status %s", status)
	}
	item := itemByEmail(t, db, "block@example.com")
	if !strings.Contains(item.Error, "already emailed") {
		t.Fatalf("reason %q", item.Error)
	}
	if len(sender.times()) != 1 || !sender.times()[0].Equal(start) {
		t.Fatalf("blocked email consumed the gap: %v", sender.times())
	}
}

func TestHookCommandProtocol(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	body := "#!/bin/sh\ncat > \"$HOOK_CAPTURE\"\nif grep -q block@example.com \"$HOOK_CAPTURE\"; then echo 'already emailed' >&2; exit 1; fi\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(dir, "capture.json")
	t.Setenv("HOOK_CAPTURE", capture)
	code, reason, err := RunCommandHook(context.Background(), script, HookPayload{Email: "block@example.com", Step: "first", Subject: "Hi"})
	if err != nil || code != 1 || !strings.Contains(reason, "already emailed") {
		t.Fatalf("code %d reason %q err %v", code, reason, err)
	}
	code, _, err = RunCommandHook(context.Background(), script, HookPayload{Email: "ok@example.com", Step: "fu-d3"})
	if err != nil || code != 0 {
		t.Fatalf("allow code %d err %v", code, err)
	}
}

func TestTwoDispatchersDoNotBothSend(t *testing.T) {
	if os.Getenv("MAILSEND_DISPATCH_CHILD") == "1" {
		err := runDispatchChild()
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(2)
		}
		os.Exit(0)
	}
	dbPath := filepath.Join(t.TempDir(), "state.sqlite3")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Minute).UTC()
	seedBatch(t, db, "batch-a", start, []queued{{email: "one@example.com", at: start}, {email: "two@example.com", at: start}})
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "sent.log")
	lockPath := filepath.Join(t.TempDir(), "locks", "dispatch-sunbeam.lock")
	startChild := func() (*exec.Cmd, *strings.Builder) {
		output := &strings.Builder{}
		cmd := exec.Command(os.Args[0], "-test.run=^TestTwoDispatchersDoNotBothSend$", "-test.count=1")
		cmd.Env = append(os.Environ(),
			"MAILSEND_DISPATCH_CHILD=1",
			"MAILSEND_TEST_DB="+dbPath,
			"MAILSEND_SEND_LOG="+logPath,
			"MAILSEND_LOCK_PATH="+lockPath,
		)
		cmd.Stdout = output
		cmd.Stderr = output
		return cmd, output
	}
	first, firstOut := startChild()
	second, secondOut := startChild()
	if err = first.Start(); err != nil {
		t.Fatal(err)
	}
	if err = second.Start(); err != nil {
		t.Fatal(err)
	}
	wait := func(cmd *exec.Cmd) int {
		t.Helper()
		err := cmd.Wait()
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		t.Fatal(err)
		return 1
	}
	codes := []int{wait(first), wait(second)}
	success, busy := 0, 0
	for _, code := range codes {
		switch code {
		case 0:
			success++
		case 2:
			busy++
		default:
			t.Fatalf("exit %d\n%s\n%s", code, firstOut.String(), secondOut.String())
		}
	}
	if success != 1 || busy != 1 {
		t.Fatalf("exits %v\n%s\n%s", codes, firstOut.String(), secondOut.String())
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.FieldsFunc(strings.TrimSpace(string(logged)), func(r rune) bool { return r == '\n' })
	if len(lines) != 1 {
		t.Fatalf("sent log:\n%s\n%s\n%s", logged, firstOut.String(), secondOut.String())
	}
	combined := firstOut.String() + secondOut.String()
	if !strings.Contains(combined, "already running") {
		t.Fatalf("busy process did not report the lock:\n%s", combined)
	}
}

type queued struct {
	email, subject, step string
	at                   time.Time
}

func seedBatch(t *testing.T, db *store.DB, id string, start time.Time, emails []queued) {
	t.Helper()
	items := make([]store.QueueItem, 0, len(emails))
	for i, email := range emails {
		subject := email.subject
		if subject == "" {
			subject = "Hello " + email.email
		}
		at := email.at
		if at.IsZero() {
			at = start.Add(time.Duration(i) * 5 * time.Minute)
		}
		items = append(items, store.QueueItem{
			Position: i, Email: email.email, Subject: subject, Body: "Hello", Step: email.step, ScheduledAt: store.FormatStamp(at),
		})
	}
	if _, err := db.InsertQueueBatch(store.QueueBatch{
		ID: id, Account: "sunbeam", Label: id, Status: "active", IntervalSeconds: 300, StartAt: store.FormatStamp(start),
	}, items); err != nil {
		t.Fatal(err)
	}
}

func openDispatchDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func runDispatch(t *testing.T, db *store.DB, clock *fakeClock, sender *fakeSender, probe SentProbe, interval time.Duration, cap int, loc *time.Location) (Result, error) {
	t.Helper()
	guardSleeps(t, clock, 80)
	return testDispatcher(db, clock, sender, probe, interval, cap, loc).Run(context.Background())
}

func testDispatcher(db *store.DB, clock *fakeClock, sender *fakeSender, probe SentProbe, interval time.Duration, cap int, loc *time.Location) *Dispatcher {
	return &Dispatcher{
		DB: db, Account: "sunbeam", Interval: interval, DailyCap: cap, Timezone: loc,
		Clock: clock, Sender: sender, Probe: probe, Poll: 15 * time.Second,
	}
}

func guardSleeps(t *testing.T, clock *fakeClock, limit int) {
	t.Helper()
	previous := clock.onSleep
	clock.onSleep = func(n int, delay time.Duration) {
		if n > limit {
			t.Fatalf("dispatcher waited %d times; clock %s", n, clock.Now())
		}
		if previous != nil {
			previous(n, delay)
		}
	}
}

func itemStatus(t *testing.T, db *store.DB, email string) string {
	t.Helper()
	return itemByEmail(t, db, email).Status
}

func itemByEmail(t *testing.T, db *store.DB, email string) store.QueueItem {
	t.Helper()
	items, err := db.ListQueueItems("batch-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Email == email {
			return item
		}
	}
	t.Fatalf("missing %s", email)
	return store.QueueItem{}
}

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	sleeps  int
	onSleep func(n int, delay time.Duration)
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Sleep(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.sleeps++
	n := f.sleeps
	if delay > 0 {
		f.now = f.now.Add(delay)
	}
	hook := f.onSleep
	f.mu.Unlock()
	if hook != nil {
		hook(n, delay)
	}
	return ctx.Err()
}

type fakeSender struct {
	mu    sync.Mutex
	sent  []string
	at    []time.Time
	clock *fakeClock
	fn    func(email string) (string, bool, error)
}

func (f *fakeSender) Send(ctx context.Context, built mailmsg.BuiltMessage) (string, bool, error) {
	email := ""
	if len(built.Recipients) > 0 {
		email = built.Recipients[0]
	}
	f.mu.Lock()
	f.sent = append(f.sent, email)
	if f.clock != nil {
		f.at = append(f.at, f.clock.Now())
	}
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(email)
	}
	return "msg-" + email, false, nil
}

func (f *fakeSender) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	copy(out, f.sent)
	return out
}

func (f *fakeSender) times() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Time, len(f.at))
	copy(out, f.at)
	return out
}

func (f *fakeSender) FromAddress() string    { return "misbah@example.com" }
func (f *fakeSender) DefaultReplyTo() string { return "" }
func (f *fakeSender) Provider() string       { return "smtp" }

type staticProbe struct{ evidence SentEvidence }

func (s staticProbe) ProbeSent(context.Context, SentQuery) (SentEvidence, error) {
	return s.evidence, nil
}

type fileSender struct{ path string }

func (f fileSender) Send(ctx context.Context, built mailmsg.BuiltMessage) (string, bool, error) {
	email := ""
	if len(built.Recipients) > 0 {
		email = built.Recipients[0]
	}
	handle, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return "", false, err
	}
	_, err = fmt.Fprintf(handle, "%d %s\n", os.Getpid(), email)
	_ = handle.Close()
	if err != nil {
		return "", false, err
	}
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return "", true, ctx.Err()
	}
	return "msg-" + email, false, nil
}

func (fileSender) FromAddress() string    { return "misbah@example.com" }
func (fileSender) DefaultReplyTo() string { return "" }
func (fileSender) Provider() string       { return "smtp" }

func TestRequireSentCheckBlocksARecentRecipient(t *testing.T) {
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	seedBatch(t, db, "batch-a", start, []queued{{email: "ada@example.com", at: start}, {email: "new@example.com", at: start}})
	clock := &fakeClock{now: start}
	sender := &fakeSender{clock: clock}
	dispatcher := testDispatcher(db, clock, sender, nil, 0, 0, time.UTC)
	dispatcher.Once = false
	dispatcher.RequireSentCheck = true
	dispatcher.SentWithin = 7 * 24 * time.Hour
	dispatcher.HookRunner = func(_ context.Context, _ string, payload HookPayload) (int, string, error) {
		if payload.Email == "ada@example.com" {
			t.Fatal("hook ran for a recipient already in Sent")
		}
		return 0, "", nil
	}
	dispatcher.Hook = "precheck"
	dispatcher.SentLister = scriptedSent{hits: map[string][]SentHit{
		"ada@example.com": {{ID: "gmail-9", To: "ada@example.com", Subject: "Hello ada@example.com", Date: start.Add(-time.Hour)}},
	}}
	guardSleeps(t, clock, 40)
	result, err := dispatcher.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.BlockedThisRun != 1 || result.SentThisRun != 1 || len(sender.calls()) != 1 || sender.calls()[0] != "new@example.com" {
		t.Fatalf("result %#v calls %v", result, sender.calls())
	}
	blocked := itemByEmail(t, db, "ada@example.com")
	if blocked.Status != "blocked" || !strings.Contains(blocked.Error, "gmail-9") {
		t.Fatalf("blocked %#v", blocked)
	}
}

func TestRequireSentCheckFailsClosedWithoutSending(t *testing.T) {
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	seedBatch(t, db, "batch-a", start, []queued{{email: "ada@example.com", at: start}})
	clock := &fakeClock{now: start}
	sender := &fakeSender{clock: clock}
	dispatcher := testDispatcher(db, clock, sender, nil, 0, 0, time.UTC)
	dispatcher.Once = true
	dispatcher.RequireSentCheck = true
	dispatcher.SentLister = scriptedSent{err: auth.ErrScopeMissing}
	_, err := dispatcher.Run(context.Background())
	if !errors.Is(err, auth.ErrScopeMissing) {
		t.Fatalf("error %v", err)
	}
	if len(sender.calls()) != 0 || itemStatus(t, db, "ada@example.com") != "pending" {
		t.Fatalf("sent %v status %s", sender.calls(), itemStatus(t, db, "ada@example.com"))
	}

	dispatcher.SentLister = nil
	_, err = dispatcher.Run(context.Background())
	if err == nil || !errors.Is(err, auth.ErrScopeMissing) {
		t.Fatalf("missing lister %v", err)
	}
	if len(sender.calls()) != 0 {
		t.Fatalf("sent without a checker %v", sender.calls())
	}
}

func TestDispatchWithoutSentGateStillSends(t *testing.T) {
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	seedBatch(t, db, "batch-a", start, []queued{{email: "ada@example.com", at: start}})
	clock := &fakeClock{now: start}
	sender := &fakeSender{clock: clock}
	dispatcher := testDispatcher(db, clock, sender, nil, 0, 0, time.UTC)
	dispatcher.Once = true
	dispatcher.SentLister = scriptedSent{err: auth.ErrScopeMissing, fail: t}
	result, err := dispatcher.Run(context.Background())
	if err != nil || result.SentThisRun != 1 || len(sender.calls()) != 1 {
		t.Fatalf("result %#v err %v calls %v", result, err, sender.calls())
	}
}

type scriptedSent struct {
	hits map[string][]SentHit
	err  error
	fail interface{ Fatalf(string, ...any) }
}

func (s scriptedSent) ListSent(_ context.Context, email string, _ time.Time) ([]SentHit, error) {
	if s.fail != nil {
		s.fail.Fatalf("sent check ran for %s while the gate was off", email)
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.hits[email], nil
}

func runDispatchChild() error {
	db, err := store.Open(os.Getenv("MAILSEND_TEST_DB"))
	if err != nil {
		return err
	}
	defer db.Close()
	dispatcher := &Dispatcher{
		DB: db, Account: "sunbeam", Interval: 0, Timezone: time.UTC, Once: true,
		Sender: fileSender{path: os.Getenv("MAILSEND_SEND_LOG")}, LockPath: os.Getenv("MAILSEND_LOCK_PATH"),
		Probe: staticProbe{evidence: SentEvidence{Verified: true, Found: false}},
	}
	_, err = dispatcher.Run(context.Background())
	return err
}
