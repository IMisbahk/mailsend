# mailsend

`mailsend` is a standalone command-line email client for Gmail. It authenticates with Google OAuth 2.0 and sends through Gmail's `users.messages.send` API. It does not use Apps Script, SMTP, or Gmail passwords.

The program is a Go binary with SQLite compiled in using a pure-Go driver. End users do not need Go, Python, SQLite, or another runtime installed. OAuth client credentials are configured separately from the binary, and refresh/access tokens are held in the operating system credential store.

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

The CLI requests `gmail.send` plus OpenID's basic `openid` and `email` identity scopes. Google's [`users.messages.send` method](https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/send) accepts the send-only scope; OpenID is used to display which account was authorized. The CLI does not request mailbox read access or Gmail settings access, so alias listings are unavailable. The From address is the authenticated account and cannot be set to an arbitrary address. OAuth uses Google's [installed-app loopback flow](https://developers.google.com/identity/protocols/oauth2/native-app) with PKCE.

Then:

```sh
mailsend login
mailsend account
mailsend send --to someone@example.com --subject "Test" --body "Hello from mailsend"
```

Google opens the authorization page in your system browser and returns to a temporary local callback. Sign in to the Gmail account you want to use and grant access. The Gmail password is entered only on Google's page; mailsend never asks for or stores it. Tokens are saved in macOS Keychain, Windows Credential Manager, or the Linux Secret Service/keyring. A desktop keyring/Secret Service must be available on Linux.

While an external OAuth project is in **Testing**, Google expires refresh tokens for apps requesting Gmail scopes after seven days. That means a testing account may need to run `mailsend login` again weekly. For ongoing distribution, move the consent configuration to production and complete the applicable Google verification. See Google's [refresh-token expiration guidance](https://developers.google.com/identity/protocols/oauth2#expiration).

For multiple named accounts:

```sh
mailsend login --account sunbeam
mailsend login --account personal
mailsend account --account personal
mailsend send --account sunbeam --to alex@example.com --subject "Hello" --body "Hi"
```

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

`--daily-limit` caps the bulk campaign against successful sends recorded by this installation today. It cannot see mail sent outside mailsend, and it does not constrain standalone `send` commands. Gmail does not provide a reliable remaining daily-send counter through the API, so the CLI never represents this local limit as Google's quota. Gmail still applies its own limits; a quota/rate response stops the campaign. Use small batches and comply with Google's [sending limits and API error guidance](https://developers.google.com/workspace/gmail/api/guides/handle-errors).

Jobs and individual recipients are persisted in SQLite before sending. Use the printed job ID to inspect or resume:

```sh
mailsend history
mailsend history 20261004T120000-abcdef0123456789abcdef01
mailsend bulk --resume 20261004T120000-abcdef0123456789abcdef01 --yes
```

Successful recipients are never resent. Definite API failures and unsent pending rows can be resumed. If the process loses a send response, the recipient becomes `uncertain` and is not automatically resent because Gmail may already have accepted it. A user can inspect job history and decide how to handle that address. A process-level lock prevents two local processes from running one bulk job at once. Attachment file hashes are checked on resume; a changed attachment is refused.

`sent` means Gmail returned a successful API response and message ID. It is not a delivery receipt or proof the message reached the recipient's inbox.

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

`quota` clearly reports that Gmail does not expose a reliable remaining-send count. It does not claim that mailsend can bypass Gmail limits. `account` reports the authorized account and authentication state; it does not query aliases because that would require additional Gmail settings scope.

`history --json` and other useful output commands support `--json`. OAuth tokens are never included in command output. Normal history shows the To recipient and message ID, not BCC.

### Scheduling

```sh
mailsend send --to alex@example.com --subject "Reminder" --body "Hello" \
  --schedule "2026-12-01 10:30"
```

The schedule is local time; RFC3339 timestamps with an explicit zone are also accepted. The command persists the job and keeps the process running until send time. Keep the machine awake and the process open; this first version does not install a daemon or OS scheduler.

## Data and security

- OAuth access and refresh tokens are stored in the OS keyring, never in the SQLite database or logs.
- The SQLite database, config directory, account metadata, and saved templates are created with owner-only permissions. The local database includes rendered message content so interrupted jobs can resume exactly; protect the OS user account and do not share the config directory.
- OAuth client JSON is read from the configured path and is ignored by git patterns. Do not commit it.
- Bulk attempts that may have reached Gmail are marked `uncertain` and never automatically retried. Only explicit HTTP 429 / user rate-limit responses are retried with exponential backoff (and `Retry-After` when supplied); transport loss and server errors are not retried.
- Gmail API does not give this CLI a safe way to determine whether an ambiguous timed-out send was delivered. Check the Gmail Sent folder before manually retrying an uncertain row.

## Development and tests

Requires Go 1.23 or newer:

```sh
go test ./...
make build
./build/mailsend --help
```

The unit suite uses local HTTP mocks for Gmail and does not require Google credentials. It covers OAuth API error classification, send-response ambiguity, MIME alternatives and attachments, recipient parsing, templates, CSV parsing, rate delay bounds, SQLite job history, duplicate detection, and crash recovery.

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

To build one platform locally, run `./scripts/build-release.sh darwin arm64`, replacing the target with one of `darwin|linux|windows` and `amd64|arm64`. The script sets `CGO_ENABLED=0`; no external runtime libraries are needed for SQLite. The destination operating system still needs its normal keyring service for secure OAuth token storage.
