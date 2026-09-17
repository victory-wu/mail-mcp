// Command mail-mcp serves IMAP and SMTP mailboxes to AI agents over MCP.
//
// Credentials live in server-side Redis and never reach the client:
// HTTP authentication selects the account for every tool call.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	// Embed the timezone database in the binary. Message Date headers carry
	// zone names as well as numeric offsets, and the runtime image is
	// scratch — there is no /usr/share/zoneinfo to fall back on.
	_ "time/tzdata"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kacperkwapisz/mail-mcp/internal/config"
	"github.com/kacperkwapisz/mail-mcp/internal/httpx"
	"github.com/kacperkwapisz/mail-mcp/internal/mailbox"
	"github.com/kacperkwapisz/mail-mcp/internal/tools"
)

// version is overridden at build time:
//
//	go build -ldflags "-X main.version=1.0.0"
var version = "dev"

const mcpPath = "/mcp"

// @title Mail MCP Account Management API
// @version 1.0
// @description Administrative REST endpoints for Redis account configuration. Enable with --backend-api-key or BACKEND_API_KEY. MCP JSON-RPC tools are described by MCP tool discovery, not this REST specification.
// @BasePath /
// @securityDefinitions.apikey BackendBearer
// @in header
// @name Authorization
// @description Enter Bearer followed by the management key configured with --backend-api-key or BACKEND_API_KEY.
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mail-mcp: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath = flag.String("config", envOr("CONFIG_PATH", "config.yml"), "path to the YAML config file")
		addr       = flag.String("addr", envOr("ADDR", ":"+envOr("PORT", "3000")), "address to listen on")

		logLevel      = flag.String("log-level", envOr("LOG_LEVEL", "info"), "log level: debug, info, warn, error")
		trustProxy    = flag.Bool("trust-proxy", envBool("TRUST_PROXY", false), "trust X-Forwarded-For for rate limiting; only enable behind a proxy you control")
		showVersion   = flag.Bool("version", false, "print the version and exit")
		backendAPIKey = flag.String("backend-api-key", envOr("BACKEND_API_KEY", "7858fc761b8544f398fc761b8534f3cd"), "bearer key for backend HTTP endpoints; empty disables them")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("mail-mcp", version)
		return nil
	}

	logger := newLogger(*logLevel)
	mailbox.Version = version

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	logger.Info("configuration loaded",
		"path", *configPath,
		"accounts", len(cfg.Accounts),
		"idle_connection_timeout", cfg.IdleConnTTL,
	)

	pool := mailbox.NewPool(cfg, logger)
	defer pool.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serveHTTP(ctx, pool, cfg, logger, *addr, *trustProxy, *backendAPIKey)
}

func newMCPServer(toolServer *tools.Server, logger *slog.Logger) *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "mail-mcp", Version: version, Title: "Mail"},
		&mcp.ServerOptions{Instructions: tools.Instructions, Logger: logger},
	)
	toolServer.Register(srv)
	return srv
}

func accountMCPHandler(cfg *config.Config, pool *mailbox.Pool, logger *slog.Logger, downloadSecret string) http.Handler {
	servers := make(map[string]*mcp.Server, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		servers[account.ID] = newMCPServer(tools.NewForAccount(cfg, account, pool, logger, version, downloadSecret), logger)
	}
	return mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return servers[httpx.AuthenticatedAccount(r.Context())] },
		&mcp.StreamableHTTPOptions{Logger: logger, Stateless: true},
	)
}

func serveHTTP(ctx context.Context, pool *mailbox.Pool, cfg *config.Config, logger *slog.Logger, addr string, trustProxy bool, backendAPIKey string) error {
	// Download signatures must not use a client-known subkey: that would let
	// clients forge links to other accounts' files. Restart invalidates old links.
	downloadSecret := rand.Text()
	mcpHandler := accountMCPHandler(cfg, pool, logger, downloadSecret)
	attachments := httpx.AttachmentHandler(downloadSecret, cfg.Limits.AttachmentDir, logger)
	var accounts http.Handler

	store, err := cfg.Redis.AccountStore()
	if err != nil {
		return err
	}
	defer store.Close()
	accounts = httpx.AccountsHandler(backendAPIKey, store, logger)

	handler := httpx.Handler(
		cfg.AccountIDs(), logger, trustProxy,
		envInt("RATE_LIMIT_GET_RPM", 60),
		envInt("RATE_LIMIT_POST_RPM", 240),
		mcpHandler, attachments, accounts,
	)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelDebug),
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr, "path", mcpPath, "attachments", httpx.DownloadPrefix, "version", version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

// newLogger writes server logs to stderr.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(envOr(key, "")); err == nil {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	switch strings.ToLower(envOr(key, "")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
