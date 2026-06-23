package fingerprint

import "testing"

func TestTokenCorpus_BlocksStableAndNonEmpty(t *testing.T) {
	if TokenCorpusBase() == "" {
		t.Fatal("corpus base must not be empty")
	}
	blocks := TokenCorpusBlocks()
	if len(blocks) != 3 {
		t.Fatalf("expected 3 corpus blocks, got %d", len(blocks))
	}
	seen := map[string]bool{}
	for _, b := range blocks {
		if b.Name == "" || b.Text == "" {
			t.Fatalf("block %q has empty name/text", b.Name)
		}
		if seen[b.Name] {
			t.Fatalf("duplicate block name %q", b.Name)
		}
		seen[b.Name] = true
		// every block must have a truth range registered
		if _, ok := claudeTokenDeltaTruth[b.Name]; !ok {
			t.Fatalf("block %q has no truth range", b.Name)
		}
	}
}

func TestClassifyTokenDelta_InRangeAndOut(t *testing.T) {
	tr := claudeTokenDeltaTruth["cjk"]
	// in-range midpoint
	mid := (tr.Low + tr.High) / 2
	if v := ClassifyTokenDelta("cjk", mid); !v.InRange || !v.HasTruth {
		t.Fatalf("midpoint %d should be in range [%d,%d]", mid, tr.Low, tr.High)
	}
	// below range
	if v := ClassifyTokenDelta("cjk", tr.Low-1); v.InRange {
		t.Fatal("below-range value should not be InRange")
	}
	// unknown block has no truth
	if v := ClassifyTokenDelta("nonexistent", 50); v.HasTruth {
		t.Fatal("unknown block should report HasTruth=false")
	}
}

func TestTierOfModel(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-7":           "opus",
		"claude-sonnet-4-6":         "sonnet",
		"claude-haiku-4-5-20251001": "haiku",
		"gpt-4o":                    "",
	}
	for model, want := range cases {
		if got := tierOfModel(model); got != want {
			t.Fatalf("tierOfModel(%q)=%q, want %q", model, got, want)
		}
	}
}

func TestClassifyThroughputTier_DowngradeSuspect(t *testing.T) {
	// Claimed opus, observed far above opus max → downgrade suspect.
	var opusMax float64
	for _, tr := range claudeThroughputTiers {
		if tr.Tier == "opus" {
			opusMax = tr.MaxTokPerSec
		}
	}
	// 用 haiku 高速带的值（远超 opus 上限）作为「宣称 opus 实测却是 haiku 级」场景。
	var haikuMin float64
	for _, tr := range claudeThroughputTiers {
		if tr.Tier == "haiku" {
			haikuMin = tr.MinTokPerSec
		}
	}
	v := ClassifyThroughputTier("claude-opus-4-7", haikuMin+20)
	if !v.DowngradeSuspect {
		t.Fatalf("observed %.0f tok/s (haiku-level) for claimed opus should be downgrade suspect", haikuMin+20)
	}
	// Decisive 要求：已标定 + 落在某个更廉价档。haiku 级吞吐应给出 decisive。
	if baselineCalibrated && !v.Decisive {
		t.Fatal("calibrated baseline + haiku-level throughput for claimed opus should be decisive")
	}
	_ = opusMax
	// No observed throughput → empty verdict.
	if v := ClassifyThroughputTier("claude-opus-4-7", 0); v.DowngradeSuspect || len(v.FitTiers) != 0 {
		t.Fatal("zero throughput should yield empty verdict")
	}
	// Unknown claimed model → no tier, no suspect.
	if v := ClassifyThroughputTier("gpt-4o", 999); v.ClaimedTier != "" || v.DowngradeSuspect {
		t.Fatal("unknown claimed model should not produce a tier verdict")
	}
}
