package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/IMisbahk/mailsend/internal/auth"
	mailmsg "github.com/IMisbahk/mailsend/internal/mail"
	"github.com/IMisbahk/mailsend/internal/store"
	templatetool "github.com/IMisbahk/mailsend/internal/template"
	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
)

type options struct {
	account string
	json    bool
	debug   bool
}

var global options
var version = "dev"

func Execute() int {
	root := newRoot()
	err := root.Execute()
	if err == nil {
		return 0
	}
	var status *statusExit
	if errors.As(err, &status) {
		if status.err != nil {
			printCommandError(status.err)
		}
		if status.code <= 0 {
			return 1
		}
		return status.code
	}
	printCommandError(err)
	return 1
}

// statusExit is a command result whose process status is not 1.
// sent-check uses 3 when Sent matches and 2 when the check cannot be trusted.
type statusExit struct {
	code int
	err  error
}

func (s *statusExit) Error() string {
	if s == nil || s.err == nil {
		return ""
	}
	return s.err.Error()
}

func (s *statusExit) Unwrap() error { return s.err }
func newRoot() *cobra.Command {
	global = options{}
	root := &cobra.Command{Use: "mailsend", Short: "Send email from your terminal", Long: "mailsend is a local email client. Gmail accounts send through the Gmail API with OAuth. SMTP accounts send through the server configured for that account. Tokens and SMTP passwords stay in the operating system keychain, and send history stays in a local SQLite database.", SilenceUsage: true, SilenceErrors: true}
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if global.debug {
			auth.SetDebug(commandStderr)
		} else {
			auth.SetDebug(nil)
		}
		if global.account != "" {
			return auth.ValidateAccountName(global.account)
		}
		return nil
	}
	root.PersistentFlags().StringVar(&global.account, "account", "", "Named account (default: the current default account)")
	root.PersistentFlags().BoolVar(&global.json, "json", false, "Write machine-readable JSON output")
	root.PersistentFlags().BoolVar(&global.debug, "debug", false, "Write a step trace to stderr")
	root.AddCommand(loginCmd(), logoutCmd(), accountCmd(), sendCmd(), bulkCmd(), quotaCmd(), historyCmd(), templateCmd(), versionCmd(), queueCmd(), dispatchCmd(), sentCheckCmd())
	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print the mailsend build version", RunE: func(cmd *cobra.Command, args []string) error {
		return output(map[string]string{"version": version}, func() { fmt.Println(version) })
	}}
}
func output(value any, human func()) error {
	if global.json {
		e := json.NewEncoder(commandStdout)
		e.SetIndent("", "  ")
		return e.Encode(value)
	}
	if human != nil {
		human()
	}
	return nil
}
func selectedAccount() string {
	if global.account != "" {
		return global.account
	}
	if d := auth.DefaultAccount(); d != "" {
		return d
	}
	return "default"
}
func database() (*store.DB, error) {
	dir, e := auth.ConfigDir()
	if e != nil {
		return nil, e
	}
	db, e := store.Open(filepath.Join(dir, "mailsend.sqlite3"))
	return db, e
}
func loginCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "login", Short: "Authenticate a Gmail account with Google OAuth", Long: "Opens Google's OAuth authorization page and stores the resulting token in the OS keychain. The Gmail password is never requested or stored.", RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 6*time.Minute)
		defer cancel()
		name := selectedAccount()
		loginOutput := os.Stdout
		if global.json {
			loginOutput = os.Stderr
		}
		email, e := auth.Login(ctx, name, loginOutput)
		if e != nil {
			return e
		}
		return output(map[string]any{"status": "authenticated", "account": name, "email": email}, func() { fmt.Printf("Authenticated %s as %s\n", name, email) })
	}}
	return cmd
}
func logoutCmd() *cobra.Command {
	return &cobra.Command{Use: "logout", Short: "Remove an account token or SMTP password from the OS keychain", RunE: func(cmd *cobra.Command, args []string) error {
		name := selectedAccount()
		if e := auth.Logout(name); e != nil {
			return e
		}
		return output(map[string]any{"status": "logged_out", "account": name}, func() { fmt.Printf("Logged out %s\n", name) })
	}}
}
func historyCmd() *cobra.Command {
	return &cobra.Command{Use: "history [job-id]", Args: cobra.MaximumNArgs(1), Short: "Show recent send history or one job's status", RunE: func(cmd *cobra.Command, args []string) error {
		db, e := database()
		if e != nil {
			return e
		}
		defer db.Close()
		job := ""
		if len(args) > 0 {
			job = args[0]
		}
		rows, e := db.History(job, 200, global.account)
		if e != nil {
			return e
		}
		if job != "" {
			j, e := db.JobByID(job)
			if e != nil {
				return fmt.Errorf("job %q not found", job)
			}
			if global.account != "" && global.account != j.Account {
				return fmt.Errorf("job belongs to account %q", j.Account)
			}
			v := map[string]any{"job": j, "recipients": rows}
			return output(v, func() {
				fmt.Printf("Job %s | %s | %s\n", j.ID, j.Kind, j.Status)
				for _, r := range rows {
					printHistoryRow(r)
				}
			})
		}
		return output(map[string]any{"history": rows}, func() {
			fmt.Printf("%-25s %-36s %-9s %-24s %s\n", "DATE", "RECIPIENT", "STATUS", "MESSAGE ID", "SUBJECT")
			for _, r := range rows {
				printHistoryRow(r)
			}
		})
	}}
}
func printHistoryRow(r store.HistoryRow) {
	date := r.Date
	if t, e := time.Parse(time.RFC3339Nano, date); e == nil {
		date = t.Local().Format("2006-01-02 15:04:05")
	}
	fmt.Printf("%-25s %-36s %-9s %-24s %s\n", date, r.Recipient, r.Status, r.MessageID, truncate(r.Subject, 45))
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 4 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func sendCmd() *cobra.Command {
	var to, cc, bcc, attachments []string
	var subject, body, bodyFile, htmlFile, replyTo, fromName, schedule string
	var dry, yes bool
	cmd := &cobra.Command{Use: "send", Short: "Send one email", Long: "Send one email through the selected account. Gmail accounts use the authenticated address. SMTP accounts use the From address saved with the account. There is no per-message From override.", RunE: func(cmd *cobra.Command, args []string) error {
		if body != "" && bodyFile != "" {
			return fmt.Errorf("use only one of --body and --body-file")
		}
		text := body
		if bodyFile != "" {
			b, e := os.ReadFile(bodyFile)
			if e != nil {
				return fmt.Errorf("cannot read body file: %w", e)
			}
			text = string(b)
		}
		html := ""
		if htmlFile != "" {
			b, e := os.ReadFile(htmlFile)
			if e != nil {
				return fmt.Errorf("cannot read HTML file: %w", e)
			}
			html = string(b)
		}
		if subject == "" {
			return fmt.Errorf("--subject is required")
		}
		allTo, e := mailmsg.ParseAddresses(to)
		if e != nil {
			return e
		}
		if len(allTo) == 0 {
			return fmt.Errorf("--to is required")
		}
		allCC, e := mailmsg.ParseAddresses(cc)
		if e != nil {
			return e
		}
		allBCC, e := mailmsg.ParseAddresses(bcc)
		if e != nil {
			return e
		}
		name := selectedAccount()
		msg := mailmsg.Message{To: allTo, Cc: allCC, Bcc: allBCC, Subject: subject, Text: text, HTML: html, Attachments: attachments, ReplyTo: replyTo, FromName: fromName}
		if dry {
			built, e := composeSendMessage(name, msg)
			if e != nil {
				return e
			}
			return output(map[string]any{"status": "dry_run", "to": allTo, "cc": allCC, "bcc": allBCC, "subject": subject, "attachment_names": baseNames(attachments), "body": text, "html": html, "message_size_bytes": len(built.Raw)}, func() {
				fmt.Printf("To: %s\nCC: %s\nBCC: %s\nSubject: %s\nAttachments: %s\n\n%s\n", strings.Join(allTo, ", "), strings.Join(allCC, ", "), strings.Join(allBCC, ", "), subject, strings.Join(baseNames(attachments), ", "), text)
				if html != "" {
					fmt.Printf("\nHTML:\n%s\n", html)
				}
				fmt.Println("No emails were sent.")
			})
		}
		sender, e := openSender(cmd.Context(), name)
		if e != nil {
			return e
		}
		if e = applySenderIdentity(&msg, sender); e != nil {
			return e
		}
		built, e := msg.Build()
		if e != nil {
			return e
		}
		when := time.Now()
		if schedule != "" {
			t, e := parseSchedule(schedule)
			if e != nil {
				return e
			}
			if !t.After(time.Now()) {
				return fmt.Errorf("--schedule must be a future date and time")
			}
			when = t
		}
		if schedule == "" {
			return sendOne(cmd.Context(), sender, name, msg, "send")
		}
		db, e := database()
		if e != nil {
			return e
		}
		defer db.Close()
		id, row, e := createSendJob(db, name, "send", "pending", msg)
		if e != nil {
			return e
		}
		if when.After(time.Now()) {
			fmt.Fprintf(os.Stderr, "Scheduled for %s. Keep mailsend running until it sends.\n", when.Local().Format(time.RFC3339))
			timer := time.NewTimer(time.Until(when))
			select {
			case <-timer.C:
			case <-cmd.Context().Done():
				_ = db.UpdateJobStatus(id, "skipped", "interrupted before scheduled send")
				_ = db.FinishAttempt(0, row.ID, "skipped", "", "interrupted before send")
				return cmd.Context().Err()
			}
		}
		return sendSavedOne(cmd.Context(), db, sender, row, built, id)
	}}
	cmd.Flags().StringArrayVar(&to, "to", nil, "Recipient address (repeatable; comma-separated lists also work)")
	cmd.Flags().StringArrayVar(&cc, "cc", nil, "CC recipient (repeatable)")
	cmd.Flags().StringArrayVar(&bcc, "bcc", nil, "BCC recipient (repeatable; hidden from normal output)")
	cmd.Flags().StringVar(&subject, "subject", "", "Email subject")
	cmd.Flags().StringVar(&body, "body", "", "Plain-text body")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Read plain-text body from a file")
	cmd.Flags().StringVar(&htmlFile, "html-file", "", "Read HTML body from a file")
	cmd.Flags().StringArrayVar(&attachments, "attach", nil, "Attachment path (repeatable)")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "Reply-To address")
	cmd.Flags().StringVar(&fromName, "from-name", "", "Display name for the From address")
	cmd.Flags().StringVar(&schedule, "schedule", "", "Schedule as YYYY-MM-DD HH:MM local time or RFC3339; keeps this process open")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "Validate and preview without sending")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation when applicable")
	_ = yes
	return cmd
}
func sendSavedOne(ctx context.Context, db *store.DB, sender mailSender, r store.Recipient, built mailmsg.BuiltMessage, jobID string) error {
	aid, e := db.StartAttempt(r)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	msgid, ambiguous, e := sendSafe(ctx, sender, built)
	status := "sent"
	if e != nil {
		status = "failed"
		if ambiguous {
			status = "uncertain"
		}
	}
	if se := db.FinishAttempt(aid, r.ID, status, msgid, errorString(e)); se != nil {
		return se
	}
	_ = db.UpdateJobStatus(jobID, status, errorString(e))
	if e != nil {
		return fmt.Errorf("send job %s %s: %w", jobID, status, e)
	}
	return output(map[string]any{"status": "sent", "recipient": r.Email, "message_id": msgid, "job_id": jobID}, func() { fmt.Printf("Sent to %s (message ID %s)\nJob: %s\n", r.Email, msgid, jobID) })
}
func errorString(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}
func joinJSON(v []string) string { b, _ := json.Marshal(v); return string(b) }
func baseNames(paths []string) []string {
	a := make([]string, len(paths))
	for i, p := range paths {
		a[i] = filepath.Base(p)
	}
	return a
}
func parseSchedule(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04"} {
		if t, e := time.Parse(layout, s); e == nil {
			if layout == "2006-01-02 15:04" {
				if local, e := time.ParseInLocation(layout, s, time.Local); e == nil {
					return local, nil
				}
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid schedule %q; use YYYY-MM-DD HH:MM or RFC3339", s)
}

func templateCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "template", Short: "Manage local reusable email templates"}
	cmd.AddCommand(templateListCmd(), templateCreateCmd(), templateRenderCmd())
	return cmd
}
func templatesDir() (string, error) {
	d, e := auth.ConfigDir()
	if e != nil {
		return "", e
	}
	p := filepath.Join(d, "templates")
	if e = os.MkdirAll(p, 0700); e != nil {
		return "", e
	}
	return p, nil
}
func templateListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List saved templates", RunE: func(cmd *cobra.Command, args []string) error {
		dir, e := templatesDir()
		if e != nil {
			return e
		}
		files, e := filepath.Glob(filepath.Join(dir, "*.txt"))
		if e != nil {
			return e
		}
		names := make([]string, 0, len(files))
		for _, p := range files {
			names = append(names, strings.TrimSuffix(filepath.Base(p), ".txt"))
		}
		sort.Strings(names)
		return output(map[string]any{"templates": names}, func() {
			if len(names) == 0 {
				fmt.Println("No templates. Create one with mailsend template create.")
				return
			}
			for _, n := range names {
				fmt.Println(n)
			}
		})
	}}
}
func templateCreateCmd() *cobra.Command {
	var subject, bodyFile, htmlFile string
	cmd := &cobra.Command{Use: "create <name>", Args: cobra.ExactArgs(1), Short: "Save a plain-text template and optional HTML/subject files", RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if !safeTemplateName(name) {
			return fmt.Errorf("template name may contain letters, numbers, dash, underscore, and dot")
		}
		if bodyFile == "" {
			return fmt.Errorf("--body-file is required")
		}
		body, e := os.ReadFile(bodyFile)
		if e != nil {
			return fmt.Errorf("cannot read body file: %w", e)
		}
		dir, e := templatesDir()
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(dir, name+".txt"), body, 0600); e != nil {
			return e
		}
		if subject != "" {
			if e = os.WriteFile(filepath.Join(dir, name+".subject"), []byte(subject), 0600); e != nil {
				return e
			}
		}
		if htmlFile != "" {
			h, e := os.ReadFile(htmlFile)
			if e != nil {
				return e
			}
			if e = os.WriteFile(filepath.Join(dir, name+".html"), h, 0600); e != nil {
				return e
			}
		}
		return output(map[string]any{"status": "created", "name": name}, func() { fmt.Printf("Created template %s\n", name) })
	}}
	cmd.Flags().StringVar(&subject, "subject", "", "Reusable subject line (supports {{variables}})")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Plain-text template file")
	cmd.Flags().StringVar(&htmlFile, "html-file", "", "Optional HTML template file")
	return cmd
}
func templateRenderCmd() *cobra.Command {
	var vars []string
	cmd := &cobra.Command{Use: "render <name>", Args: cobra.ExactArgs(1), Short: "Render a saved template with variable values", RunE: func(cmd *cobra.Command, args []string) error {
		src, e := loadTemplate(args[0])
		if e != nil {
			return e
		}
		values, e := parseVars(vars)
		if e != nil {
			return e
		}
		body, e := templatetool.Render(src.body, values)
		if e != nil {
			return e
		}
		subject, e := templatetool.Render(src.subject, values)
		if e != nil {
			return e
		}
		html := ""
		if src.html != "" {
			html, e = templatetool.Render(src.html, values)
			if e != nil {
				return e
			}
		}
		v := map[string]any{"name": args[0], "subject": subject, "body": body, "html": html}
		return output(v, func() {
			fmt.Printf("Subject: %s\n\n%s\n", subject, body)
			if html != "" {
				fmt.Printf("\nHTML:\n%s\n", html)
			}
		})
	}}
	cmd.Flags().StringArrayVar(&vars, "var", nil, "Template variable as name=value (repeatable)")
	return cmd
}
func safeTemplateName(s string) bool {
	if s == "" || strings.Contains(s, "..") || strings.ContainsAny(s, "/\\") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
func parseVars(args []string) (map[string]string, error) {
	out := map[string]string{}
	for _, v := range args {
		i := strings.IndexByte(v, '=')
		if i <= 0 {
			return nil, fmt.Errorf("invalid --var %q; use key=value", v)
		}
		out[v[:i]] = v[i+1:]
	}
	return out, nil
}

type emailTemplate struct {
	path, subject, body, html string
	hash                      string
}

func loadTemplate(value string) (emailTemplate, error) {
	var p string
	if _, e := os.Stat(value); e == nil {
		p = value
	} else {
		dir, e := templatesDir()
		if e != nil {
			return emailTemplate{}, e
		}
		if !safeTemplateName(value) {
			return emailTemplate{}, fmt.Errorf("invalid template name or file path %q", value)
		}
		p = filepath.Join(dir, value+".txt")
	}
	body, e := os.ReadFile(p)
	if e != nil {
		return emailTemplate{}, fmt.Errorf("cannot read template %q: %w", p, e)
	}
	base := strings.TrimSuffix(p, ".txt")
	sub, _ := os.ReadFile(base + ".subject")
	html, _ := os.ReadFile(base + ".html")
	h := sha256.Sum256(append(append(append([]byte{}, body...), sub...), html...))
	return emailTemplate{path: p, subject: strings.TrimSpace(string(sub)), body: string(body), html: string(html), hash: hex.EncodeToString(h[:])}, nil
}

var _ = bufio.NewReader

type preparedRecipient struct {
	store.Recipient
	Send bool `json:"send"`
}

func bulkCmd() *cobra.Command {
	var csvPath, templateName, subject, resume string
	var cc, bcc, attachments []string
	var delay, minDelay, maxDelay, dailyLimit int
	var dedupe, dryRun, yes bool
	cmd := &cobra.Command{Use: "bulk", Short: "Send individually addressed emails to a CSV list", Long: "Renders one email per CSV row and sends each recipient separately. Bulk runs require confirmation unless --yes is used. Gmail limits still apply.", RunE: func(cmd *cobra.Command, args []string) error {
		if resume != "" {
			return resumeBulk(cmd, resume, yes, dryRun)
		}
		if csvPath == "" || templateName == "" {
			return fmt.Errorf("--csv and --template are required (or use --resume <job-id>)")
		}
		if delay < 0 || minDelay < 0 || maxDelay < 0 || dailyLimit < 0 {
			return fmt.Errorf("delay and daily limit values cannot be negative")
		}
		if (minDelay > 0 || maxDelay > 0) && (cmd.Flags().Changed("delay")) {
			return fmt.Errorf("use either --delay or --min-delay/--max-delay")
		}
		if (minDelay > 0 || maxDelay > 0) && (minDelay == 0 || maxDelay < minDelay) {
			return fmt.Errorf("set both --min-delay and --max-delay, with max >= min")
		}
		if minDelay == 0 && maxDelay == 0 && delay == 0 {
			delay = 1
		}
		rows, e := templatetool.ReadCSV(csvPath)
		if e != nil {
			return e
		}
		src, e := loadTemplate(templateName)
		if e != nil {
			return e
		}
		subjectSource := subject
		if subjectSource == "" {
			subjectSource = src.subject
		}
		if strings.TrimSpace(subjectSource) == "" {
			subjectSource = strings.TrimSuffix(filepath.Base(templateName), filepath.Ext(templateName))
			subjectSource = strings.ReplaceAll(strings.ReplaceAll(subjectSource, "-", " "), "_", " ")
			if subjectSource == "" {
				return fmt.Errorf("could not derive a subject; provide --subject")
			}
		}
		allCC, e := mailmsg.ParseAddresses(cc)
		if e != nil {
			return e
		}
		allBCC, e := mailmsg.ParseAddresses(bcc)
		if e != nil {
			return e
		}
		attInfo, e := inspectAttachments(attachments)
		if e != nil {
			return e
		}
		account := selectedAccount()
		fingerprintParts := []string{src.hash, subjectSource, account, joinJSON(allCC), joinJSON(allBCC)}
		for _, attachment := range attInfo {
			fingerprintParts = append(fingerprintParts, attachment.Path, attachment.SHA256)
		}
		fingerprint := hashStrings(fingerprintParts...)
		db, e := database()
		if e != nil {
			return e
		}
		defer db.Close()
		prepared := make([]preparedRecipient, 0, len(rows))
		var preview []map[string]any
		for _, row := range rows {
			rowSubject := subjectSource
			if subject == "" && src.subject == "" && strings.TrimSpace(row.Values["subject"]) != "" {
				rowSubject = row.Values["subject"]
			}
			renderedSubject, e := templatetool.Render(rowSubject, row.Values)
			if e != nil {
				return fmt.Errorf("CSV row %d: %w", row.Number, e)
			}
			body, e := templatetool.Render(src.body, row.Values)
			if e != nil {
				return fmt.Errorf("CSV row %d: %w", row.Number, e)
			}
			html := ""
			if src.html != "" {
				html, e = templatetool.Render(src.html, row.Values)
				if e != nil {
					return fmt.Errorf("CSV row %d: %w", row.Number, e)
				}
			}
			msg := mailmsg.Message{To: []string{row.Email}, Cc: allCC, Bcc: allBCC, Subject: renderedSubject, Text: body, HTML: html, Attachments: attachments}
			if _, e = msg.Build(); e != nil {
				return fmt.Errorf("CSV row %d (%s): %w", row.Number, row.Email, e)
			}
			status := "pending"
			send := true
			if dedupe {
				already, e := db.HasSentInCampaign(row.Email, fingerprint)
				if e != nil {
					return e
				}
				if already {
					status = "skipped"
					send = false
				}
			}
			varsJSON, _ := json.Marshal(row.Values)
			rec := store.Recipient{Email: row.Email, ToJSON: joinJSON([]string{row.Email}), CcJSON: joinJSON(allCC), BccJSON: joinJSON(allBCC), Subject: renderedSubject, Body: body, HTML: html, Status: status, Variables: row.Values, Attachments: attInfo}
			prepared = append(prepared, preparedRecipient{Recipient: rec, Send: send})
			preview = append(preview, map[string]any{"recipient": row.Email, "cc": allCC, "bcc": allBCC, "subject": renderedSubject, "variables": row.Values, "attachment_names": baseNames(attachments), "body": body, "html": html, "status": status, "send": send, "_vars": string(varsJSON)})
		}
		if dryRun {
			return output(map[string]any{"status": "dry_run", "count": len(prepared), "emails": stripInternal(preview), "message": "No emails were sent."}, func() {
				for i, p := range preview {
					fmt.Printf("--- Email %d (%s) ---\nTo: %s\nCC: %s\nBCC: %s\nSubject: %s\nVariables: %s\nAttachments: %s\n\n%s\n", i+1, p["recipient"], p["recipient"], strings.Join(allCC, ", "), strings.Join(allBCC, ", "), p["subject"], formatVariables(prepared[i].Variables), strings.Join(baseNames(attachments), ", "), p["body"])
					if p["html"] != "" {
						fmt.Printf("\nHTML:\n%s\n", p["html"])
					}
					if p["send"] == false {
						fmt.Println("Status: skipped (already sent in this campaign)")
					}
					fmt.Println()
				}
				fmt.Println("No emails were sent.")
			})
		}
		toSend := 0
		for _, p := range prepared {
			if p.Send {
				toSend++
			}
		}
		if toSend == 0 {
			return output(map[string]any{"status": "complete", "sent": 0, "skipped": len(prepared)}, func() { fmt.Println("No new recipients to send.") })
		}
		if e = checkDailyLimit(db, account, toSend, dailyLimit); e != nil {
			return e
		}
		if !yes {
			if global.json {
				return fmt.Errorf("bulk sends in JSON mode require --yes")
			}
			if !confirm(toSend) {
				return fmt.Errorf("bulk send cancelled")
			}
		}
		sender, e := openSender(cmd.Context(), account)
		if e != nil {
			return e
		}
		id, e := store.NewID()
		if e != nil {
			return e
		}
		stored := make([]store.Recipient, len(prepared))
		for i := range prepared {
			stored[i] = prepared[i].Recipient
		}
		effectiveDelay := delay
		if minDelay > 0 {
			effectiveDelay = minDelay
		}
		if e = db.CreateJob(store.Job{ID: id, Kind: "bulk", Status: "pending", Account: account, Subject: subjectSource, TemplatePath: src.path, TemplateHash: src.hash, DailyLimit: dailyLimit, DelaySeconds: effectiveDelay, MaxDelaySeconds: maxDelay}, fingerprint, stored); e != nil {
			return e
		}
		fmt.Fprintf(os.Stderr, "Starting bulk job %s (%d messages).\n", id, toSend)
		return processBulk(cmd.Context(), db, sender, id, yes, dailyLimit, delay, minDelay, maxDelay)
	}}
	cmd.Flags().StringVar(&csvPath, "csv", "", "CSV file with an email column and template variables")
	cmd.Flags().StringVar(&templateName, "template", "", "Template file path or saved template name")
	cmd.Flags().StringVar(&subject, "subject", "", "Subject template; defaults to saved template subject")
	cmd.Flags().StringArrayVar(&cc, "cc", nil, "CC recipient (repeatable)")
	cmd.Flags().StringArrayVar(&bcc, "bcc", nil, "BCC recipient (repeatable)")
	cmd.Flags().StringArrayVar(&attachments, "attach", nil, "Attachment path (repeatable)")
	cmd.Flags().IntVar(&delay, "delay", 1, "Fixed delay in seconds between recipients")
	cmd.Flags().IntVar(&minDelay, "min-delay", 0, "Randomized delay minimum in seconds (use with --max-delay)")
	cmd.Flags().IntVar(&maxDelay, "max-delay", 0, "Randomized delay maximum in seconds (use with --min-delay)")
	cmd.Flags().IntVar(&dailyLimit, "daily-limit", 0, "Local per-account daily sending cap; 0 disables the local cap")
	cmd.Flags().BoolVar(&dedupe, "dedupe", false, "Skip recipients already sent this same template campaign")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Render and display all emails without sending")
	cmd.Flags().BoolVar(&yes, "yes", false, "Send without an interactive confirmation")
	cmd.Flags().StringVar(&resume, "resume", "", "Resume a persisted bulk job by job ID")
	return cmd
}
func stripInternal(rows []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		c := map[string]any{}
		for k, v := range r {
			if !strings.HasPrefix(k, "_") {
				c[k] = v
			}
		}
		out = append(out, c)
	}
	return out
}
func formatVariables(v map[string]string) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	items := make([]string, 0, len(keys))
	for _, k := range keys {
		items = append(items, k+"="+v[k])
	}
	return strings.Join(items, ", ")
}
func hashStrings(values ...string) string {
	h := sha256.New()
	for _, v := range values {
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func inspectAttachments(paths []string) ([]store.Attachment, error) {
	out := make([]store.Attachment, 0, len(paths))
	for _, p := range paths {
		absolute, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("invalid attachment path %q: %w", p, err)
		}
		b, e := os.ReadFile(absolute)
		if e != nil {
			return nil, fmt.Errorf("cannot read attachment %q: %w", p, e)
		}
		h := sha256.Sum256(b)
		out = append(out, store.Attachment{Path: absolute, SHA256: hex.EncodeToString(h[:])})
	}
	return out, nil
}
func confirm(n int) bool {
	if st, e := os.Stdin.Stat(); e != nil || st.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintln(os.Stderr, "Confirmation requires an interactive terminal; pass --yes to continue.")
		return false
	}
	fmt.Printf("Send %d individually addressed emails? [y/N] ", n)
	line, _, e := bufio.NewReader(os.Stdin).ReadLine()
	if e != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(string(line)), "y") || strings.EqualFold(strings.TrimSpace(string(line)), "yes")
}
func resumeBulk(cmd *cobra.Command, id string, yes, dry bool) error {
	db, e := database()
	if e != nil {
		return e
	}
	defer db.Close()
	job, e := db.JobByID(id)
	if e != nil {
		return fmt.Errorf("job %q not found", id)
	}
	if job.Kind != "bulk" {
		return fmt.Errorf("job %q is not a bulk job", id)
	}
	if global.account != "" && global.account != job.Account {
		return fmt.Errorf("job belongs to account %q; select that account to resume", job.Account)
	}
	if dry {
		return fmt.Errorf("--dry-run cannot be combined with --resume; use mailsend history %s to inspect the persisted job", id)
	}
	if !auth.Authenticated(job.Account) {
		cfg, _ := auth.LookupProvider(job.Account)
		if cfg.Provider == auth.ProviderSMTP {
			return fmt.Errorf("account %q has no SMTP password in the OS keychain; run mailsend account add --provider smtp", job.Account)
		}
		return fmt.Errorf("account %q is not authenticated; run mailsend login --account %s", job.Account, job.Account)
	}
	sender, e := openSender(cmd.Context(), job.Account)
	if e != nil {
		return e
	}
	recipients, e := db.Recipients(id)
	if e != nil {
		return e
	}
	pending := 0
	for _, r := range recipients {
		if r.Status == "pending" || r.Status == "failed" {
			pending++
		}
	}
	if pending == 0 {
		minDelay, maxDelay := 0, 0
		if job.MaxDelaySeconds > 0 {
			minDelay, maxDelay = job.DelaySeconds, job.MaxDelaySeconds
		}
		return processBulk(cmd.Context(), db, sender, id, true, job.DailyLimit, job.DelaySeconds, minDelay, maxDelay)
	}
	if e = checkDailyLimit(db, job.Account, pending, job.DailyLimit); e != nil {
		return e
	}
	if !yes {
		if global.json {
			return fmt.Errorf("bulk resume in JSON mode requires --yes")
		}
		if !confirm(pending) {
			return fmt.Errorf("bulk resume cancelled")
		}
	}
	minDelay, maxDelay := 0, 0
	if job.MaxDelaySeconds > 0 {
		minDelay, maxDelay = job.DelaySeconds, job.MaxDelaySeconds
	}
	return processBulk(cmd.Context(), db, sender, id, yes, job.DailyLimit, job.DelaySeconds, minDelay, maxDelay)
}
func countStatus(rows []store.Recipient, status string) int {
	n := 0
	for _, r := range rows {
		if r.Status == status {
			n++
		}
	}
	return n
}
func processBulk(ctx context.Context, db *store.DB, sender mailSender, id string, yes bool, dailyLimit, delay, minDelay, maxDelay int) error {
	lock, e := acquireJobLock(id)
	if e != nil {
		return e
	}
	defer lock.Unlock()
	job, e := db.JobByID(id)
	if e != nil {
		return e
	}
	accountLock, e := acquireJobLock("account-" + hashStrings(job.Account)[:24])
	if e != nil {
		return e
	}
	defer accountLock.Unlock()
	if e = db.RecoverSending(id); e != nil {
		return e
	}
	if e = db.UpdateJobStatus(id, "sending", ""); e != nil {
		return e
	}
	rows, e := db.Recipients(id)
	if e != nil {
		return e
	}
	success, failed, uncertain := 0, 0, 0
	processedThisRun := 0
	for i, r := range rows {
		if r.Status != "pending" && r.Status != "failed" {
			continue
		}
		if dailyLimit > 0 {
			sent, e := db.DailySentCount(job.Account)
			if e != nil {
				return e
			}
			if sent >= dailyLimit {
				_ = db.UpdateJobStatus(id, "partial", "configured daily limit reached; pending recipients remain")
				return fmt.Errorf("daily limit %d reached; job %s has pending recipients", dailyLimit, id)
			}
		}
		if processedThisRun > 0 {
			seconds := delay
			if minDelay > 0 {
				seconds, e = randomDelay(minDelay, maxDelay)
				if e != nil {
					return e
				}
			}
			if seconds > 0 {
				timer := time.NewTimer(time.Duration(seconds) * time.Second)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					_ = db.UpdateJobStatus(id, "partial", "interrupted by user; resume with --resume")
					return ctx.Err()
				}
			}
		}
		to, e := store.DecodeStringList(r.ToJSON)
		if e != nil {
			return fmt.Errorf("job %s recipient %s has an unreadable address list: %w", id, r.Email, e)
		}
		cc, e := store.DecodeStringList(r.CcJSON)
		if e != nil {
			return fmt.Errorf("job %s recipient %s has an unreadable cc list: %w", id, r.Email, e)
		}
		bcc, e := store.DecodeStringList(r.BccJSON)
		if e != nil {
			return fmt.Errorf("job %s recipient %s has an unreadable bcc list: %w", id, r.Email, e)
		}
		aid, e := db.StartAttempt(r)
		if e != nil {
			return e
		}
		if e = verifyAttachments(r.Attachments); e != nil {
			failed++
			if se := db.FinishAttempt(aid, r.ID, "failed", "", e.Error()); se != nil {
				return se
			}
			processedThisRun++
			continue
		}
		msg := mailmsg.Message{To: to, Cc: cc, Bcc: bcc, Subject: r.Subject, Text: r.Body, HTML: r.HTML, Attachments: attachmentPaths(r.Attachments)}
		if e = applySenderIdentity(&msg, sender); e != nil {
			failed++
			if se := db.FinishAttempt(aid, r.ID, "failed", "", e.Error()); se != nil {
				return se
			}
			processedThisRun++
			continue
		}
		built, e := msg.Build()
		if e != nil {
			failed++
			if se := db.FinishAttempt(aid, r.ID, "failed", "", e.Error()); se != nil {
				return se
			}
			processedThisRun++
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		msgID, ambiguous, e := sendSafe(sendCtx, sender, built)
		cancel()
		status := "sent"
		if e != nil {
			status = "failed"
			if ambiguous {
				status = "uncertain"
				uncertain++
			} else {
				failed++
			}
		} else {
			success++
		}
		processedThisRun++
		if se := db.FinishAttempt(aid, r.ID, status, msgID, errorString(e)); se != nil {
			return se
		}
		if e != nil {
			fmt.Fprintf(os.Stderr, "[%d/%d] %s: %s\n", i+1, len(rows), r.Email, e)
		} else {
			fmt.Fprintf(os.Stderr, "[%d/%d] sent to %s (message %s)\n", i+1, len(rows), r.Email, msgID)
		}
		if e != nil && auth.IsQuotaError(e) {
			label := "Gmail quota"
			if sender.Provider() == auth.ProviderSMTP {
				label = "SMTP rate limit"
			}
			_ = db.UpdateJobStatus(id, "partial", label+" response; pending recipients remain")
			return fmt.Errorf("%s stopped job %s safely: %w", label, id, e)
		}
		if e != nil && ambiguous {
			_ = db.UpdateJobStatus(id, "partial", "send response was ambiguous; pending recipients remain")
			return fmt.Errorf("job %s stopped after an uncertain send; resume is disabled for that recipient to avoid duplication: %w", id, e)
		}
		if e != nil && ctx.Err() != nil {
			_ = db.UpdateJobStatus(id, "partial", "interrupted by user; pending recipients remain")
			return fmt.Errorf("job %s interrupted; resume pending recipients with --resume: %w", id, ctx.Err())
		}
		if e != nil && auth.IsAuthError(e) {
			reason := "Google authorization failed; reauthenticate before resuming"
			text := "Google authorization failed"
			if sender.Provider() == auth.ProviderSMTP {
				reason = "SMTP authentication failed; check the account password before resuming"
				text = "SMTP authentication failed"
			}
			_ = db.UpdateJobStatus(id, "partial", reason)
			return fmt.Errorf("job %s stopped because %s: %w", id, text, e)
		}
	}
	latest, e := db.Recipients(id)
	if e != nil {
		return e
	}
	finalStatus := "sent"
	for _, recipient := range latest {
		if recipient.Status == "failed" || recipient.Status == "uncertain" || recipient.Status == "pending" || recipient.Status == "sending" {
			finalStatus = "partial"
			break
		}
	}
	if e = db.UpdateJobStatus(id, finalStatus, ""); e != nil {
		return e
	}
	result := map[string]any{"job_id": id, "status": finalStatus, "sent_this_run": success, "failed_this_run": failed, "uncertain_this_run": uncertain, "total_recipients": len(rows)}
	return output(result, func() {
		fmt.Printf("Bulk job %s finished: %d sent, %d failed, %d uncertain.\n", id, success, failed, uncertain)
		if failed > 0 || uncertain > 0 {
			fmt.Printf("Resume safe failures with: mailsend bulk --resume %s\n", id)
		}
	})
}

func acquireJobLock(id string) (*flock.Flock, error) {
	if id == "" || len(id) > 64 || filepath.Base(id) != id || strings.Trim(id, "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-") != "" {
		return nil, fmt.Errorf("invalid job ID %q", id)
	}
	dir, err := auth.ConfigDir()
	if err != nil {
		return nil, err
	}
	locks := filepath.Join(dir, "locks")
	if err = os.MkdirAll(locks, 0700); err != nil {
		return nil, err
	}
	_ = os.Chmod(locks, 0700)
	lock := flock.New(filepath.Join(locks, id+".lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("cannot lock job %s: %w", id, err)
	}
	if !ok {
		return nil, fmt.Errorf("job %s is already running in another mailsend process", id)
	}
	return lock, nil
}

func sendSafe(ctx context.Context, sender mailSender, built mailmsg.BuiltMessage) (string, bool, error) {
	for attempt := 0; attempt < 4; attempt++ {
		id, ambiguous, err := sender.Send(ctx, built)
		if err == nil || ambiguous || !auth.Retryable(err) || attempt == 3 {
			return id, ambiguous, err
		}
		backoff := auth.RetryDelay(err, time.Duration(1<<attempt)*time.Second)
		if backoff > 30*time.Second {
			return "", false, err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return "", false, fmt.Errorf("send retry was interrupted after the server rejected the previous attempt: %w", ctx.Err())
		}
	}
	return "", false, fmt.Errorf("send retry loop ended unexpectedly")
}
func randomDelay(min, max int) (int, error) {
	if max <= min {
		return min, nil
	}
	n, e := randInt(max - min + 1)
	if e != nil {
		return 0, e
	}
	return min + n, nil
}
func randInt(max int) (int, error) {
	b := make([]byte, 8)
	if _, e := rand.Read(b); e != nil {
		return 0, e
	}
	var n uint64
	for _, x := range b {
		n = n<<8 | uint64(x)
	}
	return int(n % uint64(max)), nil
}
func attachmentPaths(a []store.Attachment) []string {
	out := make([]string, len(a))
	for i, v := range a {
		out[i] = v.Path
	}
	return out
}
func verifyAttachments(a []store.Attachment) error {
	for _, v := range a {
		b, e := os.ReadFile(v.Path)
		if e != nil {
			return fmt.Errorf("attachment %q unavailable during resume: %w", v.Path, e)
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != v.SHA256 {
			return fmt.Errorf("attachment %q changed since job creation; refusing to send a different file", v.Path)
		}
	}
	return nil
}

func checkDailyLimit(db *store.DB, account string, planned, limit int) error {
	if limit <= 0 {
		return nil
	}
	sent, err := db.DailySentCount(account)
	if err != nil {
		return err
	}
	if sent+planned > limit {
		return fmt.Errorf("daily limit %d would be exceeded: %d sent today and %d recipients remain", limit, sent, planned)
	}
	return nil
}
