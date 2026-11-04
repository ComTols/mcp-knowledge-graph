//go:build integration

package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"mcp-knowledge-graph/internal/adapter/database"
	"mcp-knowledge-graph/internal/adapter/embedding/hashing"
	env "mcp-knowledge-graph/internal/environment"
	"mcp-knowledge-graph/internal/indexing"
	"mcp-knowledge-graph/internal/knowledge"
)

// The integration tests drive the MCP tools through a real MCP client
// against a real PostgreSQL with pgvector:
//
//   - Default: a pgvector/pgvector container is started via testcontainers.
//   - KG_TEST_DATABASE_URL (postgres:// URL of a role with CREATEDB) uses an
//     existing server instead.
//
// A template database is migrated once; every test gets its own copy.

const (
	pgImage      = "pgvector/pgvector:pg17"
	templateName = "kg_mcp_template"
)

var suite struct {
	adminURL string
	createMu sync.Mutex
}

func TestMain(m *testing.M) {
	code, err := runSuite(m)
	if err != nil {
		log.Printf("integration test setup failed: %v", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runSuite(m *testing.M) (int, error) {
	ctx := context.Background()
	suite.adminURL = os.Getenv("KG_TEST_DATABASE_URL")
	if suite.adminURL == "" {
		ctr, err := tcpostgres.Run(ctx, pgImage,
			tcpostgres.WithDatabase("postgres"),
			tcpostgres.WithUsername("postgres"),
			tcpostgres.WithPassword("postgres"),
			tcpostgres.BasicWaitStrategies(),
		)
		if err != nil {
			return 0, err
		}
		defer func() { _ = testcontainers.TerminateContainer(ctr) }()
		if suite.adminURL, err = ctr.ConnectionString(ctx, "sslmode=disable"); err != nil {
			return 0, err
		}
	}
	if err := adminExec(ctx, `DROP DATABASE IF EXISTS `+templateName+` WITH (FORCE)`); err != nil {
		return 0, err
	}
	if err := adminExec(ctx, `CREATE DATABASE `+templateName); err != nil {
		return 0, err
	}
	defer func() { _ = adminExec(ctx, `DROP DATABASE IF EXISTS `+templateName+` WITH (FORCE)`) }()
	if err := database.Migrate(ctx, withDatabase(suite.adminURL, templateName)); err != nil {
		return 0, fmt.Errorf("migrate template: %w", err)
	}
	return m.Run(), nil
}

func adminExec(ctx context.Context, sql string) error {
	conn, err := pgx.Connect(ctx, suite.adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	return err
}

func withDatabase(rawURL, name string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(fmt.Sprintf("KG_TEST_DATABASE_URL must be a postgres:// URL: %v", err))
	}
	u.Path = "/" + name
	return u.String()
}

// env is an isolated database with a store, an MCP client session and a
// raw pool for assertions.
type testEnv struct {
	Store *database.Store
	Raw   *pgxpool.Pool
	CS    *mcp.ClientSession
}

type envOption func(*envOptions)

type envOptions struct {
	embedder knowledge.Embedder
}

func withEmbedder(e knowledge.Embedder) envOption {
	return func(o *envOptions) { o.embedder = e }
}

func newEnv(t *testing.T, opts ...envOption) *testEnv {
	t.Helper()
	o := envOptions{embedder: hashing.Embedder{}}
	for _, opt := range opts {
		opt(&o)
	}
	ctx := context.Background()

	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	name := "kg_mcp_test_" + hex.EncodeToString(suffix[:])
	suite.createMu.Lock()
	err := adminExec(ctx, `CREATE DATABASE `+name+` TEMPLATE `+templateName)
	suite.createMu.Unlock()
	require.NoError(t, err)

	dbURL := withDatabase(suite.adminURL, name)
	pool, err := database.OpenPool(ctx, dbURL)
	require.NoError(t, err)
	raw, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	cfg := env.GetDefaultConf[database.Config]()
	cfg.URL = dbURL
	store, err := database.New(pool, database.WithConfig(cfg), database.WithEmbedder(o.embedder))
	require.NoError(t, err)
	require.NoError(t, store.VerifyRoot(ctx))

	e := &testEnv{Store: store, Raw: raw}
	e.CS = connect(t, NewServer(store, Options{Version: "test"}))

	t.Cleanup(func() {
		if !t.Failed() {
			violations, err := store.CheckIntegrity(ctx)
			if err != nil {
				t.Errorf("integrity check: %v", err)
			}
			for _, v := range violations {
				t.Errorf("integrity violation: %s", v)
			}
		}
		store.Close()
		raw.Close()
		_ = adminExec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})
	return e
}

func (e *testEnv) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, e.Raw.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func (e *testEnv) jobs(t *testing.T) int {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM embedding_jobs`)
}

// indexAll drains the embedding queue with the given embedder.
func (e *testEnv) indexAll(t *testing.T, embedder knowledge.Embedder) {
	t.Helper()
	cfg := env.GetDefaultConf[indexing.Config]()
	cfg.BatchSize = 64
	w, err := indexing.NewWorker(e.Store, embedder, indexing.WithConfig(cfg))
	require.NoError(t, err)
	for {
		st, err := w.RunOnce(context.Background())
		require.NoError(t, err)
		if st.Failed > 0 {
			var lastErr string
			_ = e.Raw.QueryRow(context.Background(),
				`SELECT coalesce(last_error, '') FROM embedding_jobs WHERE last_error IS NOT NULL LIMIT 1`).Scan(&lastErr)
			t.Fatalf("%d embedding jobs failed: %s", st.Failed, lastErr)
		}
		if st.Claimed == 0 {
			return
		}
	}
}

// createDoc creates a document via the MCP tool.
func (e *testEnv) createDoc(t *testing.T, title string, parents []string, blocks ...string) CreateDocumentOutput {
	t.Helper()
	bs := make([]map[string]any, len(blocks))
	for i, b := range blocks {
		bs[i] = map[string]any{"content": b}
	}
	return callOK[CreateDocumentOutput](t, e.CS, ToolCreateDocument, map[string]any{
		"title": title, "summary": title + " summary", "parent_ids": parents, "blocks": bs,
	})
}

var rootID = knowledge.RootID.String()
