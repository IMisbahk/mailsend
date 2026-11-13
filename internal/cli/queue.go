package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/example-account/mailsend/internal/auth"
	"github.com/example-account/mailsend/internal/dispatch"
	"github.com/example-account/mailsend/internal/store"
	"github.com/spf13/cobra"
)

func queueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue",
		Short: "Queue batches of individually addressed email",
		Long:  "Queue stores batches in the local SQLite database. mailsend dispatch sends them with one shared gap, hold files, and a daily cap. queue does not send mail by itself.",
	}
	cmd.AddCommand(queueAddCmd(), queueListCmd(), queueStatusCmd(), queueCancelCmd(), queueResolveCmd())
	return cmd
}

func queueAddCmd() *cobra.Command {
	var batchFile, startAt, label, timezoneName, hookCommand, afterSend, fromName, replyTo string
	var interval time.Duration
	var dailyCap int
	var holdPaths, holdSteps []string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a batch from a JSON or CSV file",
		Long:  "The file is a JSON object with an emails array, a JSON array of emails, or a CSV. A batch start time and interval space the messages. An email can set its own at time. Nothing is sent until mailsend dispatch runs.",
		Example: `  mailsend queue add --file india.json --account acme
  mailsend queue add --file wave.csv --start "2026-10-21 09:00" --interval 5m --timezone Asia/Kolkata`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(batchFile) == "" {
				return fmt.Errorf("--file is required")
			}
			holds, err := collectHolds(holdPaths, holdSteps)
			if err != nil {
				return err
			}
			defaults := dispatch.ParseDefaults{
				Label: label, Timezone: timezoneName, Hook: hookCommand, AfterSend: afterSend,
				FromName: fromName, ReplyTo: replyTo, Holds: holds,
			}
			if global.account != "" {
				defaults.Account = global.account
			}
			if cmd.Flags().Changed("start") {
				loc, err := dispatch.LoadLocation(timezoneName)
				if err != nil {
					return err
				}
				parsed, err := parseQueueTime(startAt, loc)
				if err != nil {
					return fmt.Errorf("--start: %w", err)
				}
				defaults.Start = parsed
				defaults.HasStart = true
			}
			if cmd.Flags().Changed("interval") {
				defaults.Interval = interval
				defaults.HasInterval = true
			}
			if cmd.Flags().Changed("daily-cap") {
				defaults.DailyCap = dailyCap
				defaults.HasDailyCap = true
			}
			batch, items, err := dispatch.ParseBatchFile(batchFile, defaults, queueTemplate)
			if err != nil {
				return err
			}
			if batch.Account == "" {
				batch.Account = selectedAccount()
			}
			if err = auth.ValidateAccountName(batch.Account); err != nil {
				return err
			}
			db, err := database()
			if err != nil {
				return err
			}
			defer db.Close()
			batchID, err := db.InsertQueueBatch(batch, items)
			if err != nil {
				return err
			}
			saved, err := db.QueueBatch(batchID)
			if err != nil {
				return err
			}
			return output(map[string]any{
				"status": "queued", "batch_id": saved.ID, "account": saved.Account, "label": saved.Label,
				"count": len(items), "interval_seconds": saved.IntervalSeconds, "start": saved.StartAt, "timezone": saved.Timezone,
			}, func() {
				fmt.Fprintf(commandStdout, "Queued batch %s (%d emails, every %s) for %s\n", saved.ID, len(items), time.Duration(saved.IntervalSeconds)*time.Second, saved.Account)
			})
		},
	}
	cmd.Flags().StringVar(&batchFile, "file", "", "JSON or CSV file of emails")
	cmd.Flags().StringVar(&startAt, "start", "", "First send time as RFC3339 or YYYY-MM-DD HH:MM (overrides the file)")
	cmd.Flags().DurationVar(&interval, "interval", 0, "Spacing between emails (overrides the file; default 5m when omitted)")
	cmd.Flags().StringVar(&label, "label", "", "Batch label, such as a region name")
	cmd.Flags().StringVar(&timezoneName, "timezone", "", "Timezone for times that have no zone, such as Asia/Kolkata")
	cmd.Flags().IntVar(&dailyCap, "daily-cap", 0, "Daily cap stored on the batch; 0 leaves the file value unchanged unless this flag is set")
	cmd.Flags().StringVar(&hookCommand, "hook", "", "Pre-send command stored on the batch")
	cmd.Flags().StringVar(&afterSend, "after-send", "", "Command stored on the batch and run after a confirmed send")
	cmd.Flags().StringVar(&fromName, "from-name", "", "Display name stored for every email in the batch")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "Reply-To stored for every email in the batch")
	cmd.Flags().StringArrayVar(&holdPaths, "hold", nil, "Hold file stored on the batch; while it exists the dispatcher waits")
	cmd.Flags().StringArrayVar(&holdSteps, "hold-step", nil, "Step-scoped hold as step=path or step1,step2=path")
	return cmd
}

func queueListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List queued batches",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := database()
			if err != nil {
				return err
			}
			defer db.Close()
			batches, err := db.ListQueueBatches(global.account)
			if err != nil {
				return err
			}
			return output(map[string]any{"batches": batches}, func() {
				if len(batches) == 0 {
					fmt.Fprintln(commandStdout, "No queued batches.")
					return
				}
				fmt.Fprintf(commandStdout, "%-28s %-12s %-16s %-16s %8s %8s %8s %8s\n", "BATCH", "ACCOUNT", "LABEL", "STATUS", "PENDING", "SENT", "BLOCKED", "PARKED")
				for _, batch := range batches {
					fmt.Fprintf(commandStdout, "%-28s %-12s %-16s %-16s %8d %8d %8d %8d\n", batch.ID, batch.Account, truncate(batch.Label, 16), batch.Status, batch.Counts["pending"], batch.Counts["sent"], batch.Counts["blocked"], batch.Counts["parked"])
				}
			})
		},
	}
}

func queueStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [batch-id]",
		Args:  cobra.MaximumNArgs(1),
		Short: "Show one batch and its emails, or every batch",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := database()
			if err != nil {
				return err
			}
			defer db.Close()
			if len(args) == 1 {
				batch, err := db.QueueBatch(args[0])
				if err != nil {
					return err
				}
				if global.account != "" && batch.Account != global.account {
					return fmt.Errorf("batch belongs to account %q", batch.Account)
				}
				items, err := db.ListQueueItems(batch.ID)
				if err != nil {
					return err
				}
				cursor, ok, err := db.DispatchCursor(batch.Account)
				if err != nil {
					return err
				}
				payload := map[string]any{"batch": batch, "items": items}
				if ok {
					payload["last_sent_at"] = cursor.Format(time.RFC3339Nano)
				}
				return output(payload, func() { printBatchStatus(batch, items) })
			}
			batches, err := db.ListQueueBatches(global.account)
			if err != nil {
				return err
			}
			return output(map[string]any{"batches": batches}, func() {
				if len(batches) == 0 {
					fmt.Fprintln(commandStdout, "No queued batches.")
					return
				}
				for _, batch := range batches {
					items, err := db.ListQueueItems(batch.ID)
					if err != nil {
						fmt.Fprintf(commandStderr, "Error: %v\n", err)
						continue
					}
					printBatchStatus(batch, items)
					fmt.Fprintln(commandStdout)
				}
			})
		},
	}
}

func queueCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <batch-id>",
		Args:  cobra.ExactArgs(1),
		Short: "Cancel pending emails in a batch",
		Long:  "Pending emails are cancelled. An email already marked sending is left for the dispatcher to reconcile so it is not sent twice.",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := database()
			if err != nil {
				return err
			}
			defer db.Close()
			batch, err := db.QueueBatch(args[0])
			if err != nil {
				return err
			}
			if global.account != "" && batch.Account != global.account {
				return fmt.Errorf("batch belongs to account %q", batch.Account)
			}
			cancelled, err := db.CancelQueueBatch(batch.ID, time.Now())
			if err != nil {
				return err
			}
			return output(map[string]any{"status": "cancelled", "batch_id": batch.ID, "cancelled_items": cancelled}, func() {
				fmt.Fprintf(commandStdout, "Cancelled batch %s (%d pending emails)\n", batch.ID, cancelled)
			})
		},
	}
}

func queueResolveCmd() *cobra.Command {
	var sent, retry, drop bool
	var messageID, reason string
	cmd := &cobra.Command{
		Use:   "resolve <item-id>",
		Args:  cobra.ExactArgs(1),
		Short: "Close or retry one parked, failed, or blocked email",
		Long:  "Use this after a person has checked the mailbox. --sent records a confirmed message id and does not send. --retry puts the email back on the queue. --drop cancels it.",
		Example: `  mailsend queue resolve 12 --sent --message-id 18c5
  mailsend queue resolve 12 --retry
  mailsend queue resolve 12 --drop --reason "checked Sent; do not retry"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			choices := 0
			if sent {
				choices++
			}
			if retry {
				choices++
			}
			if drop {
				choices++
			}
			if choices != 1 {
				return fmt.Errorf("choose one of --sent, --retry, or --drop")
			}
			if sent && strings.TrimSpace(messageID) == "" {
				return fmt.Errorf("--sent requires --message-id")
			}
			itemID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || itemID <= 0 {
				return fmt.Errorf("item id must be a positive number")
			}
			db, err := database()
			if err != nil {
				return err
			}
			defer db.Close()
			item, err := db.QueueItem(itemID)
			if err != nil {
				return err
			}
			batch, err := db.QueueBatch(item.BatchID)
			if err != nil {
				return err
			}
			if global.account != "" && batch.Account != global.account {
				return fmt.Errorf("item belongs to account %q", batch.Account)
			}
			if item.Status == "sending" {
				return fmt.Errorf("item %d is sending; leave it for mailsend dispatch to reconcile", item.ID)
			}
			now := time.Now()
			switch {
			case sent:
				if item.Status == "sent" {
					return fmt.Errorf("item %d is already sent", item.ID)
				}
				if err = db.RecordResolvedSend(item, batch.Account, messageID, reason, now); err != nil {
					return err
				}
				if err = db.SetDispatchCursor(batch.Account, now); err != nil {
					return err
				}
				if err = db.RefreshQueueBatches(batch.Account, now); err != nil {
					return err
				}
				return output(map[string]any{"status": "sent", "id": item.ID, "message_id": messageID}, func() {
					fmt.Fprintf(commandStdout, "Recorded item %d as sent (message %s)\n", item.ID, messageID)
				})
			case retry:
				if item.Status == "sent" {
					return fmt.Errorf("item %d was already sent", item.ID)
				}
				note := "released for another attempt"
				if strings.TrimSpace(reason) != "" {
					note = note + ": " + strings.TrimSpace(reason)
				}
				if err = db.SetQueueItemStatus(item.ID, "pending", note, now); err != nil {
					return err
				}
				if err = db.ActivateQueueBatch(item.BatchID, now); err != nil {
					return err
				}
				return output(map[string]any{"status": "pending", "id": item.ID, "action": "retry"}, func() {
					fmt.Fprintf(commandStdout, "Item %d is pending again\n", item.ID)
				})
			default:
				if item.Status == "sent" {
					return fmt.Errorf("item %d was already sent", item.ID)
				}
				note := strings.TrimSpace(reason)
				if note == "" {
					note = "dropped by operator"
				}
				if err = db.SetQueueItemStatus(item.ID, "cancelled", note, now); err != nil {
					return err
				}
				if err = db.RefreshQueueBatches(batch.Account, now); err != nil {
					return err
				}
				return output(map[string]any{"status": "cancelled", "id": item.ID, "action": "drop"}, func() {
					fmt.Fprintf(commandStdout, "Dropped item %d\n", item.ID)
				})
			}
		},
	}
	cmd.Flags().BoolVar(&sent, "sent", false, "Record the email as sent with --message-id and do not send it")
	cmd.Flags().BoolVar(&retry, "retry", false, "Put the email back on the queue")
	cmd.Flags().BoolVar(&drop, "drop", false, "Cancel the email")
	cmd.Flags().StringVar(&messageID, "message-id", "", "Provider message id for --sent")
	cmd.Flags().StringVar(&reason, "reason", "", "Note stored with the decision")
	return cmd
}

func dispatchCmd() *cobra.Command {
	var interval, poll time.Duration
	sentWithin := spanValue(7 * 24 * time.Hour)
	var dailyCap int
	var timezoneName, hookCommand, afterSend, sentCheck string
	var holdPaths, holdSteps []string
	var once, requireSentCheck, gapFromHistory bool
	var lockFile string
	cmd := &cobra.Command{
		Use:   "dispatch",
		Short: "Send queued email for one account",
		Long: `Dispatch is the only sender for an account's queues. It takes a lock so a second process cannot send, and it keeps one gap across every active batch.

A scheduled time that has already passed is sent as soon as the gap allows. Later emails keep that same spacing. They are not skipped and they are not sent in a burst.

Before the provider is called the email is stored as sending. It becomes sent only when the provider returns a message id. After a crash, an open sending row is checked against history and, when the token can read Sent, against Gmail Sent or --sent-check. If it cannot be confirmed, it is parked for mailsend queue resolve.

--require-sent-check looks in Gmail Sent for that recipient before sending. A match within --sent-within (default 7 days) blocks that email. A scope or search error stops the dispatcher without sending. Tokens from before gmail.readonly keep sending when this flag is off; re-run mailsend login to grant the read scope.

Hold files that exist pause the dispatcher. A daily cap counts every sent row this installation has for the account, including mail sent outside the queue, and waits for the next midnight in --timezone.

--lock-file holds an exclusive flock around each item's pre-send hook, send, and after-send hook, then releases it between items and while waiting. Another sender using that file waits its turn. --gap-from-history also spaces from the newest sent history row for the account.`,
		Example: `  mailsend --account acme dispatch --daily-cap 95 --timezone Asia/Kolkata --interval 5m \
    --hold /var/lib/outbound/SEND_HOLD \
    --hold-step fu-d3,fu-d7,ooo,fu=/var/lib/outbound/FOLLOWUP_HOLD \
    --hook ./scripts/mailsend-presend-hook --after-send ./scripts/mailsend-presend-hook`,
		RunE: func(cmd *cobra.Command, args []string) error {
			account := selectedAccount()
			holds, err := collectHolds(holdPaths, holdSteps)
			if err != nil {
				return err
			}
			db, err := database()
			if err != nil {
				return err
			}
			defer db.Close()
			if !cmd.Flags().Changed("daily-cap") {
				dailyCap, err = batchDailyCap(db, account)
				if err != nil {
					return err
				}
			}
			if timezoneName == "" {
				timezoneName, err = batchTimezone(db, account)
				if err != nil {
					return err
				}
			}
			loc, err := dispatch.LoadLocation(timezoneName)
			if err != nil {
				return err
			}
			sender, err := openSender(cmd.Context(), account)
			if err != nil {
				return err
			}
			dir, err := auth.ConfigDir()
			if err != nil {
				return err
			}
			lockPath, err := dispatch.LockPath(dir, account)
			if err != nil {
				return err
			}
			runner := &dispatch.Dispatcher{
				DB: db, Account: account, Interval: interval, DailyCap: dailyCap, Timezone: loc,
				Holds: holds, Hook: hookCommand, AfterSend: afterSend, Once: once, Poll: poll,
				Sender: sender, Probe: probeFor(sender, sentCheck), SentLister: sentListerFor(sender),
				RequireSentCheck: requireSentCheck, SentWithin: sentWithin.Duration(), LockPath: lockPath,
				ItemLockPath: lockFile, GapFromHistory: gapFromHistory,
				Logf: func(format string, args ...any) {
					fmt.Fprintf(commandStderr, format+"\n", args...)
				},
			}
			result, err := runner.Run(cmd.Context())
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			if errors.Is(err, context.Canceled) {
				result.Status = "stopped"
			}
			return output(result, func() {
				fmt.Fprintf(commandStdout, "Dispatch %s for %s: %d sent, %d blocked, %d failed, %d parked\n", result.Status, result.Account, result.SentThisRun, result.BlockedThisRun, result.FailedThisRun, len(result.Parked))
				if result.Detail != "" {
					fmt.Fprintln(commandStdout, result.Detail)
				}
			})
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 5*time.Minute, "Minimum gap between sends for this account; use 0s to keep only each batch interval")
	cmd.Flags().IntVar(&dailyCap, "daily-cap", 0, "Maximum confirmed sends for this account in one timezone day; 0 uses the tightest cap stored on an active batch, or no cap")
	cmd.Flags().StringVar(&timezoneName, "timezone", "", "Timezone for the daily cap (default: the first batch timezone, otherwise local)")
	cmd.Flags().StringArrayVar(&holdPaths, "hold", nil, "Pause every send while this file exists (repeatable)")
	cmd.Flags().StringArrayVar(&holdSteps, "hold-step", nil, "Pause matching steps while this file exists, as step=path or step1,step2=path")
	cmd.Flags().StringVar(&hookCommand, "hook", "", "Pre-send command used when the batch does not set one")
	cmd.Flags().StringVar(&afterSend, "after-send", "", "Command run after a confirmed send when the batch does not set one")
	cmd.Flags().StringVar(&sentCheck, "sent-check", "", "Command that confirms or rules out a crashed send; exit 2 means unverified")
	cmd.Flags().BoolVar(&once, "once", false, "Send at most one due email, then exit")
	cmd.Flags().DurationVar(&poll, "poll", 15*time.Second, "How often to recheck holds, the cap, and the clock while waiting")
	cmd.Flags().BoolVar(&requireSentCheck, "require-sent-check", false, "Block a recipient already present in Gmail Sent, and stop instead of sending when the check errors")
	cmd.Flags().Var(&sentWithin, "sent-within", "How far back --require-sent-check looks in Gmail Sent (default 7d)")
	cmd.Flags().StringVar(&lockFile, "lock-file", "", "Exclusive flock held around each item's pre-send hook, send, and after-send hook; released between items and while waiting")
	cmd.Flags().BoolVar(&gapFromHistory, "gap-from-history", false, "Also space from the newest sent history row for this account")
	return cmd
}

func queueTemplate(name string) (string, string, string, error) {
	src, err := loadTemplate(name)
	if err != nil {
		return "", "", "", err
	}
	return src.subject, src.body, src.html, nil
}

func collectHolds(paths, steps []string) ([]store.HoldFile, error) {
	holds := make([]store.HoldFile, 0, len(paths)+len(steps))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			return nil, fmt.Errorf("hold file path is empty")
		}
		holds = append(holds, store.HoldFile{Path: path})
	}
	for _, spec := range steps {
		hold, err := parseHoldStep(spec)
		if err != nil {
			return nil, err
		}
		holds = append(holds, hold)
	}
	return holds, nil
}

func parseHoldStep(spec string) (store.HoldFile, error) {
	split := strings.LastIndex(spec, "=")
	if split <= 0 || split == len(spec)-1 {
		return store.HoldFile{}, fmt.Errorf("--hold-step must be step=path or step1,step2=path")
	}
	rawSteps := strings.Split(spec[:split], ",")
	cleaned := make([]string, 0, len(rawSteps))
	for _, step := range rawSteps {
		step = strings.TrimSpace(step)
		if step == "" {
			return store.HoldFile{}, fmt.Errorf("--hold-step must be step=path or step1,step2=path")
		}
		cleaned = append(cleaned, step)
	}
	return store.HoldFile{Path: strings.TrimSpace(spec[split+1:]), Steps: cleaned}, nil
}

func parseQueueTime(value string, loc *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	if loc == nil {
		loc = time.Local
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if parsed, err := time.ParseInLocation(layout, value, loc); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q (use RFC3339 or YYYY-MM-DD HH:MM)", value)
}

func batchDailyCap(db *store.DB, account string) (int, error) {
	batches, err := db.ListQueueBatches(account)
	if err != nil {
		return 0, err
	}
	cap := 0
	for _, batch := range batches {
		if batch.Status != "active" && batch.Status != "needs_attention" {
			continue
		}
		if batch.DailyCap > 0 && (cap == 0 || batch.DailyCap < cap) {
			cap = batch.DailyCap
		}
	}
	return cap, nil
}

func batchTimezone(db *store.DB, account string) (string, error) {
	batches, err := db.ListQueueBatches(account)
	if err != nil {
		return "", err
	}
	for _, batch := range batches {
		if batch.Timezone != "" && (batch.Status == "active" || batch.Status == "needs_attention") {
			return batch.Timezone, nil
		}
	}
	return "", nil
}

func printBatchStatus(batch store.QueueBatch, items []store.QueueItem) {
	fmt.Fprintf(commandStdout, "Batch %s | %s | %s | %s | every %s\n", batch.ID, batch.Account, emptyLabel(batch.Label), batch.Status, time.Duration(batch.IntervalSeconds)*time.Second)
	for _, item := range items {
		when := item.ScheduledAt
		if parsed, err := store.ParseStamp(item.ScheduledAt); err == nil {
			when = parsed.Local().Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(commandStdout, "  %d  %-24s %-9s %-20s %s\n", item.ID, when, item.Status, item.Email, truncate(item.Subject, 40))
		if item.Error != "" && item.Status != "sent" {
			fmt.Fprintf(commandStdout, "      %s\n", item.Error)
		}
	}
}

func emptyLabel(label string) string {
	if label == "" {
		return "(no label)"
	}
	return label
}

type gmailProbe struct {
	client *auth.Client
}

func (g gmailProbe) ProbeSent(ctx context.Context, query dispatch.SentQuery) (dispatch.SentEvidence, error) {
	lookup, err := g.client.SearchSent(ctx, query.Email, query.Subject, query.AttemptedAt)
	return dispatch.SentEvidence{Found: lookup.Found, MessageID: lookup.MessageID, Verified: lookup.Verified, Detail: lookup.Detail}, err
}

type gmailSentLister struct {
	client *auth.Client
}

func (g gmailSentLister) ListSent(ctx context.Context, email string, since time.Time) ([]dispatch.SentHit, error) {
	listed, err := g.client.ListSent(ctx, email, "", since)
	if err != nil {
		return nil, err
	}
	hits := make([]dispatch.SentHit, 0, len(listed.Messages))
	for _, message := range listed.Messages {
		hits = append(hits, dispatch.SentHit{ID: message.ID, Date: message.Date, To: message.To, Subject: message.Subject})
	}
	return hits, nil
}

func sentListerFor(sender mailSender) dispatch.SentLister {
	gmail, ok := sender.(gmailSender)
	if !ok || gmail.client == nil {
		return nil
	}
	return gmailSentLister{client: gmail.client}
}

type chainProbe struct {
	probes []dispatch.SentProbe
}

func (c chainProbe) ProbeSent(ctx context.Context, query dispatch.SentQuery) (dispatch.SentEvidence, error) {
	var last dispatch.SentEvidence
	var lastErr error
	for _, probe := range c.probes {
		evidence, err := probe.ProbeSent(ctx, query)
		if err != nil && ctx.Err() != nil {
			return evidence, err
		}
		if evidence.Verified {
			return evidence, nil
		}
		last = evidence
		lastErr = err
	}
	if last.Detail == "" && lastErr != nil {
		last.Detail = lastErr.Error()
	}
	return last, nil
}

func probeFor(sender mailSender, command string) dispatch.SentProbe {
	var probes []dispatch.SentProbe
	if strings.TrimSpace(command) != "" {
		probes = append(probes, dispatch.CommandProbe{Command: strings.TrimSpace(command)})
	}
	if gmail, ok := sender.(gmailSender); ok && gmail.client != nil {
		probes = append(probes, gmailProbe{client: gmail.client})
	}
	switch len(probes) {
	case 0:
		return nil
	case 1:
		return probes[0]
	default:
		return chainProbe{probes: probes}
	}
}
