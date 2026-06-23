package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HashSSESequence hashes the ordered sequence of SSE event types observed
// during a streaming response. Different relay implementations buffer or
// rewrite event boundaries (e.g. merging deltas, dropping ping events),
// so the sequence pattern is a useful same-source signal.
//
// Phase 3 feature.
func HashSSESequence(eventTypes []string) string {
	if len(eventTypes) == 0 {
		return ""
	}
	normalized := make([]string, 0, len(eventTypes))
	for _, t := range eventTypes {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		normalized = append(normalized, t)
	}
	if len(normalized) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(normalized, "\n")))
	return hex.EncodeToString(sum[:])
}
