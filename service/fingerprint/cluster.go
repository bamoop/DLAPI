package fingerprint

import (
	"sort"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

// Cluster groups channels whose fingerprints look like they resolve to the
// same real upstream provider.
type Cluster struct {
	ClusterID    string         `json:"cluster_id"`
	Strength     string         `json:"strength"` // "strong" | "soft"
	ChannelIDs   []int          `json:"channel_ids"`
	SharedHashes map[string]any `json:"shared_hashes,omitempty"`
}

type clusterEntry struct {
	id int
	fp model.UpstreamFingerprint
}

// BuildClusters scans the supplied channels and returns groups of size >= 2
// that share enough fingerprint signal to be considered same-source.
//
// Two passes:
//   - strong: exact CompositeHash equality
//   - soft: union-find — pairs that match in >= 3 of {headers, errors, models, tokenClass}
//
// Channels with no fingerprint or with a stale ProbeVersion are skipped.
func BuildClusters(channels []*model.Channel) []Cluster {
	eligible := make([]clusterEntry, 0, len(channels))
	for _, ch := range channels {
		if ch == nil {
			continue
		}
		fp := ch.ChannelInfo.Fingerprint
		if fp.ProbeVersion < constant.FingerprintProbeVersion {
			continue
		}
		if fp.LastProbedAt == 0 {
			continue
		}
		if fp.CompositeHash == "" {
			continue
		}
		eligible = append(eligible, clusterEntry{id: ch.Id, fp: fp})
	}
	if len(eligible) < 2 {
		return nil
	}

	// strong pass
	byComposite := make(map[string][]clusterEntry)
	for _, e := range eligible {
		byComposite[e.fp.CompositeHash] = append(byComposite[e.fp.CompositeHash], e)
	}

	strongMembers := make(map[int]struct{})
	clusters := make([]Cluster, 0)
	for hash, group := range byComposite {
		if len(group) < 2 {
			continue
		}
		ids := make([]int, 0, len(group))
		for _, e := range group {
			ids = append(ids, e.id)
			strongMembers[e.id] = struct{}{}
		}
		sort.Ints(ids)
		clusters = append(clusters, Cluster{
			ClusterID:  shortID(hash),
			Strength:   "strong",
			ChannelIDs: ids,
			SharedHashes: map[string]any{
				"composite": hash,
				"headers":   group[0].fp.HeaderSetHash,
				"errors":    group[0].fp.ErrorShapeHash,
				"models":    group[0].fp.ModelSetHash,
			},
		})
	}

	// soft pass — union-find over eligible entries not already in a strong cluster
	softCandidates := make([]clusterEntry, 0, len(eligible))
	for _, e := range eligible {
		if _, strong := strongMembers[e.id]; strong {
			continue
		}
		softCandidates = append(softCandidates, e)
	}
	if len(softCandidates) >= 2 {
		clusters = append(clusters, softClusters(softCandidates)...)
	}

	// stable order: by ClusterID
	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].ClusterID < clusters[j].ClusterID
	})
	return clusters
}

func softClusters(entries []clusterEntry) []Cluster {
	n := len(entries)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	matches := func(a, b clusterEntry) int {
		score := 0
		if a.fp.HeaderSetHash != "" && a.fp.HeaderSetHash == b.fp.HeaderSetHash {
			score++
		}
		if a.fp.ErrorShapeHash != "" && a.fp.ErrorShapeHash == b.fp.ErrorShapeHash {
			score++
		}
		if a.fp.ModelSetHash != "" && a.fp.ModelSetHash == b.fp.ModelSetHash {
			score++
		}
		if a.fp.TokenAccuracyClass != "" && a.fp.TokenAccuracyClass == b.fp.TokenAccuracyClass {
			score++
		}
		return score
	}

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if matches(entries[i], entries[j]) >= 3 {
				union(i, j)
			}
		}
	}

	groups := make(map[int][]int)
	for i := range entries {
		root := find(i)
		groups[root] = append(groups[root], i)
	}

	out := make([]Cluster, 0)
	for root, members := range groups {
		if len(members) < 2 {
			continue
		}
		ids := make([]int, 0, len(members))
		for _, idx := range members {
			ids = append(ids, entries[idx].id)
		}
		sort.Ints(ids)
		seed := entries[root].fp
		out = append(out, Cluster{
			ClusterID:  "soft-" + shortID(seed.CompositeHash),
			Strength:   "soft",
			ChannelIDs: ids,
			SharedHashes: map[string]any{
				"headers": seed.HeaderSetHash,
				"errors":  seed.ErrorShapeHash,
				"models":  seed.ModelSetHash,
			},
		})
	}
	return out
}

func shortID(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:8]
}
