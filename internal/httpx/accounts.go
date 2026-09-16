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
	mux.HandleFunc("POST "+AccountsPath, func(w http.ResponseWriter, r *http.Request) {
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
		writeAccountJSON(w, http.StatusCreated, map[string]string{"subkey": subkey})
	})
	mux.HandleFunc("GET "+AccountsPath, func(w http.ResponseWriter, r *http.Request) {
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
		writeAccountJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
	})
	mux.HandleFunc("DELETE "+AccountsPath+"/{subkey}", func(w http.ResponseWriter, r *http.Request) {
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
	})
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
