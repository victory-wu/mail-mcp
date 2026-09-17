package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kacperkwapisz/mail-mcp/internal/config"
)

const AccountsPath = "/admin/accounts"

// AccountStore is separate from the MCP tools: these administrative responses
// contain connection settings and must only be exposed with the management key.
type AccountStore interface {
	Create(context.Context, config.CreateAccountInput) (string, error)
	Find(context.Context, string) ([]config.AccountRecord, error)
	Delete(context.Context, string) (bool, error)
}

func AccountsHandler(secret string, store AccountStore, logger *slog.Logger) http.Handler {
	// An unset management key disables the surface instead of accepting an empty bearer.
	if strings.TrimSpace(secret) == "" {
		return http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+AccountsPath, createAccountHandler(store))
	mux.HandleFunc("GET "+AccountsPath, findAccountsHandler(store))
	mux.HandleFunc("DELETE "+AccountsPath+"/{subkey}", deleteAccountHandler(store))
	return RequireBearer(secret, logger, mux)
}

func accountStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, config.ErrInvalidAccount) {
		writeJSONError(w, http.StatusBadRequest, "invalid_account", err.Error())
		return
	}
	// Redis errors may contain connection details; keep them out of responses.
	writeJSONError(w, http.StatusServiceUnavailable, "account_store_unavailable", "account store operation failed; check Redis availability and configuration")
}

func writeAccountJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// @Summary Create an account
// @Description Stores account configuration under hostname-email-UUID. Hostname identifies the calling machine, independently of IMAP/SMTP hosts. Maximum request body: 64 KiB. MCP tools reload changes on restart.
// @Tags Accounts
// @Accept json
// @Produce json
// @Security BackendBearer
// @Param account body config.CreateAccountInput true "Account configuration; hostname, email, imap.host and imap.password are required"
// @Success 201 {object} accountCreatedResponse
// @Failure 400 {object} accountErrorResponse
// @Failure 401 {object} accountErrorResponse
// @Failure 429 {object} accountErrorResponse
// @Failure 503 {object} accountErrorResponse
// @Router /admin/accounts [post]
func createAccountHandler(store AccountStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var in config.CreateAccountInput
		if err := dec.Decode(&in); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "body must be a valid account JSON object (maximum 64 KiB)")
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "body must contain exactly one JSON object")
			return
		}
		subkey, err := store.Create(r.Context(), in)
		if err != nil {
			accountStoreError(w, err)
			return
		}
		writeAccountJSON(w, http.StatusCreated, accountCreatedResponse{Subkey: subkey})
	}
}

// @Summary Query accounts by subkey prefix
// @Description Literal, case-sensitive matching against hostname-email-UUID. Returns full account configuration, including credentials. No matches returns an empty array. Responses are not cached.
// @Tags Accounts
// @Produce json
// @Security BackendBearer
// @Param prefix query string true "Subkey prefix: hostname-email"
// @Success 200 {object} accountsResponse
// @Failure 400 {object} accountErrorResponse
// @Failure 401 {object} accountErrorResponse
// @Failure 429 {object} accountErrorResponse
// @Failure 503 {object} accountErrorResponse
// @Router /admin/accounts [get]
func findAccountsHandler(store AccountStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prefix := strings.TrimSpace(r.URL.Query().Get("prefix"))
		if prefix == "" {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "prefix is required (hostname-email)")
			return
		}
		accounts, err := store.Find(r.Context(), prefix)
		if err != nil {
			accountStoreError(w, err)
			return
		}
		writeAccountJSON(w, http.StatusOK, accountsResponse{Accounts: accounts})
	}
}

// @Summary Delete an account by subkey
// @Description Deletes exactly one Redis hash field without deleting mailbox messages. MCP tools reload changes on restart.
// @Tags Accounts
// @Produce json
// @Security BackendBearer
// @Param subkey path string true "Complete hostname-email-UUID subkey"
// @Success 204 "Account deleted"
// @Failure 400 {object} accountErrorResponse
// @Failure 401 {object} accountErrorResponse
// @Failure 404 {object} accountErrorResponse
// @Failure 429 {object} accountErrorResponse
// @Failure 503 {object} accountErrorResponse
// @Router /admin/accounts/{subkey} [delete]
func deleteAccountHandler(store AccountStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deleted, err := store.Delete(r.Context(), r.PathValue("subkey"))
		if err != nil {
			accountStoreError(w, err)
			return
		}
		if !deleted {
			writeJSONError(w, http.StatusNotFound, "not_found", "account subkey does not exist")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type accountCreatedResponse struct {
	Subkey string `json:"subkey"`
}

type accountsResponse struct {
	Accounts []config.AccountRecord `json:"accounts"`
}

type accountErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}
