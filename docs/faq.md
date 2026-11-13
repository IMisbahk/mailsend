# FAQ

### Does mailsend support only Gmail?

No. It supports Gmail API OAuth and generic configured SMTP. SMTP accounts can optionally use IMAP for mailbox listing and reading. Gmail inbox reading is not implemented.

### Do SMTP users need Google Cloud?

No. Google Cloud is only for Gmail API OAuth accounts.

### Where are passwords and tokens stored?

In the OS credential store: macOS Keychain, Windows Credential Manager, or Linux Secret Service/keyring. Account metadata and the SQLite database are in the OS config directory. See [USAGE.md](../USAGE.md#data-configuration-and-credentials).

### Does `account test` send an email?

No. It checks authentication. `account test --send-to ADDRESS` sends exactly one test email; confirm the destination with the user first.

### Does queueing send mail?

No. `queue add` writes to SQLite. `dispatch` is the sender and can remain running to deliver scheduled messages.

### Can I send bulk messages without confirmation?

`bulk` requires confirmation unless `--yes` is set. Review a `--dry-run` first and get explicit approval before sending; `--yes` is for an approved noninteractive run.

### Can Gmail send from an alias?

Mailsend uses the authenticated Gmail address and does not request settings access to list aliases. There is no per-message arbitrary From override.

### What does a successful send mean?

It means the provider accepted the message and returned an ID. It is not a delivery receipt.

### Can a scheduled job run on a server?

Yes, if the dispatch process has access to its config, SQLite database, and usable OS keyring. See [dispatch-service.md](dispatch-service.md). Linux headless keyring setup needs special attention.
