package httpx

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"net/http"
)

type accountContextKey struct{}

// AuthenticatedAccount is set only after matching an exact configured subkey.
func AuthenticatedAccount(ctx context.Context) string {
	id, _ := ctx.Value(accountContextKey{}).(string)
	return id
}

// RequireAccountBearer uses the same startup snapshot as the mailbox tools.
// Hashing keeps raw credentials out of the lookup keys and compares fixed sizes.
func RequireAccountBearer(accountIDs []string, logger *slog.Logger, next http.Handler) http.Handler {
	accounts := make(map[[32]byte]string, len(accountIDs))
	for _, id := range accountIDs {
		if id != "" {
			accounts[sha256.Sum256([]byte(id))] = id
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		id, found := accounts[sha256.Sum256([]byte(token))]
		if !ok || !found {
			logger.Warn("rejected unauthenticated MCP request", "remote", clientIP(r))
			w.Header().Set("WWW-Authenticate", `Bearer realm="mail-mcp"`)
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "a valid account subkey bearer token is required")
			return
		}
		ctx := context.WithValue(r.Context(), accountContextKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
