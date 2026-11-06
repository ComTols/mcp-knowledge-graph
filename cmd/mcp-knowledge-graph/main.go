// Command mcp-knowledge-graph runs the knowledge graph MCP server.
//
// Configuration is read from the environment: KG_MCP_* (transport,
// authentication, limits), KG_DATABASE_URL / KG_SEARCH_* / KG_PATHS_*
// (database), KG_EMBEDDING_* (embedding provider) and KG_INDEXING_*
// (embedding worker). Logs are written to stderr; in stdio mode stdout
// carries only MCP protocol messages.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/caarlos0/env/v11"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-knowledge-graph/internal/adapter/database"
	"mcp-knowledge-graph/internal/adapter/embedding/openai"
	"mcp-knowledge-graph/internal/indexing"
	"mcp-knowledge-graph/internal/mcpserver"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// appConfig holds process level settings that are not part of a package.
type appConfig struct {
	// RunIndexer starts the embedding worker inside this process.
	RunIndexer bool `env:"KG_MCP_RUN_INDEXER" envDefault:"true"`
	// LogLevel is debug, info, warn or error.
	LogLevel string `env:"KG_MCP_LOG_LEVEL" envDefault:"info"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var app appConfig
	if err := env.Parse(&app); err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	logger, err := newLogger(app.LogLevel)
	if err != nil {
		return err
	}

	var cfg mcpserver.Config
	if err := env.Parse(&cfg); err != nil {
		return fmt.Errorf("load MCP configuration: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid MCP configuration: %w", err)
	}
	dbCfg, err := env.ParseAs[database.Config]()
	if err != nil {
		return fmt.Errorf("load database configuration: %w", err)
	}
	embCfg, err := env.ParseAs[openai.Config]()
	if err != nil {
		return fmt.Errorf("load embedding configuration: %w", err)
	}
	idxCfg, err := env.ParseAs[indexing.Config]()
	if err != nil {
		return fmt.Errorf("load indexing configuration: %w", err)
	}

	embedder, err := openai.New(openai.WithConfig(&embCfg))
	if err != nil {
		return err
	}
	store, err := database.Setup(ctx, database.WithConfig(&dbCfg), database.WithEmbedder(embedder))
	if err != nil {
		return fmt.Errorf("set up database: %w", err)
	}
	defer store.Close()

	workerDone := make(chan struct{})
	if app.RunIndexer {
		worker, err := indexing.NewWorker(store, embedder, indexing.WithConfig(&idxCfg), indexing.WithLogger(logger))
		if err != nil {
			return err
		}
		go func() {
			defer close(workerDone)
			if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("embedding worker stopped", slog.String("error", err.Error()))
			}
		}()
	} else {
		close(workerDone)
	}

	server := mcpserver.NewServer(store, mcpserver.Options{
		Version:        version,
		Logger:         logger,
		Limits:         cfg.Limits,
		RequestTimeout: cfg.RequestTimeout,
	})

	switch cfg.Transport {
	case mcpserver.TransportStdio:
		logger.Info("MCP server running on stdio")
		err = server.Run(ctx, &mcp.StdioTransport{})
		if errors.Is(err, context.Canceled) {
			err = nil
		}
	default:
		if !cfg.RequireAuth {
			logger.Warn("authentication is disabled; only use this for local development")
		}
		var handler http.Handler
		if handler, err = mcpserver.NewHTTPHandler(server, &cfg, store, store, logger); err == nil {
			err = mcpserver.ServeHTTP(ctx, &cfg, handler, logger)
		}
	}
	stop()
	<-workerDone
	return err
}

func newLogger(level string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.ToUpper(level))); err != nil {
		return nil, fmt.Errorf("KG_MCP_LOG_LEVEL must be debug, info, warn or error, got %q", level)
	}
	// stderr only: stdout is reserved for the stdio transport.
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})), nil
}
