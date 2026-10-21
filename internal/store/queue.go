package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const stampLayout = "2006-01-02T15:04:05.000000000Z"

// FormatStamp stores instants so SQLite text ordering matches time order.
func FormatStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(stampLayout)
}

// ParseStamp reads a timestamp written by FormatStamp or by older RFC3339 history rows.
func ParseStamp(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	for _, layout := range []string{stampLayout, time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
}

// HoldFile pauses dispatch while Path exists. Empty Steps pauses every item.
// When Steps is set, only items with that step wait.
type HoldFile struct {
	Path  string   `json:"path"`
	Steps []string `json:"steps,omitempty"`
}

// QueueBatch is one scheduled list of individually addressed emails.
type QueueBatch struct {
	ID              string         `json:"id"`
	Account         string         `json:"account"`
	Label           string         `json:"label"`
	Status          string         `json:"status"`
	IntervalSeconds int            `json:"interval_seconds"`
	StartAt         string         `json:"start"`
	Timezone        string         `json:"timezone,omitempty"`
	DailyCap        int            `json:"daily_cap"`
	HookCommand     string         `json:"hook,omitempty"`
	AfterCommand    string         `json:"after_send,omitempty"`
	Holds           []HoldFile     `json:"holds,omitempty"`
	FromName        string         `json:"from_name,omitempty"`
	ReplyTo         string         `json:"reply_to,omitempty"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
	Counts          map[string]int `json:"counts,omitempty"`
}

// Interval is the batch's fixed spacing.
func (b QueueBatch) Interval() time.Duration {
	if b.IntervalSeconds < 0 {
		return 0
	}
	return time.Duration(b.IntervalSeconds) * time.Second
}

// QueueItem is one queued recipient. Status moves pending → sending → sent,
// or to blocked, failed, parked, or cancelled.
type QueueItem struct {
	ID                 int64    `json:"id"`
	BatchID            string   `json:"batch_id"`
	Position           int      `json:"position"`
	Email              string   `json:"email"`
	Cc                 []string `json:"cc,omitempty"`
	Bcc                []string `json:"bcc,omitempty"`
	Subject            string   `json:"subject"`
	Body               string   `json:"body,omitempty"`
	HTML               string   `json:"html,omitempty"`
	Step               string   `json:"step,omitempty"`
	Tag                string   `json:"tag,omitempty"`
	ScheduledAt        string   `json:"scheduled_at"`
	Status             string   `json:"status"`
	MessageID          string   `json:"message_id,omitempty"`
	Error              string   `json:"error,omitempty"`
	Attempts           int      `json:"attempts"`
	AttemptStartedAt   string   `json:"attempt_started_at,omitempty"`
	HistoryJobID       string   `json:"history_job_id,omitempty"`
	HistoryRecipientID int64    `json:"-"`
	AttemptID          int64    `json:"-"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

// QueueSend joins an item with the batch fields the dispatcher needs.
type QueueSend struct {
	Item  QueueItem
	Batch QueueBatch
}

func migrateQueue(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS queue_batches (
  id TEXT PRIMARY KEY,
  account TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  interval_seconds INTEGER NOT NULL,
  start_at TEXT NOT NULL,
  timezone TEXT NOT NULL DEFAULT '',
  daily_cap INTEGER NOT NULL DEFAULT 0,
  hook_command TEXT NOT NULL DEFAULT '',
  after_command TEXT NOT NULL DEFAULT '',
  holds_json TEXT NOT NULL DEFAULT '[]',
  from_name TEXT NOT NULL DEFAULT '',
  reply_to TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS queue_items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  batch_id TEXT NOT NULL REFERENCES queue_batches(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  email TEXT NOT NULL,
  cc_json TEXT NOT NULL DEFAULT '[]',
  bcc_json TEXT NOT NULL DEFAULT '[]',
  subject TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  html TEXT NOT NULL DEFAULT '',
  step TEXT NOT NULL DEFAULT '',
  tag TEXT NOT NULL DEFAULT '',
  scheduled_at TEXT NOT NULL,
  status TEXT NOT NULL,
  message_id TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0,
  attempt_started_at TEXT NOT NULL DEFAULT '',
  history_job_id TEXT NOT NULL DEFAULT '',
  history_recipient_id INTEGER NOT NULL DEFAULT 0,
  attempt_id INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS dispatch_cursor (
  account TEXT PRIMARY KEY,
  last_sent_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS queue_items_batch_idx ON queue_items(batch_id, position);
CREATE INDEX IF NOT EXISTS queue_items_due_idx ON queue_items(status, scheduled_at);
CREATE INDEX IF NOT EXISTS queue_batches_account_idx ON queue_batches(account, status);`)
	return err
}

func (d *DB) InsertQueueBatch(batch QueueBatch, items []QueueItem) (string, error) {
	if strings.TrimSpace(batch.ID) == "" {
		id, err := NewID()
		if err != nil {
			return "", err
		}
		batch.ID = id
	}
	if batch.Account == "" {
		return "", fmt.Errorf("queue batch requires an account")
	}
	if batch.Status == "" {
		batch.Status = "active"
	}
	if batch.IntervalSeconds < 0 {
		return "", fmt.Errorf("interval cannot be negative")
	}
	if len(items) == 0 {
		return "", fmt.Errorf("queue batch has no emails")
	}
	if batch.Holds == nil {
		batch.Holds = []HoldFile{}
	}
	holds, err := json.Marshal(batch.Holds)
	if err != nil {
		return "", err
	}
	tx, err := d.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	n := now()
	if batch.CreatedAt != "" {
		n = batch.CreatedAt
	}
	_, err = tx.Exec(`INSERT INTO queue_batches(id,account,label,status,interval_seconds,start_at,timezone,daily_cap,hook_command,after_command,holds_json,from_name,reply_to,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		batch.ID, batch.Account, batch.Label, batch.Status, batch.IntervalSeconds, batch.StartAt, batch.Timezone, batch.DailyCap, batch.HookCommand, batch.AfterCommand, string(holds), batch.FromName, batch.ReplyTo, n, n)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if strings.TrimSpace(item.Email) == "" || strings.TrimSpace(item.Subject) == "" {
			return "", fmt.Errorf("each queued email needs a recipient and a subject")
		}
		if strings.TrimSpace(item.Body) == "" && strings.TrimSpace(item.HTML) == "" {
			return "", fmt.Errorf("queued email to %s has no body", item.Email)
		}
		if item.ScheduledAt == "" {
			return "", fmt.Errorf("queued email to %s has no scheduled time", item.Email)
		}
		cc, err := json.Marshal(item.Cc)
		if err != nil {
			return "", err
		}
		if len(item.Cc) == 0 {
			cc = []byte("[]")
		}
		bcc, err := json.Marshal(item.Bcc)
		if err != nil {
			return "", err
		}
		if len(item.Bcc) == 0 {
			bcc = []byte("[]")
		}
		if item.Status == "" {
			item.Status = "pending"
		}
		_, err = tx.Exec(`INSERT INTO queue_items(batch_id,position,email,cc_json,bcc_json,subject,body,html,step,tag,scheduled_at,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			batch.ID, item.Position, item.Email, string(cc), string(bcc), item.Subject, item.Body, item.HTML, item.Step, item.Tag, item.ScheduledAt, item.Status, n, n)
		if err != nil {
			return "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return batch.ID, nil
}

func (d *DB) ListQueueBatches(account string) ([]QueueBatch, error) {
	query := `SELECT id,account,label,status,interval_seconds,start_at,timezone,daily_cap,hook_command,after_command,holds_json,from_name,reply_to,created_at,updated_at FROM queue_batches`
	args := []any{}
	if account != "" {
		query += ` WHERE account=?`
		args = append(args, account)
	}
	query += ` ORDER BY created_at, id`
	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueBatch
	for rows.Next() {
		batch, err := scanQueueBatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, batch)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []QueueBatch{}
	}
	for i := range out {
		counts, err := d.CountQueueItems(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Counts = counts
	}
	return out, nil
}

func (d *DB) QueueBatch(id string) (QueueBatch, error) {
	row := d.QueryRow(`SELECT id,account,label,status,interval_seconds,start_at,timezone,daily_cap,hook_command,after_command,holds_json,from_name,reply_to,created_at,updated_at FROM queue_batches WHERE id=?`, id)
	batch, err := scanQueueBatch(row)
	if errors.Is(err, sql.ErrNoRows) {
		return QueueBatch{}, fmt.Errorf("batch %q not found", id)
	}
	if err != nil {
		return QueueBatch{}, err
	}
	batch.Counts, err = d.CountQueueItems(batch.ID)
	return batch, err
}

func (d *DB) ListQueueItems(batchID string) ([]QueueItem, error) {
	rows, err := d.Query(queueItemSelect+` WHERE batch_id=? ORDER BY position, id`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectQueueItems(rows)
}

func (d *DB) QueueItem(id int64) (QueueItem, error) {
	row := d.QueryRow(queueItemSelect+` WHERE id=?`, id)
	item, err := scanQueueItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return QueueItem{}, fmt.Errorf("queue item %d not found", id)
	}
	return item, err
}

func (d *DB) CountQueueItems(batchID string) (map[string]int, error) {
	rows, err := d.Query(`SELECT status, count(*) FROM queue_items WHERE batch_id=? GROUP BY status`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err = rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		counts[status] = n
	}
	return counts, rows.Err()
}

func (d *DB) PendingQueueSends(account string) ([]QueueSend, error) {
	return d.queueSends(account, `i.status='pending' AND b.status='active'`, `i.scheduled_at, b.id, i.position, i.id`)
}

func (d *DB) SendingQueueSends(account string) ([]QueueSend, error) {
	return d.queueSends(account, `i.status='sending'`, `i.attempt_started_at, i.id`)
}

func (d *DB) ParkedQueueItems(account string) ([]QueueItem, error) {
	rows, err := d.Query(queueItemSelect+` WHERE status='parked' AND batch_id IN (SELECT id FROM queue_batches WHERE account=?) ORDER BY id`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectQueueItems(rows)
}

func (d *DB) queueSends(account, where, order string) ([]QueueSend, error) {
	query := `
SELECT i.id,i.batch_id,i.position,i.email,i.cc_json,i.bcc_json,i.subject,i.body,i.html,i.step,i.tag,i.scheduled_at,i.status,i.message_id,i.error,i.attempts,i.attempt_started_at,i.history_job_id,i.history_recipient_id,i.attempt_id,i.created_at,i.updated_at,
b.id,b.account,b.label,b.status,b.interval_seconds,b.start_at,b.timezone,b.daily_cap,b.hook_command,b.after_command,b.holds_json,b.from_name,b.reply_to,b.created_at,b.updated_at
FROM queue_items i JOIN queue_batches b ON b.id=i.batch_id
WHERE b.account=? AND ` + where + ` ORDER BY ` + order
	rows, err := d.Query(query, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueSend
	for rows.Next() {
		var send QueueSend
		var cc, bcc, holds string
		err = rows.Scan(&send.Item.ID, &send.Item.BatchID, &send.Item.Position, &send.Item.Email, &cc, &bcc, &send.Item.Subject, &send.Item.Body, &send.Item.HTML, &send.Item.Step, &send.Item.Tag, &send.Item.ScheduledAt, &send.Item.Status, &send.Item.MessageID, &send.Item.Error, &send.Item.Attempts, &send.Item.AttemptStartedAt, &send.Item.HistoryJobID, &send.Item.HistoryRecipientID, &send.Item.AttemptID, &send.Item.CreatedAt, &send.Item.UpdatedAt,
			&send.Batch.ID, &send.Batch.Account, &send.Batch.Label, &send.Batch.Status, &send.Batch.IntervalSeconds, &send.Batch.StartAt, &send.Batch.Timezone, &send.Batch.DailyCap, &send.Batch.HookCommand, &send.Batch.AfterCommand, &holds, &send.Batch.FromName, &send.Batch.ReplyTo, &send.Batch.CreatedAt, &send.Batch.UpdatedAt)
		if err != nil {
			return nil, err
		}
		send.Item.Cc, err = DecodeStringList(cc)
		if err != nil {
			return nil, err
		}
		send.Item.Bcc, err = DecodeStringList(bcc)
		if err != nil {
			return nil, err
		}
		send.Batch.Holds, err = decodeHolds(holds)
		if err != nil {
			return nil, err
		}
		out = append(out, send)
	}
	return out, rows.Err()
}

// ClaimQueueItem writes the sending record, including a history row, before the provider is called.
func (d *DB) ClaimQueueItem(id int64, when time.Time) (QueueItem, error) {
	tx, err := d.Begin()
	if err != nil {
		return QueueItem{}, err
	}
	defer tx.Rollback()
	n := FormatStamp(when)
	res, err := tx.Exec(`UPDATE queue_items SET status='sending', attempts=attempts+1, attempt_started_at=?, error='', updated_at=? WHERE id=? AND status='pending'`, n, n, id)
	if err != nil {
		return QueueItem{}, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return QueueItem{}, err
	}
	if changed != 1 {
		return QueueItem{}, fmt.Errorf("queue item %d is not pending", id)
	}
	var batchID, email, cc, bcc, subject, body, html, account string
	err = tx.QueryRow(`SELECT i.batch_id,i.email,i.cc_json,i.bcc_json,i.subject,i.body,i.html,b.account FROM queue_items i JOIN queue_batches b ON b.id=i.batch_id WHERE i.id=?`, id).Scan(&batchID, &email, &cc, &bcc, &subject, &body, &html, &account)
	if err != nil {
		return QueueItem{}, err
	}
	jobID, err := NewID()
	if err != nil {
		return QueueItem{}, err
	}
	_, err = tx.Exec(`INSERT INTO jobs(id,kind,status,account,subject,template_path,template_hash,daily_limit,delay_seconds,max_delay_seconds,error,created_at,updated_at) VALUES(?,'queue','sending',?,?,'','',0,0,0,'',?,?)`, jobID, account, subject, n, n)
	if err != nil {
		return QueueItem{}, err
	}
	res, err = tx.Exec(`INSERT INTO recipients(job_id,email,to_json,cc_json,bcc_json,subject,body,html,variables_json,attachments_json,status,attempts,message_id,error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,'{}','[]','sending',1,'','',?,?)`, jobID, email, mustJSONList(email), addressColumn(cc), addressColumn(bcc), subject, body, html, n, n)
	if err != nil {
		return QueueItem{}, err
	}
	recipientID, err := res.LastInsertId()
	if err != nil {
		return QueueItem{}, err
	}
	res, err = tx.Exec(`INSERT INTO attempts(recipient_id,started_at,status) VALUES(?,?,'sending')`, recipientID, n)
	if err != nil {
		return QueueItem{}, err
	}
	attemptID, err := res.LastInsertId()
	if err != nil {
		return QueueItem{}, err
	}
	_, err = tx.Exec(`UPDATE queue_items SET history_job_id=?, history_recipient_id=?, attempt_id=? WHERE id=?`, jobID, recipientID, attemptID, id)
	if err != nil {
		return QueueItem{}, err
	}
	if err = tx.Commit(); err != nil {
		return QueueItem{}, err
	}
	return d.QueueItem(id)
}

// FinishQueueItem closes a claimed item. status pending returns it to the queue after the open history row is closed as failed.
func (d *DB) FinishQueueItem(id int64, status, messageID, errText string, when time.Time) error {
	switch status {
	case "sent", "failed", "parked", "pending":
	default:
		return fmt.Errorf("unsupported queue status %q", status)
	}
	item, err := d.QueueItem(id)
	if err != nil {
		return err
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	n := FormatStamp(when)
	historyStatus := status
	queueStatus := status
	if status == "pending" {
		historyStatus = "failed"
		if strings.TrimSpace(errText) == "" {
			errText = "returned to the queue after reconciliation"
		}
	}
	if item.AttemptID != 0 {
		if _, err = tx.Exec(`UPDATE attempts SET finished_at=?, status=?, message_id=?, error=? WHERE id=?`, n, historyStatus, messageID, errText, item.AttemptID); err != nil {
			return err
		}
	}
	if item.HistoryRecipientID != 0 {
		if _, err = tx.Exec(`UPDATE recipients SET status=?, message_id=?, error=?, updated_at=? WHERE id=?`, historyStatus, messageID, errText, n, item.HistoryRecipientID); err != nil {
			return err
		}
	}
	if item.HistoryJobID != "" {
		if _, err = tx.Exec(`UPDATE jobs SET status=?, error=?, updated_at=? WHERE id=?`, historyStatus, errText, n, item.HistoryJobID); err != nil {
			return err
		}
	}
	if queueStatus == "pending" {
		_, err = tx.Exec(`UPDATE queue_items SET status='pending', message_id='', error=?, attempt_started_at='', history_job_id='', history_recipient_id=0, attempt_id=0, updated_at=? WHERE id=?`, errText, n, id)
	} else {
		_, err = tx.Exec(`UPDATE queue_items SET status=?, message_id=?, error=?, updated_at=? WHERE id=?`, queueStatus, messageID, errText, n, id)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SetQueueItemStatus updates an item that has not been claimed, such as a blocked or cancelled row.
func (d *DB) SetQueueItemStatus(id int64, status, errText string, when time.Time) error {
	if status != "blocked" && status != "cancelled" && status != "pending" && status != "failed" {
		return fmt.Errorf("unsupported queue status %q", status)
	}
	res, err := d.Exec(`UPDATE queue_items SET status=?, error=?, updated_at=? WHERE id=? AND status IN ('pending','blocked','failed','parked','cancelled')`, status, errText, FormatStamp(when), id)
	if err != nil {
		return err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("queue item %d cannot move to %s", id, status)
	}
	return nil
}

func (d *DB) CancelQueueBatch(id string, when time.Time) (int, error) {
	if _, err := d.QueueBatch(id); err != nil {
		return 0, err
	}
	tx, err := d.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := FormatStamp(when)
	res, err := tx.Exec(`UPDATE queue_items SET status='cancelled', error='batch cancelled', updated_at=? WHERE batch_id=? AND status='pending'`, n, id)
	if err != nil {
		return 0, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`UPDATE queue_batches SET status='cancelled', updated_at=? WHERE id=?`, n, id); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return int(changed), nil
}

func (d *DB) ActivateQueueBatch(id string, when time.Time) error {
	_, err := d.Exec(`UPDATE queue_batches SET status='active', updated_at=? WHERE id=? AND status IN ('active','completed','needs_attention','cancelled')`, FormatStamp(when), id)
	return err
}

func (d *DB) RefreshQueueBatches(account string, when time.Time) error {
	batches, err := d.ListQueueBatches(account)
	if err != nil {
		return err
	}
	n := FormatStamp(when)
	for _, batch := range batches {
		if batch.Status != "active" && batch.Status != "needs_attention" && batch.Status != "completed" {
			continue
		}
		pending := batch.Counts["pending"]
		sending := batch.Counts["sending"]
		parked := batch.Counts["parked"]
		status := "active"
		if pending == 0 && sending == 0 {
			if parked > 0 {
				status = "needs_attention"
			} else {
				status = "completed"
			}
		}
		if status == batch.Status {
			continue
		}
		if _, err = d.Exec(`UPDATE queue_batches SET status=?, updated_at=? WHERE id=?`, status, n, batch.ID); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) SetDispatchCursor(account string, when time.Time) error {
	n := FormatStamp(when)
	_, err := d.Exec(`INSERT INTO dispatch_cursor(account,last_sent_at,updated_at) VALUES(?,?,?)
ON CONFLICT(account) DO UPDATE SET last_sent_at=excluded.last_sent_at, updated_at=excluded.updated_at
WHERE excluded.last_sent_at > dispatch_cursor.last_sent_at`, account, n, n)
	return err
}

func (d *DB) DispatchCursor(account string) (time.Time, bool, error) {
	var raw string
	err := d.QueryRow(`SELECT last_sent_at FROM dispatch_cursor WHERE account=?`, account).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || strings.TrimSpace(raw) == "" {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, false, nil
		}
		if err != nil {
			return time.Time{}, false, err
		}
		return time.Time{}, false, nil
	}
	parsed, err := ParseStamp(raw)
	if err != nil {
		return time.Time{}, false, err
	}
	return parsed, true, nil
}

// SentCountSince counts confirmed sends for the account at or after start.
// Queue sends and ordinary send/bulk history are both included. Timestamps are
// parsed in Go because history rows are not all stored with a fixed precision.
func (d *DB) SentCountSince(account string, start time.Time) (int, error) {
	rows, err := d.Query(`SELECT r.updated_at FROM recipients r JOIN jobs j ON j.id=r.job_id WHERE j.account=? AND r.status='sent'`, account)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return 0, err
		}
		stamp, err := ParseStamp(raw)
		if err != nil {
			return 0, fmt.Errorf("history timestamp %q: %w", raw, err)
		}
		if !stamp.Before(start) {
			count++
		}
	}
	return count, rows.Err()
}

// FindConfirmedSend looks for a history row that already proves this recipient and subject were accepted.
func (d *DB) FindConfirmedSend(account, email, subject string, after time.Time, exceptRecipient int64) (string, bool, error) {
	rows, err := d.Query(`SELECT r.id, r.subject, r.message_id, r.updated_at FROM recipients r JOIN jobs j ON j.id=r.job_id WHERE j.account=? AND lower(r.email)=lower(?) AND r.status='sent' AND r.message_id!=''`, account, email)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	want := strings.TrimSpace(subject)
	var foundID string
	var foundAt time.Time
	for rows.Next() {
		var id int64
		var storedSubject, messageID, raw string
		if err = rows.Scan(&id, &storedSubject, &messageID, &raw); err != nil {
			return "", false, err
		}
		if id == exceptRecipient {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(storedSubject), want) {
			continue
		}
		stamp, err := ParseStamp(raw)
		if err != nil {
			return "", false, err
		}
		if stamp.Before(after) {
			continue
		}
		if foundID == "" || stamp.Before(foundAt) {
			foundID = messageID
			foundAt = stamp
		}
	}
	if err = rows.Err(); err != nil {
		return "", false, err
	}
	if foundID == "" {
		return "", false, nil
	}
	return foundID, true, nil
}

func (d *DB) RecordResolvedSend(item QueueItem, account, messageID, note string, when time.Time) error {
	if messageID == "" {
		return fmt.Errorf("message id is required")
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	n := FormatStamp(when)
	if item.HistoryRecipientID != 0 {
		if _, err = tx.Exec(`UPDATE recipients SET status='sent', message_id=?, error=?, updated_at=? WHERE id=?`, messageID, note, n, item.HistoryRecipientID); err != nil {
			return err
		}
		if item.AttemptID != 0 {
			if _, err = tx.Exec(`UPDATE attempts SET finished_at=?, status='sent', message_id=?, error=? WHERE id=?`, n, messageID, note, item.AttemptID); err != nil {
				return err
			}
		}
		if item.HistoryJobID != "" {
			if _, err = tx.Exec(`UPDATE jobs SET status='sent', error=?, updated_at=? WHERE id=?`, note, n, item.HistoryJobID); err != nil {
				return err
			}
		}
	} else {
		jobID, err := NewID()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO jobs(id,kind,status,account,subject,template_path,template_hash,daily_limit,delay_seconds,max_delay_seconds,error,created_at,updated_at) VALUES(?,'queue','sent',?,?,'','',0,0,0,?,?,?)`, jobID, account, item.Subject, note, n, n); err != nil {
			return err
		}
		cc, _ := json.Marshal(item.Cc)
		bcc, _ := json.Marshal(item.Bcc)
		if _, err = tx.Exec(`INSERT INTO recipients(job_id,email,to_json,cc_json,bcc_json,subject,body,html,variables_json,attachments_json,status,attempts,message_id,error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,'{}','[]','sent',0,?,'',?,?)`, jobID, item.Email, mustJSONList(item.Email), string(cc), string(bcc), item.Subject, item.Body, item.HTML, messageID, n, n); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE queue_items SET status='sent', message_id=?, error=?, updated_at=? WHERE id=?`, messageID, note, n, item.ID); err != nil {
		return err
	}
	return tx.Commit()
}

const queueItemSelect = `SELECT id,batch_id,position,email,cc_json,bcc_json,subject,body,html,step,tag,scheduled_at,status,message_id,error,attempts,attempt_started_at,history_job_id,history_recipient_id,attempt_id,created_at,updated_at FROM queue_items`

type queueScanner interface {
	Scan(dest ...any) error
}

func scanQueueBatch(row queueScanner) (QueueBatch, error) {
	var batch QueueBatch
	var holds string
	err := row.Scan(&batch.ID, &batch.Account, &batch.Label, &batch.Status, &batch.IntervalSeconds, &batch.StartAt, &batch.Timezone, &batch.DailyCap, &batch.HookCommand, &batch.AfterCommand, &holds, &batch.FromName, &batch.ReplyTo, &batch.CreatedAt, &batch.UpdatedAt)
	if err != nil {
		return QueueBatch{}, err
	}
	batch.Holds, err = decodeHolds(holds)
	return batch, err
}

func scanQueueItem(row queueScanner) (QueueItem, error) {
	var item QueueItem
	var cc, bcc string
	err := row.Scan(&item.ID, &item.BatchID, &item.Position, &item.Email, &cc, &bcc, &item.Subject, &item.Body, &item.HTML, &item.Step, &item.Tag, &item.ScheduledAt, &item.Status, &item.MessageID, &item.Error, &item.Attempts, &item.AttemptStartedAt, &item.HistoryJobID, &item.HistoryRecipientID, &item.AttemptID, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return QueueItem{}, err
	}
	item.Cc, err = DecodeStringList(cc)
	if err != nil {
		return QueueItem{}, err
	}
	item.Bcc, err = DecodeStringList(bcc)
	return item, err
}

func collectQueueItems(rows *sql.Rows) ([]QueueItem, error) {
	var out []QueueItem
	for rows.Next() {
		item, err := scanQueueItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if out == nil {
		out = []QueueItem{}
	}
	return out, rows.Err()
}

func decodeHolds(raw string) ([]HoldFile, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var holds []HoldFile
	if err := json.Unmarshal([]byte(raw), &holds); err != nil {
		return nil, fmt.Errorf("invalid hold list: %w", err)
	}
	return holds, nil
}

func mustJSONList(email string) string {
	encoded, _ := json.Marshal([]string{email})
	return string(encoded)
}
