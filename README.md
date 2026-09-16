# mail-mcp

An MCP server that gives AI agents controlled access to IMAP and SMTP mailboxes — read, search, organize, and send — without ever handing them your credentials.

Written in Go. Single static binary, Redis account store, ~20 MB resident.

## Why credentials stay server-side

The agent never sees a hostname, username, or password. Tools take an opaque `account_id`; the server resolves it against the server-side Redis account store and makes the connection itself. Run it remotely and the model has no way to reconstruct how to reach your mailbox, even if it wanted to.

The same idea extends to message handles. A `message_id` is an opaque token encoding the account, folder, UIDVALIDITY, and UID together — so an agent cannot pair a handle from one mailbox with a different account, and a folder that gets renumbered produces a clear "this handle is stale" error instead of quietly acting on the wrong message.

## Install

### Docker

```bash
docker run -d --name mail-mcp \
  -p 3000:3000 \
  -v "$PWD/config.yml:/config.yml:ro" \
  -e MCP_API_KEY="$(openssl rand -hex 32)" \
  ghcr.io/kacperkwapisz/mail-mcp:latest
```

### From source

```bash
git clone https://github.com/kacperkwapisz/mail-mcp.git
cd mail-mcp
make build          # → bin/mail-mcp
```

### Binaries

Download for your platform from [Releases](https://github.com/kacperkwapisz/mail-mcp/releases).

## Setup

**1. Write the config.**

```bash
cp config.example.yml config.yml
```

```yaml
allow_send: false
allow_delete: false
redis:
  addr: 127.0.0.1:6379
  # username: default
  # password: your-redis-password
  db: 0
  timeout: 10s
```

Accounts live in the Redis Hash `mcp_accounts`. Each field is `hostname-email-uuid`, with a server-generated UUID v4. `hostname` is the calling machine's identifier, supplied by the caller; it is independent of `imap.host` and `smtp.host`. The email is the IMAP login address. The stored account ID equals its subkey. For Docker, use a Redis address reachable from the container.

Normal startup only reads Redis and rejects YAML accounts. Empty stores are allowed so the management API can create the first account. Unavailable Redis or invalid account data prevents startup. Management APIs read and write current Redis data; MCP tools reload account changes on restart. No mailbox credentials are exposed through MCP tools.

### Account management API

Enable these HTTP endpoints with a separate startup key:

```bash
./bin/mail-mcp --config config.yml --accounts-api-key 'your-management-secret'
```

`ACCOUNTS_API_KEY` is the environment alternative; an explicit flag takes precedence. If neither is set, the management endpoints are disabled. `MCP_API_KEY` is still required for the HTTP service and authenticates `/mcp`; the management endpoints validate their own key. All three endpoints use:

```http
Authorization: Bearer your-management-secret
```

| Method | Path | Result |
| --- | --- | --- |
| POST | `/admin/accounts` | Create a Redis hash field; return `201` with `{"subkey":"hostname-email-uuid"}`. |
| GET | `/admin/accounts?prefix=hostname-email` | Return `200` with `{"accounts":[{"subkey":"...","account":{...}}]}`; no matches gives an empty array. |
| DELETE | `/admin/accounts/{subkey}` | Delete exactly the supplied field; return `204`, or `404` if absent. |

Create request example (`Content-Type: application/json`):

```json
{
  "hostname": "client-pc-01",
  "email": "you@icloud.com",
  "imap": {
    "host": "imap.mail.me.com",
    "port": 993,
    "security": "tls",
    "password": "your-app-password"
  },
  "smtp": {
    "host": "smtp.mail.me.com",
    "port": 587,
    "security": "starttls",
    "username": "you@icloud.com",
    "password": "your-app-password"
  },
  "from_address": "you@yourdomain.com",
  "from_name": "Your Name",
  "allow_send": true,
  "allow_delete": false,
  "save_sent": true
}
```

`hostname`, `email`, `imap.host`, and `imap.password` are required. `imap.username` is filled from `email`; if supplied it must match. Other endpoint defaults and per-account gates follow the normal configuration rules. The server generates `id`; callers cannot set it. Creation validates configuration without logging into the mailbox. Repeated creation generates distinct UUIDs and never overwrites an existing field.

Query example: `/admin/accounts?prefix=client-pc-01-you%40icloud.com`. `prefix` is required and matched literally, case-sensitively, against the beginning of the complete subkey; it is not a Redis glob. URL-encode query values (especially `+` in email addresses) and subkeys in paths. The query returns full account configuration, including credentials, only to holders of the management key; responses use `Cache-Control: no-store`.

Invalid input returns `400`, missing or incorrect management keys return `401`, and Redis operation failures return `503`. Request bodies are limited to 64 KiB. Deletion affects stored configuration only; it does not delete mailbox messages. Existing MCP account snapshots and pooled connections remain in use until restart.

Most providers need an app-specific password rather than your account password — [iCloud](https://support.apple.com/en-us/102654), [Gmail](https://support.google.com/accounts/answer/185833), Fastmail, and Zoho all work this way.

SMTP inherits the IMAP host and credentials when omitted, and connection security is inferred from the port (993 → TLS, 143 → STARTTLS, 465 → TLS, 587 → STARTTLS) unless you set `security` explicitly.

**2. Generate a token and run.**

```bash
export MCP_API_KEY=$(openssl rand -hex 32)
./bin/mail-mcp --config config.yml
```

The HTTP transport refuses to start without `MCP_API_KEY`. There is no unauthenticated mode — this process can read and send your mail.

**3. Verify.**

```bash
npx @modelcontextprotocol/inspector
```

Connect to `http://localhost:3000/mcp` over Streamable HTTP with an `Authorization: Bearer <MCP_API_KEY>` header, then call `verify_account` to confirm both IMAP and SMTP authenticate.

### Local use

For a client on the same machine, stdio skips the network entirely and needs no token:

```jsonc
{
  "mcpServers": {
    "mail": {
      "command": "/usr/local/bin/mail-mcp",
      "args": ["--config", "/etc/mail-mcp/config.yml", "--transport", "stdio"]
    }
  }
}
```

## Tools

**Discovery**

| Tool              | Purpose                                                            |
| ----------------- | ------------------------------------------------------------------ |
| `list_accounts`   | Every configured mailbox and what it's allowed to do. No network.   |
| `verify_account`  | Live IMAP + SMTP connectivity and auth check.                       |
| `get_server_info` | Version and the operational limits governing the other tools.       |

**Reading**

| Tool             | Purpose                                                                  |
| ---------------- | ------------------------------------------------------------------------ |
| `search_emails`  | Search by sender, recipient, subject, body, date, or flags. Paginated.    |
| `read_email`     | One message: headers, body, attachment metadata. HTML opt-in.             |
| `get_attachment` | Write one attachment to disk. Returns `file_path` and, on HTTP, a 15-minute `download_url`. |

**Sending**

| Tool             | Purpose                                                            |
| ---------------- | ------------------------------------------------------------------ |
| `send_email`     | New message, with attachments and full threading control.          |
| `reply_email`    | Reply with derived subject, recipients, and threading headers.     |
| `forward_email`  | Forward, carrying attachments and quoting the original.            |
| `create_draft`   | Save to Drafts without sending. Works even when sending is off.    |

**Organizing**

| Tool             | Purpose                                                       |
| ---------------- | ------------------------------------------------------------- |
| `archive_email`  | Move to the account's Archive folder.                         |
| `move_email`     | Move to any folder.                                           |
| `mark_email`     | read / unread / flagged / unflagged / answered / unanswered.  |
| `delete_email`   | Move to Trash. Gated, and requires `confirm: true`.           |
| `list_folders`   | Folders with their normalized roles.                          |
| `create_folder` · `rename_folder` · `delete_folder` | Folder management.         |

## Design decisions

**Attachment bytes never enter the response.** `read_email` returns attachment metadata with a `part_id`; `get_attachment` writes the file to disk and returns `file_path` (on the server) plus, when `public_url` is set, a 15-minute signed `download_url`. A remote agent curls that URL onto its own machine. A 7 MB PDF base64-encoded into a tool result would blow the context window without accomplishing anything.

**Bodies are truncated and HTML is opt-in.** Message HTML is attacker-controlled and enormous. It is sanitized through bluemonday before it is ever returned, and omitted entirely unless `include_html` is set. Messages with no plain-text part get one derived from the HTML, with paragraph breaks preserved.

**One pooled IMAP connection per account.** A TLS handshake plus LOGIN on every tool call is the single biggest source of latency in servers of this kind. Connections are kept authenticated, health-checked with NOOP after idling, and reconnected transparently. A background cleanup task closes connections that have not been accessed for `idle_connection_timeout` (24 hours by default) without interrupting active commands. Operations on one account are serialized, since IMAP's selected-mailbox state makes ordering matter; different accounts run concurrently.

**Every network operation is bounded.** Separate timeouts for IMAP connect, IMAP command, SMTP connect, and SMTP send — the last one generous, because DATA transmission for a large attachment on a slow uplink legitimately takes minutes. A command that exceeds its budget closes the socket, which is the only thing that reliably unblocks a stuck IMAP read.

**Sent copies are the exact bytes that were delivered.** Rather than rebuilding an approximation for the Sent folder, the serialized message is captured before transmission and APPENDed verbatim. Whether to append at all is provider-aware: Gmail and Zoho file their own copy on submission, so a second one is redundant or a visible duplicate; iCloud, Office 365, and generic relays file nothing, so skipping it loses the copy.

**Send validation happens before a socket opens.** Recipients, subject length, header injection via embedded newlines, and body presence are all checked locally. A malformed call fails instantly with a specific message instead of after a TLS handshake.

**Tool-call syntax in a body is rejected outright.** LLMs periodically concatenate `body_text` and `body_html` into one argument and leak the separator markup into the recipient's inbox. Prompt instructions do not reliably prevent this, so the server refuses any send whose fields contain `</body_text>`, `<parameter name="body_html">`, and similar markers. Legitimate technical content that merely mentions `<parameter>` still passes.

**Folder roles come from the server, not a name list.** SPECIAL-USE attributes are used where available, with a localized name table as fallback — so a Polish "Wysłane" or a Gmail "[Gmail]/Sent Mail" is recognized as Sent rather than triggering the creation of a duplicate folder.

**Destructive operations are gated twice.** `allow_delete` is false by default, and even when enabled `delete_email` requires `confirm: true` per call and moves to Trash rather than expunging. `delete_folder` additionally refuses INBOX and any special-use folder.

## Configuration reference

### Environment

| Variable              | Default      | Purpose                                                         |
| --------------------- | ------------ | --------------------------------------------------------------- |
| `MCP_API_KEY`         | —            | Bearer token. **Required** for HTTP; unused for stdio.           |
| `ACCOUNTS_API_KEY` | empty | Independent management API bearer key; empty disables the endpoints. |
| `CONFIG_PATH`         | `config.yml` | Path to the YAML config.                                         |
| `TRANSPORT`           | `http`       | `http` or `stdio`.                                               |
| `PORT` / `ADDR`       | `3000`       | Listen port or full address.                                     |
| `LOG_LEVEL`           | `info`       | `debug`, `info`, `warn`, `error`.                                |
| `TRUST_PROXY`         | `false`      | Trust `X-Forwarded-For` for rate limiting.                       |
| `RATE_LIMIT_GET_RPM`  | `60`         | Per-client GET budget per minute.                                |
| `RATE_LIMIT_POST_RPM` | `240`        | Per-client POST budget per minute.                               |

Flags mirror these: `--config`, `--transport`, `--addr`, `--log-level`, `--trust-proxy`, `--version`, `--accounts-api-key`.

### Config file

See [`config.example.yml`](config.example.yml) for the annotated version.

| Key                          | Default        | Purpose                                                    |
| ---------------------------- | -------------- | ---------------------------------------------------------- |
| `redis.addr` | required | Redis server host:port. |
| `redis.username` | empty | Redis ACL username. |
| `redis.password` | empty | Redis authentication password. |
| `redis.db` | `0` | Redis database number. |
| `redis.timeout` | `10s` | Positive timeout for Redis connection and complete operation. |
| `allow_send`                 | `false`        | Global send gate.                                          |
| `allow_delete`               | `false`        | Global delete gate.                                        |
| `limits.max_body_chars`      | `50000`        | Per-part body truncation.                                  |
| `limits.max_search_results`  | `100`          | Largest search page.                                       |
| `limits.max_attachment_bytes`| `26214400`     | Attachment size ceiling.                                   |
| `limits.attachment_dir`      | system temp    | Where `get_attachment` writes.                             |
| `public_url`                 | empty          | Origin used to mint `download_url`. Empty disables it.     |
| `idle_connection_timeout`    | `24h`          | Close pooled IMAP connections idle for this duration.      |
| `timeouts.*`                 | see example    | `imap_connect`, `imap_command`, `smtp_connect`, `smtp_send`. |
| `Redis account: allow_send`      | inherits global| Per-account send gate.                                     |
| `Redis account: allow_delete`    | inherits global| Per-account delete gate.                                   |
| `Redis account: save_sent`       | provider-aware | Force the Sent-folder copy on or off.                      |

Redis account values support legacy poke-mail v1 account fields: the flat `imap_host` / `smtp_username` style keys are folded into the nested form automatically.

## Security

- **Authentication is mandatory** on HTTP. No token, no start.
- **Credentials never leave the server.** Tools receive ids, not connection details.
- **Only declared routes are exposed:** `/mcp`, signed `/attachments/` downloads, and optionally `/admin/accounts` with its separate management key.
- **TLS is mandatory for STARTTLS accounts.** No opportunistic fallback to plaintext, which would send your password in the clear.
- **Rate limited per client**, with separate GET and POST budgets so polling cannot starve real work. `X-Forwarded-For` is ignored unless you declare a trusted proxy, since otherwise any caller could spoof it.
- **Attachment filenames are sanitized** before touching the filesystem — path separators, traversal segments, and control characters are stripped.
- **HTML is sanitized** with bluemonday before it is returned.
- **DNS rebinding protection** is on by default via the MCP SDK.

Put it behind TLS in any real deployment — a reverse proxy or a tunnel. The bearer token is the only thing standing between the internet and your mail.

## Development

```bash
make check    # fmt + vet + test
make test
make race
make cover
make build
make docker
```

## License

MIT — see [LICENSE](LICENSE).
