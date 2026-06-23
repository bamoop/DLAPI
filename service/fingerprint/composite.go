package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/QuantumNous/new-api/model"
)

// Compose populates the CompositeHash field of the fingerprint from the
// individual hash components. Empty components are skipped so partial
// fingerprints still produce a stable composite.
func Compose(fp *model.UpstreamFingerprint) {
	if fp == nil {
		return
	}
	parts := []string{
		"h=" + fp.HeaderSetHash,
		"e=" + fp.ErrorShapeHash,
		"m=" + fp.ModelSetHash,
		"s=" + fp.SSESequenceHash,
		"t=" + fp.TokenAccuracyClass,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	fp.CompositeHash = hex.EncodeToString(sum[:])
}
