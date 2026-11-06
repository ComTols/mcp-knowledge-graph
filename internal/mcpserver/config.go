package mcpserver

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Supported transports.
const (
	TransportHTTP  = "http"
	TransportStdio = "stdio"
)

// minBearerTokenLength is the minimum accepted length of the static bearer
// token. Short tokens are trivially guessable.
const minBearerTokenLength = 32

// Config is the configuration of the MCP server. It is loaded from the
// environment (github.com/caarlos0/env) with the prefix KG_MCP_.
type Config struct {
	// Transport is "http" (Streamable HTTP) or "stdio".
	Transport string `env:"KG_MCP_TRANSPORT" envDefault:"http"`
	// ListenAddress is the HTTP listen address.
	ListenAddress string `env:"KG_MCP_LISTEN_ADDRESS" envDefault:"127.0.0.1:8080"`
	// BearerToken is the static secret required on every /mcp request.
	BearerToken string `env:"KG_MCP_BEARER_TOKEN"`
	// RequireAuth enforces bearer authentication in HTTP mode.
	RequireAuth bool `env:"KG_MCP_REQUIRE_AUTH" envDefault:"true"`
	// AllowUnauthenticatedNonLoopback is a development-only override that
	// permits RequireAuth=false on a non-loopback listen address. Never
	// enable it in a deployment.
	AllowUnauthenticatedNonLoopback bool `env:"KG_MCP_DEV_ALLOW_UNAUTHENTICATED_NON_LOOPBACK" envDefault:"false"`
	// TrustedOrigins are browser origins (e.g. https://app.example.com)
	// that may send cross-origin requests to /mcp.
	TrustedOrigins []string `env:"KG_MCP_TRUSTED_ORIGINS" envSeparator:","`
	// PublicURL is the externally reachable base URL of this server (e.g.
	// https://kg.example.com, a path prefix is allowed). It is used for the
	// absolute parent links in the viewer Markdown; empty yields
	// root-relative links.
	PublicURL string `env:"KG_MCP_PUBLIC_URL"`
	// MaxRequestBytes limits the HTTP request body size.
	MaxRequestBytes int64 `env:"KG_MCP_MAX_REQUEST_BYTES" envDefault:"2097152"`
	// RequestTimeout bounds the execution of a single tool call.
	RequestTimeout time.Duration `env:"KG_MCP_REQUEST_TIMEOUT" envDefault:"30s"`
	// ShutdownTimeout bounds the graceful HTTP shutdown.
	ShutdownTimeout time.Duration `env:"KG_MCP_SHUTDOWN_TIMEOUT" envDefault:"10s"`
	// SessionTimeout closes idle MCP sessions; 0 keeps them forever.
	SessionTimeout time.Duration `env:"KG_MCP_SESSION_TIMEOUT" envDefault:"30m"`
	// Limits are the per-tool content limits.
	Limits Limits
}

// Limits bounds the size of tool arguments. Oversized writes are rejected
// with invalid_input; content is never truncated silently.
type Limits struct {
	MaxTitleBytes       int `env:"KG_MCP_MAX_TITLE_BYTES" envDefault:"512"`
	MaxSummaryBytes     int `env:"KG_MCP_MAX_SUMMARY_BYTES" envDefault:"4096"`
	MaxBlockBytes       int `env:"KG_MCP_MAX_BLOCK_BYTES" envDefault:"65536"`
	MaxSourceURIBytes   int `env:"KG_MCP_MAX_SOURCE_URI_BYTES" envDefault:"2048"`
	MaxSourceTitleBytes int `env:"KG_MCP_MAX_SOURCE_TITLE_BYTES" envDefault:"512"`
	MaxEditOperations   int `env:"KG_MCP_MAX_EDIT_OPERATIONS" envDefault:"100"`
}

// Fixed contract limits of the public tools.
const (
	maxQueryRunes        = 1000
	defaultSearchLimit   = 10
	maxSearchLimit       = 50
	defaultMaxPaths      = 5
	maxMaxPaths          = 10
	defaultPageLimit     = 30
	maxPageLimit         = 100
	maxCreateParents     = 20
	maxCreateBlocks      = 100
	maxCreateSources     = 50
	hardMaxEditOperation = 1000
)

// Validate checks the configuration and reports all problems at once.
func (c *Config) Validate() error {
	var errs []error
	switch c.Transport {
	case TransportHTTP:
		errs = append(errs, c.validateHTTP()...)
	case TransportStdio:
	default:
		errs = append(errs, fmt.Errorf("KG_MCP_TRANSPORT must be %q or %q, got %q", TransportHTTP, TransportStdio, c.Transport))
	}
	if c.RequestTimeout <= 0 {
		errs = append(errs, fmt.Errorf("KG_MCP_REQUEST_TIMEOUT must be positive, got %s", c.RequestTimeout))
	}
	if c.ShutdownTimeout <= 0 {
		errs = append(errs, fmt.Errorf("KG_MCP_SHUTDOWN_TIMEOUT must be positive, got %s", c.ShutdownTimeout))
	}
	if c.SessionTimeout < 0 {
		errs = append(errs, fmt.Errorf("KG_MCP_SESSION_TIMEOUT must not be negative, got %s", c.SessionTimeout))
	}
	errs = append(errs, c.Limits.validate()...)
	return errors.Join(errs...)
}

func (c *Config) validateHTTP() []error {
	var errs []error
	host, _, err := net.SplitHostPort(c.ListenAddress)
	if err != nil {
		errs = append(errs, fmt.Errorf("KG_MCP_LISTEN_ADDRESS must be host:port, got %q", c.ListenAddress))
	}
	if c.MaxRequestBytes <= 0 {
		errs = append(errs, fmt.Errorf("KG_MCP_MAX_REQUEST_BYTES must be positive, got %d", c.MaxRequestBytes))
	}
	if c.RequireAuth {
		if c.BearerToken == "" {
			errs = append(errs, errors.New("KG_MCP_BEARER_TOKEN is required when KG_MCP_REQUIRE_AUTH=true"))
		} else if len(c.BearerToken) < minBearerTokenLength {
			errs = append(errs, fmt.Errorf("KG_MCP_BEARER_TOKEN must be at least %d characters long", minBearerTokenLength))
		}
	} else if err == nil && !isLoopbackHost(host) && !c.AllowUnauthenticatedNonLoopback {
		errs = append(errs, fmt.Errorf("KG_MCP_REQUIRE_AUTH=false is only allowed on a loopback address, got %q "+
			"(development-only override: KG_MCP_DEV_ALLOW_UNAUTHENTICATED_NON_LOOPBACK=true)", c.ListenAddress))
	}
	if c.PublicURL != "" {
		u, perr := url.Parse(c.PublicURL)
		switch {
		case perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
			errs = append(errs, fmt.Errorf("KG_MCP_PUBLIC_URL must be an absolute http(s) URL such as https://kg.example.com, got %q", c.PublicURL))
		case u.RawQuery != "" || u.Fragment != "" || u.User != nil:
			errs = append(errs, errors.New("KG_MCP_PUBLIC_URL must not contain user info, query or fragment"))
		}
	}
	for _, o := range c.TrustedOrigins {
		if strings.TrimSpace(o) == "" {
			errs = append(errs, errors.New("KG_MCP_TRUSTED_ORIGINS must not contain empty entries"))
		}
	}
	return errs
}

func (l Limits) validate() []error {
	var errs []error
	positive := []struct {
		name  string
		value int
	}{
		{"KG_MCP_MAX_TITLE_BYTES", l.MaxTitleBytes},
		{"KG_MCP_MAX_SUMMARY_BYTES", l.MaxSummaryBytes},
		{"KG_MCP_MAX_BLOCK_BYTES", l.MaxBlockBytes},
		{"KG_MCP_MAX_SOURCE_URI_BYTES", l.MaxSourceURIBytes},
		{"KG_MCP_MAX_SOURCE_TITLE_BYTES", l.MaxSourceTitleBytes},
		{"KG_MCP_MAX_EDIT_OPERATIONS", l.MaxEditOperations},
	}
	for _, p := range positive {
		if p.value <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive, got %d", p.name, p.value))
		}
	}
	if l.MaxEditOperations > hardMaxEditOperation {
		errs = append(errs, fmt.Errorf("KG_MCP_MAX_EDIT_OPERATIONS must not exceed %d, got %d", hardMaxEditOperation, l.MaxEditOperations))
	}
	return errs
}

// isLoopbackHost reports whether host only accepts local connections. An
// empty host (":8080") listens on all interfaces and is not loopback.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
