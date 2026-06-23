package controller

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/fingerprint"

	"github.com/gin-gonic/gin"
)

// GetChannelClusters groups channels whose stored upstream fingerprints
// indicate they resolve to the same real provider.
func GetChannelClusters(c *gin.Context) {
	channels, err := model.GetAllChannels(0, 0, true, false)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	clusters := fingerprint.BuildClusters(channels)
	c.JSON(http.StatusOK, gin.H{
		"success":       true,
		"data":          clusters,
		"probe_version": constant.FingerprintProbeVersion,
	})
}

// probeOptions selects which key is used and whether the result is
// written to the persistent history (only "manual" probes are).
type probeOptions struct {
	source         string // "auto" | "manual"
	useKeyIndex    bool
	keyIndex       int
	successHeaders http.Header // for "auto" path: re-use already-received headers; "manual" runs its own warmup
	respectGate    bool        // "auto" honours the rate gate; "manual" never does
}

// runChannelFingerprintProbe centralizes the probe logic for both the
// auto (channel-test piggyback) path and the manual (per-key) path.
//
// Returns the resulting fingerprint and an error if the probe failed
// before any signal could be collected. A partial probe (e.g. error
// shape works but model list fetch fails) still returns a non-nil
// fingerprint so the caller can decide whether to record it.
func runChannelFingerprintProbe(channel *model.Channel, opts probeOptions) (*model.UpstreamFingerprint, int, error) {
	if channel == nil {
		return nil, 0, nil
	}
	defer func() {
		if r := recover(); r != nil {
			common.SysError("channel fingerprint probe panic recovered")
		}
	}()

	now := time.Now().Unix()
	if opts.respectGate && !fingerprint.ShouldProbe(channel.ChannelInfo.Fingerprint, now) {
		return nil, 0, nil
	}

	start := time.Now()
	key, err := resolveProbeKey(channel, opts)
	if err != nil {
		return nil, 0, err
	}

	headers := opts.successHeaders
	if headers == nil {
		headers = probeSuccessHeaders(channel, key)
	}

	inputs := fingerprint.ProbeInputs{
		SuccessHeaders: headers,
	}

	if ids, err := fetchChannelUpstreamModelIDsWithKey(channel, key); err == nil {
		inputs.UpstreamModels = ids
	}

	if body, ok := probeErrorShape(channel, key); ok {
		inputs.ErrorBody = body
	}

	fp := fingerprint.Build(inputs)
	fp.LastProbedAt = now

	durationMs := int(time.Since(start) / time.Millisecond)

	// Persist "latest" snapshot back to channel for the cluster view.
	freshChannel, fetchErr := model.GetChannelById(channel.Id, true)
	if fetchErr == nil && freshChannel != nil {
		freshChannel.ChannelInfo.Fingerprint = fp
		if saveErr := freshChannel.SaveChannelInfo(); saveErr != nil {
			common.SysError("failed to save channel fingerprint: " + saveErr.Error())
		}
	}

	return &fp, durationMs, nil
}

func resolveProbeKey(channel *model.Channel, opts probeOptions) (string, error) {
	if opts.useKeyIndex {
		keys := channel.GetKeys()
		if opts.keyIndex < 0 || opts.keyIndex >= len(keys) {
			return "", errors.New("invalid key index")
		}
		return strings.TrimSpace(keys[opts.keyIndex]), nil
	}
	key, _, apiErr := channel.GetNextEnabledKey()
	if apiErr != nil {
		return "", apiErr
	}
	return strings.TrimSpace(key), nil
}

// probeSuccessHeaders does a lightweight GET against the upstream that
// should return a valid response (or at least an authenticated response
// with characteristic headers). Only used by the manual path — auto
// path already has headers from the real channel test.
func probeSuccessHeaders(channel *model.Channel, key string) http.Header {
	baseURL := resolveChannelBaseURL(channel)
	if baseURL == "" {
		return nil
	}
	endpoint, ok := modelListEndpoint(channel.Type, baseURL)
	if !ok {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	applyProbeAuth(req, channel.Type, key)
	client, err := service.NewProxyHttpClient(channel.GetSetting().Proxy)
	if err != nil {
		return nil
	}
	client.Timeout = 15 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8*1024))
	return resp.Header.Clone()
}

func probeErrorShape(channel *model.Channel, key string) ([]byte, bool) {
	baseURL := resolveChannelBaseURL(channel)
	if baseURL == "" {
		return nil, false
	}
	endpoint, ok := errorProbeEndpoint(channel.Type, baseURL)
	if !ok {
		return nil, false
	}

	payload, err := common.Marshal(map[string]any{
		"model":      "__newapi_probe_invalid_model__",
		"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	})
	if err != nil {
		return nil, false
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	applyProbeAuth(req, channel.Type, key)

	client, err := service.NewProxyHttpClient(channel.GetSetting().Proxy)
	if err != nil {
		return nil, false
	}
	client.Timeout = 15 * time.Second

	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return nil, false
	}
	return body, true
}

func resolveChannelBaseURL(channel *model.Channel) string {
	baseURL := constant.ChannelBaseURLs[channel.Type]
	if channel.GetBaseURL() != "" {
		baseURL = channel.GetBaseURL()
	}
	return baseURL
}

func applyProbeAuth(req *http.Request, channelType int, key string) {
	if channelType == constant.ChannelTypeAnthropic {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
}

func errorProbeEndpoint(channelType int, baseURL string) (string, bool) {
	baseURL = strings.TrimRight(baseURL, "/")
	switch channelType {
	case constant.ChannelTypeOpenAI,
		constant.ChannelTypeAzure,
		constant.ChannelTypeCustom,
		constant.ChannelTypeOpenAIMax,
		constant.ChannelTypeOhMyGPT,
		constant.ChannelTypeAIProxy,
		constant.ChannelTypeAIProxyLibrary,
		constant.ChannelTypeAPI2GPT,
		constant.ChannelTypeAIGC2D,
		constant.ChannelTypeOpenRouter,
		constant.ChannelTypeFastGPT,
		constant.ChannelTypeMoonshot,
		constant.ChannelTypeDeepSeek,
		constant.ChannelTypeMiniMax,
		constant.ChannelTypeMistral,
		constant.ChannelTypePerplexity,
		constant.ChannelTypeLingYiWanWu,
		constant.ChannelTypeSiliconFlow,
		constant.ChannelTypeXai,
		constant.ChannelTypeAILS:
		return baseURL + "/v1/chat/completions", true
	case constant.ChannelTypeAnthropic:
		return baseURL + "/v1/messages", true
	default:
		return "", false
	}
}

func modelListEndpoint(channelType int, baseURL string) (string, bool) {
	baseURL = strings.TrimRight(baseURL, "/")
	switch channelType {
	case constant.ChannelTypeOpenAI,
		constant.ChannelTypeAzure,
		constant.ChannelTypeCustom,
		constant.ChannelTypeOpenAIMax,
		constant.ChannelTypeOhMyGPT,
		constant.ChannelTypeAIProxy,
		constant.ChannelTypeAIProxyLibrary,
		constant.ChannelTypeAPI2GPT,
		constant.ChannelTypeAIGC2D,
		constant.ChannelTypeOpenRouter,
		constant.ChannelTypeFastGPT,
		constant.ChannelTypeMoonshot,
		constant.ChannelTypeDeepSeek,
		constant.ChannelTypeMiniMax,
		constant.ChannelTypeMistral,
		constant.ChannelTypePerplexity,
		constant.ChannelTypeLingYiWanWu,
		constant.ChannelTypeSiliconFlow,
		constant.ChannelTypeXai,
		constant.ChannelTypeAILS:
		return baseURL + "/v1/models", true
	case constant.ChannelTypeAnthropic:
		return baseURL + "/v1/models", true
	default:
		return "", false
	}
}

// fetchChannelUpstreamModelIDsWithKey is a thin wrapper that mirrors
// fetchChannelUpstreamModelIDs but takes an explicit key — needed so
// manual probes can target a specific key in a multi-key channel.
//
// Falls back to the existing helper (which calls GetNextEnabledKey
// internally) when the channel type isn't covered by our direct
// /v1/models path.
func fetchChannelUpstreamModelIDsWithKey(channel *model.Channel, key string) ([]string, error) {
	if key == "" {
		return fetchChannelUpstreamModelIDs(channel)
	}
	baseURL := resolveChannelBaseURL(channel)
	endpoint, ok := modelListEndpoint(channel.Type, baseURL)
	if !ok {
		// type not covered by our direct probe path; defer to existing helper
		return fetchChannelUpstreamModelIDs(channel)
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	applyProbeAuth(req, channel.Type, key)
	client, err := service.NewProxyHttpClient(channel.GetSetting().Proxy)
	if err != nil {
		return nil, err
	}
	client.Timeout = 15 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, err
	}
	var parsed OpenAIModelsResponse
	if err := common.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		id := strings.TrimPrefix(m.ID, "models/")
		ids = append(ids, id)
	}
	return ids, nil
}

// ============================================================================
// HTTP handlers
// ============================================================================

// ProbeChannelFingerprint is the admin handler that runs a manual probe
// against a specific channel (and optional key index), records a row in
// channel_fingerprint_history, and returns the new fingerprint.
func ProbeChannelFingerprint(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	channel, err := model.GetChannelById(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	keyIndex := -1
	if v := c.Query("key_index"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			keyIndex = parsed
		}
	}

	opts := probeOptions{source: "manual"}
	if keyIndex >= 0 {
		opts.useKeyIndex = true
		opts.keyIndex = keyIndex
	}

	fp, durationMs, err := runChannelFingerprintProbe(channel, opts)
	record := &model.ChannelFingerprintHistory{
		ChannelId:    channel.Id,
		KeyIndex:     keyIndex,
		KeyHint:      buildKeyHint(channel, keyIndex),
		Source:       "manual",
		ProbedAt:     time.Now().Unix(),
		DurationMs:   durationMs,
		ProbeVersion: constant.FingerprintProbeVersion,
	}
	if err != nil {
		record.ErrorMessage = err.Error()
	}
	if fp != nil {
		record.HeaderSetHash = fp.HeaderSetHash
		record.ErrorShapeHash = fp.ErrorShapeHash
		record.ModelSetHash = fp.ModelSetHash
		record.CompositeHash = fp.CompositeHash
	}
	if insertErr := model.InsertChannelFingerprintHistory(record); insertErr != nil {
		common.SysError("failed to record fingerprint history: " + insertErr.Error())
	}

	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
			"data":    record,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"fingerprint": fp,
			"history":     record,
		},
	})
}

// GetChannelFingerprintHistory returns the persisted manual probe history
// for a channel, most recent first.
func GetChannelFingerprintHistory(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	limit := 200
	if v := c.Query("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	rows, err := model.ListChannelFingerprintHistory(id, limit)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	channel, err := model.GetChannelById(id, true)
	keys := []gin.H{}
	if err == nil && channel != nil {
		raw := channel.GetKeys()
		for i, k := range raw {
			keys = append(keys, gin.H{
				"index": i,
				"hint":  maskKey(k),
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"history": rows,
			"keys":    keys,
		},
	})
}

func buildKeyHint(channel *model.Channel, keyIndex int) string {
	if keyIndex < 0 {
		return ""
	}
	keys := channel.GetKeys()
	if keyIndex >= len(keys) {
		return ""
	}
	return maskKey(keys[keyIndex])
}

func maskKey(key string) string {
	k := strings.TrimSpace(key)
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return strings.Repeat("*", len(k))
	}
	return k[:4] + "..." + k[len(k)-4:]
}
