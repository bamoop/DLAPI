package fingerprint

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

func chanWith(id int, fp model.UpstreamFingerprint) *model.Channel {
	ch := &model.Channel{Id: id}
	ch.ChannelInfo.Fingerprint = fp
	return ch
}

func validFP(h, e, m, t string, composite string) model.UpstreamFingerprint {
	return model.UpstreamFingerprint{
		HeaderSetHash:      h,
		ErrorShapeHash:     e,
		ModelSetHash:       m,
		TokenAccuracyClass: t,
		CompositeHash:      composite,
		LastProbedAt:       1700000000,
		ProbeVersion:       constant.FingerprintProbeVersion,
	}
}

func TestBuildClusters_StrongByCompositeHash(t *testing.T) {
	channels := []*model.Channel{
		chanWith(1, validFP("h1", "e1", "m1", "exact", "C1")),
		chanWith(2, validFP("h1", "e1", "m1", "exact", "C1")),
		chanWith(3, validFP("hX", "eX", "mX", "exact", "C2")),
	}
	clusters := BuildClusters(channels)
	if len(clusters) != 1 {
		t.Fatalf("expected 1 strong cluster, got %d", len(clusters))
	}
	if clusters[0].Strength != "strong" {
		t.Fatalf("expected strong, got %s", clusters[0].Strength)
	}
	if len(clusters[0].ChannelIDs) != 2 {
		t.Fatalf("expected 2 members, got %d", len(clusters[0].ChannelIDs))
	}
}

func TestBuildClusters_SoftByThreeOfFour(t *testing.T) {
	channels := []*model.Channel{
		chanWith(1, validFP("h1", "e1", "m1", "exact", "C1")),
		// matches on headers, errors, models — but token class differs
		chanWith(2, validFP("h1", "e1", "m1", "near", "C2")),
		// matches only on headers — should not cluster
		chanWith(3, validFP("h1", "eY", "mY", "off-by-large", "C3")),
	}
	clusters := BuildClusters(channels)
	if len(clusters) != 1 {
		t.Fatalf("expected 1 soft cluster, got %d", len(clusters))
	}
	if clusters[0].Strength != "soft" {
		t.Fatalf("expected soft, got %s", clusters[0].Strength)
	}
	if len(clusters[0].ChannelIDs) != 2 {
		t.Fatalf("expected 2 members, got %v", clusters[0].ChannelIDs)
	}
	if clusters[0].ChannelIDs[0] != 1 || clusters[0].ChannelIDs[1] != 2 {
		t.Fatalf("unexpected members: %v", clusters[0].ChannelIDs)
	}
}

func TestBuildClusters_SkipsUnprobedAndStale(t *testing.T) {
	channels := []*model.Channel{
		// not probed
		chanWith(1, model.UpstreamFingerprint{CompositeHash: "C1"}),
		// stale version
		chanWith(2, model.UpstreamFingerprint{
			CompositeHash: "C1",
			LastProbedAt:  1700000000,
			ProbeVersion:  constant.FingerprintProbeVersion - 1,
		}),
	}
	clusters := BuildClusters(channels)
	if len(clusters) != 0 {
		t.Fatalf("expected 0 clusters, got %d", len(clusters))
	}
}

func TestBuildClusters_StrongPrefersOverSoft(t *testing.T) {
	channels := []*model.Channel{
		chanWith(1, validFP("h1", "e1", "m1", "exact", "C1")),
		chanWith(2, validFP("h1", "e1", "m1", "exact", "C1")),
		chanWith(3, validFP("h1", "e1", "m1", "near", "C2")),
	}
	clusters := BuildClusters(channels)
	// channels 1,2 -> strong; channel 3 should NOT be soft-grouped
	// with strong members since they're removed before soft pass
	strongFound := false
	for _, cl := range clusters {
		if cl.Strength == "strong" {
			strongFound = true
			if len(cl.ChannelIDs) != 2 {
				t.Fatalf("strong cluster should have 2 members, got %d", len(cl.ChannelIDs))
			}
		}
	}
	if !strongFound {
		t.Fatal("expected at least one strong cluster")
	}
}
