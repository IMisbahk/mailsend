# Getting started

This walkthrough installs mailsend and configures an account without sending a message until the final, user-approved step. For a human-readable overview, start here; for an agent checklist and safety rules, see [USAGE.md](../USAGE.md).

## 1. Install and verify

With Go installed:

```sh
go install github.com/IMisbahk/mailsend/cmd/mailsend@latest
mailsend version
mailsend --help
```

Or install a matching binary from GitHub Releases. On Windows the executable is `mailsend.exe`. Release targets cover macOS, Linux, and Windows on amd64 and arm64. The version command prints the build version; root help lists commands. Neither contacts an email provider.

## 2. Configure SMTP or Gmail

SMTP needs the server details and password from the mail provider; Google Cloud is not involved:

```sh
mailsend account add work --provider smtp --host mail.example.com \
  --port 587 --tls starttls --username you@example.com --from you@example.com
```

The command prompts for the password in an interactive terminal and stores it in the OS keyring. Continue with `mailsend account test work`.

For Gmail, create the Google OAuth desktop client as described in [gmail-oauth.md](gmail-oauth.md), then use `mailsend login --account work`. The login opens a browser; the human selects the Gmail account and grants consent. Continue with `mailsend account --account work` and `mailsend account test --account work`.

## 3. Preview, then test

Preview a message without authentication or network access:

```sh
mailsend send --account work --to you@example.com \
  --subject "Setup preview" --body "Preview only" --dry-run
```

After authentication succeeds, confirm with the user that `you@example.com` is their own address before sending a first test:

```sh
mailsend account test work --send-to you@example.com
```

This sends exactly one test message and is therefore not a dry run. Successful output shows authentication and the test send result. The test is recorded in history with kind `test`:

```sh
mailsend history
```

## 4. Next steps

- Save a [template](smtp-imap.md) (template use details are also in [commands.md](commands.md#template)).
- Read an SMTP/IMAP mailbox with [inbox commands](smtp-imap.md#read-mail).
- Review [queue and dispatch](dispatch-service.md) before scheduling sends.
- Diagnose errors with [troubleshooting.md](troubleshooting.md).
