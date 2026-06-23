package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Headers that vary on every request or carry no upstream-identity signal.
// Excluding them keeps the hash stable across calls to the same upstream.
var ignoredHeaders = map[string]struct{}{
	"content-length":      {},
	"content-encoding":    {},
	"date":                {},
	"connection":          {},
	"keep-alive":          {},
	"transfer-encoding":   {},
	"proxy-connection":    {},
	"upgrade":             {},
	"te":                  {},
	"trailer":             {},
	"set-cookie":          {},
	"age":                 {},
	"expires":             {},
	"last-modified":       {},
	"etag":                {},
	"vary":                {},
	"accept-ranges":       {},
	"content-disposition": {},
	"alt-svc":             {},
}

var (
	rePatHex32    = regexp.MustCompile(`^[0-9a-fA-F]{32,}$`)
	rePatHexLong  = regexp.MustCompile(`^[0-9a-fA-F]{12,}$`)
	rePatUUID     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	rePatInt      = regexp.MustCompile(`^-?\d+$`)
	rePatFloat    = regexp.MustCompile(`^-?\d+\.\d+$`)
	rePatISO8601  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)
	rePatPrefix   = regexp.MustCompile(`^[A-Za-z_]+[_-][0-9A-Za-z]{6,}$`) // e.g. req_abc123, msg_01ABC...
	rePatHexDash  = regexp.MustCompile(`^[0-9a-fA-F]{8,}-[A-Za-z0-9]{2,}$`) // e.g. cf-ray 8a1f2b3c-SJC
	rePatAlphaNum = regexp.MustCompile(`^[A-Za-z0-9]+$`)
)

// classifyValue replaces a header value with a coarse pattern token so that
// rotating IDs/timestamps don't change the hash, but structural differences
// (e.g. plain string vs UUID vs prefixed ID) still do.
func classifyValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "empty"
	}
	switch {
	case rePatUUID.MatchString(v):
		return "uuid"
	case rePatHex32.MatchString(v):
		return "hex32"
	case rePatISO8601.MatchString(v):
		return "iso8601"
	case rePatInt.MatchString(v):
		return "int"
	case rePatFloat.MatchString(v):
		return "float"
	case rePatHexDash.MatchString(v):
		// cf-ray style: long hex prefix + suffix
		return "hex-suffix"
	case rePatPrefix.MatchString(v):
		// preserve the prefix portion so anthropic req_ ids stay distinct
		// from openai sess_ ids
		idx := strings.IndexAny(v, "_-")
		if idx > 0 && idx < len(v)-1 {
			return "prefixed:" + strings.ToLower(v[:idx])
		}
		return "prefixed"
	case rePatHexLong.MatchString(v):
		return "hex"
	case rePatAlphaNum.MatchString(v) && len(v) >= 16:
		return "opaque-id"
	default:
		return "literal:" + strings.ToLower(v)
	}
}

// HashHeaders produces a deterministic fingerprint of the response headers
// suitable for clustering. It captures the *set* of header names plus the
// *shape* of their values, not the values themselves.
func HashHeaders(h http.Header) string {
	if len(h) == 0 {
		return ""
	}
	type kv struct {
		name     string
		patterns []string
	}
	items := make([]kv, 0, len(h))
	for name, values := range h {
		lower := strings.ToLower(name)
		if _, skip := ignoredHeaders[lower]; skip {
			continue
		}
		patterns := make([]string, 0, len(values))
		for _, v := range values {
			patterns = append(patterns, classifyValue(v))
		}
		sort.Strings(patterns)
		items = append(items, kv{name: lower, patterns: patterns})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].name < items[j].name
	})

	var sb strings.Builder
	for _, it := range items {
		sb.WriteString(it.name)
		sb.WriteByte('=')
		sb.WriteString(strings.Join(it.patterns, ","))
		sb.WriteByte(';')
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}
