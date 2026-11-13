# mailsend

`mailsend` is a local Go command-line email client. It sends through Gmail's API using OAuth or through an SMTP server configured for an account. SMTP accounts can optionally use IMAP to list and read mail. Templates, bulk jobs, and scheduled queues are stored and operated locally.

## Quickstart

Install a binary from a GitHub release, or with Go:

```sh
go install github.com/IMisbahk/mailsend/cmd/mailsend@latest
mailsend version
```

For Gmail, set up the desktop OAuth client described in [USAGE.md](USAGE.md#gmail-account-oauth), then run:

```sh
mailsend login
mailsend account
mailsend send --to you@example.com --subject "Mailsend test" --body "Test message"
```

For SMTP/IMAP, see [USAGE.md](USAGE.md#smtp-and-imap-account) — Google Cloud setup is not required. Use `--dry-run` to preview a message without sending it.

## Documentation

- [USAGE.md](USAGE.md): canonical setup and end-to-end guide for people and coding agents.
- [Getting started](docs/getting-started.md)
- [Gmail OAuth](docs/gmail-oauth.md)
- [SMTP and IMAP](docs/smtp-imap.md)
- [Command and flag reference](docs/commands.md)
- [Queue dispatch service](docs/dispatch-service.md)
- [Troubleshooting](docs/troubleshooting.md)
- [FAQ](docs/faq.md)

The sample dispatcher service uses step-scoped holds with `--hold-step fu-d3,fu-d7,ooo,fu=/path`; its parsed step list is `["fu-d3", "fu-d7", "ooo", "fu"]`. Replace those workflow names and paths when adapting the unit.

## Development

Requires Go as listed in [go.mod](go.mod). Build with `make build`; run checks with `make test` and `go vet ./...`. See [AGENTS.md](AGENTS.md) for contributor notes.

mailsend is licensed under the [MIT License](LICENSE).
