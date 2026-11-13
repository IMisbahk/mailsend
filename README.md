<div align="center">

# mailsend

Send and manage email from your terminal with Gmail OAuth or SMTP.

[![Go version](https://img.shields.io/badge/Go-1.23%2B-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Platforms](https://img.shields.io/badge/platforms-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)](#platforms)

</div>

## Why mailsend?

Use one local CLI for everyday email tasks, whether your account sends through Gmail's API or an SMTP server. Credentials stay in your operating system's keychain, and history, templates, and queued work stay on your machine.

## Features

- ✉️ Send individual or CSV-based email through Gmail OAuth or SMTP.
- 🧪 Preview messages with `--dry-run` before sending.
- 🧰 Reuse templates and manage local send history and scheduled queues.
- 📬 Read mail from SMTP accounts with IMAP.
- 🔐 Store OAuth tokens and SMTP passwords in the OS credential store.

## Install

Requires Go 1.23 or later to install with Go or build from source.

```sh
go install github.com/IMisbahk/mailsend/cmd/mailsend@latest
```

Or build from a source checkout:

```sh
git clone https://github.com/IMisbahk/mailsend.git
cd mailsend
make build
./build/mailsend version
```

You can also build directly with Go:

```sh
go build -o mailsend ./cmd/mailsend
./mailsend version
```

## Quickstart

For Gmail, complete the [OAuth setup](docs/gmail-oauth.md), then authenticate and confirm the account:

```sh
mailsend login
mailsend account
mailsend account test
```

Preview a message before sending. This command validates and displays the message without contacting the mail provider:

```sh
mailsend send --to you@example.com \
  --subject "Hello from mailsend" \
  --body "A preview from the command line." \
  --dry-run
```

To send for real, remove `--dry-run` after checking the recipient and message. For SMTP setup and sending, see [SMTP and IMAP](docs/smtp-imap.md).

## Use it with an AI agent

Ask your agent: **“Read [USAGE.md](USAGE.md) and set up mailsend.”** The guide walks through installation, provider setup, safe verification, and sending a user-approved test.

## Documentation

| Guide | What it covers |
| --- | --- |
| [USAGE.md](USAGE.md) | Canonical installation, setup, and usage guide |
| [AGENTS.md](AGENTS.md) | Instructions for agents and contributors |
| [docs/commands.md](docs/commands.md) | Commands and flags |
| [docs/dispatch-service.md](docs/dispatch-service.md) | Running the queue dispatcher as a service |
| [docs/faq.md](docs/faq.md) | Frequently asked questions |
| [docs/getting-started.md](docs/getting-started.md) | Getting started walkthrough |
| [docs/gmail-oauth.md](docs/gmail-oauth.md) | Gmail OAuth setup |
| [docs/smtp-imap.md](docs/smtp-imap.md) | SMTP sending and IMAP inbox setup |
| [docs/troubleshooting.md](docs/troubleshooting.md) | Common setup and connection issues |

## Platforms

Release builds target macOS, Linux, and Windows on amd64 and arm64. See [USAGE.md](USAGE.md) for platform-specific credential-store requirements.

## License

MIT; see [LICENSE](LICENSE).
