package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example-account/mailsend/internal/store"
	"github.com/gofrs/flock"
)

func TestItemLockHeldAroundSendAndFreeWhileIdle(t *testing.T) {
	if os.Getenv("MAILSEND_ITEM_LOCK_CHILD") == "probe" {
		probeItemLock()
	}
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	seedBatch(t, db, "batch-a", start, []queued{
		{email: "one@example.com", at: start},
		{email: "two@example.com", at: start},
	})
	lockPath := filepath.Join(t.TempDir(), "send.lock")
	clock := &fakeClock{now: start}
	sender := &fakeSender{clock: clock}
	var sawPre, sawAfter, sawIdle bool
	clock.onSleep = func(int, time.Duration) {
		sawIdle = true
		if got := runItemLockProbe(t, lockPath); got != "free" {
			t.Fatalf("lock was %s while idle", got)
		}
	}
	dispatcher := testDispatcher(db, clock, sender, nil, 5*time.Minute, 0, time.UTC)
	dispatcher.Poll = 5 * time.Minute
	dispatcher.ItemLockPath = lockPath
	dispatcher.Hook = "pre"
	dispatcher.AfterSend = "after"
	dispatcher.HookRunner = func(_ context.Context, command string, payload HookPayload) (int, string, error) {
		if got := runItemLockProbe(t, lockPath); got != "held" {
			t.Fatalf("lock was %s during %s", got, command)
		}
		if payload.MessageID != "" {
			sawAfter = true
		} else {
			sawPre = true
		}
		return 0, "", nil
	}
	guardSleeps(t, clock, 10)
	result, err := dispatcher.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.SentThisRun != 2 || len(sender.calls()) != 2 {
		t.Fatalf("result %#v calls %v", result, sender.calls())
	}
	if !sawPre || !sawAfter || !sawIdle {
		t.Fatalf("pre %v after %v idle %v", sawPre, sawAfter, sawIdle)
	}
	if got := runItemLockProbe(t, lockPath); got != "free" {
		t.Fatalf("lock still %s after dispatch", got)
	}
}

func TestItemLockWaitsForAnotherProcess(t *testing.T) {
	if os.Getenv("MAILSEND_ITEM_LOCK_CHILD") == "hold" {
		holdItemLock()
	}
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	seedBatch(t, db, "batch-a", start, []queued{{email: "one@example.com", at: start}})
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "send.lock")
	ready := filepath.Join(dir, "ready")
	release := filepath.Join(dir, "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestItemLockWaitsForAnotherProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"MAILSEND_ITEM_LOCK_CHILD=hold",
		"MAILSEND_ITEM_LOCK="+lockPath,
		"MAILSEND_LOCK_READY="+ready,
		"MAILSEND_LOCK_RELEASE="+release,
	)
	output := &strings.Builder{}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte("1"), 0600)
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("holder did not lock: %v\n%s", err, output.String())
	}
	clock := &fakeClock{now: start}
	sender := &fakeSender{clock: clock}
	var hooked atomic.Bool
	dispatcher := testDispatcher(db, clock, sender, nil, 0, 0, time.UTC)
	dispatcher.Once = true
	dispatcher.ItemLockPath = lockPath
	dispatcher.Hook = "pre"
	dispatcher.HookRunner = func(context.Context, string, HookPayload) (int, string, error) {
		hooked.Store(true)
		return 0, "", nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := dispatcher.Run(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("dispatch finished while the lock was held: %v calls %v\n%s", err, sender.calls(), output.String())
	case <-time.After(250 * time.Millisecond):
	}
	if hooked.Load() || len(sender.calls()) != 0 {
		t.Fatalf("hooked %v calls %v while another process held the lock", hooked.Load(), sender.calls())
	}
	if err := os.WriteFile(release, []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not continue after the lock was released")
	}
	if !hooked.Load() || len(sender.calls()) != 1 {
		t.Fatalf("hooked %v calls %v", hooked.Load(), sender.calls())
	}
}

func TestItemLockStopsWhenContextEnds(t *testing.T) {
	if os.Getenv("MAILSEND_ITEM_LOCK_CHILD") == "hold" {
		holdItemLock()
	}
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	seedBatch(t, db, "batch-a", start, []queued{{email: "one@example.com", at: start}})
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "send.lock")
	ready := filepath.Join(dir, "ready")
	release := filepath.Join(dir, "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestItemLockStopsWhenContextEnds$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"MAILSEND_ITEM_LOCK_CHILD=hold",
		"MAILSEND_ITEM_LOCK="+lockPath,
		"MAILSEND_LOCK_READY="+ready,
		"MAILSEND_LOCK_RELEASE="+release,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte("1"), 0600)
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: start}
	sender := &fakeSender{clock: clock}
	dispatcher := testDispatcher(db, clock, sender, nil, 0, 0, time.UTC)
	dispatcher.ItemLockPath = lockPath
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := dispatcher.Run(ctx)
		done <- err
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dispatch did not stop when the context ended")
	}
	if len(sender.calls()) != 0 || itemStatus(t, db, "one@example.com") != "pending" {
		t.Fatalf("calls %v status %s", sender.calls(), itemStatus(t, db, "one@example.com"))
	}
}

func TestGapFromHistoryUsesTheNewestSentRow(t *testing.T) {
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	insertHistory(t, db, "older", "sent", "old@example.com", start.Add(-10*time.Minute))
	insertHistory(t, db, "newer", "sent", "recent@example.com", start.Add(-time.Minute))
	insertHistory(t, db, "failed-row", "failed", "nope@example.com", start.Add(time.Hour))
	if err := db.SetDispatchCursor("acme", start.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	seedBatch(t, db, "batch-a", start, []queued{{email: "next@example.com", at: start}})
	clock := &fakeClock{now: start}
	sender := &fakeSender{clock: clock}
	dispatcher := testDispatcher(db, clock, sender, nil, 5*time.Minute, 0, time.UTC)
	dispatcher.Once = true
	dispatcher.GapFromHistory = true
	result, err := dispatcher.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := start.Add(4 * time.Minute).Format(time.RFC3339)
	if result.Status != "idle" || len(sender.calls()) != 0 || !strings.Contains(result.Detail, want) {
		t.Fatalf("history anchor result %#v", result)
	}
	if err = db.SetDispatchCursor("acme", start); err != nil {
		t.Fatal(err)
	}
	result, err = dispatcher.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want = start.Add(5 * time.Minute).Format(time.RFC3339)
	if result.Status != "idle" || len(sender.calls()) != 0 || !strings.Contains(result.Detail, want) {
		t.Fatalf("cursor anchor result %#v", result)
	}
	clock.now = start.Add(5 * time.Minute)
	result, err = dispatcher.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SentThisRun != 1 || len(sender.calls()) != 1 || !sender.times()[0].Equal(start.Add(5*time.Minute)) {
		t.Fatalf("result %#v times %v", result, sender.times())
	}
}

func TestGapWithoutHistoryFlagIgnoresOrdinarySends(t *testing.T) {
	db := openDispatchDB(t)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	insertHistory(t, db, "recent", "sent", "recent@example.com", start.Add(-time.Minute))
	seedBatch(t, db, "batch-a", start, []queued{{email: "next@example.com", at: start}})
	clock := &fakeClock{now: start}
	sender := &fakeSender{clock: clock}
	dispatcher := testDispatcher(db, clock, sender, nil, 5*time.Minute, 0, time.UTC)
	dispatcher.Once = true
	result, err := dispatcher.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SentThisRun != 1 || len(sender.calls()) != 1 || !sender.times()[0].Equal(start) {
		t.Fatalf("result %#v times %v", result, sender.times())
	}
}

func insertHistory(t *testing.T, db *store.DB, id, status, email string, at time.Time) {
	t.Helper()
	if err := db.CreateJob(store.Job{
		ID: id, Kind: "send", Status: "completed", Account: "acme", Subject: "Earlier",
	}, "", []store.Recipient{{
		Email: email, Subject: "Earlier", Body: "Hi", Status: status,
	}}); err != nil {
		t.Fatal(err)
	}
	stamp := store.FormatStamp(at)
	if _, err := db.Exec(`UPDATE recipients SET updated_at=?, created_at=?, message_id=? WHERE job_id=?`, stamp, stamp, "hist-"+id, id); err != nil {
		t.Fatal(err)
	}
}

func probeItemLock() {
	lock := flock.New(os.Getenv("MAILSEND_ITEM_LOCK"))
	ok, err := lock.TryLock()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if ok {
		_ = lock.Unlock()
		fmt.Println("free")
	} else {
		fmt.Println("held")
	}
	os.Exit(0)
}

func holdItemLock() {
	lock := flock.New(os.Getenv("MAILSEND_ITEM_LOCK"))
	ok, err := lock.TryLock()
	if err != nil || !ok {
		fmt.Fprintf(os.Stderr, "hold lock: ok=%v err=%v\n", ok, err)
		os.Exit(2)
	}
	if err = os.WriteFile(os.Getenv("MAILSEND_LOCK_READY"), []byte("1"), 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	release := os.Getenv("MAILSEND_LOCK_RELEASE")
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(release); err == nil {
			_ = lock.Unlock()
			os.Exit(0)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = lock.Unlock()
	fmt.Fprintln(os.Stderr, "holder timed out")
	os.Exit(2)
}

func runItemLockProbe(t *testing.T, path string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestItemLockHeldAroundSendAndFreeWhileIdle$", "-test.count=1")
	cmd.Env = append(os.Environ(), "MAILSEND_ITEM_LOCK_CHILD=probe", "MAILSEND_ITEM_LOCK="+path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe: %v %s", err, out)
	}
	return strings.TrimSpace(string(out))
}
