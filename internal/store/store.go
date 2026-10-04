package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrRecipientClaimed = fmt.Errorf("recipient is no longer pending or safely retryable")

type DB struct {
	*sql.DB
	path string
}
type Job struct {
	ID, Kind, Status, Account, Subject, TemplatePath, TemplateHash, CreatedAt, UpdatedAt, Error string
	DailyLimit                                                                                  int
	DelaySeconds, MaxDelaySeconds                                                               int
}
type Recipient struct {
	ID                                                                                                         int64
	JobID, Email, ToJSON, CcJSON, BccJSON, Subject, Body, HTML, Status, MessageID, Error, CreatedAt, UpdatedAt string
	Attempts                                                                                                   int
	Variables                                                                                                  map[string]string
	Attachments                                                                                                []Attachment
}
type Attachment struct{ Path, SHA256 string }
type Attempt struct {
	ID                                              int64
	RecipientID                                     int64
	StartedAt, FinishedAt, Status, MessageID, Error string
}
type HistoryRow struct{ Date, Recipient, Subject, Status, MessageID, JobID string }

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(path), 0700)
	secureFile, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	_ = secureFile.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY, kind TEXT NOT NULL, status TEXT NOT NULL, account TEXT NOT NULL, subject TEXT NOT NULL DEFAULT '', template_path TEXT NOT NULL DEFAULT '', template_hash TEXT NOT NULL DEFAULT '', daily_limit INTEGER NOT NULL DEFAULT 0, delay_seconds INTEGER NOT NULL DEFAULT 0, max_delay_seconds INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS campaigns(id INTEGER PRIMARY KEY AUTOINCREMENT, job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE, fingerprint TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS recipients(id INTEGER PRIMARY KEY AUTOINCREMENT, job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE, email TEXT NOT NULL, to_json TEXT NOT NULL DEFAULT '[]', cc_json TEXT NOT NULL DEFAULT '[]', bcc_json TEXT NOT NULL DEFAULT '[]', subject TEXT NOT NULL DEFAULT '', body TEXT NOT NULL DEFAULT '', html TEXT NOT NULL DEFAULT '', variables_json TEXT NOT NULL DEFAULT '{}', attachments_json TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, message_id TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(job_id,email));
CREATE TABLE IF NOT EXISTS attempts(id INTEGER PRIMARY KEY AUTOINCREMENT, recipient_id INTEGER NOT NULL REFERENCES recipients(id) ON DELETE CASCADE, started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, message_id TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS recipients_status_idx ON recipients(job_id,status); CREATE INDEX IF NOT EXISTS recipients_created_idx ON recipients(created_at); CREATE INDEX IF NOT EXISTS jobs_created_idx ON jobs(created_at);`); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0600)
	_ = os.Chmod(path+"-wal", 0600)
	_ = os.Chmod(path+"-shm", 0600)
	return &DB{DB: db, path: path}, nil
}
func (d *DB) Close() error { return d.DB.Close() }
func now() string          { return time.Now().UTC().Format(time.RFC3339Nano) }
func NewID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405"), hex.EncodeToString(b)), nil
}
func (d *DB) CreateJob(j Job, fingerprint string, recipients []Recipient) error {
	tx, e := d.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	n := now()
	j.CreatedAt = n
	j.UpdatedAt = n
	_, e = tx.Exec(`INSERT INTO jobs(id,kind,status,account,subject,template_path,template_hash,daily_limit,delay_seconds,max_delay_seconds,error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, j.ID, j.Kind, j.Status, j.Account, j.Subject, j.TemplatePath, j.TemplateHash, j.DailyLimit, j.DelaySeconds, j.MaxDelaySeconds, j.Error, n, n)
	if e != nil {
		return e
	}
	if fingerprint != "" {
		if _, e = tx.Exec(`INSERT INTO campaigns(job_id,fingerprint,created_at) VALUES(?,?,?)`, j.ID, fingerprint, n); e != nil {
			return e
		}
	}
	for _, r := range recipients {
		vars, _ := json.Marshal(r.Variables)
		attachments, _ := json.Marshal(r.Attachments)
		to, _ := json.Marshal(r.ToJSON)
		cc, _ := json.Marshal(r.CcJSON)
		bcc, _ := json.Marshal(r.BccJSON)
		_, e = tx.Exec(`INSERT INTO recipients(job_id,email,to_json,cc_json,bcc_json,subject,body,html,variables_json,attachments_json,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, j.ID, r.Email, string(to), string(cc), string(bcc), r.Subject, r.Body, r.HTML, string(vars), string(attachments), r.Status, n, n)
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (d *DB) JobByID(id string) (Job, error) {
	var j Job
	e := d.QueryRow(`SELECT id,kind,status,account,subject,template_path,template_hash,daily_limit,delay_seconds,max_delay_seconds,error,created_at,updated_at FROM jobs WHERE id=?`, id).Scan(&j.ID, &j.Kind, &j.Status, &j.Account, &j.Subject, &j.TemplatePath, &j.TemplateHash, &j.DailyLimit, &j.DelaySeconds, &j.MaxDelaySeconds, &j.Error, &j.CreatedAt, &j.UpdatedAt)
	return j, e
}
func scanRecipient(rows *sql.Rows) (Recipient, error) {
	var r Recipient
	var vars, attachments string
	e := rows.Scan(&r.ID, &r.JobID, &r.Email, &r.ToJSON, &r.CcJSON, &r.BccJSON, &r.Subject, &r.Body, &r.HTML, &vars, &attachments, &r.Status, &r.Attempts, &r.MessageID, &r.Error, &r.CreatedAt, &r.UpdatedAt)
	if e == nil {
		_ = json.Unmarshal([]byte(vars), &r.Variables)
		_ = json.Unmarshal([]byte(attachments), &r.Attachments)
	}
	return r, e
}
func (d *DB) Recipients(job string) ([]Recipient, error) {
	rows, e := d.Query(`SELECT id,job_id,email,to_json,cc_json,bcc_json,subject,body,html,variables_json,attachments_json,status,attempts,message_id,error,created_at,updated_at FROM recipients WHERE job_id=? ORDER BY id`, job)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Recipient
	for rows.Next() {
		r, e := scanRecipient(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (d *DB) UpdateJobStatus(id, status, msg string) error {
	_, e := d.Exec(`UPDATE jobs SET status=?,error=?,updated_at=? WHERE id=?`, status, msg, now(), id)
	return e
}
func (d *DB) StartAttempt(r Recipient) (int64, error) {
	tx, e := d.Begin()
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	n := now()
	res, e := tx.Exec(`UPDATE recipients SET status='sending',attempts=attempts+1,error='',updated_at=? WHERE id=? AND status IN ('pending','failed')`, n, r.ID)
	if e != nil {
		return 0, e
	}
	changed, e := res.RowsAffected()
	if e != nil {
		return 0, e
	}
	if changed != 1 {
		return 0, ErrRecipientClaimed
	}
	res, e = tx.Exec(`INSERT INTO attempts(recipient_id,started_at,status) VALUES(?,?,'sending')`, r.ID, n)
	if e != nil {
		return 0, e
	}
	id, e := res.LastInsertId()
	if e != nil {
		return 0, e
	}
	return id, tx.Commit()
}
func (d *DB) FinishAttempt(aid, rid int64, status, msgid, errtext string) error {
	n := now()
	tx, e := d.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.Exec(`UPDATE attempts SET finished_at=?,status=?,message_id=?,error=? WHERE id=?`, n, status, msgid, errtext, aid)
	if e != nil {
		return e
	}
	_, e = tx.Exec(`UPDATE recipients SET status=?,message_id=?,error=?,updated_at=? WHERE id=?`, status, msgid, errtext, n, rid)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (d *DB) DailySentCount(account string) (int, error) {
	var n int
	t := time.Now().In(time.Local)
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local).UTC().Format(time.RFC3339Nano)
	e := d.QueryRow(`SELECT count(*) FROM recipients r JOIN jobs j ON j.id=r.job_id WHERE j.account=? AND r.status='sent' AND r.updated_at>=?`, account, start).Scan(&n)
	return n, e
}
func (d *DB) CampaignAlreadySent(fingerprint string) (bool, error) {
	var n int
	e := d.QueryRow(`SELECT count(*) FROM campaigns c JOIN recipients r ON r.job_id=c.job_id WHERE c.fingerprint=? AND r.status='sent'`, fingerprint).Scan(&n)
	return n > 0, e
}
func (d *DB) History(job string, limit int, accountFilter ...string) ([]HistoryRow, error) {
	q := `SELECT r.created_at,r.email,r.subject,r.status,r.message_id,r.job_id FROM recipients r JOIN jobs j ON j.id=r.job_id`
	args := []any{}
	where := []string{}
	if job != "" {
		where = append(where, `r.job_id=?`)
		args = append(args, job)
	}
	if len(accountFilter) > 0 && accountFilter[0] != "" {
		where = append(where, `j.account=?`)
		args = append(args, accountFilter[0])
	}
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY r.created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, e := d.Query(q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []HistoryRow
	for rows.Next() {
		var r HistoryRow
		if e = rows.Scan(&r.Date, &r.Recipient, &r.Subject, &r.Status, &r.MessageID, &r.JobID); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (d *DB) Jobs(limit int) ([]Job, error) {
	rows, e := d.Query(`SELECT id,kind,status,account,subject,template_path,template_hash,daily_limit,delay_seconds,max_delay_seconds,error,created_at,updated_at FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if e = rows.Scan(&j.ID, &j.Kind, &j.Status, &j.Account, &j.Subject, &j.TemplatePath, &j.TemplateHash, &j.DailyLimit, &j.DelaySeconds, &j.MaxDelaySeconds, &j.Error, &j.CreatedAt, &j.UpdatedAt); e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (d *DB) HasSentTo(email string) (bool, error) {
	var n int
	e := d.QueryRow(`SELECT count(*) FROM recipients WHERE lower(email)=lower(?) AND status='sent'`, email).Scan(&n)
	return n > 0, e
}

func (d *DB) HasSentInCampaign(email, fingerprint string) (bool, error) {
	var n int
	e := d.QueryRow(`SELECT count(*) FROM campaigns c JOIN recipients r ON r.job_id=c.job_id WHERE c.fingerprint=? AND lower(r.email)=lower(?) AND r.status='sent'`, fingerprint, email).Scan(&n)
	return n > 0, e
}

func (d *DB) RecoverSending(job string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	n := now()
	if _, err = tx.Exec(`UPDATE attempts SET status='uncertain',finished_at=?,error='process exited while sending; Gmail acceptance is unknown' WHERE status='sending' AND recipient_id IN (SELECT id FROM recipients WHERE job_id=?)`, n, job); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE recipients SET status='uncertain',error='process exited while sending; Gmail acceptance is unknown',updated_at=? WHERE status='sending' AND job_id=?`, n, job); err != nil {
		return err
	}
	return tx.Commit()
}
