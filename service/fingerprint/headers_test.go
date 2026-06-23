package fingerprint

import (
	"net/http"
	"testing"
)

func TestHashHeaders_StableAcrossRandomIDs(t *testing.T) {
	a := http.Header{}
	a.Set("X-Request-Id", "req_abc1234567")
	a.Set("Cf-Ray", "8a1f2b3c4d5e6f70-SJC")
	a.Set("Anthropic-Ratelimit-Requests-Remaining", "42")
	a.Set("Date", "Mon, 01 Jan 2026 00:00:00 GMT")
	a.Set("Content-Length", "1234")

	b := http.Header{}
	b.Set("X-Request-Id", "req_xyz9876543")
	b.Set("Cf-Ray", "9b2e3f4a5d6c7e80-LHR")
	b.Set("Anthropic-Ratelimit-Requests-Remaining", "5")
	b.Set("Date", "Tue, 02 Jan 2026 12:00:00 GMT")
	b.Set("Content-Length", "9999")

	if HashHeaders(a) != HashHeaders(b) {
		t.Fatalf("hashes should match: only rotating values differ")
	}
}

func TestHashHeaders_DiffersOnStructureChange(t *testing.T) {
	a := http.Header{}
	a.Set("X-Request-Id", "req_abc1234567")
	a.Set("Anthropic-Ratelimit-Requests-Remaining", "42")

	b := http.Header{}
	b.Set("X-Request-Id", "req_xyz9876543")
	// missing the ratelimit header — different upstream shape

	if HashHeaders(a) == HashHeaders(b) {
		t.Fatalf("hashes should differ when header set differs")
	}
}

func TestHashHeaders_DiffersOnValueShape(t *testing.T) {
	a := http.Header{}
	a.Set("X-Request-Id", "req_abc1234567") // prefixed style

	b := http.Header{}
	b.Set("X-Request-Id", "12345") // plain int style

	if HashHeaders(a) == HashHeaders(b) {
		t.Fatalf("hashes should differ when value patterns differ")
	}
}

func TestHashHeaders_EmptyReturnsEmpty(t *testing.T) {
	if HashHeaders(nil) != "" {
		t.Fatal("nil headers should hash to empty string")
	}
	if HashHeaders(http.Header{}) != "" {
		t.Fatal("empty headers should hash to empty string")
	}
}
