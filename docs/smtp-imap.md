# SMTP and IMAP accounts

SMTP accounts send through the exact host, port, and TLS mode configured for the account. They do not use Google Cloud. If IMAP is enabled, the same keyring password is used to list and read the mailbox.

## Add an SMTP account

Ask the mail provider for server hostname, port, TLS mode, username, and password. Create a uniquely named account:

```sh
mailsend account add work --provider smtp \
  --host mail.example.com --port 587 --tls starttls \
  --username you@example.com --from you@example.com
```

TLS modes are `starttls`, `ssl` (implicit TLS), or `none`. Defaults are port 587, 465, or 25 respectively; default mode is `starttls`. TLS modes `starttls` and `ssl` require TLS 1.2+ and a valid certificate for the host. `none` is cleartext. There is no option to skip certificate verification.

In a terminal, mailsend prompts for the password. For noninteractive use, `--password-stdin` reads it from stdin; alternatively `MAILSEND_SMTP_PASSWORD` is supported. A password is never accepted as a flag or written to `accounts.json`. Avoid putting it in shell history. If an account with that name already exists as SMTP, the command updates settings and replaces its keyring password; a Gmail account cannot be overwritten this way.

Check saved settings and authenticate without sending:

```sh
mailsend account --account work
mailsend account test work
```

`account test` negotiates SMTP TLS and authenticates. When IMAP is configured it also connects and logs out without opening a mailbox. Add `--send-to you@example.com` only when the user explicitly approves a single test message to their own address.

Set `MAILSEND_SMTP_CA_FILE` to a PEM trust file if the provider uses a private certificate authority.

## Enable IMAP

Check that the provider offers IMAP. Defaults use the SMTP host, SMTP username, port 993 and implicit TLS:

```sh
mailsend account set-imap work
mailsend account test work
```

Override any IMAP setting if the server differs:

```sh
mailsend account set-imap work --imap-host mail.example.com \
  --imap-port 993 --imap-tls ssl --imap-username you@example.com
```

`account add` also accepts `--imap-host`, `--imap-port`, `--imap-tls`, and `--imap-username`. The SMTP password is reused, so setting IMAP does not prompt for another password.

## Read mail

```sh
mailsend inbox --account work
mailsend inbox --account work --folder INBOX --limit 20 --unread --since 7d
mailsend read 42 --account work
mailsend read 42 --account work --folder INBOX --mark-read
```

`inbox` lists newest first with UID, date, sender, subject, snippet, and seen state. The default folder is `INBOX`, default limit 20, and default time window 7 days. `--since 0s` means no time limit; limit must be 1–1000. Listing opens read-only and fetches with BODY.PEEK, so it leaves messages unread. `read` displays headers, text body (or stripped HTML when no text body exists), and attachment names/sizes; it does not download attachment bytes. It leaves the message unread unless `--mark-read` is given.

Gmail inbox reading is not supported. Gmail Sent search is supported by `sent-check`; SMTP Sent search requires IMAP and uses a server-advertised Sent folder or common fallback names.

## Send

```sh
mailsend send --account work --to you@example.com \
  --subject "Example" --body "Hello"
```

Gmail uses the authenticated From address; SMTP uses the configured From address. There is no per-message From override. `--from-name` changes the display name; `--reply-to` sets Reply-To. SMTP quota information is not exposed by the provider; mailsend's local bulk cap still works.
