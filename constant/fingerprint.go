package constant

// FingerprintProbeVersion identifies the schema/algorithm version of the
// upstream fingerprint probe. Bump it whenever the hashing logic changes so
// that stored fingerprints from older versions are treated as stale and
// re-probed on the next channel test cycle.
const FingerprintProbeVersion = 1

// FingerprintProbeMinIntervalSeconds is the minimum gap between two probes
// for the same channel. Probes piggyback on channel tests; this guard avoids
// re-running the extra HTTP calls on every manual test click.
const FingerprintProbeMinIntervalSeconds = 600
