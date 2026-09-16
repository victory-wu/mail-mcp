package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kacperkwapisz/mail-mcp/internal/config"
)

type fakeAccountStore struct {
	calls   int
	input   config.CreateAccountInput
	prefix  string
	subkey  string
	err     error
	deleted bool
}

func (s *fakeAccountStore) Create(_ context.Context, in config.CreateAccountInput) (string, error) {
	s.calls++
	s.input = in
	return "caller-me@example.com-550e8400-e29b-41d4-a716-446655440000", s.err
}
func (s *fakeAccountStore) Find(_ context.Context, prefix string) ([]config.AccountRecord, error) {
	s.calls++
	s.prefix = prefix
	return []config.AccountRecord{}, s.err
}
func (s *fakeAccountStore) Delete(_ context.Context, subkey string) (bool, error) {
	s.calls++
	s.subkey = subkey
	return s.deleted, s.err
}

func TestAccountAPIAuthenticationAndRoutes(t *testing.T) {
	const adminKey = "management-secret"
	store := &fakeAccountStore{deleted: true}
	handler := Handler(testSecret, discardLogger(), false, 0, 0, okHandler(), http.NotFoundHandler(), AccountsHandler(adminKey, store, discardLogger()))
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		path := AccountsPath
		if method == http.MethodDelete {
			path += "/some-key"
		}
		for _, token := range []string{"", testSecret, "wrong"} {
			if rec := request(method, path, token, `{}`); rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s auth: %d", method, rec.Code)
			}
		}
	}
	if store.calls != 0 {
		t.Fatal("unauthorized request reached Redis")
	}
	if rec := request(http.MethodPost, "/mcp", adminKey, `{}`); rec.Code != http.StatusUnauthorized {
		t.Fatal("management key granted MCP access")
	}
	if rec := request(http.MethodPost, AccountsPath, adminKey, `{"hostname":"caller","email":"me@example.com","imap":{"host":"imap.example.com","password":"secret"}}`); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"subkey"`) || strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("create response: %d %s", rec.Code, rec.Body.String())
	}
	if store.input.Hostname != "caller" || store.input.IMAP.Host != "imap.example.com" {
		t.Fatal("creation parameters not passed through")
	}
	if rec := request(http.MethodGet, AccountsPath+"?prefix=caller-me%40example.com", adminKey, ""); rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("lookup: %d", rec.Code)
	}
	if store.prefix != "caller-me@example.com" {
		t.Fatal("query prefix changed")
	}
	if rec := request(http.MethodDelete, AccountsPath+"/exact-subkey", adminKey, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if store.subkey != "exact-subkey" {
		t.Fatal("delete did not target exact subkey")
	}
	store.deleted = false
	if rec := request(http.MethodDelete, AccountsPath+"/missing", adminKey, ""); rec.Code != http.StatusNotFound {
		t.Fatal("missing account must return 404")
	}
	before := store.calls
	for _, body := range []string{`{`, `{"unknown":"secret"}`, `{} {}`, strings.Repeat("x", 65<<10)} {
		if rec := request(http.MethodPost, AccountsPath, adminKey, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid JSON accepted: %d", rec.Code)
		}
	}
	if rec := request(http.MethodGet, AccountsPath, adminKey, ""); rec.Code != http.StatusBadRequest {
		t.Fatal("missing prefix accepted")
	}
	if store.calls != before {
		t.Fatal("malformed request reached store")
	}
	store.err = config.ErrInvalidAccount
	if rec := request(http.MethodPost, AccountsPath, adminKey, `{}`); rec.Code != http.StatusBadRequest {
		t.Fatal("invalid account should return 400")
	}
	store.err = errors.New("Redis password=secret")
	if rec := request(http.MethodGet, AccountsPath+"?prefix=caller", adminKey, ""); rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("store error leaked credentials or wrong status")
	}
}

func TestAccountAPIDisabledWithoutKey(t *testing.T) {
	store := &fakeAccountStore{}
	handler := AccountsHandler("", store, discardLogger())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, AccountsPath, strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound || store.calls != 0 {
		t.Fatal("unset key must disable account management")
	}
}
