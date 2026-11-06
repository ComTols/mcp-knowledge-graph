package mcpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcp-knowledge-graph/internal/webui"
)

// BearerAuth rejects every request (all HTTP methods) without the expected
// static bearer token with 401 before it reaches next. The comparison is
// constant time; tokens are never logged.
func BearerAuth(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			unauthorized(w, "")
			return
		}
		// Hashing first makes the comparison independent of the length of
		// the presented token.
		sum := sha256.Sum256([]byte(got))
		if subtle.ConstantTimeCompare(sum[:], want[:]) != 1 {
			unauthorized(w, "invalid_token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// BrowserAuth protects the browser facing routes (/ui, /graph). It accepts
// the same static token either as "Authorization: Bearer <token>" or as the
// password of HTTP Basic auth (any user name), so that a browser can open the
// UI with its native login prompt and then send the credentials on its own
// same-origin requests. /mcp keeps accepting Bearer only.
func BrowserAuth(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			_, got, ok = r.BasicAuth()
		}
		if !ok || got == "" {
			basicChallenge(w)
			return
		}
		sum := sha256.Sum256([]byte(got))
		if subtle.ConstantTimeCompare(sum[:], want[:]) != 1 {
			basicChallenge(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func basicChallenge(w http.ResponseWriter) {
	w.Header().Add("WWW-Authenticate", `Basic realm="knowledge-graph", charset="UTF-8"`)
	w.Header().Add("WWW-Authenticate", `Bearer realm="knowledge-graph"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

func unauthorized(w http.ResponseWriter, errCode string) {
	challenge := `Bearer realm="knowledge-graph"`
	if errCode != "" {
		challenge += `, error="` + errCode + `"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// NewHTTPHandler returns the HTTP routes of the server:
//
//   - /mcp: Streamable HTTP MCP endpoint (all methods, authenticated if
//     cfg.RequireAuth),
//   - GET /healthz: process liveness,
//   - GET /readyz: backend readiness (pinger may be nil).
//   - GET /graph: the complete graph as Cytoscape.js JSON (authenticated like
//     /mcp; only registered if graph is non-nil),
//   - GET /graph/fulltext?id=<uuid>: one document as Markdown for the viewer
//     (same auth),
//   - /ui/ and /ui/viewer: embedded Cytoscape.js frontend for /graph (same auth; Basic auth
//     with the token as password is accepted so browsers can log in).
func NewHTTPHandler(server *mcp.Server, cfg *Config, pinger Pinger, graph GraphSource, logger *slog.Logger) (http.Handler, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	var mcpHandler http.Handler = mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			JSONResponse:        true,
			MaxRequestBodyBytes: cfg.MaxRequestBytes,
			SessionTimeout:      cfg.SessionTimeout,
			Logger:              logger,
		},
	)

	// Browser cross-origin protection (Sec-Fetch-Site / Origin). It
	// complements, but never replaces, authentication. DNS rebinding
	// protection for localhost is enabled by default in the SDK handler.
	cop := http.NewCrossOriginProtection()
	for _, origin := range cfg.TrustedOrigins {
		if err := cop.AddTrustedOrigin(strings.TrimSpace(origin)); err != nil {
			return nil, fmt.Errorf("invalid trusted origin %q: %w", origin, err)
		}
	}
	mcpHandler = cop.Handler(mcpHandler)

	if cfg.RequireAuth {
		if cfg.BearerToken == "" {
			return nil, errors.New("bearer token is required when authentication is enabled")
		}
		mcpHandler = BearerAuth(cfg.BearerToken, mcpHandler)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	if graph != nil {
		var gh http.Handler = graphHandler(graph, cfg.RequestTimeout, logger)
		var ft http.Handler = fulltextHandler(graph, strings.TrimRight(cfg.PublicURL, "/"), cfg.RequestTimeout, logger)
		ui := webui.Handler()
		var redirect http.Handler = http.RedirectHandler("/ui/", http.StatusMovedPermanently)
		if cfg.RequireAuth {
			gh = BrowserAuth(cfg.BearerToken, gh)
			ft = BrowserAuth(cfg.BearerToken, ft)
			ui = BrowserAuth(cfg.BearerToken, ui)
			redirect = BrowserAuth(cfg.BearerToken, redirect)
		}
		// All methods are routed so that unauthenticated requests always get
		// 401; the handlers themselves only accept GET/HEAD.
		mux.Handle("/graph", gh)
		mux.Handle("/graph/fulltext", ft)
		mux.Handle("/ui/", ui)
		mux.Handle("/ui", redirect)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if pinger != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			if err := pinger.Ping(ctx); err != nil {
				// Details stay in the server log.
				logger.WarnContext(ctx, "readiness check failed", slog.String("error", err.Error()))
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("not ready"))
				return
			}
		}
		_, _ = w.Write([]byte("ready"))
	})
	return mux, nil
}

// ServeHTTP runs the HTTP server until ctx is canceled and then shuts it
// down gracefully: the listener is closed immediately and in-flight
// requests get cfg.ShutdownTimeout to complete. A graceful shutdown returns
// nil.
func ServeHTTP(ctx context.Context, cfg *Config, handler http.Handler, logger *slog.Logger) error {
	ln, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddress, err)
	}
	return serveListener(ctx, cfg, ln, handler, logger)
}

func serveListener(ctx context.Context, cfg *Config, ln net.Listener, handler http.Handler, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	errc := make(chan error, 1)
	go func() {
		logger.Info("MCP HTTP server listening", slog.String("address", ln.Addr().String()), slog.String("endpoint", "/mcp"))
		errc <- srv.Serve(ln)
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down MCP HTTP server")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
