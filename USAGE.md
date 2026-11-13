# mailsend setup and usage

This is the canonical, executable setup guide for mailsend. Commands are written for a POSIX shell unless marked Windows. Replace account names and server settings with the user's own values. Examples use only `example.com` addresses and hosts.

## For AI agents

**Goal:** install mailsend, configure one user-selected Gmail or SMTP account, verify authentication, and leave a safe, ready-to-use setup.

**Checklist:**

1. Identify the OS, CPU architecture, shell, and whether the user prefers Gmail OAuth or SMTP/IMAP.
2. Install using a release binary when available, otherwise `go install` or a source build; verify `mailsend version` and `mailsend --help`.
3. Configure the selected account by following [Gmail OAuth](docs/gmail-oauth.md) or [SMTP and IMAP](docs/smtp-imap.md).
4. Verify with `mailsend account` (Gmail makes a profile request; SMTP reports saved settings) and `mailsend account test` (network/authentication check, no message sent).
5. Ask the user to confirm the destination and send one first test message only to the user's own address. Do not send unless the user has identified/confirmed that address.
6. If requested, show the user how to use templates, bulk, queue, dispatch, and inbox; require explicit confirmation before any bulk send or enabling a running dispatcher.

**Ask the human for:** provider choice; their own email address for the test; SMTP host, port, TLS mode, username, and password (entered through a prompt or secure stdin); or, for Gmail, access to a browser to approve OAuth. Creating a Google Cloud project/client and creating a provider app password may require the human. Never ask the user to paste a Gmail password.

**Verify success:** `mailsend version` prints a version; `mailsend account` shows the selected provider/account; `mailsend account test` succeeds without sending. After the user-approved self-test, `mailsend history` shows the test job. `mailsend send --dry-run ...` is a no-network preview and is not proof of authentication.

**Safety rules:** never print, log, or expose passwords, OAuth tokens, or secret JSON contents. Do not put SMTP passwords in command arguments or source files. Use the OS keyring; avoid shell tracing (`set -x`) around credentials. Send tests only to the user's own confirmed address. Ask before bulk sends, scheduling, or starting a persistent dispatcher. Review recipient data and rendered content before a campaign.

## Prerequisites

- **macOS:** supported on amd64 and arm64. A working login Keychain is needed for credentials. Downloaded unsigned binaries may require approval in System Settings → Privacy & Security.
- **Linux:** supported on amd64 and arm64. A Secret Service-compatible desktop keyring must be available/unlocked to save or read credentials. Headless installations need a working keyring service; mailsend times out rather than waiting indefinitely for a missing prompt.
- **Windows:** supported on amd64 and arm64; credentials use Windows Credential Manager. Use PowerShell or Command Prompt for Windows-specific paths and binary names.
- **Go:** only needed for `go install` or building from source. The module's Go version is declared in `go.mod`. Release binaries are standalone; end users do not need Go, SQLite, Python, or a separate SQLite installation.
- Gmail requires internet access, a browser for OAuth approval, and an OAuth desktop-client JSON. SMTP/IMAP users do not need Google Cloud or a Google account.

## Install

### Release binary

Download the archive for the operating system and architecture from the project's GitHub Releases page. Release archives are tar.gz except Windows archives, which are zip; the archive includes `mailsend` and `README.md`. Extract it and place the executable in a directory on `PATH` (for example `/usr/local/bin` if writable, or `~/.local/bin`). On Windows, place `mailsend.exe` in a directory on `PATH`.

The build targets are macOS, Linux, and Windows on amd64 and arm64. Match both OS and architecture; do not run an executable from a different target.

### Install with Go

```sh
go install github.com/IMisbahk/mailsend/cmd/mailsend@latest
```

Ensure `$(go env GOPATH)/bin` (usually `~/go/bin`) is on `PATH`. PowerShell equivalent: add `(go env GOPATH)\bin` to the user PATH.

### Build from a source checkout

Requires Go. From the repository root:

```sh
make build
./build/mailsend version
```

For cross-platform release builds, `./scripts/build-release.sh` creates six targets under `build/release/`; to build one target use `./scripts/build-release.sh linux amd64` (replace with a supported OS/architecture pair).

### Verify

```sh
mailsend version
mailsend --help
```

The first prints the build version; the second lists commands. Use `mailsend <command> --help` for flags. These checks do not authenticate or send mail.

## Choose and configure an account

Accounts use a name such as `default` or `work`. Omit `--account` to use the current default. You can select a named account for a command with the persistent `--account` flag. Gmail accounts are created by `login`; SMTP accounts are created by `account add`.

### SMTP and IMAP account

No Google Cloud setup is needed. Ask the mail provider for the SMTP hostname, port, TLS mode, username, and password. Common modes are `starttls` on port 587, `ssl` on 465, and `none` on 25; follow the provider's settings.

Create an account. With no `MAILSEND_SMTP_PASSWORD` set and an interactive terminal, mailsend securely prompts for the password:

```sh
mailsend account add work --provider smtp \
  --host mail.example.com --port 587 --tls starttls \
  --username you@example.com --from you@example.com
```

For automation, read the password from stdin without putting it in argv:

```sh
read -rsp 'SMTP password: ' SMTP_PASSWORD; printf '\n'
printf '%s\n' "$SMTP_PASSWORD" | mailsend account add work --provider smtp \
  --host mail.example.com --port 587 --tls starttls \
  --username you@example.com --from you@example.com --password-stdin
unset SMTP_PASSWORD
```

The password is stored in the OS keyring, not in the config file. A coding agent must not invent or retrieve a password; the human enters it. Environment alternative: `MAILSEND_SMTP_PASSWORD` is supported, but avoid putting secrets in shell history or logs.

To enable inbox access, IMAP must be supported by the server. Defaults are SMTP host, port 993, TLS `ssl`, and SMTP username:

```sh
mailsend account set-imap work
mailsend account test work
mailsend account --account work
```

Override defaults with `--imap-host`, `--imap-port`, `--imap-tls` (`starttls`, `ssl`, `none`), and `--imap-username`. `account test` authenticates without sending. See [SMTP and IMAP](docs/smtp-imap.md).

### Gmail account (OAuth)

Gmail setup is distinct from SMTP. SMTP/IMAP users do not need Google Cloud. For Gmail, a human may need to create the cloud project/OAuth client and must approve access in a browser. Follow the detailed [Gmail OAuth steps](docs/gmail-oauth.md), then:

```sh
mailsend login
mailsend account
mailsend account test
```

Use `mailsend login --account work` to create another named Gmail account. Login is the only command that opens a browser. Mailsend requests Gmail send and read-only access plus basic identity, saves tokens in the OS keyring, and never asks for the Gmail password.

## Send a first test

After the account test succeeds, confirm the user's own address with them. Then send one message only to that address:

```sh
mailsend send --account work --to you@example.com \
  --subject "Mailsend test" --body "This is my setup test."
```

For SMTP, substitute the configured account name; omit `--account` if it is the default. Successful output includes the recipient and provider message ID. `send` records a normal send job. Alternatively, `mailsend account test work --send-to you@example.com` sends one message recorded with kind `test`; use it only after user confirmation.

Preview composition without network access or authentication:

```sh
mailsend send --to you@example.com --subject "Preview" --body "Preview only" --dry-run
```

## Templates and bulk sends

Create a body file with `{{variable}}` placeholders, then save and preview a template:

```sh
cat > welcome.txt <<'EOF'
Hello {{name}},

Welcome to {{company}}.
EOF
mailsend template create welcome --subject 'Welcome, {{name}}' --body-file welcome.txt
mailsend template list
mailsend template render welcome --var name=Example --var company=ExampleCo
```

Bulk CSV files need an `email` header; each row becomes an individually addressed message. First run only a dry run, review every rendered row/recipient, and get the user's explicit approval before a real send:

```sh
mailsend bulk --account work --csv contacts.csv --template welcome --dry-run
```

Then, only after approval, execute with pacing and a local daily cap, for example:

```sh
mailsend bulk --account work --csv contacts.csv --template welcome \
  --delay 30 --daily-limit 25
```

Interactive bulk runs request confirmation; `--yes` skips it and must not be used without explicit approval. See [commands](docs/commands.md#bulk).

## Scheduled queue and dispatch

`queue` stores batches in local SQLite; it does not send mail. JSON/CSV inputs are parsed into queued messages. Inspect queue syntax and options with `mailsend queue add --help`; then inspect with `queue list` and `queue status`. The dispatcher is the sending process. Review the exact schedule, recipients, account, interval, and daily cap before running it.

For a one-time safe check use `mailsend dispatch --once` only when a due queued message is intentionally ready to send. Persistent dispatch services run continuously and can send future messages; configure them only with explicit user direction. See [dispatch service setup](docs/dispatch-service.md).

## Read an SMTP/IMAP inbox

After IMAP is enabled and `mailsend account test` succeeds:

```sh
mailsend inbox --account work
mailsend inbox --account work --limit 20 --unread --since 7d
mailsend read 123 --account work
```

Inbox listing is read-only and leaves messages unread. `read` leaves the message unread unless `--mark-read` is supplied. Gmail inbox reading is not supported. See [SMTP and IMAP](docs/smtp-imap.md#read-mail).

## Data, configuration, and credentials

By default, the OS user config directory contains the `mailsend` directory:

- macOS: `~/Library/Application Support/mailsend/`
- Linux: `${XDG_CONFIG_HOME:-~/.config}/mailsend/`
- Windows: `%AppData%\mailsend\`

It contains `accounts.json` (account metadata, no passwords/tokens), `mailsend.sqlite3` (local history and queue), template files under `templates/`, and Gmail `client_secret.json` unless `MAILSEND_OAUTH_CLIENT_FILE` points elsewhere. Override the whole config directory with `MAILSEND_CONFIG_DIR`. The application creates the directory with owner-only permissions where supported.

Gmail OAuth tokens and SMTP passwords are stored in the OS credential store under service `mailsend`; they are not in these files. On Linux, the keyring service must be available. Protect and back up config and database files appropriately; never copy/share the keyring or OAuth client file as a substitute for account setup.

## Uninstall

1. Remove the `mailsend` executable from the directory where it was installed. For `go install`, remove the `mailsend` binary from `$(go env GOPATH)/bin`; for a source build, remove the checkout's `build/` directory if desired.
2. To remove a single account's keyring credential and account metadata, run `mailsend --account NAME logout`.
3. To erase all local mailsend data, first use `logout` for each account, then remove the OS config directory listed above. This permanently deletes history, queued work, templates, account metadata, and OAuth client JSON. Do not delete an active queue or service configuration without checking with its owner.
4. If a dispatch service was installed, stop and disable it first; see [dispatch service](docs/dispatch-service.md#remove-the-service).

## Troubleshooting

See [Troubleshooting](docs/troubleshooting.md) for SMTP authentication, app passwords, OAuth consent, keyring, and IMAP issues. General rule: inspect `mailsend account test`, `mailsend --debug account test` (trace only; never enable shell tracing around secrets), and the command help. Debug output is not a credential display mechanism, but do not paste logs publicly without reviewing them.

## Further reference

- [Command and flag reference](docs/commands.md)
- [FAQ](docs/faq.md)
- [Getting started walkthrough](docs/getting-started.md)
- [Repository README](README.md)
