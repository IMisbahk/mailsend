package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestQueueClaimIsDurableAndCannotBeClaimedTwice(t *testing.T) {
	db := openQueueDB(t)
	when := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	id, err := db.InsertQueueBatch(QueueBatch{
		ID: "batch-a", Account: "acme", Label: "India", Status: "active", IntervalSeconds: 300, StartAt: FormatStamp(when),
	}, []QueueItem{{
		Position: 0, Email: "a@example.com", Subject: "Hello", Body: "Hi", Step: "first", ScheduledAt: FormatStamp(when),
	}})
	if err != nil || id != "batch-a" {
		t.Fatalf("insert %s %v", id, err)
	}
	items, err := db.ListQueueItems("batch-a")
	if err != nil || len(items) != 1 {
		t.Fatalf("items %#v %v", items, err)
	}
	claimed, err := db.ClaimQueueItem(items[0].ID, when)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != "sending" || claimed.HistoryJobID == "" || claimed.Attempts != 1 {
		t.Fatalf("claim %#v", claimed)
	}
	if _, err = db.ClaimQueueItem(items[0].ID, when); err == nil {
		t.Fatal("second claim should fail")
	}
	rows, err := db.Recipients(claimed.HistoryJobID)
	if err != nil || len(rows) != 1 || rows[0].Status != "sending" {
		t.Fatalf("history %#v %v", rows, err)
	}
	reopened, err := Open(db.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.QueueItem(items[0].ID)
	if err != nil || again.Status != "sending" || again.HistoryJobID != claimed.HistoryJobID {
		t.Fatalf("reopened %#v %v", again, err)
	}
	if err = reopened.FinishQueueItem(again.ID, "sent", "gmail-1", "", when.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	sent, err := reopened.SentCountSince("acme", when)
	if err != nil || sent != 1 {
		t.Fatalf("sent count %d %v", sent, err)
	}
	latest, ok, err := reopened.LatestSentAt("acme")
	if err != nil || !ok || !latest.Equal(when.Add(time.Second)) {
		t.Fatalf("latest %s ok %v err %v", latest, ok, err)
	}
	if _, ok, err = reopened.LatestSentAt("other"); err != nil || ok {
		t.Fatalf("empty account ok %v err %v", ok, err)
	}
	finished, err := reopened.QueueItem(again.ID)
	if err != nil || finished.Status != "sent" || finished.MessageID != "gmail-1" {
		t.Fatalf("finished %#v %v", finished, err)
	}
}

func openQueueDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
