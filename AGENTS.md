# AGENTS.md

Guidance for coding agents working in `mail-mcp`.

## Project Snapshot

- Language: Go (1.25)
- Module: `github.com/kacperkwapisz/mail-mcp`
- Domain: IMAP + SMTP mailbox access exposed over MCP
- Transport: Streamable HTTP only (bearer-authenticated); no --transport flag or TRANSPORT setting
- HTTP `/mcp` authenticates with `Authorization: Bearer <subkey>` against the startup account snapshot, with exact case-sensitive matching. No global MCP API key is used. Each HTTP MCP server instance receives a private config containing only the authenticated account, including discovery and message-handle resolution. Subkeys and message handles are sensitive access credentials. Account changes and revocations take effect after restart. Tool inputs do not accept account_id; authentication selects the account.
- Entry point: `cmd/mail-mcp/main.go`
- Accounts load at startup from Redis hash `mcp_accounts`: field is `hostname-email-uuid` (UUID v4), value is the account JSON using snake_case keys. `hostname` is the caller-supplied host identifier, independent of IMAP/SMTP hosts; email is `imap.username`. Account ID equals subkey. Restart for MCP tools to reload Redis changes. Empty stores can start for initial provisioning.
- YAML contains `redis.addr` (required), `redis.username`, `redis.password`, `redis.db` (default 0), and `redis.timeout` (default 10s, positive). Startup rejects YAML accounts; manage account configuration directly in Redis.
- Account management HTTP endpoints use `--backend-api-key` (or `BACKEND_API_KEY`) as their independent Bearer secret; unset disables them. POST `/admin/accounts` creates an account, GET `/admin/accounts?prefix=hostname-email` matches literal case-sensitive subkey prefixes, DELETE `/admin/accounts/{subkey}` deletes exactly one field. These administrative endpoints return full configuration; MCP tools must never expose credentials.

## Commands

Run from the repo root.

| Task | Command |
| --- | --- |
| Build | `make build` (or `go build ./...`) |
| Swagger docs | `make swagger` (also runs with `make build`; outputs `doc/swagger.json` and `doc/swagger.yaml`) |
| Run | `make run` |
| Test | `go test ./...` |
| Test with race detector | `make race` |
| Coverage | `make cover` |
| Format | `gofmt -w .` |
| Vet | `go vet ./...` |
| Lint | `golangci-lint run` |
| Everything | `make check` |
| Container | `make docker` |

### Running a single test

```
go test ./internal/msgid/ -run TestRoundTrip
go test ./internal/tools/ -run TestRequiredFields -v
go test ./internal/send/ -run 'TestBuild.*' -v
```

## Validation Before Finishing

1. `gofmt -l .` must print nothing
2. `go vet ./...`
3. `go test ./...`
4. `go mod tidy` must leave `go.mod` / `go.sum` unchanged

Do not skip vet or tests for code changes.

## Package Ownership

| Package | Responsibility |
| --- | --- |
| `cmd/mail-mcp` | Flags, HTTP wiring, graceful shutdown |
| `internal/config` | YAML loading, Redis account store, defaults, validation, account resolution, gates |
| `internal/msgid` | Opaque message handle encode/parse |
| `internal/mailmime` | MIME parsing, body extraction, sanitization, attachment extraction |
| `internal/mailbox` | IMAP connection pool and operations |
| `internal/send` | SMTP composition, delivery, validation |
| `internal/tools` | MCP tool definitions and handlers |
| `internal/httpx` | Bearer auth, rate limiting, route filtering, security headers, signed attachment downloads, account management REST endpoints |

Keep the layering one-directional: `tools` depends on `mailbox`/`send`/`config`/`httpx`, never the reverse. `httpx` must not import `tools`.

## Invariants — Do Not Break These

### Credentials never cross the tool boundary

Tool inputs never accept `account_id`; use the authenticated account. Outputs may carry `account_id`, never a host, username, or password. `internal/tools/tools_test.go` asserts this. If you add a field to any output struct, make sure it cannot carry connection details.

### Message handles are opaque and self-contained

A `message_id` encodes account + mailbox + UIDVALIDITY + UID. Consequences:

- Per-message tools must **not** accept an `account_id` parameter. The handle already names the account; accepting both would let a caller aim an operation at the wrong mailbox where the same UID is valid but points elsewhere.
- Every operation on a handle must call `Session.SelectFor`, which re-checks UIDVALIDITY.
- The encoding in `msgid.Encode` is persisted by agents across turns. Changing it invalidates every cached handle, so treat it as a wire format.

### Sending and deletion

Sending and deletion have no configuration gates. `delete_email` still requires `confirm: true`, and special-use folder protections remain enforced.

### Attachment bytes stay out of responses

`read_email` returns metadata only. `get_attachment` writes to disk and returns `file_path` plus, when `public_url` is set, a 15-minute HMAC `download_url`. Do not add an option that inlines attachment content into a tool result. The download token is HMAC-SHA256 of expiry+filename keyed with a server-only random secret generated at HTTP startup; restart invalidates existing links. Never use a client-known subkey as the signing secret. Verification failures are 404, never 401, so scanners learn nothing. HTTP attachment filenames have account-specific prefixes and random components; output directories are restricted to the configured directory, and outgoing file paths must resolve to that account's files there.

### Send validation precedes the network

`send.Validate` runs before any socket opens. New send parameters need validation there, not after connecting.

### Wrapper-leak rejection stays wired

`send.ValidateNoWrapperLeak` guards every outgoing body. It exists because prompt instructions alone did not stop models from leaking tool-call markup into recipients' inboxes. Any new send path must call it.

## Code Style

### Imports

Three groups, separated by blank lines, in order:

1. standard library
2. external modules
3. `github.com/kacperkwapisz/mail-mcp/...`

### Errors

- Wrap with `%w` and enough context to act on: which account, which folder, which address.
- Error strings are read by an LLM. Say what went wrong *and* what to do instead.
- Never `panic` in a request path. `Pool.Do` recovers, but do not rely on it.
- Return errors from tool handlers rather than encoding failure in the output struct, so the SDK marks the result `IsError` and the model can self-correct.

### Naming

- Types `UpperCamelCase`, functions `mixedCaps`, no underscores.
- Use protocol vocabulary where it is protocol behaviour: `UIDVALIDITY`, `mailbox`, `part_id`.
- Use user vocabulary in tool names and descriptions: `folder`, not `mailbox`.

### Comments

Explain *why*, not *what*. Existing comments justify non-obvious decisions — the provider-aware Sent logic, per-connection serialization, UID EXPUNGE over EXPUNGE. Match that. Do not narrate control flow.

### Tool schemas

- Add `jsonschema:"..."` descriptions to every field. A test enforces this.
- Optional fields need `omitempty`; required fields must not have it.
- Booleans defaulting to true must be `*bool` so unset is distinguishable from false.

### Concurrency

- All IMAP work goes through `Pool.Do`, which holds the per-account lock.
- Do not hold a `*Session` past the end of its callback.
- Every network call must be timeout-bounded by a configured value, never a hardcoded one.
- The pool cleanup task closes connections idle for `idle_connection_timeout` (24 hours by default) and must not interrupt active commands.

## Testing Expectations

- Pure logic (parsing, validation, pagination, roles, handles) gets unit tests.
- Tool registration, schemas, and gating are covered through an in-memory MCP client in `internal/tools/tools_test.go`. Add new tools to `TestAllToolsRegister`.
- Bug fixes get a regression test reproducing the original failure.
- Tests must not require network access or a live mailbox.

## Change Discipline

- Prefer minimal, focused diffs.
- Do not rename tools without an explicit request — clients cache them.
- Do not add compatibility shims unless asked. The one exception already present is legacy flat config keys, which exist to carry poke-mail v1 configs across the rename.
- Adding an env var or config key means updating `config.example.yml`, `.env.example` (env vars only), `README.md`, and this file together.
