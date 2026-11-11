package core

import (
	"crypto/sha256"
	"encoding/binary"
)

// InSample reports whether an item is in a fixed share of all items,
// decided by its content ID alone: the same item is always in or out, so a
// re-send or an edit keeps its place (the owner's D-053, 11 Nov 2026:
// analyse 10% of comments). A share of 1 or more takes every item; 0 or
// less, none.
func InSample(contentID string, share float64) bool {
	if share >= 1 {
		return true
	}
	if share <= 0 {
		return false
	}
	h := sha256.Sum256([]byte("sb-analysis sample:" + contentID))
	// The top 53 bits as a fraction in [0, 1).
	return float64(binary.BigEndian.Uint64(h[:8])>>11)/(1<<53) < share
}
