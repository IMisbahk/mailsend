# mailsend

`mailsend` is a standalone command-line email client. Gmail accounts authenticate with Google OAuth 2.0 and send through Gmail's `users.messages.send` API. SMTP accounts send through the host, port, and TLS mode stored for that account. The program does not use Apps Script or Gmail passwords, and it does not assume any SMTP server.

The program is a Go binary with SQLite compiled in using a pure-Go driver. End users do not need Go, Python, SQLite, or another runtime installed. OAuth client credentials are configured separately from the binary. Gmail refresh/access tokens and SMTP passwords are held in the operating system credential store, not in `accounts.json`.

## Package the binary

From this repository, build release binaries for macOS, Linux, and Windows (amd64 and arm64):

```sh
./scripts/build-release.sh
ls -lh build/release/
```

Build only one target, for example Apple Silicon macOS:

```sh
./scripts/build-release.sh darwin arm64
```

Or build the current machine's executable:

```sh
make build
./build/mailsend version
```

The resulting binary is self-contained and can be copied to another machine with `scp`, AirDrop, or your preferred distribution method. Pick the binary matching the recipient's operating system and CPU architecture. On macOS, first launch may require allowing the downloaded executable in **System Settings → Privacy & Security** if it is not code-signed/notarized.

The recipient also needs an OAuth desktop-client JSON for a Google Cloud project where Gmail API is enabled. Installed-app OAuth client IDs are intended for native applications and do not keep a client secret confidential; do not mistake the JSON for a user password. Never include a user's OAuth token store or SQLite data when sharing the binary. Public distribution of a Gmail-scope OAuth app may require Google's app verification.

## Google and OAuth setup

Complete this once for the Google Cloud project whose OAuth client the CLI will use:

1. Create a Google Cloud project in the [Google Cloud Console](https://console.cloud.google.com/).
2. In **APIs & Services → Library**, enable **Gmail API** for that project.
3. Configure the OAuth consent screen/Google Auth Platform branding and audience. Add the Gmail users who will test the CLI as test users while the app is in testing. Follow Google's verification steps before broadly distributing an external app that requests Gmail access.
4. In **Clients** (or **APIs & Services → Credentials → Create credentials**), create an OAuth client ID with application type **Desktop app**. Do not create a web application client for this CLI.
5. Download the OAuth client JSON. Place it at the OS config path below, or set `MAILSEND_OAUTH_CLIENT_FILE` to its path. Keep this file out of source control.

Default client JSON locations:

- macOS: `~/Library/Application Support/mailsend/client_secret.json`
- Linux: `${XDG_CONFIG_HOME:-~/.config}/mailsend/client_secret.json`
- Windows: `%AppData%\mailsend\client_secret.json`

Example on macOS:

```sh
mkdir -p "$HOME/Library/Application Support/mailsend"
cp ~/Downloads/client_secret_*.json "$HOME/Library/Application Support/mailsend/client_secret.json"
chmod 600 "$HOME/Library/Application Support/mailsend/client_secret.json"
```

On Linux, use `install -m 600 ~/Downloads/client_secret_*.json "${XDG_CONFIG_HOME:-$HOME/.config}/mailsend/client_secret.json"` after creating the `mailsend` config directory. On Windows, copy the downloaded JSON to the path above. You can keep the file elsewhere and point to it with `MAILSEND_OAUTH_CLIENT_FILE=/path/to/client_secret.json`.

The CLI requests `gmail.send`, `gmail.readonly`, and OpenID's basic `openid` and `email` identity scopes. Google's [`users.messages.send` method](https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/send) accepts the send-only scope. [`users.messages.list`](https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/list) documents that the `q` search parameter cannot be used with `gmail.metadata`, so Sent search uses `gmail.readonly`, which is the narrowest scope that allows `in:sent to:<address>`. `gmail.metadata` is not requested, because a token that includes it is held to the metadata restriction even when a broader scope is present. OpenID is used to display which account was authorized. The CLI does not request Gmail settings access, so alias listings are unavailable. The From address is the authenticated account and cannot be set to an arbitrary address. OAuth uses Google's [installed-app loopback flow](https://developers.google.com/identity/protocols/oauth2/native-app) with PKCE.

A token saved by an older login, which has only `gmail.send`, still sends. `sent-check` and `dispatch --require-sent-check` need the read scope. Run `mailsend login` again for that account and approve the additional access. Until then those checks fail with `scope missing, re-run mailsend login` and do not treat the mailbox as empty.

Keychain reads and Google calls have deadlines, including token refresh. On a headless machine the Secret Service can sit forever waiting for a prompt that will never appear; mailsend returns that as an error instead of waiting. `account` still asks Google who the token belongs to, and if that request fails it prints the address already stored for the account. `sent-check` returns exit 2 within 20 seconds on any failure. `--debug` writes a step trace to stderr and does not print the token. Only `mailsend login` opens a browser.

Then:

```sh
mailsend login
mailsend account
mailsend send --to someone@example.com --subject "Test" --body "Hello from mailsend"
```

Google opens the authorization page in your system browser and returns to a temporary local callback. Sign in to the Gmail account you want to use and grant access. The Gmail password is entered only on Google's page; mailsend never asks for or stores it. Tokens are saved in macOS Keychain, Windows Credential Manager, or the Linux Secret Service/keyring. A desktop keyring/Secret Service must be available on Linux.

While an external OAuth project is in **Testing**, Google expires refresh tokens for apps requesting Gmail scopes after seven days. That means a testing account may need to run `mailsend login` again weekly. For ongoing distribution, move the consent configuration to production and complete the applicable Google verification. See Google's [refresh-token expiration guidance](https://developers.google.com/identity/protocols/oauth2#expiration).

For multiple named Gmail accounts:

```sh
mailsend login --account sunbeam
mailsend login --account personal
mailsend account --account personal
mailsend send --account sunbeam --to alex@example.com --subject "Hello" --body "Hi"
```

Accounts saved before SMTP support have no `provider` field. Those accounts, including an existing `sunbeam` entry, remain Gmail accounts. Adding SMTP does not rewrite them.

## SMTP accounts

Each account is either `gmail` or `smtp`. The host is always the one you pass to `account add`. Default ports are protocol defaults only: 587 for `starttls`, 465 for `ssl` (implicit TLS), and 25 for `none`. The default TLS mode is `starttls`.

The password is never a command flag and is never written to `accounts.json`. It is stored in the same OS keychain service as Gmail tokens, under a separate item. Supply it in one of three ways, in this order: `--password-stdin`, the `MAILSEND_SMTP_PASSWORD` environment variable, or an interactive prompt when stdin is a terminal.

Use a new account name so you do not replace a Gmail account. This example uses `sunbeam-smtp` and the From address `misbah@sunbeam.agency`. Replace `mail.example.com` with your server:

```sh
mailsend account add sunbeam-smtp \
  --provider smtp \
  --host mail.example.com \
  --port 587 \
  --tls starttls \
  --username misbah@sunbeam.agency \
  --from misbah@sunbeam.agency
```

The same account from stdin, or from the environment:

```sh
printf '%s\n' "$SMTP_PASSWORD" | mailsend account add sunbeam-smtp \
  --provider smtp \
  --host mail.example.com \
  --port 587 \
  --tls starttls \
  --username misbah@sunbeam.agency \
  --from misbah@sunbeam.agency \
  --reply-to replies@sunbeam.agency \
  --password-stdin

MAILSEND_SMTP_PASSWORD="$SMTP_PASSWORD" mailsend account add sunbeam-smtp \
  --provider smtp \
  --host mail.example.com \
  --port 587 \
  --tls starttls \
  --username misbah@sunbeam.agency \
  --from misbah@sunbeam.agency
```

`--password-stdin` wins over the environment variable. Running `account add` again for the same SMTP name updates the server settings and replaces the keychain password. A Gmail name such as `sunbeam` is refused; log that account out first or pick another name. The first account stays the default. A later SMTP account does not replace an existing default.

Check the connection without sending a message. This opens the configured host, negotiates the selected TLS mode, and authenticates:

```sh
mailsend account test sunbeam-smtp
mailsend account --account sunbeam-smtp
```

`--send-to` authenticates and then sends one test message, which is recorded in history with kind `test`:

```sh
mailsend account test sunbeam-smtp --send-to alex@example.com
```

`send`, `bulk`, `history`, `--dry-run`, `--from-name`, `--json`, and the local delay / `--daily-limit` controls use the same commands for both providers:

```sh
mailsend send --account sunbeam-smtp \
  --to alex@example.com \
  --subject "Hello" \
  --body "Hi from SMTP" \
  --from-name "Misbah"
```

`--tls none` is cleartext and is reported by `account test`. TLS 1.2 or newer is required for `starttls` and `ssl`, and the certificate must match the configured host. Point `MAILSEND_SMTP_CA_FILE` at a PEM file when the server uses a private CA. There is no option to skip certificate verification.

`quota` on an SMTP account says the server does not expose a remaining-send count. The local bulk `--daily-limit` still applies. On a Gmail account, `quota` keeps the existing Gmail API explanation.

SMTP messages include a `Date` header, a `Message-ID` on the From domain, UTF-8 encoded headers, plain text, and optional HTML. Attachments use the same MIME structure as Gmail sends. The envelope sender is the configured From address. BCC recipients are on the envelope and are omitted from the visible headers. Gmail sends still include the BCC header so the Gmail API can deliver it.

Temporary SMTP 4xx replies are retried with the same backoff as retryable Gmail errors. Permanent 5xx replies fail that recipient. Authentication failures and quota-like 4xx text stop a bulk job. If the connection drops after DATA and no reply arrives, the recipient is `uncertain` and is not retried automatically. `--json` errors for SMTP include `type` `smtp_error`, `stage`, `temporary`, `permanent`, and `smtp_code`.

What differs from Gmail:

- Gmail stores an OAuth token. SMTP stores a password in the keychain and the host, port, TLS mode, username, From address, and optional reply-to in `accounts.json`.
- The Gmail From address is the Google account. The SMTP From address is `--from`. Neither path accepts an arbitrary per-message From address. `--from-name` sets the display name for both.
- Gmail returns the API message id. SMTP records the Message-ID generated on the sender's domain.
- Gmail messages include a BCC header. SMTP messages keep BCC only in the envelope.
- `account` and `account test` call Google's profile endpoint for Gmail. For SMTP they report the saved settings, or connect, negotiate TLS, and authenticate, without sending unless `--send-to` is set.
- `quota` text names the active provider. Both report that a remaining-send count is unavailable.
- SMTP 4xx responses are temporary and retryable. SMTP 5xx responses are permanent. A lost reply after DATA is uncertain for both providers.

## Commands

All commands and subcommands provide `--help`. `--account <name>` and `--json` are global flags and can be used with any command.

### Send

```sh
mailsend send \
  --to alex@example.com \
  --cc bob@example.com \
  --bcc archive@example.com \
  --subject "Project update" \
  --body-file ./update.txt \
  --attach ./report.pdf
```

Recipient and attachment flags can be repeated. Comma-separated recipient lists are accepted. `--body` and `--body-file` are alternatives. To send both plain text and HTML, provide `--body-file` (or `--body`) and `--html-file`; mailsend builds a MIME `multipart/alternative` message and wraps it in `multipart/mixed` when attachments are present. BCC addresses are included in the Gmail API message so Gmail can deliver to them; Gmail removes the BCC header from delivered copies. BCC values are not printed in normal send output or history.

Use `--from-name` to set the display name while keeping the authenticated address. `--reply-to` sets a reply address. There is no arbitrary `--from` option.

Preview without OAuth or network access:

```sh
mailsend send --to alex@example.com --subject "Hello" --body "Preview" --dry-run
```

JSON output and errors:

```sh
mailsend send --to alex@example.com --subject "Hello" --body "Hi" --json
```

### Bulk

A CSV must have an `email` header; all other columns are available as `{{variable}}` values. Duplicate email addresses in a single CSV are collapsed case-insensitively. Each row is sent as a separate message, so other recipients are not exposed.

```csv
email,name,company
john@example.com,John,Acme
sarah@example.com,Sarah,Example
```

```text
Hi {{name}},

I was looking at {{company}} and wanted to reach out.

Best,
Misbah
```

```sh
mailsend bulk --csv contacts.csv --template outreach.txt --dry-run
```

If no subject is stored with the template and no `--subject` is supplied, the template filename becomes the subject (for example, `outreach.txt` gives `outreach`). A CSV `subject` column overrides that fallback per row. Use `--subject "Hi {{name}}"` for an explicit subject template.

Dry-run output includes the recipient, CC/BCC, subject, row variables, attachment names, and rendered bodies. It ends with `No emails were sent.` It does not require login.

A real campaign requires confirmation. Use `--yes` for a non-interactive run:

```sh
mailsend bulk --csv contacts.csv --template outreach.txt --subject "Hello {{name}}" \
  --delay 30 --daily-limit 25

mailsend bulk --csv contacts.csv --template outreach.txt --subject "Hello {{name}}" \
  --min-delay 20 --max-delay 60 --daily-limit 25 --yes
```

`--daily-limit` caps the bulk campaign against successful sends recorded by this installation today. It cannot see mail sent outside mailsend, and it does not constrain standalone `send` commands. Neither Gmail nor a generic SMTP server provides a reliable remaining daily-send counter, so the CLI never represents this local limit as the provider's quota. A Gmail quota/rate response, or an SMTP 4xx response whose text describes a rate or quota limit, stops the campaign. Other SMTP 4xx replies are retried for that recipient; SMTP 5xx replies fail that recipient and the job continues. Use small batches and comply with Google's [sending limits and API error guidance](https://developers.google.com/workspace/gmail/api/guides/handle-errors) when the account is Gmail.

Jobs and individual recipients are persisted in SQLite before sending. Use the printed job ID to inspect or resume:

```sh
mailsend history
mailsend history 20261004T120000-abcdef0123456789abcdef01
mailsend bulk --resume 20261004T120000-abcdef0123456789abcdef01 --yes
```

Successful recipients are never resent. Definite API failures and unsent pending rows can be resumed. If the process loses a send response, the recipient becomes `uncertain` and is not automatically resent because Gmail may already have accepted it. A user can inspect job history and decide how to handle that address. A process-level lock prevents two local processes from running one bulk job at once. Attachment file hashes are checked on resume; a changed attachment is refused.

`sent` means the provider accepted the message and returned an id. For Gmail that id comes from the API. For SMTP it is the Message-ID on the sender's domain. It is not a delivery receipt or proof the message reached the recipient's inbox.

### Templates

Templates are plain files under the mailsend config directory. `template create` copies the source files there with private file permissions.

```sh
mailsend template create outreach --subject "Hello {{name}}" \
  --body-file ./outreach.txt --html-file ./outreach.html
mailsend template list
mailsend template render outreach --var name=John --var company=Acme
mailsend bulk --csv contacts.csv --template outreach
```

A saved template uses `outreach.txt` for text, optional `outreach.html` for HTML, and optional `outreach.subject` for the subject. Missing variables fail before a campaign is created or sent.

### Account, quota, history, and logout

```sh
mailsend account
mailsend quota
mailsend history
mailsend history <job-id>
mailsend logout
mailsend logout --account personal
```

`quota` reports that the selected provider does not expose a reliable remaining-send count. It does not claim that mailsend can bypass Gmail or SMTP limits. `account` reports the authorized Gmail identity, or the saved SMTP host and From address. The Gmail view does not query aliases because that would require additional Gmail settings scope. `account test [name]` checks Gmail with the profile endpoint, or connects and authenticates to the configured SMTP server without sending.

`history --json` and other useful output commands support `--json`. OAuth tokens are never included in command output. Normal history shows the To recipient and message ID, not BCC.

### Scheduling

```sh
mailsend send --to alex@example.com --subject "Reminder" --body "Hello" \
  --schedule "2026-12-01 10:30"
```

The schedule is local time; RFC3339 timestamps with an explicit zone are also accepted. The command persists the job and keeps the process running until send time. Keep the machine awake and the process open; this first version does not install a daemon or OS scheduler.

## Queue and dispatch

`queue` and `dispatch` send a batch over time from one account. They do not change `send`, `bulk`, `history`, or the other existing commands. A batch is a list of individually addressed emails. `dispatch` is a long-running process: it sends one email, waits, then sends the next.

The gap is per account, not per batch. One lock file stops a second `dispatch` process for that account from sending at all. If a scheduled time is already in the past, that email goes out as soon as the gap allows, and every later email keeps the same spacing from that point. Past-due mail is not skipped and it is not sent in a burst.

```sh
mailsend --account sunbeam queue add --file india.json
mailsend --account sunbeam dispatch \
  --interval 5m \
  --daily-cap 95 \
  --timezone Asia/Kolkata \
  --hold /var/lib/outbound/SEND_HOLD \
  --hold-step fu-d3,fu-d7,ooo,fu=/var/lib/outbound/FOLLOWUP_HOLD \
  --hook ./scripts/mailsend-presend-hook \
  --after-send ./scripts/mailsend-presend-hook
```

`--interval` is the minimum time between any two sends for the account. The default is 5 minutes. A batch interval longer than that is used when that batch's email is the one being sent. `--interval 0s` leaves only the batch interval. The daily cap counts every `sent` row for the account in the SQLite history, including ordinary `send` and `bulk` messages, inside the named timezone. At midnight the count resets and a waiting dispatcher continues. `0` means there is no cap unless a batch stored one.

`--gap-from-history` makes that same gap start from the newest `sent` row in history for the account, including ordinary `send` and `bulk` messages, when that row is later than the queue's own cursor. Without the flag, only the queue cursor counts.

```sh
mailsend --account sunbeam dispatch --interval 5m --gap-from-history
```

`--lock-file` takes an exclusive `flock` on that path around each email's pre-send hook, the send, and the after-send hook. The lock is released between emails and while the dispatcher is waiting, so another sender that locks the same file can run in those gaps. The dispatcher waits for the lock. It does not skip the email.

```sh
mailsend --account sunbeam dispatch --interval 5m --lock-file /var/lib/outbound/send.lock
```

A hold file that exists pauses the dispatcher. The emails stay queued. `--hold` pauses everything. `--hold-step fu-d3,fu-d7,ooo,fu=/path/FOLLOWUP_HOLD` pauses only emails whose `step` is one of those names, which is how a follow-up hold can leave first emails moving. Removing the file lets the dispatcher continue.

Each email is stored as `sending` before the provider is called, and becomes `sent` only when Gmail or SMTP returns an id. History then shows that send, so the daily cap sees it. If the process dies in between, the next `dispatch` reconciles the open row:

- history, Gmail Sent, or `--sent-check` proves it was accepted: it is marked `sent` and is not sent again
- the check proves it was not accepted: it is tried again, up to three attempts
- the check cannot decide: the email is `parked` and is not retried

After `mailsend login` grants `gmail.readonly`, a crashed `sending` row is reconciled against live Gmail Sent for that recipient and subject. A token that is still send-only cannot do that search. The row is parked with `scope missing, re-run mailsend login` and is not retried. Other queued mail still sends. The dispatch flag `--sent-check` is a different, optional external program for that same reconcile step. It receives JSON on stdin and prints `{"found": true, "message_id": "..."}` or `{"found": false}`. Exit 2 means it could not tell.

`dispatch --require-sent-check` is a separate gate that runs before each send. It asks Gmail Sent whether this recipient already has a message within `--sent-within` (default 7 days). A match blocks that email and the batch continues. A search error, including a missing scope, stops the dispatcher and leaves the email pending, so nothing is sent on an unknown answer. Without the flag, an old send-only token keeps sending.

```sh
mailsend sent-check --account sunbeam --to ada@example.com --since 30d --json
mailsend --account sunbeam dispatch --require-sent-check --sent-within 7d --interval 5m
```

`sent-check` prints matching messages (`id`, `date`, `to`, `subject`). Exit 0 means none. Exit 3 means at least one. Exit 2 means the search failed or the scope is missing. `--since` defaults to 30 days. `--subject` is optional. The pre-send gate matches on recipient and time only.

```sh
mailsend queue list
mailsend queue status
mailsend queue status <batch-id>
mailsend queue cancel <batch-id>
mailsend queue resolve <item-id> --sent --message-id <id>
mailsend queue resolve <item-id> --retry
mailsend queue resolve <item-id> --drop --reason "checked Sent; do not send"
```

`--json` on these commands prints one JSON object, same as `history` and `send`. `dispatch --json` prints a summary when the process stops. Progress lines go to stderr.

### Batch file

JSON object, JSON array, or CSV. Times without a zone use `--timezone` or the batch `timezone`. Emails without their own `at` are spaced from `start` by `interval` (default 5m). `body`, `body_file`, or `template` supplies the text. `{{name}}` is rendered when the batch is added, and the rendered text is what gets stored.

```json
{
  "label": "India",
  "start": "2026-10-21T09:00:00+05:30",
  "interval": "5m",
  "timezone": "Asia/Kolkata",
  "daily_cap": 95,
  "holds": [
    {"path": "/var/lib/outbound/SEND_HOLD"},
    {"path": "/var/lib/outbound/FOLLOWUP_HOLD", "steps": ["fu-d3", "fu-d7", "ooo", "fu"]}
  ],
  "emails": [
    {"to": "ada@example.com", "subject": "Hello {{name}}", "body": "Hi {{name}}", "vars": {"name": "Ada"}, "step": "first", "tag": "in-1"},
    {"to": "bao@example.com", "subject": "Following up", "body_file": "bao.txt", "step": "fu-d3", "at": "2026-10-21T09:40:00+05:30"}
  ]
}
```

```csv
to,subject,body,step,tag,name
ada@example.com,Hello {{name}},Hi {{name}},first,in-1,Ada
```

```sh
mailsend queue add --file wave.csv --start "2026-10-21 09:00" --interval 5m --timezone Asia/Kolkata --account sunbeam
```

### Pre-send hook

`--hook` runs before each send. The batch's own `hook` wins when it is set. The command receives one JSON object on stdin:

```json
{"account":"sunbeam","batch_id":"...","label":"India","email":"ada@example.com","subject":"Hello","step":"first","tag":"in-1","scheduled_at":"...","position":0}
```

Exit 0 allows the send. Any other exit blocks that email, stores the stderr (or stdout) as the reason, and the batch continues. The gap is not used for a blocked email. `--after-send` runs the same protocol after the provider id is stored, with `message_id` and `"status":"sent"` added. A failure there does not unsend the message.

`scripts/mailsend-presend-hook` turns that JSON into a `fu-precheck.py` style invocation. Point `MAILSEND_PRECHECK` at the checker. It runs `check --reserve` before the send and `confirm --message-id` after. `MAILSEND_SLOT`, `MAILSEND_DEFAULT_KIND`, and `MAILSEND_GMAIL_JSON` are optional. `MAILSEND_PRECHECK_NO_GMAIL=1` adds `--no-gmail` to the pre-send `check` only; `confirm` still records the sent message. The script does not contain a machine-specific directory. Leave `MAILSEND_SLOT` unset when each batch label is the ledger slot; a unit-wide value overwrites those names.

### Keep the dispatcher running

`dispatch` runs until the account has nothing left pending, or until it is stopped. A supervisor should start it again so a batch added later is picked up. An example unit is `scripts/mailsend-dispatch.service`; replace the paths, account, cap, and hook before installing it.

```sh
install -m 644 scripts/mailsend-dispatch.service /etc/systemd/system/mailsend-dispatch.service
systemctl daemon-reload
systemctl enable --now mailsend-dispatch.service
```

A shell loop does the same job with `--once`, which sends at most one due email and exits when the next email is in the future, a hold is present, or the cap is exhausted:

```sh
while true; do
  mailsend --account sunbeam dispatch --once --interval 5m --daily-cap 95 --timezone Asia/Kolkata \
    --hold "$OUTBOUND/SEND_HOLD" \
    --hold-step "fu-d3,fu-d7,ooo,fu=$OUTBOUND/FOLLOWUP_HOLD" \
    --hook ./scripts/mailsend-presend-hook \
    --after-send ./scripts/mailsend-presend-hook
  sleep 15
done
```

### Moving a slot sender

A slot folder that used to run its own sender can become one batch, and one `dispatch` process replaces every regional sender.

1. Build a JSON or CSV file from the slot queue. `to` is the recipient, `subject` and `body` (or `body_file`) are the note, and `step` is `first`, `fu-d3`, `fu-d7`, `ooo`, or `fu`. Use `at` for a specific clock time, or `start` plus `interval` for a fixed spacing. The old random 3–7 minute pause is a fixed interval here; 5 minutes is the default.
2. Stop the per-slot processes. Leave `SEND_HOLD` and `FOLLOWUP_HOLD` in place until you mean to send. Pass those paths to `--hold` and `--hold-step`. The dispatcher waits while the files exist.
3. Set `--daily-cap 95 --timezone Asia/Kolkata` (or your own cap and zone). The cap includes mail already recorded by `send` and `bulk`.
4. Point `--hook` and `--after-send` at `scripts/mailsend-presend-hook`, and set `MAILSEND_PRECHECK` to your checker. The checker still owns its ledger. mailsend's `sending` / `sent` rows are the record that prevents a duplicate after a crash.
5. Run one dispatcher for the account, not one process per region. `queue status` shows parked rows. Inspect Sent, then `queue resolve --sent` or `--retry` or `--drop`. Do not delete a `sending` row to force a retry.

## Data and security

- OAuth access and refresh tokens, and SMTP passwords, are stored in the OS keyring, never in the SQLite database, `accounts.json`, or logs. `logout` removes the token or password for that account.
- The SQLite database, config directory, account metadata, and saved templates are created with owner-only permissions. The local database includes rendered message content so interrupted jobs can resume exactly; protect the OS user account and do not share the config directory.
- OAuth client JSON is read from the configured path and is ignored by git patterns. Do not commit it. An existing `accounts.json` without a `providers` object remains valid.
- Bulk attempts that may have reached the provider are marked `uncertain` and never automatically retried. Retryable failures are HTTP 429 / Gmail user rate-limit responses and SMTP 4xx replies, using exponential backoff (and `Retry-After` when Gmail supplies it). Transport loss, a lost SMTP reply after DATA, and permanent 5xx replies are not retried.
- Bulk and one-off sends still cannot prove an ambiguous timed-out delivery. Check Sent before retrying an `uncertain` row. Queue dispatch can search Gmail Sent when the token has `gmail.readonly`, and `mailsend sent-check` does the same lookup on demand.

## Development and tests

Requires Go 1.23 or newer:

```sh
go test ./...
make build
./build/mailsend --help
```

The unit suite uses local HTTP mocks for Gmail and an in-process SMTP server. It does not require Google credentials or a real mail server. It covers OAuth API error classification, SMTP authentication, STARTTLS, implicit TLS, cleartext, 4xx and 5xx replies, send-response ambiguity, MIME alternatives and attachments, recipient parsing, templates, CSV parsing, rate delay bounds, SQLite job history, duplicate detection, and crash recovery. Legacy account files without a provider stay on the Gmail path.

Manual integration test after OAuth setup (this sends a real message):

```sh
mailsend account
mailsend send --to your-test-address@example.com \
  --subject "mailsend integration test" \
  --body "Gmail API integration test at $(date -Is)"
```

Confirm delivery and the recorded Gmail message ID with:

```sh
mailsend history
```

To build one platform locally, run `./scripts/build-release.sh darwin arm64`, replacing the target with one of `darwin|linux|windows` and `amd64|arm64`. The script sets `CGO_ENABLED=0`; no external runtime libraries are needed for SQLite. The destination operating system still needs its normal keyring service for Gmail tokens and SMTP passwords.
