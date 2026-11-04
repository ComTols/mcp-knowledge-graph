//go:build integration

package database_test

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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"mcp-knowledge-graph/internal/adapter/database"
	"mcp-knowledge-graph/internal/adapter/embedding/hashing"
	env "mcp-knowledge-graph/internal/environment"
	"mcp-knowledge-graph/internal/indexing"
	"mcp-knowledge-graph/internal/knowledge"
)

// The integration tests run against a real PostgreSQL with pgvector.
//
//   - Default: a pgvector/pgvector container is started via testcontainers.
//   - KG_TEST_DATABASE_URL (postgres:// URL of a role with CREATEDB) uses an
//     existing server instead, e.g. the one from docker-compose.yml.
//
// A template database is migrated once; every test gets its own copy, so
// tests are isolated and can run in parallel.

const (
	pgImage      = "pgvector/pgvector:pg17"
	templateName = "kg_template"
)

var suite struct {
	adminURL  string // URL of the maintenance database
	container *tcpostgres.PostgresContainer
	createMu  sync.Mutex
}

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		log.Printf("integration test setup failed: %v", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	ctx := context.Background()

	suite.adminURL = os.Getenv("KG_TEST_DATABASE_URL")
	if suite.adminURL == "" {
		ctr, err := startContainer(ctx)
		if err != nil {
			return 0, err
		}
		suite.container = ctr
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
	if err := database.Migrate(ctx, withDatabase(suite.adminURL, templateName)); err != nil {
		return 0, fmt.Errorf("migrate template: %w", err)
	}
	defer func() { _ = adminExec(ctx, `DROP DATABASE IF EXISTS `+templateName+` WITH (FORCE)`) }()

	return m.Run(), nil
}

func startContainer(ctx context.Context) (*tcpostgres.PostgresContainer, error) {
	return tcpostgres.Run(ctx, pgImage,
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
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

// testDB is an isolated database for a single test.
type testDB struct {
	URL   string
	Store *database.Store
	Raw   *pgxpool.Pool // direct access for assertions and fault injection
}

// newEmptyDB creates an empty, unmigrated database and returns its URL and a
// plain pool (without pgvector type registration).
func newEmptyDB(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	name := randomDBName()
	suite.createMu.Lock()
	err := adminExec(ctx, `CREATE DATABASE `+name)
	suite.createMu.Unlock()
	require.NoError(t, err)

	dbURL := withDatabase(suite.adminURL, name)
	raw, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	t.Cleanup(func() {
		raw.Close()
		_ = adminExec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})
	return dbURL, raw
}

func randomDBName() string {
	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	return "kg_test_" + hex.EncodeToString(suffix[:])
}

// newTestDB creates a fresh, migrated database and a Store using the
// hashing stub embedder. Integrity is checked when the test finishes.
func newTestDB(t *testing.T, opts ...database.Option) *testDB {
	t.Helper()
	ctx := context.Background()

	name := randomDBName()

	suite.createMu.Lock()
	err := adminExec(ctx, `CREATE DATABASE `+name+` TEMPLATE `+templateName)
	suite.createMu.Unlock()
	require.NoError(t, err)

	dbURL := withDatabase(suite.adminURL, name)
	pool, err := database.OpenPool(ctx, dbURL)
	require.NoError(t, err)
	raw, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)

	// Defaults first; options of the test may override them.
	opts = append([]database.Option{
		database.WithConfig(testConfig(dbURL, nil)),
		database.WithEmbedder(hashing.Embedder{}),
	}, opts...)
	store, err := database.New(pool, opts...)
	require.NoError(t, err)
	db := &testDB{URL: dbURL, Store: store, Raw: raw}
	require.NoError(t, db.Store.VerifyRoot(ctx))

	t.Cleanup(func() {
		if !t.Failed() {
			violations, err := db.Store.CheckIntegrity(ctx)
			if err != nil {
				t.Errorf("integrity check: %v", err)
			}
			for _, v := range violations {
				t.Errorf("integrity violation: %s", v)
			}
		}
		db.Store.Close()
		raw.Close()
		_ = adminExec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})
	return db
}

// create is a shortcut for creating a document below the given parents.
func (db *testDB) create(t *testing.T, title, summary string, parents ...uuid.UUID) knowledge.DocumentSummary {
	t.Helper()
	d, err := db.Store.CreateDocument(context.Background(), knowledge.CreateDocumentInput{
		Title: title, Summary: summary, ParentIDs: parents,
	})
	require.NoError(t, err)
	return d.DocumentSummary
}

// indexAll runs the embedding worker until the queue is drained.
func (db *testDB) indexAll(t *testing.T) indexing.Stats {
	t.Helper()
	return indexWith(t, db.Store, hashing.Embedder{})
}

func indexWith(t *testing.T, store indexing.JobStore, e knowledge.Embedder) indexing.Stats {
	t.Helper()
	w := newWorker(t, store, e, func(c *indexing.Config) { c.BatchSize = 64 })
	var total indexing.Stats
	for {
		st, err := w.RunOnce(context.Background())
		require.NoError(t, err)
		total.Claimed += st.Claimed
		total.Stored += st.Stored
		total.Stale += st.Stale
		total.Failed += st.Failed
		if st.Claimed == 0 {
			return total
		}
	}
}

// testConfig returns the default database configuration (independent of the
// process environment) for dbURL, optionally adjusted by modify.
func testConfig(dbURL string, modify func(*database.Config)) *database.Config {
	cfg := env.GetDefaultConf[database.Config]()
	cfg.URL = dbURL
	if modify != nil {
		modify(cfg)
	}
	return cfg
}

// newWorker returns a worker with the default configuration, optionally
// adjusted by modify.
func newWorker(t *testing.T, store indexing.JobStore, e knowledge.Embedder, modify func(*indexing.Config)) *indexing.Worker {
	t.Helper()
	cfg := env.GetDefaultConf[indexing.Config]()
	if modify != nil {
		modify(cfg)
	}
	w, err := indexing.NewWorker(store, e, indexing.WithConfig(cfg))
	require.NoError(t, err)
	return w
}

func (db *testDB) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.Raw.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

// eventually retries fn until it succeeds or the timeout expires.
func eventually(timeout time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	for {
		err := fn()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func ptr[T any](v T) *T { return &v }
