package httpx

import (
	"log/slog"
	"net/http"
)

const mcpPath = "/mcp"

// Handler exposes /mcp, signed attachment downloads, and optional account
// management with its own authentication handler.
func Handler(accountIDs []string, logger *slog.Logger, trustProxy bool, getRPM, postRPM int, mcp, attachments, accounts http.Handler) http.Handler {
	limiter := NewRateLimiter(getRPM, postRPM, trustProxy)

	wrap := func(inner http.Handler, bearer bool) http.Handler {
		if bearer {
			inner = RequireAccountBearer(accountIDs, logger, inner)
		}
		inner = limiter.Middleware(inner)
		inner = SecurityHeaders(inner)
		inner = LogRequests(logger, inner)
		return inner
	}

	mux := http.NewServeMux()
	mcpWrapped := wrap(mcp, true)
	mux.Handle(mcpPath, mcpWrapped)
	mux.Handle(mcpPath+"/", mcpWrapped)
	mux.Handle(DownloadPrefix, wrap(attachments, false))
	if accounts != nil {
		accountsWrapped := wrap(accounts, false)
		mux.Handle(AccountsPath, accountsWrapped)
		mux.Handle(AccountsPath+"/", accountsWrapped)
	}
	return OnlyPath(mux, mcpPath, DownloadPrefix, AccountsPath)
}
