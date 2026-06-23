package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/fingerprint"
	"github.com/QuantumNous/new-api/service/latencytest"

	"github.com/gin-gonic/gin"
)

// upstreamTestTarget identifies what to test. Either a free-form (base_url,
// key) pair, or a reference to an UpstreamSite group token which the server
// can resolve internally.
type upstreamTestTarget struct {
	BaseURL string `json:"base_url"`
	Key     string `json:"key"`
	// Convenience: resolve from an upstream site + group name.
	SiteId    int    `json:"site_id"`
	GroupName string `json:"group_name"`
}

func (t *upstreamTestTarget) resolve() (string, string, string, error) {
	if t.SiteId > 0 && t.GroupName != "" {
		site, err := model.GetUpstreamSiteById(t.SiteId)
		if err != nil {
			return "", "", "", fmt.Errorf("site %d not found", t.SiteId)
		}
		var tokens map[string]service.GroupTokenInfo
		if site.CachedTokens != "" {
			_ = common.UnmarshalJsonStr(site.CachedTokens, &tokens)
		}
		tok, ok := tokens[t.GroupName]
		if !ok || tok.Key == "" {
			return "", "", "", fmt.Errorf("no key cached for group %q", t.GroupName)
		}
		hint := fmt.Sprintf("site#%d/%s", site.Id, t.GroupName)
		return site.BaseURL, tok.Key, hint, nil
	}
	if t.BaseURL == "" || t.Key == "" {
		return "", "", "", fmt.Errorf("base_url and key are required")
	}
	return t.BaseURL, t.Key, maskKey(t.Key), nil
}

// quickTestRequest is the body of POST /api/upstream/test/quick.
type quickTestRequest struct {
	upstreamTestTarget
	ModelName  string `json:"model_name"`
	PromptText string `json:"prompt_text"`
}

func normalizeOpenAICompatEndpoint(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

// QuickTestUpstreamKey: single shot connectivity + latency + fingerprint test.
// Returns JSON immediately. Suitable for the "basic" mode in the unified test
// dialog.
func QuickTestUpstreamKey(c *gin.Context) {
	var req quickTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	baseURL, key, keyHint, err := req.resolve()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if req.ModelName == "" {
		req.ModelName = "claude-sonnet-4-6"
	}
	if req.PromptText == "" {
		req.PromptText = "hello"
	}

	endpoint := strings.TrimRight(baseURL, "/") + "/v1/messages"
	body, _ := common.Marshal(map[string]any{
		"model":      req.ModelName,
		"max_tokens": 64,
		"messages": []map[string]any{
			{"role": "user", "content": req.PromptText},
		},
	})

	client := &http.Client{Timeout: 60 * time.Second}
	httpReq, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", key)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	start := time.Now()
	resp, err := client.Do(httpReq)
	latencyMs := int(time.Since(start) / time.Millisecond)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
			"data": gin.H{
				"latency_ms":   latencyMs,
				"key_hint":     keyHint,
				"request_body": string(body),
			},
		})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))

	usage := struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	}{}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var parsed struct {
			Usage *struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		}
		if uerr := common.Unmarshal(respBody, &parsed); uerr == nil && parsed.Usage != nil {
			usage = struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			}{
				InputTokens:              parsed.Usage.InputTokens,
				OutputTokens:             parsed.Usage.OutputTokens,
				CacheReadInputTokens:     parsed.Usage.CacheReadInputTokens,
				CacheCreationInputTokens: parsed.Usage.CacheCreationInputTokens,
			}
		}
	}

	fp := fingerprint.Build(fingerprint.ProbeInputs{
		SuccessHeaders: resp.Header,
	})
	sameSource := findSameSourceFingerprint(fp.CompositeHash, req.SiteId)

	c.JSON(http.StatusOK, gin.H{
		"success": resp.StatusCode >= 200 && resp.StatusCode < 300,
		"data": gin.H{
			"status_code":      resp.StatusCode,
			"latency_ms":       latencyMs,
			"key_hint":         keyHint,
			"request_body":     string(body),
			"response_body":    string(respBody),
			"response_headers": flattenHeaderMap(resp.Header),
			"usage":            usage,
			"fingerprint": gin.H{
				"composite": fp.CompositeHash,
				"headers":   fp.HeaderSetHash,
				"errors":    fp.ErrorShapeHash,
				"models":    fp.ModelSetHash,
			},
			"same_source": sameSource,
		},
	})
}

// QuickTestGPTUpstreamKey sends a tiny OpenAI-compatible chat completion probe.
// It is intentionally separate from QuickTestUpstreamKey because the existing
// quick path targets Anthropic-compatible /v1/messages.
func QuickTestGPTUpstreamKey(c *gin.Context) {
	var req quickTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	baseURL, key, keyHint, err := req.resolve()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if req.ModelName == "" {
		req.ModelName = "gpt-5.5"
	}
	if req.PromptText == "" {
		req.PromptText = "hello"
	}

	endpoint := normalizeOpenAICompatEndpoint(baseURL)
	body, _ := common.Marshal(map[string]any{
		"model": req.ModelName,
		"messages": []map[string]any{
			{"role": "user", "content": req.PromptText},
		},
		"max_completion_tokens": 16,
	})

	client := &http.Client{Timeout: 60 * time.Second}
	httpReq, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)

	start := time.Now()
	resp, err := client.Do(httpReq)
	latencyMs := int(time.Since(start) / time.Millisecond)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
			"data": gin.H{
				"latency_ms":   latencyMs,
				"key_hint":     keyHint,
				"request_body": string(body),
			},
		})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))

	usage := struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}{}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var parsed struct {
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if uerr := common.Unmarshal(respBody, &parsed); uerr == nil && parsed.Usage != nil {
			usage.InputTokens = parsed.Usage.PromptTokens
			usage.OutputTokens = parsed.Usage.CompletionTokens
		}
	}

	fp := fingerprint.Build(fingerprint.ProbeInputs{
		SuccessHeaders: resp.Header,
	})
	sameSource := findSameSourceFingerprint(fp.CompositeHash, req.SiteId)

	c.JSON(http.StatusOK, gin.H{
		"success": resp.StatusCode >= 200 && resp.StatusCode < 300,
		"data": gin.H{
			"status_code":      resp.StatusCode,
			"latency_ms":       latencyMs,
			"key_hint":         keyHint,
			"request_body":     string(body),
			"response_body":    string(respBody),
			"response_headers": flattenHeaderMap(resp.Header),
			"usage":            usage,
			"fingerprint": gin.H{
				"composite": fp.CompositeHash,
				"headers":   fp.HeaderSetHash,
				"errors":    fp.ErrorShapeHash,
				"models":    fp.ModelSetHash,
			},
			"same_source": sameSource,
		},
	})
}

// findSameSourceFingerprint returns matching entities (channels + upstream
// sites — sites are matched via a transient probe, channels via stored
// fingerprint). The selfSiteId is excluded from results.
func findSameSourceFingerprint(composite string, selfSiteId int) gin.H {
	out := gin.H{
		"channels": []int{},
		"sites":    []gin.H{},
	}
	if composite == "" {
		return out
	}
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err == nil {
		ch := make([]int, 0)
		for _, c := range channels {
			if c == nil {
				continue
			}
			if c.ChannelInfo.Fingerprint.CompositeHash == composite {
				ch = append(ch, c.Id)
			}
		}
		sort.Ints(ch)
		out["channels"] = ch
	}
	return out
}

func flattenHeaderMap(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = strings.Join(v, ", ")
	}
	return out
}

// ============================================================================
// Advanced (SSE) test
// ============================================================================

type advancedTestRequest struct {
	upstreamTestTarget
	PromptPreset string                   `json:"prompt_preset"`
	PromptText   string                   `json:"prompt_text"`
	Breakpoints  []latencytest.Breakpoint `json:"breakpoints"`
	Concurrency  int                      `json:"concurrency"`
	ModelName    string                   `json:"model_name"`
}

// AdvancedTestUpstreamKey: concurrent + cache-aware test, SSE streaming.
//
// Replaces the channel-bound /api/channel/latency-test/start handler. The
// payload format / event types remain compatible so the frontend can share
// the SSE handling code.
func AdvancedTestUpstreamKey(c *gin.Context) {
	var req advancedTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	baseURL, key, keyHint, err := req.resolve()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 1
	}
	if req.ModelName == "" {
		req.ModelName = "claude-sonnet-4-6"
	}
	if req.PromptText == "" {
		if preset := latencytest.PresetById(req.PromptPreset); preset != nil {
			req.PromptText = preset.Text
		} else {
			req.PromptText = "hello"
		}
	}

	requestBody := buildAdvancedRequestBody(req)
	requestBytes, _ := common.Marshal(requestBody)

	endpoint := strings.TrimRight(baseURL, "/") + "/v1/messages"

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "streaming unsupported"})
		return
	}

	emit := func(typ string, payload any) {
		buf, _ := common.Marshal(gin.H{"type": typ, "payload": payload})
		fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", typ, buf)
		flusher.Flush()
	}

	emit("start", gin.H{
		"concurrency":   req.Concurrency,
		"model":         req.ModelName,
		"prompt_length": len(req.PromptText),
		"breakpoints":   len(req.Breakpoints),
		"key_hint":      keyHint,
	})

	runAdvancedConcurrent(c.Request.Context(), endpoint, key, requestBytes, req.Concurrency, emit)
}

func buildAdvancedRequestBody(req advancedTestRequest) map[string]any {
	if len(req.Breakpoints) == 0 {
		return map[string]any{
			"model":      req.ModelName,
			"max_tokens": 64,
			"messages": []map[string]any{
				{"role": "user", "content": req.PromptText},
			},
		}
	}
	positions := make([]int, 0, len(req.Breakpoints))
	for _, b := range req.Breakpoints {
		if b.Position > 0 && b.Position < len(req.PromptText) {
			positions = append(positions, b.Position)
		}
	}
	sort.Ints(positions)
	segments := make([]map[string]any, 0, len(positions)+1)
	prev := 0
	for _, p := range positions {
		segments = append(segments, map[string]any{
			"type":          "text",
			"text":          req.PromptText[prev:p],
			"cache_control": map[string]any{"type": "ephemeral"},
		})
		prev = p
	}
	if prev < len(req.PromptText) {
		segments = append(segments, map[string]any{
			"type": "text",
			"text": req.PromptText[prev:],
		})
	}
	return map[string]any{
		"model":      req.ModelName,
		"max_tokens": 64,
		"messages": []map[string]any{
			{"role": "user", "content": segments},
		},
	}
}

type advancedRequestResult struct {
	Sequence            int    `json:"sequence"`
	LatencyMs           int    `json:"latency_ms"`
	StatusCode          int    `json:"status_code"`
	Success             bool   `json:"success"`
	InputTokens         int    `json:"input_tokens"`
	OutputTokens        int    `json:"output_tokens"`
	CacheReadTokens     int    `json:"cache_read_tokens"`
	CacheCreationTokens int    `json:"cache_creation_tokens"`
	ErrorMessage        string `json:"error_message,omitempty"`
}

func runAdvancedConcurrent(
	ctx context.Context,
	endpoint string,
	key string,
	body []byte,
	concurrency int,
	emit func(string, any),
) {
	client := &http.Client{Timeout: 120 * time.Second}

	results := make([]advancedRequestResult, 0, concurrency)
	resultsMu := sync.Mutex{}

	windowMu := sync.Mutex{}
	window := make([]bool, 0, 16)
	var effectiveRPM atomic.Int64
	effectiveRPMSet := atomic.Bool{}

	var firstHeaders http.Header
	headersOnce := sync.Once{}

	wg := sync.WaitGroup{}
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			result := advancedRequestResult{Sequence: seq}
			start := time.Now()
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
			req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
			resp, err := client.Do(req)
			result.LatencyMs = int(time.Since(start) / time.Millisecond)
			if err != nil {
				result.ErrorMessage = err.Error()
			} else {
				result.StatusCode = resp.StatusCode
				respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
				resp.Body.Close()
				headersOnce.Do(func() {
					firstHeaders = resp.Header.Clone()
				})
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					result.Success = true
					var parsed struct {
						Usage *struct {
							InputTokens              int `json:"input_tokens"`
							OutputTokens             int `json:"output_tokens"`
							CacheReadInputTokens     int `json:"cache_read_input_tokens"`
							CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
						} `json:"usage"`
					}
					if uerr := common.Unmarshal(respBody, &parsed); uerr == nil && parsed.Usage != nil {
						result.InputTokens = parsed.Usage.InputTokens
						result.OutputTokens = parsed.Usage.OutputTokens
						result.CacheReadTokens = parsed.Usage.CacheReadInputTokens
						result.CacheCreationTokens = parsed.Usage.CacheCreationInputTokens
					}
				} else {
					result.ErrorMessage = fmt.Sprintf("http %d: %s", resp.StatusCode, truncate(string(respBody), 200))
				}
			}

			resultsMu.Lock()
			results = append(results, result)
			resultsMu.Unlock()
			emit("request", result)

			windowMu.Lock()
			window = append(window, result.Success)
			startIdx := 0
			if len(window) > 10 {
				startIdx = len(window) - 10
			}
			view := window[startIdx:]
			fail := 0
			for _, ok := range view {
				if !ok {
					fail++
				}
			}
			if !effectiveRPMSet.Load() && len(view) == 10 && fail*10 >= 3*len(view) {
				prevSuccess := 0
				for _, ok := range window[:startIdx] {
					if ok {
						prevSuccess++
					}
				}
				effectiveRPM.Store(int64(prevSuccess))
				effectiveRPMSet.Store(true)
			}
			windowMu.Unlock()
		}(i)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].Sequence < results[j].Sequence })

	successCount := 0
	failedCount := 0
	cacheHits := 0
	cacheCreations := 0
	latencies := make([]int, 0, len(results))
	totalLatency := 0
	maxLatency := 0
	for _, r := range results {
		if r.Success {
			successCount++
			latencies = append(latencies, r.LatencyMs)
			totalLatency += r.LatencyMs
			if r.LatencyMs > maxLatency {
				maxLatency = r.LatencyMs
			}
		} else {
			failedCount++
		}
		if r.CacheReadTokens > 0 {
			cacheHits++
		}
		if r.CacheCreationTokens > 0 {
			cacheCreations++
		}
	}
	sort.Ints(latencies)
	avg := 0
	p50 := 0
	p95 := 0
	if len(latencies) > 0 {
		avg = totalLatency / len(latencies)
		p50 = latencies[len(latencies)/2]
		p95 = latencies[(len(latencies)*95)/100]
		if p95 == 0 {
			p95 = latencies[len(latencies)-1]
		}
	}

	rpm := successCount
	if effectiveRPMSet.Load() {
		rpm = int(effectiveRPM.Load())
	}

	summary := gin.H{
		"total_requests":       len(results),
		"success_requests":     successCount,
		"failed_requests":      failedCount,
		"effective_rpm":        rpm,
		"avg_latency_ms":       avg,
		"p50_latency_ms":       p50,
		"p95_latency_ms":       p95,
		"max_latency_ms":       maxLatency,
		"cache_hit_count":      cacheHits,
		"cache_creation_count": cacheCreations,
	}
	if firstHeaders != nil {
		fp := fingerprint.Build(fingerprint.ProbeInputs{SuccessHeaders: firstHeaders})
		summary["fingerprint_composite"] = fp.CompositeHash
		summary["same_source"] = findSameSourceFingerprint(fp.CompositeHash, 0)
	}
	emit("summary", summary)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// ============================================================================
// Cache hit-rate test (SSE)
// ============================================================================
//
// Methodology, per Anthropic's documented caching rules:
//   1. Send a single warm-up request that establishes the cache (cache_creation > 0).
//   2. WAIT for the warm-up response — parallel requests cannot share a cache
//      that hasn't finished writing yet ("Cache entries only become available
//      after the first response begins").
//   3. Send N-1 subsequent identical requests SERIALLY (we send them one by one
//      to avoid the parallel-write race and to make per-request timing clean).
//   4. For each request, emit cache_read_input_tokens / cache_creation_input_tokens
//      so the client can compute the real hit rate.
//
// Diagnosing a warm-up that reports cache_creation == 0 AND cache_read == 0:
//   - input_tokens < model threshold  → "below_minimum" — the prompt itself is
//     genuinely too short to be cached (Sonnet 4.x: 1024 / Haiku 3.5: 2048 /
//     Opus 4.5+ & Haiku 4.5: 4096). Pick a larger preset or move the breakpoint
//     toward the end of the prompt.
//   - input_tokens >= model threshold → "not_propagated" — the prompt was large
//     enough but the upstream stripped cache_control (very common for matryoshka
//     relays). No amount of preset switching will fix this; the upstream itself
//     does not support caching.
//   - input_tokens == 0               → "no_usage" — the upstream did not return
//     a usage block at all. Either it is not an Anthropic-compatible endpoint
//     or it strips usage from the response.
//
// Result interpretation:
//   - hit rate = (number of subsequent requests where cache_read > 0) / (N - 1).
//   - 100% hit rate suggests a direct / single-workspace upstream.
//   - Fractional hit rate strongly suggests an upstream key pool — caching is
//     per-workspace, so each key in the pool has its own cache.

// modelCacheThreshold returns the minimum input_tokens count required to
// trigger Anthropic prompt caching for a given model name. Matches public
// Anthropic documentation as of 2026-05. If the model is unrecognised we
// return the smallest known threshold (1024) so we don't falsely report
// "below_minimum" against a model whose limit we just don't know about.
func modelCacheThreshold(model string) int {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus-4-5"), strings.Contains(m, "opus-4.5"),
		strings.Contains(m, "haiku-4-5"), strings.Contains(m, "haiku-4.5"):
		return 4096
	case strings.Contains(m, "haiku-3-5"), strings.Contains(m, "haiku-3.5"):
		return 2048
	default:
		return 1024
	}
}

type cacheHitrateRequest struct {
	upstreamTestTarget
	PromptPreset string                   `json:"prompt_preset"`
	PromptText   string                   `json:"prompt_text"`
	Breakpoints  []latencytest.Breakpoint `json:"breakpoints"`
	Iterations   int                      `json:"iterations"` // total requests including warm-up
	ModelName    string                   `json:"model_name"`
}

// CacheHitrateTest streams per-iteration cache outcomes. Use this instead of the
// generic AdvancedTestUpstreamKey when the operator's goal is measuring hit
// rate; that handler dispatches in parallel and so can never produce a clean
// hit-rate measurement.
func CacheHitrateTest(c *gin.Context) {
	var req cacheHitrateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	baseURL, key, keyHint, err := req.resolve()
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	if req.Iterations <= 1 {
		req.Iterations = 10
	}
	if req.Iterations > 50 {
		req.Iterations = 50
	}
	if req.ModelName == "" {
		req.ModelName = "claude-sonnet-4-6"
	}
	if req.PromptText == "" {
		if preset := latencytest.PresetById(req.PromptPreset); preset != nil {
			req.PromptText = preset.Text
		} else {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "prompt_text or prompt_preset required"})
			return
		}
	}

	// Build the shared payload once. All N requests use byte-identical bodies
	// so any cache miss reflects an upstream-side issue (key pool rotation,
	// breakpoint dropped, etc.), not our request being non-deterministic.
	adv := advancedTestRequest{
		upstreamTestTarget: req.upstreamTestTarget,
		PromptText:         req.PromptText,
		Breakpoints:        req.Breakpoints,
		ModelName:          req.ModelName,
	}
	requestBody := buildAdvancedRequestBody(adv)
	requestBytes, _ := common.Marshal(requestBody)
	endpoint := strings.TrimRight(baseURL, "/") + "/v1/messages"

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "streaming unsupported"})
		return
	}
	emit := func(typ string, payload any) {
		buf, _ := common.Marshal(gin.H{"type": typ, "payload": payload})
		fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", typ, buf)
		flusher.Flush()
	}

	emit("start", gin.H{
		"iterations":    req.Iterations,
		"model":         req.ModelName,
		"prompt_length": len(req.PromptText),
		"breakpoints":   len(req.Breakpoints),
		"key_hint":      keyHint,
		// All iterations share the same request body — send it once with
		// the start event so the frontend can show it next to each result
		// without bloating every iteration event.
		"request_body": string(requestBytes),
	})

	client := &http.Client{Timeout: 120 * time.Second}

	type iterResult struct {
		Sequence            int    `json:"sequence"`
		Role                string `json:"role"` // "warmup" | "probe"
		LatencyMs           int    `json:"latency_ms"`
		StatusCode          int    `json:"status_code"`
		Success             bool   `json:"success"`
		InputTokens         int    `json:"input_tokens"`
		OutputTokens        int    `json:"output_tokens"`
		CacheReadTokens     int    `json:"cache_read_tokens"`
		CacheCreationTokens int    `json:"cache_creation_tokens"`
		CacheStatus         string `json:"cache_status"` // "hit" | "created" | "below_minimum" | "not_propagated" | "no_usage" | "error"
		ResponseBody        string `json:"response_body,omitempty"`
		ErrorMessage        string `json:"error_message,omitempty"`
	}

	dispatch := func(seq int, role string) iterResult {
		out := iterResult{Sequence: seq, Role: role}
		start := time.Now()
		hreq, _ := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, endpoint, bytes.NewReader(requestBytes))
		hreq.Header.Set("Content-Type", "application/json")
		hreq.Header.Set("x-api-key", key)
		hreq.Header.Set("anthropic-version", "2023-06-01")
		hreq.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
		resp, err := client.Do(hreq)
		out.LatencyMs = int(time.Since(start) / time.Millisecond)
		if err != nil {
			out.ErrorMessage = err.Error()
			out.CacheStatus = "error"
			return out
		}
		defer resp.Body.Close()
		out.StatusCode = resp.StatusCode
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		// Keep snippets bounded to stop SSE payloads from ballooning when
		// the upstream returns a long completion.
		out.ResponseBody = truncate(string(body), 16*1024)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			out.ErrorMessage = fmt.Sprintf("http %d: %s", resp.StatusCode, truncate(string(body), 200))
			out.CacheStatus = "error"
			return out
		}
		out.Success = true
		var parsed struct {
			Usage *struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		}
		if perr := common.Unmarshal(body, &parsed); perr == nil && parsed.Usage != nil {
			out.InputTokens = parsed.Usage.InputTokens
			out.OutputTokens = parsed.Usage.OutputTokens
			out.CacheReadTokens = parsed.Usage.CacheReadInputTokens
			out.CacheCreationTokens = parsed.Usage.CacheCreationInputTokens
		}
		threshold := modelCacheThreshold(req.ModelName)
		switch {
		case out.CacheReadTokens > 0:
			out.CacheStatus = "hit"
		case out.CacheCreationTokens > 0:
			out.CacheStatus = "created"
		case out.InputTokens == 0:
			// Upstream returned no usage block at all — we cannot tell why
			// the cache fields are empty.
			out.CacheStatus = "no_usage"
		case out.InputTokens >= threshold:
			// Prompt was big enough to be cacheable but the upstream still
			// produced no cache_creation — strongest signal that the
			// upstream stripped cache_control on the way through.
			out.CacheStatus = "not_propagated"
		default:
			out.CacheStatus = "below_minimum"
		}
		return out
	}

	// Warmup
	warm := dispatch(0, "warmup")
	emit("iteration", warm)

	threshold := modelCacheThreshold(req.ModelName)
	if warm.CacheStatus == "below_minimum" {
		emit("summary", gin.H{
			"aborted": true,
			"reason":  "prompt_below_cache_minimum",
			"explanation": fmt.Sprintf(
				"warm-up reported input_tokens=%d, below the %s threshold of %d tokens — pick a larger Prompt preset or move the breakpoint closer to the end of the prompt",
				warm.InputTokens, req.ModelName, threshold,
			),
			"input_tokens":    warm.InputTokens,
			"model_threshold": threshold,
		})
		return
	}
	if warm.CacheStatus == "not_propagated" {
		emit("summary", gin.H{
			"aborted": true,
			"reason":  "upstream_strips_cache_control",
			"explanation": fmt.Sprintf(
				"warm-up sent input_tokens=%d (above the %d-token threshold for %s) but the upstream returned cache_creation=0 and cache_read=0 — the upstream is NOT honoring cache_control. This is typical of matryoshka relays; no preset change will help.",
				warm.InputTokens, threshold, req.ModelName,
			),
			"input_tokens":    warm.InputTokens,
			"model_threshold": threshold,
		})
		return
	}
	if warm.CacheStatus == "no_usage" {
		emit("summary", gin.H{
			"aborted":     true,
			"reason":      "upstream_omits_usage",
			"explanation": "warm-up succeeded but the upstream did not return a usage block — either the endpoint is not Anthropic-compatible or the upstream strips usage from responses. Cache behaviour cannot be measured.",
		})
		return
	}
	if warm.CacheStatus == "error" {
		emit("summary", gin.H{
			"aborted": true,
			"reason":  "warmup_failed",
			"error":   warm.ErrorMessage,
		})
		return
	}

	// Serial probes
	hits := 0
	misses := 0
	probes := req.Iterations - 1
	for i := 1; i <= probes; i++ {
		r := dispatch(i, "probe")
		emit("iteration", r)
		switch r.CacheStatus {
		case "hit":
			hits++
		case "created", "below_minimum":
			misses++
		}
	}

	hitrate := 0.0
	if probes > 0 {
		hitrate = float64(hits) / float64(probes) * 100
	}
	var interpretation string
	switch {
	case probes == 0:
		interpretation = "iterations=1: only the warm-up ran — no hit rate measurable"
	case hits == probes:
		interpretation = "perfect cache hit rate — upstream is direct or a single workspace"
	case hits == 0:
		interpretation = "zero cache hits — upstream may not be propagating cache_control, may be routing each call to a different workspace, or the cache may have expired between requests"
	default:
		interpretation = fmt.Sprintf("partial cache hit rate (%.0f%%) — upstream is likely a key-pool rotation with ~%d distinct workspaces", hitrate, int(100.0/hitrate*float64(hits)+0.5))
	}

	emit("summary", gin.H{
		"aborted":        false,
		"total_probes":   probes,
		"cache_hits":     hits,
		"cache_misses":   misses,
		"hit_rate_pct":   hitrate,
		"interpretation": interpretation,
	})
}
