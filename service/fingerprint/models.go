package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// HashModelSet produces a deterministic fingerprint of the supported model
// IDs reported by the upstream. Same model set = strong signal that two
// channels resolve to the same real upstream, since each relay vendor
// curates its model catalog differently.
func HashModelSet(models []string) string {
	if len(models) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(models))
	normalized := make([]string, 0, len(models))
	for _, m := range models {
		t := strings.ToLower(strings.TrimSpace(m))
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		normalized = append(normalized, t)
	}
	if len(normalized) == 0 {
		return ""
	}
	sort.Strings(normalized)
	sum := sha256.Sum256([]byte(strings.Join(normalized, "\n")))
	return hex.EncodeToString(sum[:])
}
