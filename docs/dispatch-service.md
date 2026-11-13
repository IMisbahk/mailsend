# Queue and dispatch service

The SQLite queue stores planned messages. `queue` itself never sends; `dispatch` sends due messages for one account and may run continuously. Review all recipients, rendered content, schedule, account, gaps, holds, and daily cap before enabling a dispatcher.

## Queue a batch

`queue add --file` accepts a JSON object containing an `emails` array, a JSON array, or CSV. Each email has one recipient and can include fields understood by the parser; `mailsend queue add --help` lists batch-level overrides. Example JSON:

```json
{
  "account": "work",
  "label": "example-batch",
  "timezone": "UTC",
  "start": "2026-11-15 09:00",
  "interval": "5m",
  "emails": [
    {"to": "you@example.com", "subject": "Example", "body": "Hello"}
  ]
}
```

Only put recipients in the file that the user authorized. `queue add` persists the batch; it does not deliver. Inspect before dispatch:

```sh
mailsend queue add --file batch.json
mailsend queue list
mailsend queue status BATCH_ID
```

`queue status` shows each item; use `queue cancel BATCH_ID` to cancel pending items. `queue resolve ITEM_ID` requires exactly one of `--sent`, `--retry`, or `--drop`; use `--sent --message-id ID` only after a person confirms delivery, because it records a send without sending.

## Run dispatch

First review `mailsend dispatch --help`. Dispatch takes an account-wide lock. Default send gap is 5 minutes; daily cap defaults to the tightest active batch cap or no cap. `--once` sends at most one due message and exits, so it is still a real send. A persistent dispatcher waits and sends as messages become due:

```sh
mailsend --account work dispatch --interval 5m --daily-cap 20 --timezone UTC
```

Hold files (`--hold PATH`) pause every send while they exist. `--hold-step step1,step2=PATH` pauses matching workflow steps. Hooks run external commands; only configure trusted commands. `--require-sent-check` checks Gmail Sent before sending and requires the read scope. On crashes, unconfirmed in-flight items can be parked; inspect queue status and the mailbox before resolving them to avoid duplicates.

## Linux systemd service

The repository provides `scripts/mailsend-dispatch.service` as a deployment example; it contains environment-specific account and hook settings that must be replaced. Create a dedicated service user and install the binary and unit through the system administrator. Set `MAILSEND_CONFIG_DIR` to a persistent directory owned by that user, and ensure the keyring can be accessed in the service environment. On headless Linux, missing Secret Service access commonly prevents use of saved SMTP passwords or OAuth tokens.

Minimal unit pattern (review paths, account, rate, cap, and service user before enabling):

```ini
[Unit]
Description=mailsend queue dispatcher
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=mailsend
Environment=MAILSEND_CONFIG_DIR=/var/lib/mailsend
ExecStart=/usr/local/bin/mailsend --account work dispatch --interval 5m --daily-cap 20 --timezone UTC
Restart=always
RestartSec=15

[Install]
WantedBy=multi-user.target
```

Install as `/etc/systemd/system/mailsend-dispatch.service`, then as an administrator:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now mailsend-dispatch.service
sudo systemctl status mailsend-dispatch.service
sudo journalctl -u mailsend-dispatch.service
```

The queue and credentials must be provisioned for the same service user and config directory used by the unit. Do not put a password in the unit or environment file. Install and test account credentials in a usable OS keyring context first.

## macOS launchd

The repository does not provide a launchd plist. A user can create a LaunchAgent plist with `ProgramArguments` pointing to the installed binary and arguments `--account`, `work`, `dispatch`, `--interval`, `5m`, `--daily-cap`, `20`, `--timezone`, `UTC`; set `RunAtLoad` true and direct stdout/stderr to suitable log files. LaunchAgents run in the logged-in user's context and can access that user's Keychain. Validate the plist and account/keychain context before loading it. System LaunchDaemons run headless and generally cannot use the logged-in user's keychain.

## Remove the service

For systemd, stop and disable before removing the unit or binary:

```sh
sudo systemctl disable --now mailsend-dispatch.service
sudo rm /etc/systemd/system/mailsend-dispatch.service
sudo systemctl daemon-reload
```

For a LaunchAgent, unload it with `launchctl bootout` for the corresponding domain, then remove its plist. Keep queue data until the owner has reviewed pending work.
