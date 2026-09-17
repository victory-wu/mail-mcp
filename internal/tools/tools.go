// Package tools exposes mailbox operations as MCP tools.
//
// HTTP authentication selects the account; message handles remain scoped to it.
package tools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kacperkwapisz/mail-mcp/internal/config"
	"github.com/kacperkwapisz/mail-mcp/internal/mailbox"
	"github.com/kacperkwapisz/mail-mcp/internal/msgid"
)

// Server holds the dependencies shared by every tool handler.
type Server struct {
	cfg              *config.Config
	pool             *mailbox.Pool
	logger           *slog.Logger
	version          string
	downloadSecret   string
	attachmentPrefix string
}

// NewForAccount isolates discovery and every account/handle resolver by giving
// the HTTP tool server a private configuration containing only its owner.
func NewForAccount(cfg *config.Config, account *config.Account, pool *mailbox.Pool, logger *slog.Logger, version, downloadSecret string) *Server {
	scoped := *cfg
	scoped.Accounts = []*config.Account{account}
	s := &Server{cfg: &scoped, pool: pool, logger: logger, version: version, downloadSecret: downloadSecret}
	s.attachmentPrefix = fmt.Sprintf("%x-", sha256.Sum256([]byte(account.ID)))
	return s
}

// HTTP clients may reuse their own downloaded files, never arbitrary server
// files or another account's attachments. Resolve symlinks before checking.
func (s *Server) checkAttachmentPath(path string) error {
	dir, err := filepath.Abs(s.cfg.Limits.AttachmentDir)
	if err == nil {
		dir, err = filepath.EvalSymlinks(dir)
	}
	resolved, pathErr := filepath.Abs(path)
	if pathErr == nil {
		resolved, pathErr = filepath.EvalSymlinks(resolved)
	}
	if err != nil || pathErr != nil || filepath.Dir(resolved) != dir || !strings.HasPrefix(filepath.Base(resolved), s.attachmentPrefix) {
		return fmt.Errorf("file_path must reference an attachment downloaded by this account; use get_attachment or supply content_base64")
	}
	return nil
}

// Register attaches every tool to the MCP server.
func (s *Server) Register(srv *mcp.Server) {
	s.registerAccounts(srv)
	s.registerRead(srv)
	s.registerSend(srv)
	s.registerManage(srv)
	s.registerFolders(srv)
}

// Instructions is the guidance handed to the connecting client.
const Instructions = `Read, search, organize, and send email for the authenticated mailbox.

ACCOUNT. HTTP authentication selects the mailbox automatically; tools do not
accept account_id. Call list_accounts to inspect the authenticated mailbox.
Obtain message_id handles from search_emails for per-message operations.

MESSAGE IDS. A message_id is an opaque handle that already identifies the
account, folder, and message. Pass it back exactly as received — never edit,
truncate, or rebuild one, and never pair it with a different account. If a tool
reports a stale handle, the folder was renumbered: search again for fresh ids.

BODIES ARE SEPARATE FIELDS. body_text and body_html are two distinct
parameters. Never concatenate them into one string and never wrap content in
pseudo-tags such as <body_text> or <parameter name="body_html">. The server
rejects any send whose fields contain tool-call syntax.

FORMATTING. For correspondence a person will read, supply both body_text and
body_html so the message renders well while keeping a plain-text fallback.
Convert markdown to real HTML tags (<p>, <strong>, <ul>, <a href>) before
sending — never paste raw markdown into body_html. For short automated notes,
body_text alone is fine.

BEFORE SENDING. Show the user To, Cc, Bcc, Subject, the full body rendered as
readable text, and any attachment names, then wait for explicit approval. Do
not dump raw HTML source into that preview; describe it as a formatted message
instead. Send only what the user approved.

READING COSTS CONTEXT. search_emails returns metadata only. read_email returns
one message and omits HTML unless you ask for it. Attachment bytes are never
inlined — read_email lists attachment metadata, and get_attachment writes a
chosen file to the server and returns file_path plus, on HTTP, a download_url.
file_path is on the server, not the agent's machine. Fetch download_url with
curl (or any HTTP client) to a local path, then open that local file. The
link expires in 15 minutes; call get_attachment again for a fresh one.

DESTRUCTIVE ACTIONS. Prefer archive_email over delete_email. Deleting requires
confirm: true, and moves the message to Trash rather than erasing it.`

// ---- shared input fragments ------------------------------------------------

// messageInput is embedded by tools that act on one message.
//
// No account_id: the handle already carries it, which makes it impossible to
// aim an operation at the wrong mailbox.
type messageInput struct {
	MessageID string `json:"message_id" jsonschema:"opaque message handle returned by search_emails; identifies the account, folder, and message"`
}

// AttachmentInput is a file to attach to an outgoing message.
type AttachmentInput struct {
	FilePath      string `json:"file_path,omitempty" jsonschema:"path to a file on the server's filesystem; preferred for anything non-trivial"`
	Filename      string `json:"filename,omitempty" jsonschema:"name the recipient sees; inferred from file_path when omitted"`
	ContentType   string `json:"content_type,omitempty" jsonschema:"MIME type; inferred from the file extension when omitted"`
	ContentBase64 string `json:"content_base64,omitempty" jsonschema:"base64-encoded content; use only for small generated files, prefer file_path otherwise"`
}

// ---- helpers ---------------------------------------------------------------

// resolveAccount requires the private configuration created during authentication.
func (s *Server) resolveAccount() (*config.Account, error) {
	if len(s.cfg.Accounts) != 1 || s.cfg.Accounts[0] == nil {
		return nil, fmt.Errorf("authenticated account unavailable; reconnect with a valid bearer token")
	}
	return s.cfg.Accounts[0], nil
}

// resolveMessage parses a handle and resolves the account it names.
func (s *Server) resolveMessage(handle string) (msgid.ID, *config.Account, error) {
	id, err := msgid.Parse(handle)
	if err != nil {
		return msgid.ID{}, nil, err
	}
	acc, err := s.resolveAccount()
	if err != nil {
		return msgid.ID{}, nil, err
	}
	if id.Account != acc.ID {
		return msgid.ID{}, nil, fmt.Errorf("message_id is not accessible to this account; use search_emails to obtain an authorized handle")
	}
	return id, acc, nil
}

// withSession runs fn against a pooled connection for acc.
func (s *Server) withSession(ctx context.Context, acc *config.Account, fn func(*mailbox.Session) error) error {
	return s.pool.Do(ctx, acc, fn)
}

// readOnlyTool marks tools that never modify server state.
func readOnlyTool() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true}
}

// writeTool marks tools that modify state but are safe to retry.
func writeTool() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{IdempotentHint: true}
}

// destructiveTool marks tools that remove or relocate user data.
func destructiveTool() *mcp.ToolAnnotations {
	t := true
	return &mcp.ToolAnnotations{DestructiveHint: &t}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func boolOr(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
