package dispatch

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	mailmsg "github.com/example-account/mailsend/internal/mail"
	"github.com/example-account/mailsend/internal/store"
	templatetool "github.com/example-account/mailsend/internal/template"

	_ "time/tzdata"
)

// ParseDefaults are the flag values supplied with queue add.
// A set field replaces the same field in the batch file.
type ParseDefaults struct {
	Account     string
	Label       string
	Start       time.Time
	HasStart    bool
	Interval    time.Duration
	HasInterval bool
	Timezone    string
	DailyCap    int
	HasDailyCap bool
	Hook        string
	AfterSend   string
	FromName    string
	ReplyTo     string
	Holds       []store.HoldFile
}

// TemplateFunc loads a saved template or a template file.
type TemplateFunc func(name string) (subject, body, html string, err error)

type batchDocument struct {
	Label     string           `json:"label"`
	Account   string           `json:"account"`
	Start     string           `json:"start"`
	Interval  json.RawMessage  `json:"interval"`
	Timezone  string           `json:"timezone"`
	DailyCap  int              `json:"daily_cap"`
	Hook      string           `json:"hook"`
	AfterSend string           `json:"after_send"`
	FromName  string           `json:"from_name"`
	ReplyTo   string           `json:"reply_to"`
	Holds     []store.HoldFile `json:"holds"`
	Emails    []emailDocument  `json:"emails"`
}

type emailDocument struct {
	To       string            `json:"to"`
	Email    string            `json:"email"`
	Cc       json.RawMessage   `json:"cc"`
	Bcc      json.RawMessage   `json:"bcc"`
	Subject  string            `json:"subject"`
	Body     string            `json:"body"`
	HTML     string            `json:"html"`
	BodyFile string            `json:"body_file"`
	HTMLFile string            `json:"html_file"`
	Template string            `json:"template"`
	Vars     map[string]string `json:"vars"`
	Step     string            `json:"step"`
	Tag      string            `json:"tag"`
	At       string            `json:"at"`
}

// ParseBatchFile reads a JSON or CSV batch. JSON may be an object with an
// emails array or an array of emails. Times without a zone use the batch timezone.
func ParseBatchFile(path string, defaults ParseDefaults, templates TemplateFunc) (store.QueueBatch, []store.QueueItem, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return store.QueueBatch{}, nil, fmt.Errorf("cannot read batch file: %w", err)
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".csv" || looksLikeCSV(body, ext) {
		return parseCSVBatch(path, body, defaults, templates)
	}
	return parseJSONBatch(path, body, defaults, templates)
}

// LoadLocation resolves a timezone name. Empty and "local" use the machine zone.
func LoadLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "local") {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q", name)
	}
	return loc, nil
}

func looksLikeCSV(body []byte, ext string) bool {
	if ext == ".json" {
		return false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return false
	}
	return trimmed[0] != '{' && trimmed[0] != '['
}

func parseJSONBatch(path string, body []byte, defaults ParseDefaults, templates TemplateFunc) (store.QueueBatch, []store.QueueItem, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return store.QueueBatch{}, nil, fmt.Errorf("batch file is empty")
	}
	var doc batchDocument
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &doc.Emails); err != nil {
			return store.QueueBatch{}, nil, fmt.Errorf("invalid batch JSON: %w", err)
		}
	} else if err := json.Unmarshal(trimmed, &doc); err != nil {
		return store.QueueBatch{}, nil, fmt.Errorf("invalid batch JSON: %w", err)
	}
	baseDir := filepath.Dir(path)
	doc.Hook = resolveCommand(baseDir, doc.Hook)
	doc.AfterSend = resolveCommand(baseDir, doc.AfterSend)
	for i := range doc.Holds {
		doc.Holds[i].Path = resolvePath(baseDir, doc.Holds[i].Path)
	}
	return finishBatch(baseDir, doc, defaults, templates)
}

func parseCSVBatch(path string, body []byte, defaults ParseDefaults, templates TemplateFunc) (store.QueueBatch, []store.QueueItem, error) {
	reader := csv.NewReader(bytes.NewReader(body))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return store.QueueBatch{}, nil, fmt.Errorf("invalid CSV header: %w", err)
	}
	index := map[string]int{}
	for i, column := range header {
		column = strings.ToLower(strings.TrimSpace(column))
		if column == "" {
			return store.QueueBatch{}, nil, fmt.Errorf("CSV header column %d is empty", i+1)
		}
		if _, ok := index[column]; ok {
			return store.QueueBatch{}, nil, fmt.Errorf("duplicate CSV column %q", column)
		}
		index[column] = i
	}
	if _, ok := index["to"]; !ok {
		if _, ok = index["email"]; !ok {
			return store.QueueBatch{}, nil, fmt.Errorf("CSV must include a to or email column")
		}
	}
	var emails []emailDocument
	line := 1
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return store.QueueBatch{}, nil, fmt.Errorf("CSV row %d: %w", line, err)
		}
		if strings.TrimSpace(strings.Join(record, "")) == "" {
			continue
		}
		field := func(name string) string {
			pos, ok := index[name]
			if !ok || pos >= len(record) {
				return ""
			}
			return strings.TrimSpace(record[pos])
		}
		vars := map[string]string{}
		for name, pos := range index {
			if knownCSVColumn(name) || pos >= len(record) {
				continue
			}
			vars[name] = record[pos]
		}
		emails = append(emails, emailDocument{
			To: field("to"), Email: field("email"), Subject: field("subject"), Body: field("body"), HTML: field("html"),
			BodyFile: field("body_file"), HTMLFile: field("html_file"), Template: field("template"), Vars: vars,
			Step: field("step"), Tag: field("tag"), At: field("at"),
			Cc: rawString(field("cc")), Bcc: rawString(field("bcc")),
		})
	}
	return finishBatch(filepath.Dir(path), batchDocument{Emails: emails}, defaults, templates)
}

func knownCSVColumn(name string) bool {
	switch name {
	case "to", "email", "cc", "bcc", "subject", "body", "html", "body_file", "html_file", "template", "step", "tag", "at":
		return true
	default:
		return false
	}
}

func rawString(value string) json.RawMessage {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

func finishBatch(baseDir string, doc batchDocument, defaults ParseDefaults, templates TemplateFunc) (store.QueueBatch, []store.QueueItem, error) {
	batch := store.QueueBatch{
		Account: strings.TrimSpace(doc.Account), Label: strings.TrimSpace(doc.Label), Timezone: strings.TrimSpace(doc.Timezone),
		DailyCap: doc.DailyCap, HookCommand: strings.TrimSpace(doc.Hook), AfterCommand: strings.TrimSpace(doc.AfterSend),
		FromName: strings.TrimSpace(doc.FromName), ReplyTo: strings.TrimSpace(doc.ReplyTo), Holds: doc.Holds, Status: "active",
	}
	if strings.TrimSpace(defaults.Account) != "" {
		batch.Account = strings.TrimSpace(defaults.Account)
	}
	if defaults.Label != "" {
		batch.Label = defaults.Label
	}
	if defaults.Timezone != "" {
		batch.Timezone = defaults.Timezone
	}
	if defaults.HasDailyCap {
		batch.DailyCap = defaults.DailyCap
	}
	if defaults.Hook != "" {
		batch.HookCommand = defaults.Hook
	}
	if defaults.AfterSend != "" {
		batch.AfterCommand = defaults.AfterSend
	}
	if defaults.FromName != "" {
		batch.FromName = defaults.FromName
	}
	if defaults.ReplyTo != "" {
		batch.ReplyTo = defaults.ReplyTo
	}
	if len(defaults.Holds) > 0 {
		batch.Holds = append(append([]store.HoldFile{}, batch.Holds...), defaults.Holds...)
	}
	if batch.DailyCap < 0 {
		return store.QueueBatch{}, nil, fmt.Errorf("daily cap cannot be negative")
	}
	loc, err := LoadLocation(batch.Timezone)
	if err != nil {
		return store.QueueBatch{}, nil, err
	}
	interval := 5 * time.Minute
	if len(doc.Interval) > 0 && string(doc.Interval) != "null" {
		parsed, ok, err := parseIntervalValue(doc.Interval)
		if err != nil {
			return store.QueueBatch{}, nil, err
		}
		if ok {
			interval = parsed
		}
	}
	if defaults.HasInterval {
		interval = defaults.Interval
	}
	if interval < 0 {
		return store.QueueBatch{}, nil, fmt.Errorf("interval cannot be negative")
	}
	if time.Duration(int(interval/time.Second))*time.Second != interval {
		return store.QueueBatch{}, nil, fmt.Errorf("interval %s is finer than one second", interval)
	}
	batch.IntervalSeconds = int(interval / time.Second)
	var start time.Time
	var hasStart bool
	if strings.TrimSpace(doc.Start) != "" {
		start, err = parseWhen(doc.Start, loc)
		if err != nil {
			return store.QueueBatch{}, nil, fmt.Errorf("batch start: %w", err)
		}
		hasStart = true
	}
	if defaults.HasStart {
		start = defaults.Start
		hasStart = true
	}
	for _, hold := range batch.Holds {
		if strings.TrimSpace(hold.Path) == "" {
			return store.QueueBatch{}, nil, fmt.Errorf("hold file path is empty")
		}
	}
	if len(doc.Emails) == 0 {
		return store.QueueBatch{}, nil, fmt.Errorf("batch has no emails")
	}
	items := make([]store.QueueItem, 0, len(doc.Emails))
	var first time.Time
	for i, email := range doc.Emails {
		item, scheduled, err := buildItem(baseDir, email, i, start, hasStart, interval, loc, templates)
		if err != nil {
			return store.QueueBatch{}, nil, fmt.Errorf("email %d: %w", i+1, err)
		}
		item.ScheduledAt = store.FormatStamp(scheduled)
		items = append(items, item)
		if first.IsZero() || scheduled.Before(first) {
			first = scheduled
		}
	}
	if hasStart {
		batch.StartAt = store.FormatStamp(start)
	} else {
		batch.StartAt = store.FormatStamp(first)
	}
	return batch, items, nil
}

func buildItem(baseDir string, email emailDocument, position int, start time.Time, hasStart bool, interval time.Duration, loc *time.Location, templates TemplateFunc) (store.QueueItem, time.Time, error) {
	address := strings.TrimSpace(email.To)
	if address == "" {
		address = strings.TrimSpace(email.Email)
	}
	if email.To != "" && email.Email != "" && !strings.EqualFold(strings.TrimSpace(email.To), strings.TrimSpace(email.Email)) {
		return store.QueueItem{}, time.Time{}, fmt.Errorf("to and email disagree")
	}
	parsedTo, err := mailmsg.ParseAddresses([]string{address})
	if err != nil {
		return store.QueueItem{}, time.Time{}, err
	}
	if len(parsedTo) != 1 {
		return store.QueueItem{}, time.Time{}, fmt.Errorf("exactly one recipient is required")
	}
	cc, err := parseAddressField(email.Cc)
	if err != nil {
		return store.QueueItem{}, time.Time{}, fmt.Errorf("cc: %w", err)
	}
	bcc, err := parseAddressField(email.Bcc)
	if err != nil {
		return store.QueueItem{}, time.Time{}, fmt.Errorf("bcc: %w", err)
	}
	subject := email.Subject
	body := email.Body
	html := email.HTML
	if email.BodyFile != "" {
		fileBody, err := os.ReadFile(resolvePath(baseDir, email.BodyFile))
		if err != nil {
			return store.QueueItem{}, time.Time{}, fmt.Errorf("cannot read body file: %w", err)
		}
		body = string(fileBody)
	}
	if email.HTMLFile != "" {
		fileHTML, err := os.ReadFile(resolvePath(baseDir, email.HTMLFile))
		if err != nil {
			return store.QueueItem{}, time.Time{}, fmt.Errorf("cannot read html file: %w", err)
		}
		html = string(fileHTML)
	}
	if email.Template != "" {
		if templates == nil {
			return store.QueueItem{}, time.Time{}, fmt.Errorf("template %q set but no template loader is configured", email.Template)
		}
		templateSubject, templateBody, templateHTML, err := templates(email.Template)
		if err != nil {
			return store.QueueItem{}, time.Time{}, err
		}
		if strings.TrimSpace(subject) == "" {
			subject = templateSubject
		}
		if body == "" {
			body = templateBody
		}
		if html == "" {
			html = templateHTML
		}
	}
	vars := email.Vars
	if vars == nil {
		vars = map[string]string{}
	}
	subject, err = templatetool.Render(subject, vars)
	if err != nil {
		return store.QueueItem{}, time.Time{}, err
	}
	body, err = templatetool.Render(body, vars)
	if err != nil {
		return store.QueueItem{}, time.Time{}, err
	}
	html, err = templatetool.Render(html, vars)
	if err != nil {
		return store.QueueItem{}, time.Time{}, err
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return store.QueueItem{}, time.Time{}, fmt.Errorf("subject is required")
	}
	if strings.TrimSpace(body) == "" && strings.TrimSpace(html) == "" {
		return store.QueueItem{}, time.Time{}, fmt.Errorf("body is required")
	}
	var scheduled time.Time
	if strings.TrimSpace(email.At) != "" {
		scheduled, err = parseWhen(email.At, loc)
		if err != nil {
			return store.QueueItem{}, time.Time{}, err
		}
	} else {
		if !hasStart {
			return store.QueueItem{}, time.Time{}, fmt.Errorf("set a batch start time or an at time")
		}
		scheduled = start.Add(time.Duration(position) * interval)
	}
	return store.QueueItem{
		Position: position,
		Email:    parsedTo[0],
		Cc:       cc,
		Bcc:      bcc,
		Subject:  subject,
		Body:     body,
		HTML:     html,
		Step:     strings.TrimSpace(email.Step),
		Tag:      strings.TrimSpace(email.Tag),
	}, scheduled, nil
}

func parseAddressField(raw json.RawMessage) ([]string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return mailmsg.ParseAddresses(many)
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		if strings.TrimSpace(one) == "" {
			return nil, nil
		}
		return mailmsg.ParseAddresses([]string{one})
	}
	return nil, fmt.Errorf("must be a string or an array of strings")
}

func parseIntervalValue(raw json.RawMessage) (time.Duration, bool, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		parsed, err := time.ParseDuration(strings.TrimSpace(text))
		if err != nil {
			return 0, false, fmt.Errorf("invalid interval %q (use a Go duration such as 5m)", text)
		}
		return parsed, true, nil
	}
	var seconds float64
	if err := json.Unmarshal(raw, &seconds); err == nil {
		return time.Duration(seconds * float64(time.Second)), true, nil
	}
	return 0, false, fmt.Errorf("invalid interval")
}

func parseWhen(value string, loc *time.Location) (time.Time, error) {
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

func resolvePath(baseDir, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(baseDir, value)
}

func resolveCommand(baseDir, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) || strings.ContainsAny(value, " \t") {
		return value
	}
	candidate := filepath.Join(baseDir, value)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return value
}
