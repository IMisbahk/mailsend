# Command reference

This reference follows the Cobra command tree in `internal/cli` and the output of `go run ./cmd/mailsend --help`. The executable also provides authoritative local help for every command: `mailsend COMMAND --help`. Every command accepts global `--account NAME`, `--json`, and `--debug`; `-h/--help` is available throughout.

## Top-level commands

| Command | Purpose |
| --- | --- |
| `login` | OAuth login for a Gmail account; browser consent is interactive. |
| `logout` | Remove selected account's OS keyring credential and metadata. |
| `account` | Show the selected account. |
| `send` | Send or preview one message; `--schedule` keeps this process open until its time. |
| `bulk` | Render/send one message per CSV row; requires confirmation unless `--yes`. |
| `quota` | Explain provider sending quota information. |
| `history [job-id]` | Show recent send history or one job and recipients. |
| `template` | Manage reusable local templates. |
| `queue` | Add/inspect/cancel/resolve SQLite queue batches. Does not send. |
| `dispatch` | Send queued messages for the selected account. |
| `sent-check` | Search Gmail Sent or SMTP/IMAP Sent for one recipient. |
| `inbox` | List mail in configured SMTP/IMAP account. |
| `read <uid>` | Show one IMAP message. |
| `version` | Print build version. |
| `completion SHELL` | Cobra-generated shell completion script (built-in). |
| `help [command]` | Show command help (built-in). |

## Account commands

```text
mailsend account [--account NAME] [--json]
mailsend account add <name> --provider smtp --host HOST --username USER --from ADDRESS [flags]
mailsend account set-imap <name> [flags]
mailsend account test [name] [--send-to ADDRESS]
```

`account add` flags: `--provider` (smtp), `--host`, `--port` (defaults 587/465/25 by TLS mode), `--tls` (`starttls|ssl|none`, default `starttls`), `--username`, `--from`, `--reply-to`, `--password-stdin`, plus `--imap-host`, `--imap-port`, `--imap-tls`, `--imap-username`. Password can otherwise come from `MAILSEND_SMTP_PASSWORD` or an interactive terminal prompt.

`set-imap` supports `--imap-host`, `--imap-port`, `--imap-tls`, `--imap-username`; it reuses the SMTP keyring password. `account test --send-to` sends one actual test message; without it the command authenticates only.

## Send

```text
mailsend send --to ADDRESS --subject TEXT [--body TEXT | --body-file PATH] [flags]
```

Flags: repeatable/comma-separated `--to`, `--cc`, `--bcc`; `--subject`; `--body` or `--body-file`; `--html-file`; repeatable `--attach`; `--from-name`; `--reply-to`; `--dry-run`; `--schedule` (`YYYY-MM-DD HH:MM` local time or RFC3339); `--yes` to skip applicable confirmation. There is no arbitrary From-address flag. A dry-run validates and previews locally without sending. A scheduled send holds the process open until the scheduled time.

## Bulk

```text
mailsend bulk --csv PATH --template NAME_OR_PATH [flags]
mailsend bulk --resume JOB_ID [--yes]
```

Flags: `--csv`, `--template`, `--subject`, repeatable `--cc`, `--bcc`, and `--attach`; `--delay` seconds (default 1); `--min-delay` and `--max-delay` for randomized delay; `--daily-limit` local cap; `--dedupe`; `--dry-run`; `--resume`; `--yes`. CSV requires an `email` header. Each row gets a separate message; fields interpolate as `{{field}}`. `--delay` cannot be combined with randomized delay flags; both randomized bounds are required. Resume never resends successful recipients, and uncertain rows require human review.

## Templates

```text
mailsend template list
mailsend template create <name> --body-file PATH [--subject TEXT] [--html-file PATH]
mailsend template render <name> [--var key=value ...]
```

Templates live under the config directory's `templates/`; body is required and subject/HTML are optional. Variables use `{{name}}` syntax. `--var` is repeatable.

## Queue and dispatch

```text
mailsend queue add --file PATH [flags]
mailsend queue list
mailsend queue status [batch-id]
mailsend queue cancel <batch-id>
mailsend queue resolve <item-id> (--sent --message-id ID | --retry | --drop) [--reason TEXT]
mailsend dispatch [flags]
```

`queue add` flags: `--file`, `--start` (RFC3339 or `YYYY-MM-DD HH:MM`), `--interval` (default file value or 5m), `--label`, `--timezone`, `--daily-cap`, `--hook`, `--after-send`, `--from-name`, `--reply-to`, repeatable `--hold`, repeatable `--hold-step` (`step=path` or `step1,step2=path`). Queue input supports JSON object with `emails`, JSON array, or CSV. It never sends.

`dispatch` flags: `--interval` (default 5m), `--daily-cap`, `--timezone`, repeatable `--hold`, repeatable `--hold-step`, `--hook`, `--after-send`, `--sent-check`, `--once`, `--poll` (default 15s), `--require-sent-check`, `--sent-within` (default 7d), `--lock-file`, and `--gap-from-history`. Dispatch is a real sender; `--once` still sends one due message at most. Refer to [dispatch service](dispatch-service.md) before running persistently.

## Inbox and Sent checks

```text
mailsend inbox [--folder INBOX] [--limit 20] [--unread] [--since 7d] [--from TEXT]
mailsend read <uid> [--folder INBOX] [--mark-read]
mailsend sent-check --to ADDRESS [--since 30d] [--subject TEXT]
```

`inbox --since 0s` removes the time bound; `--limit` is 1–1000. `read` marks seen only with `--mark-read`. `sent-check` exits 0 for no match, 3 for a match, and 2 when the search cannot be trusted. Inbox/read require SMTP with IMAP; Sent search supports Gmail or SMTP with IMAP.

For complete live flag descriptions, run `mailsend COMMAND --help` or `mailsend account add --help`, `mailsend queue add --help`, and `mailsend dispatch --help`.
