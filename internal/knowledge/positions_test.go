package knowledge_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/knowledge"
)

func TestPositionBetween(t *testing.T) {
	pos, ok := knowledge.PositionBetween(1_000_000, 2_000_000)
	assert.True(t, ok)
	assert.Equal(t, int64(1_500_000), pos)

	pos, ok = knowledge.PositionBetween(1_000_000, 1_000_002)
	assert.True(t, ok)
	assert.Equal(t, int64(1_000_001), pos)

	_, ok = knowledge.PositionBetween(1_000_000, 1_000_001)
	assert.False(t, ok)

	pos, ok = knowledge.PositionBetween(0, math.MaxInt64)
	assert.True(t, ok, "no overflow at the extremes")
	assert.Equal(t, int64(math.MaxInt64/2), pos)
}

func TestPositionAfter(t *testing.T) {
	pos, ok := knowledge.PositionAfter(3_000_000)
	assert.True(t, ok)
	assert.Equal(t, int64(4_000_000), pos)

	pos, ok = knowledge.PositionAfter(math.MaxInt64 - knowledge.PositionGap)
	assert.True(t, ok)
	assert.Equal(t, int64(math.MaxInt64), pos)

	_, ok = knowledge.PositionAfter(math.MaxInt64 - knowledge.PositionGap + 1)
	assert.False(t, ok, "overflow is detected")
}

func TestPositionBefore(t *testing.T) {
	pos, ok := knowledge.PositionBefore(1_000_000)
	assert.True(t, ok)
	assert.Equal(t, int64(500_000), pos)

	pos, ok = knowledge.PositionBefore(2)
	assert.True(t, ok)
	assert.Equal(t, int64(1), pos)

	_, ok = knowledge.PositionBefore(1)
	assert.False(t, ok)
	_, ok = knowledge.PositionBefore(0)
	assert.False(t, ok)
}

func TestSequentialPositions(t *testing.T) {
	got, err := knowledge.SequentialPositions(3)
	require.NoError(t, err)
	assert.Equal(t, []int64{1_000_000, 2_000_000, 3_000_000}, got)

	got, err = knowledge.SequentialPositions(0)
	require.NoError(t, err)
	assert.Empty(t, got)

	_, err = knowledge.SequentialPositions(int(knowledge.MaxBlocksPerDocument) + 1)
	assert.ErrorIs(t, err, knowledge.ErrInvalidInput)
}
