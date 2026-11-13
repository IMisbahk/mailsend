# Troubleshooting

Run `mailsend account test [name]` first. It checks OAuth or SMTP authentication and, when configured, IMAP authentication without sending a message. `mailsend --debug account test NAME` emits a step trace to stderr; do not publish logs without reviewing them.

## SMTP authentication and connection errors

- **Authentication rejected / 535 / invalid credentials:** confirm the SMTP username and password with the provider. Many providers require the full email address as username.
- **Gmail SMTP account:** this is SMTP, not mailsend's Gmail OAuth mode. Google may require 2-Step Verification and an app password; a normal Gmail password may not work. Creating an app password requires the human to sign into Google and generate it. Do not ask for or store their normal Gmail password. An app password is still a secret and must be entered into the OS keyring prompt.
- **Connection refused or timeout:** check hostname, port, firewall/VPN, and whether the provider permits SMTP from this network.
- **TLS handshake or certificate error:** match mode to the server: `starttls` for explicit STARTTLS (often 587), `ssl` for implicit TLS (often 465), `none` only when explicitly required. For a private CA, set `MAILSEND_SMTP_CA_FILE` to its PEM certificate. Verification cannot be disabled.
- **Password not found:** re-run `mailsend account add NAME ...` interactively or provide it using `--password-stdin`. `logout` deletes the saved credential.
- **SMTP quota message:** provider limits vary. A quota/rate response stops bulk sending. `--daily-limit` is only a local cap based on mailsend's history; it cannot count other clients' sends.

## Gmail OAuth

- **OAuth client credentials not found:** place the downloaded desktop app JSON at the OS-specific `client_secret.json` path in [gmail-oauth.md](gmail-oauth.md), or set `MAILSEND_OAUTH_CLIENT_FILE`.
- **Invalid client JSON:** download the JSON for the **Desktop app** client. It must include `installed.client_id` and `installed.client_secret`; a web client JSON is not accepted.
- **Access blocked / app not configured:** ensure Gmail API is enabled in the selected project, the correct OAuth client was downloaded, and the authorizing account is listed as a test user when the app is in testing.
- **redirect URI / browser callback failure:** run `mailsend login` locally with a browser and local loopback access. Login uses an installed-app loopback flow with PKCE; do not create a web client or enter a redirect URL.
- **Expired or revoked authorization:** run `mailsend login --account NAME` again and approve. Tokens for external testing projects requesting Gmail scopes can expire after seven days.
- **`scope missing, re-run mailsend login`:** an old token lacks Gmail read-only scope. Login again, then approve the additional scope. Basic sending may still work before reauthorization.
- **Unverified app warning:** expected for an unverified project. Only proceed if the user recognizes the project and intends to authorize it. Wider public release may require Google's verification.

## Keyring, IMAP, and send state

- **Linux keyring / Secret Service error:** install and unlock a Secret Service-compatible keyring in the account/session that runs mailsend. Headless services need explicit keyring provisioning; setting a config directory does not provide secure credential storage.
- **IMAP login failure:** check IMAP is enabled with the provider, host/port/TLS/username are correct, and SMTP password is valid. IMAP reuses the SMTP keyring password.
- **`sent-check` exits 2:** the check failed and cannot be trusted; it does not mean there are no matches. Fix OAuth scope, IMAP settings, or network access. Exit 0 means no match; 3 means a match.
- **Queue item parked/uncertain:** a send response may have been lost. Check the provider's Sent folder and history before using `queue resolve --sent`, `--retry`, or `--drop`; retrying an accepted message can duplicate it.
- **`account test --send-to` delivered a message:** that flag deliberately sends one message. Omit it for authentication-only checks.
