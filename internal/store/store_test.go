package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestJobPersistenceResumeAndHistory(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	j := Job{ID: "job-one", Kind: "bulk", Status: "pending", Account: "personal", DailyLimit: 10}
	rs := []Recipient{{Email: "a@example.com", ToJSON: `["a@example.com"]`, Subject: "hello", Status: "pending"}, {Email: "b@example.com", ToJSON: `["b@example.com"]`, Subject: "hello", Status: "pending"}}
	if err = db.CreateJob(j, "campaign-hash", rs); err != nil {
		t.Fatal(err)
	}
	items, err := db.Recipients(j.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("recipients %d, %v", len(items), err)
	}
	attempt, err := db.StartAttempt(items[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = db.FinishAttempt(attempt, items[0].ID, "sent", "gmail-id-123", ""); err != nil {
		t.Fatal(err)
	}
	already, err := db.HasSentInCampaign("a@example.com", "campaign-hash")
	if err != nil || !already {
		t.Fatalf("campaign dedupe = %v, %v", already, err)
	}
	sent, err := db.DailySentCount("personal")
	if err != nil || sent != 1 {
		t.Fatalf("daily sent %d, %v", sent, err)
	}
	history, err := db.History(j.ID, 10)
	if err != nil || len(history) != 2 || history[1].Status != "sent" || history[1].MessageID != "gmail-id-123" {
		t.Fatalf("history %#v, %v", history, err)
	}
	items, err = db.Recipients(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = db.StartAttempt(items[1])
	if err != nil {
		t.Fatal(err)
	}
	if err = db.RecoverSending(j.ID); err != nil {
		t.Fatal(err)
	}
	items, err = db.Recipients(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if items[1].Status != "uncertain" {
		t.Fatalf("crashed send should not be resent: %#v", items[1])
	}
	if items[0].Status != "sent" {
		t.Fatal("successful recipient changed during resume recovery")
	}
	_ = attempt
}
func TestAddressListsRoundTripAndLegacyEncoding(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recipient := Recipient{Email: "a@example.com", ToJSON: `["a@example.com"]`, CcJSON: `["cc@example.com"]`, BccJSON: `["hidden@example.com"]`, Subject: "hello", Status: "pending"}
	if err = db.CreateJob(Job{ID: "addr-job", Kind: "send", Status: "pending", Account: "agency"}, "", []Recipient{recipient}); err != nil {
		t.Fatal(err)
	}
	var toJSON, ccJSON, bccJSON string
	if err = db.QueryRow(`SELECT to_json, cc_json, bcc_json FROM recipients WHERE job_id=?`, "addr-job").Scan(&toJSON, &ccJSON, &bccJSON); err != nil {
		t.Fatal(err)
	}
	if toJSON != `["a@example.com"]` || ccJSON != `["cc@example.com"]` || bccJSON != `["hidden@example.com"]` {
		t.Fatalf("stored lists %s %s %s", toJSON, ccJSON, bccJSON)
	}
	legacy, err := json.Marshal(`["legacy@example.com"]`)
	if err != nil {
		t.Fatal(err)
	}
	list, err := DecodeStringList(string(legacy))
	if err != nil || len(list) != 1 || list[0] != "legacy@example.com" {
		t.Fatalf("legacy list %#v, %v", list, err)
	}
}

func TestDatabasePermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "private", "db.sqlite3")
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0077 != 0 {
		t.Fatalf("database permissions %o", st.Mode().Perm())
	}
	st, err = os.Stat(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0077 != 0 {
		t.Fatalf("directory permissions %o", st.Mode().Perm())
	}
}
