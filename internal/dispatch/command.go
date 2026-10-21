package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const hookTimeout = 180 * time.Second

// HookPayload is the JSON object written to a pre-send or after-send command.
type HookPayload struct {
	Account     string `json:"account"`
	BatchID     string `json:"batch_id"`
	Label       string `json:"label"`
	Email       string `json:"email"`
	Subject     string `json:"subject"`
	Step        string `json:"step"`
	Tag         string `json:"tag"`
	ScheduledAt string `json:"scheduled_at"`
	Position    int    `json:"position"`
	MessageID   string `json:"message_id,omitempty"`
	Status      string `json:"status,omitempty"`
}

// HookRunner runs one external command. A non-zero exit code blocks a pre-send hook.
type HookRunner func(ctx context.Context, command string, payload HookPayload) (exitCode int, reason string, err error)

// SentQuery asks whether a message was accepted.
type SentQuery struct {
	Account      string    `json:"account"`
	Email        string    `json:"email"`
	Subject      string    `json:"subject"`
	AttemptedAt  time.Time `json:"attempted_at"`
	HistoryJobID string    `json:"history_job_id,omitempty"`
}

// SentEvidence is the result of a sent-mail check.
// Verified false means the check could not decide. The item is parked, not retried.
type SentEvidence struct {
	Found     bool   `json:"found"`
	MessageID string `json:"message_id,omitempty"`
	Verified  bool   `json:"-"`
	Detail    string `json:"detail,omitempty"`
}

// SentProbe looks for a sent message. Tests and --sent-check supply implementations.
type SentProbe interface {
	ProbeSent(ctx context.Context, query SentQuery) (SentEvidence, error)
}

// SentHit is one message already in the provider's Sent mailbox.
type SentHit struct {
	ID      string
	Date    time.Time
	To      string
	Subject string
}

// SentLister lists Sent messages to one recipient since a time.
// An error means the check could not decide, including a token that cannot read Sent.
type SentLister interface {
	ListSent(ctx context.Context, email string, since time.Time) ([]SentHit, error)
}

// RunCommandHook executes command with the payload on stdin.
// Exit code 0 allows the send. Any other exit code is returned to the caller.
func RunCommandHook(ctx context.Context, command string, payload HookPayload) (int, string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return 0, "", nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, "", err
	}
	stdout, stderr, exitCode, err := runJSONCommand(ctx, command, body)
	reason := firstText(stderr, stdout)
	if err != nil {
		return exitCode, reason, err
	}
	return exitCode, reason, nil
}

// CommandProbe runs an external sent-mail check.
// Exit 0 and a JSON object means the answer is verified.
// Exit 2, or any other failure, is unverified.
type CommandProbe struct {
	Command string
}

func (p CommandProbe) ProbeSent(ctx context.Context, query SentQuery) (SentEvidence, error) {
	body, err := json.Marshal(query)
	if err != nil {
		return SentEvidence{}, err
	}
	stdout, stderr, exitCode, err := runJSONCommand(ctx, p.Command, body)
	if err != nil {
		detail := firstText(stderr, stdout)
		if detail == "" {
			detail = err.Error()
		}
		return SentEvidence{Verified: false, Detail: detail}, err
	}
	if exitCode != 0 {
		detail := firstText(stderr, stdout)
		if detail == "" {
			detail = fmt.Sprintf("sent-check exited %d", exitCode)
		}
		return SentEvidence{Verified: false, Detail: detail}, nil
	}
	var evidence SentEvidence
	if err = json.Unmarshal(stdout, &evidence); err != nil {
		return SentEvidence{Verified: false, Detail: "sent-check returned invalid JSON"}, nil
	}
	evidence.Verified = true
	return evidence, nil
}

func runJSONCommand(ctx context.Context, command string, body []byte) ([]byte, []byte, int, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, nil, 0, fmt.Errorf("command is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command)
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return stdout.Bytes(), stderr.Bytes(), 0, fmt.Errorf("command timed out after %s", hookTimeout)
	}
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
	}
	return stdout.Bytes(), stderr.Bytes(), 0, err
}

func firstText(parts ...[]byte) string {
	for _, part := range parts {
		text := strings.TrimSpace(string(part))
		if text != "" {
			return truncateText(text, 400)
		}
	}
	return ""
}

func truncateText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	if limit < 4 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}
