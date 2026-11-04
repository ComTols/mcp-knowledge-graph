package database

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"

	"mcp-knowledge-graph/migrations"
)

// migrationLockKey serializes concurrent migrators (session advisory lock).
const migrationLockKey int64 = 7654321000

var migrationFileRE = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.(up|down)\.sql$`)

type migration struct {
	version int64
	name    string
	up      string
	down    string
}

// loadMigrations reads and validates all migrations of fsys, ordered by
// version. Every version needs both an up and a down file.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	byVersion := make(map[int64]*migration)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFileRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue // e.g. embed.go
		}
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("migration %s: invalid version", e.Name())
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		mig, ok := byVersion[version]
		if !ok {
			mig = &migration{version: version, name: m[2]}
			byVersion[version] = mig
		} else if mig.name != m[2] {
			return nil, fmt.Errorf("migration version %d used with different names %q and %q", version, mig.name, m[2])
		}
		if m[3] == "up" {
			mig.up = string(body)
		} else {
			mig.down = string(body)
		}
	}

	out := make([]migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.up == "" || m.down == "" {
			return nil, fmt.Errorf("migration %d_%s needs both an up and a down file", m.version, m.name)
		}
		out = append(out, *m)
	}
	slices.SortFunc(out, func(a, b migration) int { return cmp.Compare(a.version, b.version) })
	return out, nil
}

// Migrate applies all pending up migrations. It is idempotent and safe to
// run concurrently from several processes. It uses its own connection
// because the pool's type registration requires the vector extension.
func Migrate(ctx context.Context, databaseURL string) error {
	return withMigrationConn(ctx, databaseURL, func(conn *pgx.Conn, ms []migration) error {
		applied, err := appliedVersions(ctx, conn)
		if err != nil {
			return err
		}
		known := make(map[int64]bool, len(ms))
		for _, m := range ms {
			known[m.version] = true
		}
		for v := range applied {
			if !known[v] {
				return fmt.Errorf("database schema version %d is unknown to this binary (newer schema?)", v)
			}
		}
		for _, m := range ms {
			if applied[m.version] {
				continue
			}
			err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, m.up); err != nil {
					return err
				}
				_, err := tx.Exec(ctx,
					`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name)
				return err
			})
			if err != nil {
				return fmt.Errorf("apply migration %d_%s: %w", m.version, m.name, err)
			}
		}
		return nil
	})
}

// MigrateDown reverts the given number of most recently applied migrations.
func MigrateDown(ctx context.Context, databaseURL string, steps int) error {
	if steps <= 0 {
		return nil
	}
	return withMigrationConn(ctx, databaseURL, func(conn *pgx.Conn, ms []migration) error {
		applied, err := appliedVersions(ctx, conn)
		if err != nil {
			return err
		}
		for i := len(ms) - 1; i >= 0 && steps > 0; i-- {
			m := ms[i]
			if !applied[m.version] {
				continue
			}
			err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, m.down); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, m.version)
				return err
			})
			if err != nil {
				return fmt.Errorf("revert migration %d_%s: %w", m.version, m.name, err)
			}
			steps--
		}
		return nil
	})
}

func withMigrationConn(ctx context.Context, databaseURL string, fn func(*pgx.Conn, []migration) error) (err error) {
	ms, err := loadMigrations(migrations.FS)
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect for migration: %w", err)
	}
	defer func() {
		_ = conn.Close(context.WithoutCancel(ctx))
	}()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, unlockErr := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockKey)
		if err == nil && unlockErr != nil {
			err = fmt.Errorf("release migration lock: %w", unlockErr)
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    BIGINT PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return fn(conn, ms)
}

func appliedVersions(ctx context.Context, conn *pgx.Conn) (map[int64]bool, error) {
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	applied := make(map[int64]bool, len(versions))
	for _, v := range versions {
		applied[v] = true
	}
	return applied, nil
}
