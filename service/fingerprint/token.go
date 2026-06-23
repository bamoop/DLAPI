package fingerprint

// ClassifyTokenAccuracy buckets the gap between locally-expected token count
// and the upstream-reported token count. Same bucket across channels = same
// upstream injection behavior (e.g. a relay that prepends a hidden system
// prompt will skew every channel pointing at it by the same delta).
//
// Phase 2 feature — invoked when both expected and reported are > 0.
func ClassifyTokenAccuracy(expected, reported int) string {
	if expected <= 0 || reported <= 0 {
		return ""
	}
	delta := reported - expected
	if delta < 0 {
		delta = -delta
	}
	if delta == 0 {
		return "exact"
	}
	// tolerance: tokenizers occasionally disagree by a couple of tokens
	if delta <= 2 {
		return "near"
	}
	// percentage-bucketed so different prompt sizes still cluster
	pct := (delta * 100) / expected
	switch {
	case pct <= 10:
		return "off-by-small"
	case pct <= 50:
		return "off-by-medium"
	default:
		return "off-by-large"
	}
}
