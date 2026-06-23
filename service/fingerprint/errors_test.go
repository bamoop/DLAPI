package fingerprint

import "testing"

func TestHashErrorShape_IgnoresValues(t *testing.T) {
	a := []byte(`{"error":{"type":"invalid_request_error","message":"foo"}}`)
	b := []byte(`{"error":{"type":"different_type","message":"bar baz"}}`)
	if HashErrorShape(a) != HashErrorShape(b) {
		t.Fatal("error shape hash should ignore values")
	}
}

func TestHashErrorShape_DiffersByEnvelope(t *testing.T) {
	openaiStyle := []byte(`{"error":{"type":"invalid_request_error","message":"bad"}}`)
	relayStyle := []byte(`{"success":false,"message":"bad","code":400}`)
	if HashErrorShape(openaiStyle) == HashErrorShape(relayStyle) {
		t.Fatal("different error envelopes should hash differently")
	}
}

func TestHashErrorShape_NonJSONFallsBackToRawHash(t *testing.T) {
	plain := []byte("Internal Server Error")
	h := HashErrorShape(plain)
	if h == "" {
		t.Fatal("non-JSON body should still produce a hash")
	}
	if h[:4] != "raw:" {
		t.Fatalf("expected raw: prefix for non-JSON, got %q", h)
	}
	if HashErrorShape(plain) != h {
		t.Fatal("raw hash should be stable")
	}
}

func TestHashErrorShape_EmptyReturnsEmpty(t *testing.T) {
	if HashErrorShape(nil) != "" {
		t.Fatal("nil body should return empty")
	}
}
