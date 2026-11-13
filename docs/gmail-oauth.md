# Gmail OAuth setup

Gmail accounts send through Gmail's API. They authenticate with a Google OAuth 2.0 installed/desktop client, not a Gmail password or SMTP password. SMTP/IMAP accounts do not need Google Cloud setup.

## Create the OAuth client (human step)

In the [Google Cloud Console](https://console.cloud.google.com/):

1. Open the project selector and choose **New Project**. Enter a project name and create it; select the new project.
2. Open **APIs & Services → Library**, search for **Gmail API**, open it, and select **Enable**.
3. Open **Google Auth Platform**. If it is not configured, select **Get started**. Under **Branding**, set an app name, user support email, and developer contact email; save the app information.
4. Under **Audience**, choose **Internal** only when this is for users within the Workspace organization. Otherwise choose **External**. While testing, add the Gmail addresses that will authorize the app as **Test users** and save. The Google Console UI may label this section OAuth consent screen.
5. Under **Clients**, select **Create client**. Choose **Desktop app**, give the client a name, and create it. Do not create a web application or service-account credential.
6. Download the OAuth client JSON from the client page. The file must have an `installed` object with `client_id` and `client_secret`; mailsend rejects a web-client JSON that only has `web`.

These steps require a Google account with permission to create a project and OAuth clients. Google may show an unverified-app notice during testing. Public distribution that requests Gmail scopes can require Google's OAuth verification. In external testing mode, Google currently expires refresh tokens for Gmail scopes after seven days, so the user may need to repeat login weekly.

Google's [desktop app OAuth guide](https://developers.google.com/identity/protocols/oauth2/native-app) and [Google Auth Platform setup](https://support.google.com/cloud/answer/15544987) describe the current console flow.

## Put the client file in place

Default path:

- macOS: `~/Library/Application Support/mailsend/client_secret.json`
- Linux: `${XDG_CONFIG_HOME:-~/.config}/mailsend/client_secret.json`
- Windows: `%AppData%\mailsend\client_secret.json`

macOS/Linux:

```sh
mkdir -p "$HOME/.config/mailsend"
install -m 600 /path/to/downloaded-client.json "$HOME/.config/mailsend/client_secret.json"
```

That path is the Linux default. On macOS use the path shown above. To store the JSON elsewhere, set `MAILSEND_OAUTH_CLIENT_FILE` to its path in the environment where mailsend will run. On Windows, create `%AppData%\mailsend` and copy the downloaded file there.

Keep the client JSON private and out of source control. Installed-app client credentials are not equivalent to a user's password; never share OAuth tokens or keyring data.

## Authorize Gmail (human browser step)

Run on the computer that will use mailsend, with a browser available:

```sh
mailsend login
```

For a named account:

```sh
mailsend login --account work
```

The program opens Google's authorization page and a temporary local callback. The human signs into the intended account and approves access. Mailsend requests `gmail.send`, `gmail.readonly`, `openid`, and `email`. The read-only scope supports Sent searches; the program does not request settings access and cannot list aliases. Gmail's authenticated address is the From address.

Check the authenticated account and token:

```sh
mailsend account --account work
mailsend account test work
```

The token is held in macOS Keychain, Windows Credential Manager, or the Linux Secret Service/keyring. Never inspect or print the token. A previous token with send-only scope can still send; `sent-check` and `dispatch --require-sent-check` need read access, so run login again if mailsend reports `scope missing`.

## Sign out

```sh
mailsend --account work logout
```

This removes that account's token and account metadata. It does not delete the OAuth client JSON or local send history.
