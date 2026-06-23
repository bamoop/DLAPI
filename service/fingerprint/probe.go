package fingerprint

import (
	"net/http"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

// ProbeInputs carries the raw signals gathered by the caller (controller
// layer) so that this package stays free of upstream HTTP dependencies.
//
// Any field may be left zero — partial inputs still produce a valid (but
// less discriminating) composite hash.
type ProbeInputs struct {
	SuccessHeaders http.Header // headers from a normal successful response
	ErrorBody      []byte      // body of an intentionally invalid request
	UpstreamModels []string    // result of /v1/models style probe
	SSEEventTypes  []string    // phase 3: event type sequence from a stream test
	ExpectedTokens int         // phase 2: locally counted input tokens
	ReportedTokens int         // phase 2: usage.input_tokens from upstream
}

// Build returns a fully composed UpstreamFingerprint from the supplied
// signals. The caller is responsible for stamping LastProbedAt before
// persisting.
func Build(inputs ProbeInputs) model.UpstreamFingerprint {
	fp := model.UpstreamFingerprint{
		HeaderSetHash:  HashHeaders(inputs.SuccessHeaders),
		ErrorShapeHash: HashErrorShape(inputs.ErrorBody),
		ModelSetHash:   HashModelSet(inputs.UpstreamModels),
		ProbeVersion:   constant.FingerprintProbeVersion,
	}
	if len(inputs.SSEEventTypes) > 0 {
		fp.SSESequenceHash = HashSSESequence(inputs.SSEEventTypes)
	}
	if inputs.ExpectedTokens > 0 && inputs.ReportedTokens > 0 {
		fp.TokenAccuracyClass = ClassifyTokenAccuracy(inputs.ExpectedTokens, inputs.ReportedTokens)
	}
	Compose(&fp)
	return fp
}

// ShouldProbe returns true when enough time has elapsed since the last
// successful probe, or when the stored fingerprint was produced by an older
// algorithm version.
func ShouldProbe(fp model.UpstreamFingerprint, nowUnix int64) bool {
	if fp.ProbeVersion < constant.FingerprintProbeVersion {
		return true
	}
	if fp.LastProbedAt == 0 {
		return true
	}
	return nowUnix-fp.LastProbedAt >= int64(constant.FingerprintProbeMinIntervalSeconds)
}
