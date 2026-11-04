package knowledge

import (
	"fmt"
	"math"
)

// PositionGap is the distance between block positions after initial
// numbering and after rebalancing (sparse ordering). Position values are
// internal; the public block order is ORDER BY position, id.
const PositionGap int64 = 1_000_000

// MaxBlocksPerDocument is the largest block count that can be numbered with
// PositionGap without exceeding int64.
const MaxBlocksPerDocument = math.MaxInt64 / PositionGap

// FirstPosition is the position of the first block of an empty document.
const FirstPosition = PositionGap

// PositionBetween returns the midpoint strictly between left and right
// (left < right). ok is false if no integer lies between them.
func PositionBetween(left, right int64) (pos int64, ok bool) {
	if right-left <= 1 { // no overflow: both are non-negative
		return 0, false
	}
	return left + (right-left)/2, true
}

// PositionAfter returns the position for a block appended after last. ok is
// false if last + PositionGap would overflow int64.
func PositionAfter(last int64) (pos int64, ok bool) {
	if last > math.MaxInt64-PositionGap {
		return 0, false
	}
	return last + PositionGap, true
}

// PositionBefore returns the position for a block prepended before first.
// Positions never become negative; ok is false if first <= 1.
func PositionBefore(first int64) (pos int64, ok bool) {
	if first <= 1 {
		return 0, false
	}
	return first / 2, true
}

// SequentialPositions returns the positions 1*PositionGap .. n*PositionGap
// used for initial numbering and rebalancing.
func SequentialPositions(n int) ([]int64, error) {
	if int64(n) > MaxBlocksPerDocument {
		return nil, fmt.Errorf("%w: %d blocks exceed the maximum of %d per document",
			ErrInvalidInput, n, MaxBlocksPerDocument)
	}
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i+1) * PositionGap
	}
	return out, nil
}
