package database

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/migrations"
)

func TestLoadMigrationsEmbedded(t *testing.T) {
	ms, err := loadMigrations(migrations.FS)
	require.NoError(t, err)
	require.NotEmpty(t, ms)
	assert.Equal(t, int64(1), ms[0].version)
	assert.Equal(t, "init", ms[0].name)
	assert.Contains(t, ms[0].up, "CREATE TABLE documents")
	assert.Contains(t, ms[0].down, "DROP TABLE IF EXISTS documents")
}

func TestLoadMigrationsOrderingAndValidation(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }

	ms, err := loadMigrations(fstest.MapFS{
		"0010_b.up.sql":   file("up b"),
		"0010_b.down.sql": file("down b"),
		"0002_a.up.sql":   file("up a"),
		"0002_a.down.sql": file("down a"),
		"embed.go":        file("package migrations"),
	})
	require.NoError(t, err)
	require.Len(t, ms, 2)
	assert.Equal(t, int64(2), ms[0].version)
	assert.Equal(t, int64(10), ms[1].version)

	_, err = loadMigrations(fstest.MapFS{"0001_a.up.sql": file("up")})
	assert.ErrorContains(t, err, "needs both")

	_, err = loadMigrations(fstest.MapFS{
		"0001_a.up.sql":   file("up"),
		"0001_b.down.sql": file("down"),
	})
	assert.ErrorContains(t, err, "different names")
}

func TestEscapeLike(t *testing.T) {
	assert.Equal(t, `100\% \_x\\y`, escapeLike(`100% _x\y`))
	assert.Equal(t, "AutoMigrate", escapeLike("AutoMigrate"))
}
