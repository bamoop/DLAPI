package latencytest

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
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/fingerprint"
)

// Breakpoint marks a position in the prompt where a cache_control segment ends.
type Breakpoint struct {
	Position int `json:"position"` // character offset into the prompt
}

// Config is the input for one latency-test run.
type Config struct {
	ChannelId    int
	KeyIndex     int    // -1 = auto
	PromptPreset string // "short" | "medium" | "long" | "custom"
	PromptText   string // populated for "custom" or taken from preset
	Breakpoints  []Breakpoint
	Concurrency  int
	ModelName    string
}

// Event is delivered to subscribers as the run progresses.
type Event struct {
	Type    string `json:"type"` // "start" | "request" | "summary" | "error"
	RunId   int64  `json:"run_id,omitempty"`
	Payload any    `json:"payload,omitempty"`
}

// RequestResult is the per-request event payload.
type RequestResult struct {
	Sequence            int    `json:"sequence"`
	StartedAt           int64  `json:"started_at"`
	LatencyMs           int    `json:"latency_ms"`
	StatusCode          int    `json:"status_code"`
	Success             bool   `json:"success"`
	InputTokens         int    `json:"input_tokens"`
	OutputTokens        int    `json:"output_tokens"`
	CacheReadTokens     int    `json:"cache_read_tokens"`
	CacheCreationTokens int    `json:"cache_creation_tokens"`
	ErrorMessage        string `json:"error_message,omitempty"`
}

// Summary is the final aggregated event payload.
type Summary struct {
	TotalRequests        int    `json:"total_requests"`
	SuccessRequests      int    `json:"success_requests"`
	FailedRequests       int    `json:"failed_requests"`
	EffectiveRPM         int    `json:"effective_rpm"`
	AvgLatencyMs         int    `json:"avg_latency_ms"`
	P50LatencyMs         int    `json:"p50_latency_ms"`
	P95LatencyMs         int    `json:"p95_latency_ms"`
	MaxLatencyMs         int    `json:"max_latency_ms"`
	CacheHitCount        int    `json:"cache_hit_count"`
	CacheCreationCount   int    `json:"cache_creation_count"`
	FingerprintComposite string `json:"fingerprint_composite,omitempty"`
	SameSourceChannels   []int  `json:"same_source_channels,omitempty"`
}

// Run executes the test, persisting a run row + per-request rows, and emits
// events on the returned channel. The channel is closed when the run ends.
//
// Concurrency is honoured exactly; we don't gate or cap. Sliding-window
// failure detection (>=30% failures in the last 10 results) sets the
// "effective RPM" but does NOT cancel in-flight requests — they finish
// naturally and are recorded; the marker is informational.
func Run(ctx context.Context, cfg Config) (<-chan Event, error) {
	channel, err := model.GetChannelById(cfg.ChannelId, true)
	if err != nil || channel == nil {
		return nil, fmt.Errorf("channel %d not found: %v", cfg.ChannelId, err)
	}
	if channel.Type != constant.ChannelTypeAnthropic {
		return nil, fmt.Errorf("latency test currently supports Anthropic channels only")
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}

	if cfg.PromptText == "" {
		preset := PresetById(cfg.PromptPreset)
		if preset == nil {
			return nil, fmt.Errorf("unknown preset: %s", cfg.PromptPreset)
		}
		cfg.PromptText = preset.Text
	}

	if cfg.ModelName == "" {
		cfg.ModelName = "claude-sonnet-4-6"
	}

	key, keyHint, err := pickKey(channel, cfg.KeyIndex)
	if err != nil {
		return nil, err
	}

	baseURL := channel.GetBaseURL()
	if baseURL == "" {
		baseURL = constant.ChannelBaseURLs[channel.Type]
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/v1/messages"

	requestBody := buildRequestBody(cfg)
	requestBytes, err := common.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %v", err)
	}

	run := &model.ChannelLatencyTestRun{
		ChannelId:        cfg.ChannelId,
		KeyIndex:         cfg.KeyIndex,
		KeyHint:          keyHint,
		PromptPresetId:   cfg.PromptPreset,
		PromptText:       cfg.PromptText,
		Concurrency:      cfg.Concurrency,
		ModelName:        cfg.ModelName,
		StartedAt:        time.Now().Unix(),
		Status:           "running",
	}
	if bps, err := common.Marshal(cfg.Breakpoints); err == nil {
		run.CacheBreakpoints = string(bps)
	}
	if err := model.SaveChannelLatencyTestRun(run); err != nil {
		return nil, fmt.Errorf("create run: %v", err)
	}

	events := make(chan Event, cfg.Concurrency+8)

	go func() {
		defer close(events)
		execute(ctx, cfg, channel, key, endpoint, requestBytes, run, events)
	}()

	return events, nil
}

func pickKey(channel *model.Channel, keyIndex int) (string, string, error) {
	if keyIndex >= 0 {
		keys := channel.GetKeys()
		if keyIndex >= len(keys) {
			return "", "", fmt.Errorf("key index %d out of range", keyIndex)
		}
		return strings.TrimSpace(keys[keyIndex]), maskKey(keys[keyIndex]), nil
	}
	key, _, apiErr := channel.GetNextEnabledKey()
	if apiErr != nil {
		return "", "", apiErr
	}
	return strings.TrimSpace(key), maskKey(key), nil
}

func maskKey(k string) string {
	k = strings.TrimSpace(k)
	if len(k) <= 8 {
		return strings.Repeat("*", len(k))
	}
	return k[:4] + "..." + k[len(k)-4:]
}

// buildRequestBody assembles an Anthropic /v1/messages payload, splitting
// the prompt into segments according to the breakpoint list. Each
// breakpoint-terminated segment carries cache_control: {type: "ephemeral"}.
func buildRequestBody(cfg Config) map[string]any {
	content := buildSegmentedContent(cfg.PromptText, cfg.Breakpoints)
	return map[string]any{
		"model":      cfg.ModelName,
		"max_tokens": 64,
		"messages": []map[string]any{
			{
				"role":    "user",
				"content": content,
			},
		},
	}
}

func buildSegmentedContent(text string, breakpoints []Breakpoint) []map[string]any {
	if len(breakpoints) == 0 {
		return []map[string]any{
			{"type": "text", "text": text},
		}
	}
	// dedupe + sort + clamp positions
	positions := make([]int, 0, len(breakpoints))
	for _, b := range breakpoints {
		if b.Position > 0 && b.Position < len(text) {
			positions = append(positions, b.Position)
		}
	}
	sort.Ints(positions)
	positions = dedupSortedInts(positions)

	segments := make([]map[string]any, 0, len(positions)+1)
	prev := 0
	for _, p := range positions {
		seg := map[string]any{
			"type":          "text",
			"text":          text[prev:p],
			"cache_control": map[string]any{"type": "ephemeral"},
		}
		segments = append(segments, seg)
		prev = p
	}
	if prev < len(text) {
		segments = append(segments, map[string]any{
			"type": "text",
			"text": text[prev:],
		})
	}
	return segments
}

func dedupSortedInts(xs []int) []int {
	if len(xs) == 0 {
		return xs
	}
	out := xs[:1]
	for _, x := range xs[1:] {
		if x != out[len(out)-1] {
			out = append(out, x)
		}
	}
	return out
}

// execute runs all requests, streaming results to events and writing
// per-request rows. When done it computes summary metrics, captures the
// fingerprint, and emits the final summary event.
func execute(
	ctx context.Context,
	cfg Config,
	channel *model.Channel,
	key string,
	endpoint string,
	requestBytes []byte,
	run *model.ChannelLatencyTestRun,
	events chan<- Event,
) {
	events <- Event{Type: "start", RunId: run.Id, Payload: map[string]any{
		"concurrency":   cfg.Concurrency,
		"model":         cfg.ModelName,
		"prompt_length": len(cfg.PromptText),
		"breakpoints":   len(cfg.Breakpoints),
	}}

	client, err := service.NewProxyHttpClient(channel.GetSetting().Proxy)
	if err != nil {
		failRun(run, "build http client: "+err.Error(), events)
		return
	}
	client.Timeout = 120 * time.Second

	wg := sync.WaitGroup{}
	resultsMu := sync.Mutex{}
	results := make([]*model.ChannelLatencyTestRequest, 0, cfg.Concurrency)

	// sliding-window failure tracker
	windowMu := sync.Mutex{}
	window := make([]bool, 0, 16) // true = success
	effectiveRPMSet := atomic.Bool{}
	var effectiveRPM atomic.Int64
	var firstSampleHeaders http.Header
	headersOnce := sync.Once{}

	// capture the order in which results complete, so the sliding-window
	// detector reflects real upstream behaviour, not the dispatch order.
	completionSeq := atomic.Int64{}

	for i := 0; i < cfg.Concurrency; i++ {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			row := dispatchOne(ctx, client, endpoint, key, requestBytes, seq)
			headersOnce.Do(func() {
				firstSampleHeaders = row.headers
			})

			rec := row.toRecord(run.Id)
			resultsMu.Lock()
			results = append(results, rec)
			resultsMu.Unlock()

			// emit per-request event
			events <- Event{Type: "request", RunId: run.Id, Payload: row.toEventPayload()}

			// sliding-window tracking — based on completion order so a
			// burst of late failures is detected correctly.
			completionIdx := int(completionSeq.Add(1))
			windowMu.Lock()
			window = append(window, row.success)
			start := 0
			if len(window) > 10 {
				start = len(window) - 10
			}
			view := window[start:]
			fail := 0
			for _, ok := range view {
				if !ok {
					fail++
				}
			}
			if !effectiveRPMSet.Load() && len(view) == 10 && fail*10 >= 3*len(view) {
				// 30% failure threshold reached — record success count seen *before* this window started
				prevSuccess := 0
				for _, ok := range window[:start] {
					if ok {
						prevSuccess++
					}
				}
				effectiveRPM.Store(int64(prevSuccess))
				effectiveRPMSet.Store(true)
			}
			windowMu.Unlock()
			_ = completionIdx
		}(i)
	}

	wg.Wait()

	// Aggregate
	sort.Slice(results, func(i, j int) bool { return results[i].Sequence < results[j].Sequence })

	summary := aggregate(results)
	if effectiveRPMSet.Load() {
		summary.EffectiveRPM = int(effectiveRPM.Load())
	} else {
		summary.EffectiveRPM = summary.SuccessRequests
	}

	// fingerprint capture on best-effort basis
	fp := captureFingerprint(channel, key, firstSampleHeaders)
	if fp != nil {
		summary.FingerprintComposite = fp.CompositeHash
		run.FingerprintComposite = fp.CompositeHash
		run.FingerprintHeaders = fp.HeaderSetHash
		run.FingerprintErrors = fp.ErrorShapeHash
		run.FingerprintModels = fp.ModelSetHash
		summary.SameSourceChannels = findSameSourceChannels(channel.Id, fp.CompositeHash)
	}

	run.FinishedAt = time.Now().Unix()
	run.TotalRequests = summary.TotalRequests
	run.SuccessRequests = summary.SuccessRequests
	run.FailedRequests = summary.FailedRequests
	run.EffectiveRPM = summary.EffectiveRPM
	run.AvgLatencyMs = summary.AvgLatencyMs
	run.P50LatencyMs = summary.P50LatencyMs
	run.P95LatencyMs = summary.P95LatencyMs
	run.MaxLatencyMs = summary.MaxLatencyMs
	run.CacheHitCount = summary.CacheHitCount
	run.CacheCreationCount = summary.CacheCreationCount
	run.Status = "done"
	if err := model.SaveChannelLatencyTestRun(run); err != nil {
		common.SysError("save latency run: " + err.Error())
	}
	if err := model.InsertChannelLatencyTestRequests(results); err != nil {
		common.SysError("insert latency requests: " + err.Error())
	}

	events <- Event{Type: "summary", RunId: run.Id, Payload: summary}
}

func failRun(run *model.ChannelLatencyTestRun, msg string, events chan<- Event) {
	run.Status = "failed"
	run.ErrorMessage = msg
	run.FinishedAt = time.Now().Unix()
	_ = model.SaveChannelLatencyTestRun(run)
	events <- Event{Type: "error", RunId: run.Id, Payload: map[string]string{"message": msg}}
}

// internal row shape used inside execute
type dispatchRow struct {
	seq         int
	startedAt   time.Time
	endedAt     time.Time
	statusCode  int
	success     bool
	headers     http.Header
	body        []byte
	requestBody []byte
	err         error

	inputTokens         int
	outputTokens        int
	cacheReadTokens     int
	cacheCreationTokens int
}

func (r *dispatchRow) toRecord(runId int64) *model.ChannelLatencyTestRequest {
	rec := &model.ChannelLatencyTestRequest{
		RunId:               runId,
		Sequence:            r.seq,
		StartedAt:           r.startedAt.UnixMilli(),
		EndedAt:             r.endedAt.UnixMilli(),
		LatencyMs:           int(r.endedAt.Sub(r.startedAt) / time.Millisecond),
		StatusCode:          r.statusCode,
		Success:             r.success,
		InputTokens:         r.inputTokens,
		OutputTokens:        r.outputTokens,
		CacheReadTokens:     r.cacheReadTokens,
		CacheCreationTokens: r.cacheCreationTokens,
		RequestSnippet:      snippet(r.requestBody, 4096),
		ResponseSnippet:     snippet(r.body, 8192),
	}
	if r.err != nil {
		rec.ErrorMessage = r.err.Error()
	}
	if r.headers != nil {
		hdrJSON, _ := common.Marshal(flattenHeaders(r.headers))
		rec.ResponseHeaders = string(hdrJSON)
	}
	return rec
}

func (r *dispatchRow) toEventPayload() RequestResult {
	out := RequestResult{
		Sequence:            r.seq,
		StartedAt:           r.startedAt.UnixMilli(),
		LatencyMs:           int(r.endedAt.Sub(r.startedAt) / time.Millisecond),
		StatusCode:          r.statusCode,
		Success:             r.success,
		InputTokens:         r.inputTokens,
		OutputTokens:        r.outputTokens,
		CacheReadTokens:     r.cacheReadTokens,
		CacheCreationTokens: r.cacheCreationTokens,
	}
	if r.err != nil {
		out.ErrorMessage = r.err.Error()
	}
	return out
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = strings.Join(v, ", ")
	}
	return out
}

func snippet(b []byte, max int) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...[truncated]"
}

func dispatchOne(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	key string,
	body []byte,
	seq int,
) *dispatchRow {
	row := &dispatchRow{
		seq:         seq,
		requestBody: body,
		startedAt:   time.Now(),
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		row.err = err
		row.endedAt = time.Now()
		return row
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")

	resp, err := client.Do(req)
	row.endedAt = time.Now()
	if err != nil {
		row.err = err
		return row
	}
	defer resp.Body.Close()
	row.statusCode = resp.StatusCode
	row.headers = resp.Header.Clone()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if readErr != nil {
		row.err = readErr
		return row
	}
	row.body = respBody
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		row.success = true
		row.parseUsage()
	} else {
		row.err = fmt.Errorf("http %d", resp.StatusCode)
	}
	return row
}

// parseUsage extracts the usage object from a successful Anthropic response.
func (r *dispatchRow) parseUsage() {
	if len(r.body) == 0 {
		return
	}
	var parsed struct {
		Usage struct {
			InputTokens             int `json:"input_tokens"`
			OutputTokens            int `json:"output_tokens"`
			CacheReadInputTokens    int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := common.Unmarshal(r.body, &parsed); err != nil {
		return
	}
	r.inputTokens = parsed.Usage.InputTokens
	r.outputTokens = parsed.Usage.OutputTokens
	r.cacheReadTokens = parsed.Usage.CacheReadInputTokens
	r.cacheCreationTokens = parsed.Usage.CacheCreationInputTokens
}

func aggregate(results []*model.ChannelLatencyTestRequest) Summary {
	s := Summary{TotalRequests: len(results)}
	if len(results) == 0 {
		return s
	}
	latencies := make([]int, 0, len(results))
	totalLatency := 0
	for _, r := range results {
		if r.Success {
			s.SuccessRequests++
			latencies = append(latencies, r.LatencyMs)
			totalLatency += r.LatencyMs
			if r.LatencyMs > s.MaxLatencyMs {
				s.MaxLatencyMs = r.LatencyMs
			}
		} else {
			s.FailedRequests++
		}
		if r.CacheReadTokens > 0 {
			s.CacheHitCount++
		}
		if r.CacheCreationTokens > 0 {
			s.CacheCreationCount++
		}
	}
	if len(latencies) > 0 {
		s.AvgLatencyMs = totalLatency / len(latencies)
		sort.Ints(latencies)
		s.P50LatencyMs = latencies[len(latencies)/2]
		s.P95LatencyMs = latencies[(len(latencies)*95)/100]
		if s.P95LatencyMs == 0 && len(latencies) > 0 {
			s.P95LatencyMs = latencies[len(latencies)-1]
		}
	}
	return s
}

func captureFingerprint(channel *model.Channel, key string, headers http.Header) *model.UpstreamFingerprint {
	if headers == nil {
		return nil
	}
	fp := fingerprint.Build(fingerprint.ProbeInputs{
		SuccessHeaders: headers,
	})
	fp.LastProbedAt = time.Now().Unix()
	// also write back to channel's live fingerprint
	if freshChannel, err := model.GetChannelById(channel.Id, true); err == nil && freshChannel != nil {
		freshChannel.ChannelInfo.Fingerprint = fp
		_ = freshChannel.SaveChannelInfo()
	}
	return &fp
}

// findSameSourceChannels reports other channels whose stored composite hash
// matches the supplied hash. Used to flag "this run looks like channel #X
// too" in the summary event.
func findSameSourceChannels(selfId int, composite string) []int {
	if composite == "" {
		return nil
	}
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		return nil
	}
	matches := make([]int, 0)
	for _, ch := range channels {
		if ch == nil || ch.Id == selfId {
			continue
		}
		if ch.ChannelInfo.Fingerprint.CompositeHash == composite {
			matches = append(matches, ch.Id)
		}
	}
	sort.Ints(matches)
	return matches
}
