package fingerprint

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
)

func TestCompose_DeterministicAndDistinct(t *testing.T) {
	a := model.UpstreamFingerprint{HeaderSetHash: "h1", ErrorShapeHash: "e1", ModelSetHash: "m1"}
	b := model.UpstreamFingerprint{HeaderSetHash: "h1", ErrorShapeHash: "e1", ModelSetHash: "m1"}
	c := model.UpstreamFingerprint{HeaderSetHash: "h1", ErrorShapeHash: "e2", ModelSetHash: "m1"}

	Compose(&a)
	Compose(&b)
	Compose(&c)

	if a.CompositeHash == "" {
		t.Fatal("composite hash should not be empty")
	}
	if a.CompositeHash != b.CompositeHash {
		t.Fatal("identical inputs should produce identical composite hash")
	}
	if a.CompositeHash == c.CompositeHash {
		t.Fatal("differing components should produce differing composite hash")
	}
}

func TestCompose_NilSafe(t *testing.T) {
	Compose(nil) // must not panic
}

func TestCompose_PartialComponents(t *testing.T) {
	// only headers — should still produce a stable composite
	a := model.UpstreamFingerprint{HeaderSetHash: "h1"}
	b := model.UpstreamFingerprint{HeaderSetHash: "h1"}
	Compose(&a)
	Compose(&b)
	if a.CompositeHash == "" || a.CompositeHash != b.CompositeHash {
		t.Fatal("partial composite should still match itself")
	}
}
