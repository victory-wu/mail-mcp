package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kacperkwapisz/mail-mcp/internal/config"
	"github.com/kacperkwapisz/mail-mcp/internal/httpx"
	"github.com/kacperkwapisz/mail-mcp/internal/msgid"
)

func TestHTTPAccountIsolation(t *testing.T) {
	const alice = "host-alice@example.com-550e8400-e29b-41d4-a716-446655440000"
	const bob = "host-bob@example.com-550e8400-e29b-41d4-a716-446655440001"
	cfg := &config.Config{Accounts: []*config.Account{
		{ID: alice, FromAddress: "alice@example.com", IMAP: config.Endpoint{Security: "invalid-alice"}, SMTP: config.Endpoint{Security: "invalid-alice"}},
		{ID: bob, FromAddress: "bob@example.com", IMAP: config.Endpoint{Security: "invalid-bob"}, SMTP: config.Endpoint{Security: "invalid-bob"}},
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := httpx.Handler(cfg.AccountIDs(), logger, false, 0, 0,
		accountMCPHandler(cfg, nil, logger, "download-secret"), http.NotFoundHandler(), nil)
	request := func(token, tool string, args map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": tool, "arguments": args}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-11-25")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	for _, token := range []string{"", "old-global-api-key", "alice@example.com", strings.ToUpper(alice), alice[:len(alice)-1]} {
		rec := request(token, "list_accounts", map[string]any{})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("invalid token: status %d", rec.Code)
		}
	}
	// Alternating credentials through the same handler must never reuse another
	// account's server, even when requests share their JSON-RPC id.
	for _, owner := range []string{alice, bob, alice} {
		other := bob
		if owner == bob {
			other = alice
		}
		for _, tool := range []string{"list_accounts", "get_server_info", "verify_account"} {
			rec := request(owner, tool, map[string]any{})
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, owner) || strings.Contains(body, other) || strings.Contains(body, `"isError":true`) {
				t.Fatalf("%s leaked or omitted accounts: %d %s", tool, rec.Code, body)
			}
		}
	}
	foreignHandle := msgid.Encode(bob, "INBOX", 1, 1)
	cases := map[string]map[string]any{
		"verify_account": {"account_id": bob},
		"search_emails":  {"account_id": bob},
		"list_folders":   {"account_id": bob},
		"create_folder":  {"account_id": bob, "name": "test"},
		"rename_folder":  {"account_id": bob, "old_name": "test", "new_name": "new"},
		"delete_folder":  {"account_id": bob, "name": "test", "confirm": true},
		"send_email":     {"account_id": bob, "to": []string{"x@example.com"}, "subject": "test"},
		"create_draft":   {"account_id": bob, "to": []string{"x@example.com"}, "subject": "test"},
		"read_email":     {"message_id": foreignHandle},
		"get_attachment": {"message_id": foreignHandle, "part_id": "1"},
		"reply_email":    {"message_id": foreignHandle},
		"forward_email":  {"message_id": foreignHandle, "to": []string{"x@example.com"}},
		"archive_email":  {"message_id": foreignHandle},
		"move_email":     {"message_id": foreignHandle, "to_folder": "Archive"},
		"mark_email":     {"message_id": foreignHandle, "action": "read"},
		"delete_email":   {"message_id": foreignHandle, "confirm": true},
	}
	for tool, args := range cases {
		t.Run(tool, func(t *testing.T) {
			rec := request(alice, tool, args)
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, `"isError":true`) ||
				(!strings.Contains(body, "additional properties") && !strings.Contains(body, "not accessible to this account")) {
				t.Fatalf("expected account rejection before network access: %d %s", rec.Code, body)
			}
		})
	}
	// The removed selector must also reject email aliases.
	rec := request(alice, "search_emails", map[string]any{"account_id": "bob@example.com"})
	if !strings.Contains(rec.Body.String(), "additional properties") {
		t.Fatal(rec.Body.String())
	}
}
