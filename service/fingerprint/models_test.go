package fingerprint

import "testing"

func TestHashModelSet_OrderAndCaseInsensitive(t *testing.T) {
	a := []string{"gpt-4o", "claude-3-5-sonnet", "gemini-1.5-pro"}
	b := []string{"GEMINI-1.5-PRO", " gpt-4o ", "claude-3-5-sonnet"}
	if HashModelSet(a) != HashModelSet(b) {
		t.Fatal("model set hash should be order-/case-insensitive")
	}
}

func TestHashModelSet_DiffersByMembership(t *testing.T) {
	a := []string{"gpt-4o", "claude-3-5-sonnet"}
	b := []string{"gpt-4o", "claude-3-5-sonnet", "gemini-1.5-pro"}
	if HashModelSet(a) == HashModelSet(b) {
		t.Fatal("different model sets should hash differently")
	}
}

func TestHashModelSet_DedupesDuplicates(t *testing.T) {
	a := []string{"gpt-4o", "gpt-4o", "gpt-4o"}
	b := []string{"gpt-4o"}
	if HashModelSet(a) != HashModelSet(b) {
		t.Fatal("duplicates should be deduped")
	}
}

func TestHashModelSet_EmptyReturnsEmpty(t *testing.T) {
	if HashModelSet(nil) != "" {
		t.Fatal("nil should return empty")
	}
	if HashModelSet([]string{"  ", ""}) != "" {
		t.Fatal("all-blank should return empty")
	}
}
