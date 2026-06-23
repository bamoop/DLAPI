package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// HashErrorShape parses an error response body and hashes the *structure*
// of the JSON (key paths, sorted), ignoring values. Different relay
// implementations wrap upstream errors in distinctive envelopes
// ({"error":{...}} vs {"success":false,"message":...} vs {"detail":...}),
// which makes this a strong same-source signal.
//
// On parse failure, the hash is computed over the first kilobyte of the
// raw body so plaintext error formats are still distinguishable.
func HashErrorShape(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed any
	if err := common.Unmarshal(body, &parsed); err != nil {
		// fall back to a stable hash of the leading bytes so non-JSON error
		// pages still cluster by exact text
		max := 1024
		if len(body) < max {
			max = len(body)
		}
		sum := sha256.Sum256(body[:max])
		return "raw:" + hex.EncodeToString(sum[:])
	}
	paths := make([]string, 0, 32)
	collectPaths(parsed, "", &paths)
	sort.Strings(paths)
	sum := sha256.Sum256([]byte(strings.Join(paths, "\n")))
	return hex.EncodeToString(sum[:])
}

func collectPaths(node any, prefix string, out *[]string) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			*out = append(*out, path)
			collectPaths(child, path, out)
		}
	case []any:
		// record array presence + element type only; arrays of varying
		// length are normalized to the same path
		path := prefix + "[]"
		*out = append(*out, path)
		if len(v) > 0 {
			collectPaths(v[0], path, out)
		}
	}
}
