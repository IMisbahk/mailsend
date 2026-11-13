package cli

import (
	"github.com/example-account/mailsend/internal/store"
	"testing"
)

func TestRandomDelayWithinConfiguredBounds(t *testing.T) {
	for i := 0; i < 100; i++ {
		n, err := randomDelay(20, 60)
		if err != nil {
			t.Fatal(err)
		}
		if n < 20 || n > 60 {
			t.Fatalf("delay %d outside range", n)
		}
	}
	n, err := randomDelay(5, 5)
	if err != nil || n != 5 {
		t.Fatalf("fixed delay %d, %v", n, err)
	}
}

func TestJobLockRejectsPathTraversal(t *testing.T) {
	for _, id := range []string{"../outside", "/tmp/job", "job.name", ""} {
		if lock, err := acquireJobLock(id); err == nil {
			if lock != nil {
				_ = lock.Unlock()
			}
			t.Fatalf("accepted invalid job lock ID %q", id)
		}
	}
}

func TestDailyLimitUsesPersistedSuccessfulSends(t *testing.T) {
	path := t.TempDir() + "/state.sqlite3"
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recipient := store.Recipient{Email: "one@example.com", ToJSON: `["one@example.com"]`, Subject: "test", Status: "pending"}
	if err = db.CreateJob(store.Job{ID: "daily-job", Kind: "send", Status: "pending", Account: "personal"}, "", []store.Recipient{recipient}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Recipients("daily-job")
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := db.StartAttempt(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = db.FinishAttempt(attempt, rows[0].ID, "sent", "message-1", ""); err != nil {
		t.Fatal(err)
	}
	if err = checkDailyLimit(db, "personal", 1, 2); err != nil {
		t.Fatalf("one remaining slot should fit: %v", err)
	}
	if err = checkDailyLimit(db, "personal", 2, 2); err == nil {
		t.Fatal("expected cap to reject a batch that would exceed it")
	}
}
