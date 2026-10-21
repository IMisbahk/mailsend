package dispatch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IMisbahk/mailsend/internal/store"
)

func TestParseJSONSpacesEmailsAndKeepsExplicitTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "india.json")
	body := []byte(`{
	  "label": "India",
	  "account": "sunbeam",
	  "start": "2026-10-21T09:00:00+05:30",
	  "interval": "5m",
	  "timezone": "Asia/Kolkata",
	  "daily_cap": 95,
	  "holds": [{"path": "SEND_HOLD"}, {"path": "FOLLOWUP_HOLD", "steps": ["fu-d3", "ooo"]}],
	  "emails": [
	    {"to": "Ada <a@example.com>", "subject": "Hello {{name}}", "body": "Hi {{name}}", "vars": {"name": "Ada"}, "step": "first", "tag": "in-1"},
	    {"to": "b@example.com", "subject": "Later", "body": "Hi", "at": "2026-10-21T09:30:00+05:30", "step": "fu-d3"}
	  ]
	}`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SEND_HOLD"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	batch, items, err := ParseBatchFile(path, ParseDefaults{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Account != "sunbeam" || batch.Label != "India" || batch.IntervalSeconds != 300 || batch.DailyCap != 95 {
		t.Fatalf("batch %#v", batch)
	}
	if len(batch.Holds) != 2 || !filepath.IsAbs(batch.Holds[0].Path) {
		t.Fatalf("holds %#v", batch.Holds)
	}
	if len(items) != 2 || items[0].Email != "a@example.com" || items[0].Subject != "Hello Ada" || items[0].Body != "Hi Ada" || items[0].Step != "first" {
		t.Fatalf("first item %#v", items[0])
	}
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ParseStamp(items[0].ScheduledAt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ParseStamp(items[1].ScheduledAt)
	if err != nil {
		t.Fatal(err)
	}
	wantFirst := time.Date(2026, 10, 21, 9, 0, 0, 0, loc)
	wantSecond := time.Date(2026, 10, 21, 9, 30, 0, 0, loc)
	if !first.Equal(wantFirst) || !second.Equal(wantSecond) {
		t.Fatalf("times %s %s", first, second)
	}
}

func TestParseCSVRendersTemplateColumns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wave.csv")
	csv := "to,subject,template,step,name\na@example.com,Hi {{name}},outreach,first,Ada\n"
	if err := os.WriteFile(path, []byte(csv), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	batch, items, err := ParseBatchFile(path, ParseDefaults{
		Account: "sunbeam", HasStart: true, Start: start, HasInterval: true, Interval: 5 * time.Minute,
	}, func(name string) (string, string, string, error) {
		if name != "outreach" {
			t.Fatalf("template %s", name)
		}
		return "", "Hello {{name}}", "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if batch.IntervalSeconds != 300 || len(items) != 1 || items[0].Subject != "Hi Ada" || items[0].Body != "Hello Ada" {
		t.Fatalf("batch %#v item %#v", batch, items[0])
	}
}
