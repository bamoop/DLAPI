package controller

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/fingerprint"

	"github.com/gin-gonic/gin"
)

// runProbeSafely wraps a probe so a panic in any one probe is reported as a
// failed probe row instead of killing the SSE stream and leaving the UI to
// silently hang. The captured stack goes into the probe's Error field so the
// operator can file a bug against the exact probe that misbehaved.
func runProbeSafely(id string, fn func() probeResult) (res probeResult) {
	defer func() {
		if r := recover(); r != nil {
			res = probeResult{
				Probe:  id,
				Name:   "panic in " + id,
				Status: probeStatusFail,
				Error:  fmt.Sprintf("panic: %v\n%s", r, debug.Stack()),
			}
		}
	}()
	return fn()
}

// ============================================================================
// Claude 真伪 & 渠道深度检测
// ----------------------------------------------------------------------------
// 设计原则（来自用户）：
//   - 检测以准确为核心，不省探针；
//   - 对客户负责，必须给出可复核的证据（raw_request/raw_response 全部回传给前端）；
//   - 不依赖任何上游 URL / Header `server` / `x-via` 等可伪造字段，
//     只依赖 Anthropic 协议层独有的语义。
//
// 探针序列 P1..P7 见 plan/nifty-watching-codd.md。每个 probe 一次 SSE 事件，
// 最后一个 summary 事件汇总分数 + 渠道判定 + 证据。
// ============================================================================

type claudeDetectRequest struct {
	upstreamTestTarget
	// 用户配置的检测模型；signature roundtrip 探针固定 sonnet-4-6 不受这个影响。
	ModelName string `json:"model_name"`
}

// probeResult is the structure of every `event: probe` SSE payload.
type probeResult struct {
	Probe          string         `json:"probe"`            // P1..P7
	Name           string         `json:"name"`             // human readable
	Status         string         `json:"status"`           // "pass"|"warn"|"fail"|"skip"
	ScoreDelta     int            `json:"score_delta"`      // contribution to authenticity score
	LatencyMs      int            `json:"latency_ms"`
	RequestMethod  string         `json:"request_method,omitempty"`
	RequestURL     string         `json:"request_url,omitempty"`
	RequestHeaders map[string]string `json:"request_headers,omitempty"`
	RequestBody    string         `json:"request_body,omitempty"`
	StatusCode     int            `json:"status_code,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	ResponseBody   string         `json:"response_body,omitempty"`
	// Evidence rows shown in the per-probe table:
	//   [{field, observed, expected, conclusion}]
	Evidence []evidenceRow `json:"evidence,omitempty"`
	Notes    string        `json:"notes,omitempty"`
	Error    string        `json:"error,omitempty"`
}

type evidenceRow struct {
	Field      string `json:"field"`
	Observed   string `json:"observed"`
	Expected   string `json:"expected"`
	Conclusion string `json:"conclusion"` // "ok"|"warn"|"bad"
}

const (
	probeStatusPass = "pass"
	probeStatusWarn = "warn"
	probeStatusFail = "fail"
	probeStatusSkip = "skip"

	// signature roundtrip is decisive — fix the model so we don't accidentally
	// land on Opus 4.7 which rejects budget_tokens.
	thinkingProbeModel = "claude-sonnet-4-6"
	thinkingBudget     = 4096

	// snippets in raw_request / raw_response payloads.
	maxBodyEcho = 32 * 1024
)

var (
	// Anthropic 全用 prefix + "_01" + 22 char Crockford base32 (no I L O U)
	reMsgId          = regexp.MustCompile(`^msg_01[0-9A-HJKMNP-TV-Z]{22}$`)
	reReqId          = regexp.MustCompile(`^req_01[0-9A-HJKMNP-TV-Z]{22}$`)
	reAtDateVariant  = regexp.MustCompile(`@\d{8}`) // claude-x-y-z@20251201 -> Vertex
	reBedrockLegacy  = regexp.MustCompile(`anthropic\.claude-[^"]*-v\d+:\d+`)
	reBedrockMantle  = regexp.MustCompile(`anthropic\.claude-[^"]*[^v]\d+`) // anthropic.claude-* without -vN:N
	reBase64ish      = regexp.MustCompile(`^[A-Za-z0-9+/=_\-]+$`)
	reRFC3339UTC     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`)
	validStopReasons = map[string]struct{}{
		"end_turn": {}, "max_tokens": {}, "stop_sequence": {},
		"tool_use": {}, "pause_turn": {}, "refusal": {},
	}
	validServiceTiers = map[string]struct{}{
		"standard": {}, "priority": {}, "batch": {},
	}
	// 4 个 rate-limit family 各 3 个字段，缺一不算"齐全"
	requiredRateLimitHeaders = []string{
		"anthropic-ratelimit-requests-limit",
		"anthropic-ratelimit-requests-remaining",
		"anthropic-ratelimit-requests-reset",
		"anthropic-ratelimit-tokens-limit",
		"anthropic-ratelimit-tokens-remaining",
		"anthropic-ratelimit-tokens-reset",
		"anthropic-ratelimit-input-tokens-limit",
		"anthropic-ratelimit-input-tokens-remaining",
		"anthropic-ratelimit-input-tokens-reset",
		"anthropic-ratelimit-output-tokens-limit",
		"anthropic-ratelimit-output-tokens-remaining",
		"anthropic-ratelimit-output-tokens-reset",
	}
	// Prefill-locked models — these MUST return 400 for a trailing assistant turn.
	// 来自官方文档 "Prefill not supported": Mythos Preview, Opus 4.7, Opus 4.6, Sonnet 4.6
	prefillLockedModels = []string{
		"claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6",
	}
)

// ClaudeDetectUpstreamKey runs the full 7-probe Anthropic authenticity +
// channel detection sequence against (base_url, key) (or a site/group reference)
// and streams results over SSE.
//
// Routing: POST /api/upstream/key-test/claude-detect
func ClaudeDetectUpstreamKey(c *gin.Context) {
	var req claudeDetectRequest
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

	base := strings.TrimRight(baseURL, "/")
	ctx := c.Request.Context()

	emit("start", gin.H{
		"key_hint":     keyHint,
		"base_url":     base,
		"target_model": req.ModelName,
	})

	det := newDetectionState()
	// 记录 base host 字面量：只用它判「是否直连 api.anthropic.com」，
	// 不再用可透传的 org-id 头判直连（修复 org-id 过度信任）。
	if u, perr := url.Parse(base); perr == nil {
		det.baseHost = u.Hostname()
	}

	// Probes are intentionally executed serially: P5 depends on P4's signature,
	// P8 second call depends on its own first call.
	probes := []struct {
		id string
		fn func() probeResult
	}{
		{"P1", func() probeResult { return cdProbeListModels(ctx, base, key, det) }},
		{"P2", func() probeResult { return cdProbeMessagesBasic(ctx, base, key, req.ModelName, det) }},
		{"P3", func() probeResult { return cdProbeCountTokens(ctx, base, key, req.ModelName, det) }},
		{"P4", func() probeResult { return cdProbeThinkingStream(ctx, base, key, det) }},
		{"P5", func() probeResult { return cdProbeSignatureRoundtrip(ctx, base, key, det) }},
		{"P6", func() probeResult { return cdProbeErrorShape(ctx, base, key, det) }},
		{"P7", func() probeResult { return cdProbeCapabilityEndpoints(ctx, base, key, det) }},
		{"P8", func() probeResult { return cdProbeCacheRoundtrip(ctx, base, key, det) }},
		{"P9", func() probeResult { return cdProbePrefillRejection(ctx, base, key, det) }},
		{"P10", func() probeResult { return cdProbeVersionHeaderReject(ctx, base, key, det) }},
		{"P11", func() probeResult { return cdProbeThinkingToolChoiceReject(ctx, base, key, det) }},
		{"P12", func() probeResult { return cdProbeBudgetTokensMinReject(ctx, base, key, det) }},
		{"P13", func() probeResult { return cdProbeWebSearchAcceptance(ctx, base, key, det) }},
		{"P14", func() probeResult { return cdProbeBogusBetaHandling(ctx, base, key, det) }},
		{"P15", func() probeResult { return cdProbeThroughputDowngrade(ctx, base, key, req.ModelName, det) }},
	}
	for _, p := range probes {
		r := runProbeSafely(p.id, p.fn)
		det.addScore(r.ScoreDelta)
		emit("probe", r)
	}

	dv := det.dualVerdictInfo()
	channel := det.channel()

	emit("summary", gin.H{
		"score": det.score,
		// 兼容旧前端的聚合字段。
		"verdict":       dv.legacy.label,
		"verdict_color": dv.legacy.color,
		"verdict_reason": dv.legacy.reason,
		// 新：双轴结论 + 降级旗标。前端应优先渲染这两轴。
		"backend_label":   dv.backend.label,
		"backend_color":   dv.backend.color,
		"backend_reason":  dv.backend.reason,
		"integrity_label":  dv.integrity.label,
		"integrity_color":  dv.integrity.color,
		"integrity_reason": dv.integrity.reason,
		"downgrade_suspect": det.throughputDowngrade,
		"downgrade_decisive": det.throughputDecisive,
		"baseline_calibrated": fingerprint.BaselineCalibrated(),
		"channel":            channel.label,
		"channel_evidence":   channel.evidence,
		"evidence_count":     len(det.evidence),
		"composite_evidence": det.evidence,
	})
}

// ----------------------------------------------------------------------------
// State accumulator
// ----------------------------------------------------------------------------

type detectionState struct {
	score int

	// observations
	authStyle             string // "x-api-key" | "bearer" | "both" | "neither"
	modelIds              []string
	pathPrefixSeen        string // e.g. "/anthropic/v1/messages"
	hasAnthropicRatelimit bool
	hasRequestId          bool
	requestIdShapeOk      bool // request-id matches req_01[ULID]
	requestIdValue        string
	ratelimitFamilyCount  int  // 0..4, how many of the 4 ratelimit families are fully present
	ratelimitFamiliesOk   bool // all 4 families with limit+remaining+reset, and reset is RFC3339 UTC
	hasOrgIdHeader        bool // anthropic-organization-id — only Anthropic direct
	orgIdValue            string
	hasAwsRequestId       bool // x-amzn-requestid → Claude Platform on AWS
	hasCloudflareHeaders  bool // cf-ray / server: cloudflare → in front of Anthropic direct
	msgIdShapeOk          bool
	stopReasonOk          bool
	tokenAccuracy         string // ClassifyTokenAccuracy result
	thinkingSig           string // captured for P5
	thinkingBlock         any    // captured raw block list for P5
	signatureRoundtrip    string // "pass"|"fail"|"unknown"
	sseSequenceHash       string
	sseHasMessageStop     bool
	sseHasPing            bool
	sseMsgStartStopNull   bool // message_start.message.stop_reason MUST be null
	sseDeltaUsageCumOk    bool // message_delta.usage cumulative (output_tokens monotonic increasing across multiple deltas, OR single delta with usage >= message_start's usage.output_tokens)
	serviceTier           string
	serviceTierHeader     string // anthropic-ratelimit-priority-tier
	serviceTierConsistent bool
	hasUsageOutputTokens  bool // output_tokens should NEVER be 0 per spec, even on empty replies
	errorShape            string // "anthropic"|"openai"|"unknown"
	errorBodyHasReqId     bool   // anthropic error envelope's request_id field present
	batchesAnthropic      bool
	filesAnthropic        bool
	batches404            bool
	files404              bool
	cacheCreatePresent    bool // P8 first call: cache_creation_input_tokens > 0
	cacheReadPresent      bool // P8 second call: cache_read_input_tokens > 0
	cacheNestedBreakdown  bool // usage.cache_creation.{ephemeral_5m,ephemeral_1h}_input_tokens nesting present
	prefillRejected       bool // P9: prefill-locked model correctly rejects with anthropic error

	// P1 deep observations from /v1/models response.
	modelsTypeFieldOk      bool // each entry has "type":"model"  (vs OpenAI's "object":"model")
	modelsCapabilitiesOk   bool // at least one entry has nested capabilities tree (post-2026 schema)
	modelsThinkingTypesOk  bool // capabilities.thinking.types.{adaptive,enabled} keys present
	modelsContextMgmtOk    bool // capabilities.context_management.clear_thinking_20251015 present
	modelsPaginationOk     bool // top-level has_more / first_id / last_id present (Anthropic-style cursor pagination)
	modelsHasCreatedAtRFC3339 bool

	// P10..P12 observations
	versionHeaderRejectsBogus  bool // bogus anthropic-version returns 400 + invalid_request_error
	thinkingToolChoiceRejected bool // thinking + tool_choice:"any" returns 400 with specific msg
	budgetTokensMinRejected    bool // budget_tokens < 1024 returns 400 with specific msg

	// P13..P14 backend forensics — leak signals that survive relay transparent proxy.
	// usage.inference_geo: free-form string ("us"/"eu"), populated ONLY by Anthropic direct.
	// usage.service_tier=="priority" hints at Direct (Tier 4+ paid).
	// Webserver tool support: web_search_20260209 only on Direct + Platform on AWS.
	// Bogus beta header: Direct silently ignores → 200; strict relays → 400.
	hasInferenceGeo     bool
	inferenceGeoValue   string
	modelEchoVariant    string // exact response.model from latest probe (highest-confidence backend hint)
	webSearchSupported  bool   // P13: web_search_20260209 tool schema accepted (200)
	webSearchRejected   bool   // P13: explicit 400 mentioning tool type → Bedrock/Vertex hint
	bogusBetaIgnored    bool   // P14: bogus anthropic-beta header silently accepted (200) → Direct hint
	bogusBetaRejected   bool   // P14: bogus beta header → 400 → strict relay

	// base URL host — 用于「是否字面直连 api.anthropic.com」判定，
	// 避免把透传 org-id 头的中转误判成直连。
	baseHost string

	// P3 tokenizer 差分锚点（外部真值比对，抓换模型）。
	tokenDeltaObserved map[string]int  // block name → 实测增量 Δ
	tokenDeltaInRange  map[string]bool // block name → Δ 是否落在 Claude 真值区间
	tokenDeltaChecked  int             // 有真值可比的块数
	tokenDeltaAnomaly  int             // 落在真值区间外的块数

	// P15 吞吐降级探针（抓「按 opus 收钱、走 haiku」）。
	throughputModel    string  // 实测所用宣称模型
	throughputTokPerSec float64 // 实测 output tokens/sec
	throughputTTFBms   int     // 首字延迟
	throughputDowngrade bool   // 宣称档位与实测吞吐不符，疑似降级
	throughputDecisive bool    // 已标定且信号明确，可下铁结论

	evidence []evidenceRow
}

func newDetectionState() *detectionState {
	return &detectionState{
		signatureRoundtrip: "unknown",
		errorShape:         "unknown",
	}
}

func (s *detectionState) addScore(d int) { s.score += d }

func (s *detectionState) addEvidence(rows ...evidenceRow) {
	s.evidence = append(s.evidence, rows...)
}

type verdictInfo struct {
	label  string
	color  string // "green"|"yellow"|"red"
	reason string
}

// axisInfo 是单条结论轴的输出。
type axisInfo struct {
	label  string // 人类可读结论
	color  string // "green"|"yellow"|"red"
	reason string
}

// dualVerdict 把旧的单一绿灯拆成两条独立结论 + 一个降级旗标。
//
//	backend  —— 生成 token 的后端是不是真 Claude 协议（穿透中转仍可判）；
//	integrity—— 链路是否诚信：前面有没有中转、交付模型是否=宣称模型。
//
// 关键纠偏：signature roundtrip 透传必过，故只作**负向**铁证（fail→后端红），
// 通过不再加正分、不再当「直连」证据。
type dualVerdict struct {
	backend   axisInfo
	integrity axisInfo
	// legacy 仍提供一个聚合 label，兼容旧前端字段（过渡期）。
	legacy verdictInfo
}

// backendAxis：后端真实性。证据优先级——
// 负向铁证（signature fail / 错误信封非 anthropic / tokenizer 严重偏离）> 正向特征。
func (s *detectionState) backendAxis() axisInfo {
	// 负向铁证 1：thinking 不支持 / 签名被拒 → 后端几乎肯定不是真 Claude。
	if s.signatureRoundtrip == "fail" {
		return axisInfo{label: "后端非真 Claude", color: "red",
			reason: "thinking.signature 无法在上游验过 / 上游不支持 extended thinking — 真 Claude 后端不会在此失败"}
	}
	// 负向铁证 2：错误信封被改写成非 Anthropic 形态（如 OpenAI/自造）。
	if s.errorShape == "openai" || s.errorShape == "fabricated" {
		return axisInfo{label: "后端可疑（错误形态非 Anthropic）", color: "red",
			reason: "故意触发的错误返回非 Anthropic 错误信封 — 后端或中转改写了响应"}
	}
	// 负向提示：tokenizer 差分锚点偏离（仅在已标定时作强证据）。
	if fingerprint.BaselineCalibrated() && s.tokenDeltaChecked > 0 && s.tokenDeltaAnomaly == s.tokenDeltaChecked {
		return axisInfo{label: "后端可疑（分词器画像不符）", color: "red",
			reason: "count_tokens 差分增量全部偏离 Claude 真值区间 — 后端疑似换成了非 Claude 模型"}
	}

	// 正向：协议特征齐全（注意——这些大多可透传，故只能支撑「后端是 Claude 协议」，
	// 不能支撑「直连」，方向层判定见 channel()）。
	strong := 0
	if s.signatureRoundtrip == "pass" {
		strong++
	}
	if s.errorShape == "anthropic" {
		strong++
	}
	if s.msgIdShapeOk && s.stopReasonOk {
		strong++
	}
	if s.cacheReadPresent {
		strong++
	}
	if fingerprint.BaselineCalibrated() && s.tokenDeltaChecked > 0 && s.tokenDeltaAnomaly == 0 {
		strong++
	}
	switch {
	case strong >= 3:
		return axisInfo{label: "后端是真 Claude 协议", color: "green",
			reason: "签名可验 + 错误信封 + 消息形态 + 缓存/分词器特征一致"}
	case strong >= 1:
		return axisInfo{label: "后端疑似 Claude（证据不足）", color: "yellow",
			reason: "部分协议特征匹配，但独占/差分证据不全，无法确认后端真实模型"}
	default:
		return axisInfo{label: "后端无法确认", color: "red",
			reason: "缺失关键 Claude 协议特征"}
	}
}

// integrityAxis：链路诚信。两件事——有没有中转、有没有降级。
func (s *detectionState) integrityAxis() axisInfo {
	relayed := !s.isLiteralAnthropicHost()
	// 降级旗标优先级最高：直接影响计费公平性。
	if s.throughputDowngrade {
		color := "yellow"
		reason := fmt.Sprintf("宣称 %s 但实测吞吐 %.0f tok/s 落在更快的廉价档 — 疑似降级（吞吐受网络影响，仅作旗标）",
			s.throughputModel, s.throughputTokPerSec)
		if s.throughputDecisive {
			color = "red"
			reason = fmt.Sprintf("宣称 %s 但实测吞吐 %.0f tok/s 明确落在廉价档区间 — 高度疑似按高价模型计费、走廉价后端",
				s.throughputModel, s.throughputTokPerSec)
		}
		return axisInfo{label: "疑似模型降级", color: color, reason: reason}
	}
	if !relayed {
		return axisInfo{label: "直连 Anthropic（host 字面匹配）", color: "green",
			reason: "base_url 主机名即 api.anthropic.com，无中间转发"}
	}
	// 有中转但未发现降级。
	return axisInfo{label: "经中转（≥1 跳）", color: "yellow",
		reason: "非 Anthropic 官方 host，请求经至少一层转发；未发现明显模型降级，但无法保证零掺水"}
}

// isLiteralAnthropicHost 判定 base host 是否就是 Anthropic 官方域名。
// 只认字面量，不认任何可透传的响应头（org-id 等）——这是修复「org-id 过度信任」的核心。
func (s *detectionState) isLiteralAnthropicHost() bool {
	h := strings.ToLower(s.baseHost)
	return h == "api.anthropic.com" || strings.HasSuffix(h, ".anthropic.com")
}

func (s *detectionState) verdict() verdictInfo {
	dv := s.dualVerdictInfo()
	return dv.legacy
}

// dualVerdictInfo 计算两条轴并合成一个过渡期兼容的聚合结论。
func (s *detectionState) dualVerdictInfo() dualVerdict {
	backend := s.backendAxis()
	integrity := s.integrityAxis()

	// 聚合：取两轴中较差的颜色作为总色；label 同时呈现两轴 + 降级旗标。
	worst := worseColor(backend.color, integrity.color)
	legacyLabel := fmt.Sprintf("后端：%s ｜ 链路：%s", backend.label, integrity.label)
	legacyReason := backend.reason + "；" + integrity.reason
	return dualVerdict{
		backend:   backend,
		integrity: integrity,
		legacy:    verdictInfo{label: legacyLabel, color: worst, reason: legacyReason},
	}
}

// worseColor 返回两个颜色里更「差」的那个（red > yellow > green）。
func worseColor(a, b string) string {
	rank := map[string]int{"green": 0, "yellow": 1, "red": 2}
	if rank[a] >= rank[b] {
		return a
	}
	return b
}

type channelInfo struct {
	label    string
	evidence []evidenceRow
}

// channel 做双层判定：
//   - frontend：客户端直接对话的那一层 HTTP 服务（Direct / Bedrock / Vertex / Foundry / 中转）
//   - backend：真正生成 token 的那一层模型源（只有真 Anthropic / Platform / Bedrock /
//     Vertex 能给出 token）。当 frontend 是中转时，backend 还是要尽量根据响应体里
//     穿透过来的 model echo / inference_geo / service_tier / server-tool 接受度去逆推。
//
// 输出 label 形如：
//   "Anthropic 直连"
//   "Vertex AI"
//   "中转 (后端: Bedrock Legacy)"
//   "中转 (后端: Anthropic 直连)"
//   "中转 (后端无法确认)"
func (s *detectionState) channel() channelInfo {
	out := channelInfo{evidence: []evidenceRow{}}

	frontend, frontEv := s.frontendLayer()
	out.evidence = append(out.evidence, frontEv...)

	// 如果 frontend 已经是某个一线渠道（不是中转），那么 backend 就是它自己。
	if frontend != "中转" && frontend != "无法判定" {
		out.label = frontend
		return out
	}

	// frontend 是中转：尽量从 leak signals 推 backend。
	backend, backEv := s.backendLayer()
	out.evidence = append(out.evidence, backEv...)

	switch frontend {
	case "中转":
		if backend == "无法确认" {
			out.label = "中转 (后端无法确认)"
		} else {
			out.label = fmt.Sprintf("中转 (后端: %s)", backend)
		}
	default:
		// frontend = 无法判定
		if backend != "无法确认" {
			out.label = fmt.Sprintf("无法判定前端层 (后端疑似: %s)", backend)
		} else {
			out.label = "无法判定"
			out.evidence = append(out.evidence, evidenceRow{
				Field: "channel", Observed: "ambiguous", Expected: "decisive", Conclusion: "warn",
			})
		}
	}
	return out
}

// frontendLayer：客户端面对的 HTTP 那一层是谁。
// 返回 ("Anthropic 直连" / "Vertex AI" / "Bedrock Legacy ..." / "Microsoft Foundry"
// / "Claude Platform on AWS" / "中转" / "无法判定")。
func (s *detectionState) frontendLayer() (string, []evidenceRow) {
	ev := []evidenceRow{}

	// 「Anthropic 直连」只认 base host 字面量 == api.anthropic.com。
	// 修复 org-id 过度信任：org-id / cf-ray 等都是**可透传**响应头，new-api/one-api
	// 默认原样转发，后端直连 Claude 的套壳站会原样带出这些头 → 旧逻辑误判成直连。
	// 现在它们只作辅助证据，不再单独触发「直连」结论。
	if s.isLiteralAnthropicHost() {
		ev = append(ev, evidenceRow{
			Field: "base_host", Observed: s.baseHost,
			Expected: "api.anthropic.com (字面直连)", Conclusion: "ok",
		})
		if s.hasOrgIdHeader {
			ev = append(ev, evidenceRow{
				Field: "anthropic-organization-id", Observed: maskOrgId(s.orgIdValue),
				Expected: "present", Conclusion: "ok",
			})
		}
		return "Anthropic 直连", ev
	}
	// 非官方 host 但带 org-id：仅作弱提示，归入中转路径继续判后端。
	if s.hasOrgIdHeader {
		ev = append(ev, evidenceRow{
			Field: "anthropic-organization-id", Observed: maskOrgId(s.orgIdValue),
			Expected: "可透传头，不足以证明直连", Conclusion: "warn",
		})
	}

	// Claude Platform on AWS：双 request-id（x-amzn-requestid + request-id）
	if s.hasAwsRequestId && s.hasRequestId {
		ev = append(ev,
			evidenceRow{Field: "x-amzn-requestid", Observed: "present", Expected: "Platform on AWS", Conclusion: "ok"},
			evidenceRow{Field: "request-id", Observed: s.requestIdValue, Expected: "req_01[ULID]", Conclusion: "ok"},
		)
		return "Claude Platform on AWS", ev
	}

	// Foundry：Bearer + 路径 /anthropic/v1/messages
	if s.authStyle == "bearer" && strings.Contains(s.pathPrefixSeen, "/anthropic/v1/messages") {
		ev = append(ev, evidenceRow{
			Field: "auth_style + path", Observed: "Bearer + /anthropic/v1/messages",
			Expected: "Foundry", Conclusion: "ok",
		})
		return "Microsoft Foundry", ev
	}

	// Bedrock / Vertex：通过 /v1/models 里能列出对应形态的模型 ID。
	for _, m := range s.modelIds {
		if reAtDateVariant.MatchString(m) {
			ev = append(ev, evidenceRow{
				Field: "model_id", Observed: m,
				Expected: "claude-*@YYYYMMDD (Vertex)", Conclusion: "ok",
			})
			return "Vertex AI", ev
		}
		if reBedrockLegacy.MatchString(m) {
			ev = append(ev, evidenceRow{
				Field: "model_id", Observed: m,
				Expected: "anthropic.claude-*-vN:N (Bedrock Legacy)", Conclusion: "ok",
			})
			return "Bedrock Legacy (InvokeModel/Converse)", ev
		}
		if strings.HasPrefix(m, "anthropic.claude") {
			ev = append(ev, evidenceRow{
				Field: "model_id", Observed: m,
				Expected: "anthropic.claude-* (Bedrock Mantle)", Conclusion: "ok",
			})
			return "Bedrock Mantle (anthropic/v1/messages)", ev
		}
	}

	// 走到这里说明 base host 不是 api.anthropic.com，前端层一定是某种转发。
	// 旧逻辑这里会用 batches/files/ratelimit/bogusBeta 这些**可透传**信号「推测直连」，
	// 与 host-gated 直连判定矛盾，已删除。非官方 host 一律按中转，后端再由 backendLayer 推。
	ev = append(ev, evidenceRow{
		Field: "base_host", Observed: s.baseHost,
		Expected: "非 api.anthropic.com → 经转发", Conclusion: "warn",
	})
	if s.batches404 || s.files404 {
		ev = append(ev, evidenceRow{
			Field: "batches/files endpoints", Observed: "404",
			Expected: "Direct 可用 → 该层剥离了能力端点", Conclusion: "warn",
		})
	}
	if s.bogusBetaRejected {
		ev = append(ev, evidenceRow{
			Field: "bogus anthropic-beta", Observed: "rejected (400)",
			Expected: "Direct 静默忽略 → 严格代理层", Conclusion: "warn",
		})
	}
	// 协议能跑通（签名可验/消息形态 ok）→ 明确是「中转」；否则前端层无法判定。
	if s.signatureRoundtrip == "pass" || (s.msgIdShapeOk && s.errorShape == "anthropic") {
		return "中转", ev
	}
	return "无法判定", ev
}

// backendLayer：实际生成 token 的模型源。当 frontend 是中转时，必须从响应体
// 泄漏出来的 model echo / inference_geo / service_tier / web_search 接受度去判
// 断 — 这些信号穿透字节透传代理。
//
// 优先级（高 → 低）：
//
//	1. model echo 形态：@YYYYMMDD → Vertex；anthropic.*-vN:N → Bedrock Legacy；
//	   anthropic.* (无 -vN:N) → Bedrock Mantle
//	2. usage.inference_geo 存在：Anthropic 直连
//	3. web_search server tool 接受 + msg_id 形态 ok：Direct / Claude Platform on AWS
//	4. web_search 显式拒绝 + signature_roundtrip pass：Bedrock / Vertex（一般禁用）
//	5. signature_roundtrip pass + 没其他证据：Anthropic 直连（推测）
//
// 返回 ("Anthropic 直连" / "Vertex AI" / "Bedrock Legacy ..." / "Microsoft Foundry"
// / "Claude Platform on AWS" / "无法确认")。
func (s *detectionState) backendLayer() (string, []evidenceRow) {
	ev := []evidenceRow{}

	// 1. model echo 形态：最不可伪造的后端指纹
	if s.modelEchoVariant != "" {
		if reAtDateVariant.MatchString(s.modelEchoVariant) {
			ev = append(ev, evidenceRow{
				Field: "backend.model_echo", Observed: s.modelEchoVariant,
				Expected: "claude-*@YYYYMMDD (Vertex)", Conclusion: "ok",
			})
			return "Vertex AI", ev
		}
		if reBedrockLegacy.MatchString(s.modelEchoVariant) {
			ev = append(ev, evidenceRow{
				Field: "backend.model_echo", Observed: s.modelEchoVariant,
				Expected: "anthropic.claude-*-vN:N (Bedrock Legacy)", Conclusion: "ok",
			})
			return "Bedrock Legacy (InvokeModel/Converse)", ev
		}
		if strings.HasPrefix(s.modelEchoVariant, "anthropic.claude") {
			ev = append(ev, evidenceRow{
				Field: "backend.model_echo", Observed: s.modelEchoVariant,
				Expected: "anthropic.claude-* (Bedrock Mantle)", Conclusion: "ok",
			})
			return "Bedrock Mantle (anthropic/v1/messages)", ev
		}
	}

	// 2. usage.inference_geo —— 仅 Anthropic 直连返回
	if s.hasInferenceGeo {
		ev = append(ev, evidenceRow{
			Field: "backend.usage.inference_geo", Observed: s.inferenceGeoValue,
			Expected: "present iff Anthropic 直连", Conclusion: "ok",
		})
		return "Anthropic 直连", ev
	}

	// 3. web_search server tool 接受 → Direct / Claude Platform on AWS
	if s.webSearchSupported && s.msgIdShapeOk {
		ev = append(ev, evidenceRow{
			Field: "backend.web_search_supported", Observed: "yes",
			Expected: "Direct / Platform on AWS only", Conclusion: "ok",
		})
		if s.hasAwsRequestId {
			return "Claude Platform on AWS", ev
		}
		return "Anthropic 直连 / Claude Platform on AWS", ev
	}

	// 4. web_search 显式拒绝 + signature roundtrip 通过 → 后端是 Bedrock / Vertex
	if s.webSearchRejected && s.signatureRoundtrip == "pass" {
		ev = append(ev, evidenceRow{
			Field: "backend.web_search_rejected", Observed: "yes (400 mentioning tool)",
			Expected: "yes iff Bedrock / Vertex / Foundry", Conclusion: "ok",
		})
		return "Bedrock / Vertex (server tool 不可用)", ev
	}

	// 5. service_tier == "priority" 只在 Direct Tier4+ 出现 — 弱信号
	if s.serviceTier == "priority" && s.signatureRoundtrip == "pass" {
		ev = append(ev, evidenceRow{
			Field: "backend.usage.service_tier", Observed: "priority",
			Expected: "Direct Tier4+ only", Conclusion: "ok",
		})
		return "Anthropic 直连 (priority tier)", ev
	}

	// 6. signature roundtrip 通过 + cache 命中 + msg_id 形态 → 至少是 Anthropic 系产品（推测 Direct）
	if s.signatureRoundtrip == "pass" && s.cacheReadPresent && s.msgIdShapeOk {
		ev = append(ev, evidenceRow{
			Field: "backend.signature+cache", Observed: "both ok",
			Expected: "Anthropic 系", Conclusion: "ok",
		})
		return "Anthropic 系 (推测 Direct / Platform)", ev
	}

	ev = append(ev, evidenceRow{
		Field: "backend", Observed: "无后端指纹泄漏",
		Expected: "model echo / inference_geo / web_search 接受度", Conclusion: "warn",
	})
	return "无法确认", ev
}

// maskOrgId 脱敏组织 ID（仅保留首尾各 4 字符），避免回显完整 UUID。
func maskOrgId(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:4] + "…" + s[len(s)-4:]
}

func firstMatch(items []string, re *regexp.Regexp) string {
	for _, s := range items {
		if re.MatchString(s) {
			return s
		}
	}
	return ""
}

func firstAnthropicPrefix(items []string) string {
	for _, s := range items {
		if strings.HasPrefix(s, "anthropic.claude") {
			return s
		}
	}
	return ""
}

// ----------------------------------------------------------------------------
// HTTP helpers
// ----------------------------------------------------------------------------

func newDetectClient() *http.Client {
	return &http.Client{Timeout: 120 * time.Second}
}

func headerMap(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = strings.Join(v, ", ")
	}
	return out
}

func anthropicAuthHeaders(key string) map[string]string {
	return map[string]string{
		"x-api-key":         key,
		"anthropic-version": "2023-06-01",
		"Content-Type":      "application/json",
	}
}

// ----------------------------------------------------------------------------
// P1 — /v1/models 枚举（同时试两种鉴权方式）
// ----------------------------------------------------------------------------

func cdProbeListModels(ctx context.Context, base, key string, det *detectionState) probeResult {
	res := probeResult{Probe: "P1", Name: "/v1/models 枚举 + 鉴权风格"}
	start := time.Now()

	endpoint := base + "/v1/models"
	res.RequestMethod = "GET"
	res.RequestURL = endpoint

	// Try x-api-key first.
	xapiBody, xapiStatus, xapiHeaders, xapiErr := doSimpleGet(ctx, endpoint, map[string]string{
		"x-api-key":         key,
		"anthropic-version": "2023-06-01",
	})
	// Fallback: Bearer.
	bearerBody, bearerStatus, _, bearerErr := doSimpleGet(ctx, endpoint, map[string]string{
		"Authorization":     "Bearer " + key,
		"anthropic-version": "2023-06-01",
	})
	res.LatencyMs = int(time.Since(start) / time.Millisecond)

	xapiOk := xapiErr == nil && xapiStatus >= 200 && xapiStatus < 300
	bearerOk := bearerErr == nil && bearerStatus >= 200 && bearerStatus < 300

	res.RequestHeaders = map[string]string{
		"x-api-key (attempt 1)":      "<redacted>",
		"Authorization (attempt 2)":  "Bearer <redacted>",
		"anthropic-version":          "2023-06-01",
	}
	res.ResponseHeaders = headerMap(xapiHeaders)

	// Pick the best body for echo to UI.
	var body []byte
	switch {
	case xapiOk:
		body = xapiBody
		det.authStyle = "x-api-key"
		res.StatusCode = xapiStatus
	case bearerOk:
		body = bearerBody
		det.authStyle = "bearer"
		res.StatusCode = bearerStatus
	default:
		body = xapiBody
		if len(body) == 0 {
			body = bearerBody
		}
		det.authStyle = "neither"
		res.StatusCode = xapiStatus
	}
	res.ResponseBody = truncate(string(body), maxBodyEcho)

	// Anthropic 2026+ shape:
	//   {"data":[{"id","type":"model","display_name","created_at","capabilities":{...}}],
	//    "first_id","last_id","has_more"}
	// OpenAI-style relay shape:
	//   {"object":"list","data":[{"id","object":"model","owned_by":"..."}]}
	// We parse loosely with map[string]any so partial matches still surface in evidence.
	var raw map[string]any
	_ = common.Unmarshal(body, &raw)

	// pagination cursors / has_more — only Anthropic returns these in /v1/models.
	if raw != nil {
		_, hasFirst := raw["first_id"]
		_, hasLast := raw["last_id"]
		_, hasMore := raw["has_more"]
		det.modelsPaginationOk = hasFirst && hasLast && hasMore
	}

	dataArr, _ := raw["data"].([]any)
	entriesWithCaps := 0
	entriesWithTypeModel := 0
	entriesWithThinkingTypes := 0
	entriesWithCtxMgmt := 0
	entriesWithCreatedAtRFC := 0
	for _, item := range dataArr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := m["id"].(string); id != "" {
			det.modelIds = append(det.modelIds, id)
		}
		if t, _ := m["type"].(string); t == "model" {
			entriesWithTypeModel++
		}
		if cAt, _ := m["created_at"].(string); reRFC3339UTC.MatchString(cAt) || strings.HasSuffix(cAt, ":00Z") {
			entriesWithCreatedAtRFC++
		}
		caps, ok := m["capabilities"].(map[string]any)
		if !ok {
			continue
		}
		entriesWithCaps++

		// capabilities.thinking.types.{adaptive,enabled}.supported
		if thinking, ok := caps["thinking"].(map[string]any); ok {
			if types, ok := thinking["types"].(map[string]any); ok {
				_, ad := types["adaptive"]
				_, en := types["enabled"]
				if ad || en {
					entriesWithThinkingTypes++
				}
			}
		}
		// capabilities.context_management.clear_thinking_20251015 — very Anthropic-specific
		if cm, ok := caps["context_management"].(map[string]any); ok {
			_, ct := cm["clear_thinking_20251015"]
			_, ctu := cm["clear_tool_uses_20250919"]
			if ct || ctu {
				entriesWithCtxMgmt++
			}
		}
	}
	det.modelsTypeFieldOk = entriesWithTypeModel > 0 && entriesWithTypeModel == len(dataArr)
	det.modelsCapabilitiesOk = entriesWithCaps > 0
	det.modelsThinkingTypesOk = entriesWithThinkingTypes > 0
	det.modelsContextMgmtOk = entriesWithCtxMgmt > 0
	det.modelsHasCreatedAtRFC3339 = entriesWithCreatedAtRFC > 0

	switch {
	case xapiOk && bearerOk:
		det.authStyle = "both"
		res.Status = probeStatusPass
		res.Notes = "两种鉴权风格都通过：典型中转"
	case xapiOk:
		res.Status = probeStatusPass
	case bearerOk:
		res.Status = probeStatusWarn
		res.Notes = "只接受 Bearer：可能 Vertex / Foundry / 中转"
	default:
		res.Status = probeStatusFail
		res.Notes = "两种鉴权都失败：上游可能未实现 /v1/models 或鉴权错误"
	}

	// Scoring (max 16): the /v1/models deep schema is hard to fake because
	// `capabilities` is a 2026 schema addition; relays mirror old shapes.
	score := 0
	if det.modelsTypeFieldOk {
		score += 2 // "type":"model" (vs OpenAI "object":"model")
	}
	if det.modelsCapabilitiesOk {
		score += 5 // nested capabilities object present
	}
	if det.modelsThinkingTypesOk {
		score += 4 // capabilities.thinking.types.{adaptive,enabled}
	}
	if det.modelsContextMgmtOk {
		score += 3 // context_management.clear_thinking_20251015 — very specific
	}
	if det.modelsPaginationOk {
		score += 2 // first_id/last_id/has_more cursor pagination
	}
	res.ScoreDelta = score

	res.Evidence = []evidenceRow{
		{Field: "auth_style", Observed: det.authStyle, Expected: "x-api-key (Anthropic 直连)", Conclusion: cond(xapiOk, "ok", "warn")},
		{Field: "model_count", Observed: itoa(len(det.modelIds)), Expected: ">=1", Conclusion: cond(len(det.modelIds) > 0, "ok", "warn")},
		{Field: "data[].type", Observed: fmt.Sprintf("%d/%d == \"model\"", entriesWithTypeModel, len(dataArr)), Expected: "all \"model\" (Anthropic); \"object\":\"model\" = OpenAI relay", Conclusion: cond(det.modelsTypeFieldOk, "ok", "bad")},
		{Field: "data[].capabilities", Observed: fmt.Sprintf("%d/%d entries have nested capabilities", entriesWithCaps, len(dataArr)), Expected: ">0 (2026 schema)", Conclusion: cond(det.modelsCapabilitiesOk, "ok", "bad")},
		{Field: "capabilities.thinking.types.{adaptive,enabled}", Observed: fmt.Sprintf("%d entries", entriesWithThinkingTypes), Expected: ">0 (Anthropic only)", Conclusion: cond(det.modelsThinkingTypesOk, "ok", "warn")},
		{Field: "capabilities.context_management.clear_thinking_20251015", Observed: fmt.Sprintf("%d entries", entriesWithCtxMgmt), Expected: ">0 (Anthropic only)", Conclusion: cond(det.modelsContextMgmtOk, "ok", "warn")},
		{Field: "data[].created_at (RFC3339 UTC)", Observed: fmt.Sprintf("%d/%d valid", entriesWithCreatedAtRFC, len(dataArr)), Expected: "all valid RFC3339 UTC", Conclusion: cond(det.modelsHasCreatedAtRFC3339, "ok", "warn")},
		{Field: "top-level has_more/first_id/last_id", Observed: cond(det.modelsPaginationOk, "present", "absent"), Expected: "present (Anthropic cursor pagination)", Conclusion: cond(det.modelsPaginationOk, "ok", "warn")},
	}
	det.addEvidence(res.Evidence...)
	return res
}

// ----------------------------------------------------------------------------
// P2 — /v1/messages 普通请求
// ----------------------------------------------------------------------------

func cdProbeMessagesBasic(ctx context.Context, base, key, model string, det *detectionState) probeResult {
	res := probeResult{Probe: "P2", Name: "/v1/messages 形态验证"}
	start := time.Now()

	endpoint := base + "/v1/messages"
	body, _ := common.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 64,
		"messages": []map[string]any{
			{"role": "user", "content": "Reply with the single word: PONG."},
		},
	})
	respBody, status, respHeaders, err := doPostJSON(ctx, endpoint, body, anthropicAuthHeaders(key))
	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	res.RequestMethod = "POST"
	res.RequestURL = endpoint
	res.RequestHeaders = map[string]string{
		"x-api-key":         "<redacted>",
		"anthropic-version": "2023-06-01",
		"Content-Type":      "application/json",
	}
	res.RequestBody = string(body)
	res.StatusCode = status
	res.ResponseHeaders = headerMap(respHeaders)
	res.ResponseBody = truncate(string(respBody), maxBodyEcho)
	if err != nil {
		res.Status = probeStatusFail
		res.Error = err.Error()
		res.ScoreDelta = 0
		det.addEvidence(evidenceRow{Field: "transport", Observed: err.Error(), Expected: "200", Conclusion: "bad"})
		return res
	}
	if status < 200 || status >= 300 {
		res.Status = probeStatusFail
		res.Notes = fmt.Sprintf("HTTP %d", status)
		det.addEvidence(evidenceRow{Field: "http_status", Observed: itoa(status), Expected: "2xx", Conclusion: "bad"})
		return res
	}

	var parsed struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Role       string `json:"role"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens              int    `json:"input_tokens"`
			OutputTokens             int    `json:"output_tokens"`
			CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int    `json:"cache_read_input_tokens"`
			ServiceTier              string `json:"service_tier"`
			InferenceGeo             string `json:"inference_geo"`
		} `json:"usage"`
	}
	_ = common.Unmarshal(respBody, &parsed)

	idOk := reMsgId.MatchString(parsed.ID)
	_, stopOk := validStopReasons[parsed.StopReason]
	det.msgIdShapeOk = idOk
	det.stopReasonOk = stopOk
	det.hasUsageOutputTokens = parsed.Usage.OutputTokens > 0

	// service_tier observation
	if parsed.Usage.ServiceTier != "" {
		det.serviceTier = parsed.Usage.ServiceTier
	}

	// Backend forensics — model echo + inference_geo leak through transparent relays.
	if parsed.Model != "" {
		det.modelEchoVariant = parsed.Model
		if !containsString(det.modelIds, parsed.Model) {
			det.modelIds = append(det.modelIds, parsed.Model)
		}
	}
	if parsed.Usage.InferenceGeo != "" {
		det.hasInferenceGeo = true
		det.inferenceGeoValue = parsed.Usage.InferenceGeo
	}

	// Header sweep — case-insensitive lookups via http.Header methods.
	for h := range respHeaders {
		lh := strings.ToLower(h)
		if strings.HasPrefix(lh, "anthropic-ratelimit-") {
			det.hasAnthropicRatelimit = true
		}
		if lh == "request-id" || lh == "x-request-id" {
			det.hasRequestId = true
		}
	}
	det.requestIdValue = respHeaders.Get("Request-Id")
	if det.requestIdValue == "" {
		det.requestIdValue = respHeaders.Get("X-Request-Id")
	}
	det.requestIdShapeOk = reReqId.MatchString(det.requestIdValue)
	det.orgIdValue = respHeaders.Get("Anthropic-Organization-Id")
	det.hasOrgIdHeader = det.orgIdValue != ""
	if respHeaders.Get("X-Amzn-Requestid") != "" || respHeaders.Get("X-Amzn-RequestId") != "" {
		det.hasAwsRequestId = true
	}
	if respHeaders.Get("Cf-Ray") != "" || strings.Contains(strings.ToLower(respHeaders.Get("Server")), "cloudflare") {
		det.hasCloudflareHeaders = true
	}
	det.serviceTierHeader = respHeaders.Get("Anthropic-Ratelimit-Priority-Tier")
	if det.serviceTier != "" && det.serviceTierHeader != "" {
		det.serviceTierConsistent = det.serviceTier == det.serviceTierHeader
	}

	// Check 4 ratelimit families are fully populated (limit/remaining/reset)
	// AND that the *-reset value is a valid RFC3339 UTC timestamp.
	missingHeaders := []string{}
	for _, h := range requiredRateLimitHeaders {
		if respHeaders.Get(h) == "" {
			missingHeaders = append(missingHeaders, h)
		}
	}
	families := []string{"requests", "tokens", "input-tokens", "output-tokens"}
	familyCount := 0
	for _, f := range families {
		if respHeaders.Get("Anthropic-Ratelimit-"+f+"-Limit") != "" &&
			respHeaders.Get("Anthropic-Ratelimit-"+f+"-Remaining") != "" &&
			reRFC3339UTC.MatchString(respHeaders.Get("Anthropic-Ratelimit-"+f+"-Reset")) {
			familyCount++
		}
	}
	det.ratelimitFamilyCount = familyCount
	det.ratelimitFamiliesOk = familyCount == 4

	// path prefix observation (some upstreams advertise themselves via a
	// rewritten request URL in the response, e.g. Foundry's /anthropic/v1/messages):
	if loc := respHeaders.Get("X-Forwarded-Uri"); loc != "" {
		det.pathPrefixSeen = loc
	}

	// Scoring:
	//   msg_id + stop_reason             5
	//   request-id matches req_01[ULID]  5
	//   4 ratelimit families fully ok    5
	//   anthropic-organization-id        5
	//   service_tier present + agrees    3
	//   output_tokens > 0                2
	score := 0
	if idOk && stopOk {
		score += 5
	}
	if det.requestIdShapeOk {
		score += 5
	}
	if det.ratelimitFamiliesOk {
		score += 5
	}
	if det.hasOrgIdHeader {
		score += 5
	}
	if det.serviceTier != "" {
		score += 1
		if det.serviceTierConsistent {
			score += 2
		}
	}
	if det.hasUsageOutputTokens {
		score += 2
	}
	if idOk && stopOk && det.requestIdShapeOk {
		res.Status = probeStatusPass
	} else if idOk || stopOk {
		res.Status = probeStatusWarn
	} else {
		res.Status = probeStatusFail
	}
	res.ScoreDelta = score

	res.Evidence = []evidenceRow{
		{Field: "id", Observed: parsed.ID, Expected: "msg_01[ULID 22 Crockford]", Conclusion: cond(idOk, "ok", "bad")},
		{Field: "stop_reason", Observed: parsed.StopReason, Expected: "end_turn|max_tokens|stop_sequence|tool_use|pause_turn|refusal", Conclusion: cond(stopOk, "ok", "bad")},
		{Field: "model_echoed", Observed: parsed.Model, Expected: model + " (or variant)", Conclusion: "ok"},
		{Field: "request-id header", Observed: det.requestIdValue, Expected: "req_01[ULID 22 Crockford]", Conclusion: cond(det.requestIdShapeOk, "ok", cond(det.hasRequestId, "warn", "bad"))},
		{Field: "anthropic-ratelimit (4 families)", Observed: fmt.Sprintf("%d/4 complete", det.ratelimitFamilyCount), Expected: "4/4 (Anthropic direct)", Conclusion: cond(det.ratelimitFamiliesOk, "ok", "warn")},
		{Field: "anthropic-organization-id", Observed: cond(det.hasOrgIdHeader, maskOrgId(det.orgIdValue), "absent"), Expected: "present (Anthropic direct only)", Conclusion: cond(det.hasOrgIdHeader, "ok", "warn")},
		{Field: "x-amzn-requestid (Claude Platform on AWS)", Observed: cond(det.hasAwsRequestId, "present", "absent"), Expected: "present iff Platform on AWS", Conclusion: "ok"},
		{Field: "cf-ray / server", Observed: cond(det.hasCloudflareHeaders, "Cloudflare edge", "no CF"), Expected: "Cloudflare edge iff direct", Conclusion: "ok"},
		{Field: "usage.service_tier", Observed: cond(det.serviceTier != "", det.serviceTier, "absent"), Expected: "standard|priority|batch", Conclusion: cond(det.serviceTier != "", "ok", "warn")},
		{Field: "service_tier header consistency", Observed: cond(det.serviceTierHeader != "", det.serviceTierHeader, "absent"), Expected: "= usage.service_tier", Conclusion: cond(det.serviceTierConsistent, "ok", "warn")},
		{Field: "usage.output_tokens", Observed: itoa(parsed.Usage.OutputTokens), Expected: ">0 (spec: never 0)", Conclusion: cond(det.hasUsageOutputTokens, "ok", "bad")},
		{Field: "usage.input_tokens", Observed: itoa(parsed.Usage.InputTokens), Expected: ">0", Conclusion: cond(parsed.Usage.InputTokens > 0, "ok", "warn")},
		{Field: "usage.inference_geo (backend hint)", Observed: cond(det.hasInferenceGeo, det.inferenceGeoValue, "absent"), Expected: "present iff Anthropic 直连后端", Conclusion: cond(det.hasInferenceGeo, "ok", "warn")},
		{Field: "model echo (backend hint)", Observed: parsed.Model, Expected: "原样 / @date / anthropic.*-vN:N", Conclusion: "ok"},
	}
	if len(missingHeaders) > 0 && len(missingHeaders) <= 6 {
		res.Evidence = append(res.Evidence, evidenceRow{
			Field: "missing ratelimit headers", Observed: strings.Join(missingHeaders, ", "),
			Expected: "all 12 present", Conclusion: "warn",
		})
	}
	det.addEvidence(res.Evidence...)
	return res
}

// ----------------------------------------------------------------------------
// P3 — count_tokens 精度比对
// ----------------------------------------------------------------------------

// countTokensFor 调一次 count_tokens，返回上游报告的 input_tokens。
// 失败返回 (-1, err/status说明)。
func countTokensFor(ctx context.Context, base, key, model, content string) (int, string) {
	body, _ := common.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]any{
			{"role": "user", "content": content},
		},
	})
	respBody, status, _, err := doPostJSON(ctx, base+"/v1/messages/count_tokens", body, anthropicAuthHeaders(key))
	if err != nil {
		return -1, err.Error()
	}
	if status < 200 || status >= 300 {
		return -1, fmt.Sprintf("HTTP %d", status)
	}
	var ct struct {
		InputTokens int `json:"input_tokens"`
	}
	_ = common.Unmarshal(respBody, &ct)
	return ct.InputTokens, ""
}

// cdProbeCountTokens (P3) —— tokenizer 差分锚点。
//
// 旧实现把同一上游的 count_tokens 与它自己的 /v1/messages usage 比，永远自洽，
// 套壳站白拿满分。新实现引入**外部真值**：对固定语料 base 与 base+BLOCK 分别
// 计数，求增量 Δ(BLOCK)，与硬编码的 Claude 真值区间（service/fingerprint/baseline.go）
// 比对。中转注入的固定前缀在做差时抵消 → Δ 不受注入污染；后端换成 GPT/Gemini/
// 廉价模型时各块 Δ 画像不同 → 露馅。
func cdProbeCountTokens(ctx context.Context, base, key, model string, det *detectionState) probeResult {
	res := probeResult{Probe: "P3", Name: "count_tokens 分词器差分指纹"}
	start := time.Now()
	res.RequestMethod = "POST"
	res.RequestURL = base + "/v1/messages/count_tokens"
	res.RequestHeaders = map[string]string{"x-api-key": "<redacted>", "anthropic-version": "2023-06-01"}

	det.tokenDeltaObserved = map[string]int{}
	det.tokenDeltaInRange = map[string]bool{}

	// 基底计数（减数）。
	baseTokens, baseErr := countTokensFor(ctx, base, key, model, fingerprint.TokenCorpusBase())
	if baseTokens < 0 {
		res.Status = probeStatusFail
		res.Notes = fmt.Sprintf("base 语料 count_tokens 失败：%s — 上游未实现 count_tokens（仅 Anthropic / Platform / Mantle 提供）", baseErr)
		det.addEvidence(evidenceRow{Field: "count_tokens", Observed: baseErr, Expected: "200", Conclusion: "bad"})
		res.LatencyMs = int(time.Since(start) / time.Millisecond)
		return res
	}

	evidences := []evidenceRow{
		{Field: "count_tokens.base", Observed: itoa(baseTokens), Expected: ">0", Conclusion: cond(baseTokens > 0, "ok", "bad")},
	}
	var reqBlocks []string

	for _, blk := range fingerprint.TokenCorpusBlocks() {
		full := fingerprint.TokenCorpusBase() + blk.Text
		fullTokens, ferr := countTokensFor(ctx, base, key, model, full)
		if fullTokens < 0 {
			evidences = append(evidences, evidenceRow{
				Field: "delta." + blk.Name, Observed: ferr, Expected: "200", Conclusion: "bad",
			})
			continue
		}
		delta := fullTokens - baseTokens
		det.tokenDeltaObserved[blk.Name] = delta
		v := fingerprint.ClassifyTokenDelta(blk.Name, delta)
		if v.HasTruth {
			det.tokenDeltaChecked++
			det.tokenDeltaInRange[blk.Name] = v.InRange
			if !v.InRange {
				det.tokenDeltaAnomaly++
			}
			conclusion := "ok"
			if !v.InRange {
				conclusion = "bad"
			}
			evidences = append(evidences, evidenceRow{
				Field:      "delta." + blk.Name,
				Observed:   itoa(delta),
				Expected:   fmt.Sprintf("[%d,%d] (Claude 真值)", v.Low, v.High),
				Conclusion: conclusion,
			})
		} else {
			evidences = append(evidences, evidenceRow{
				Field: "delta." + blk.Name, Observed: itoa(delta), Expected: "(无真值)", Conclusion: "warn",
			})
		}
		reqBlocks = append(reqBlocks, blk.Name)
	}

	// 评分：仅在已标定真值时给正分（占位值阶段只作弱提示，避免误判）。
	score := 0
	switch {
	case !fingerprint.BaselineCalibrated():
		res.Status = probeStatusWarn
		res.Notes = "基线真值未标定（baselineCalibrated=false）：差分仅作参考，未计分。Stage 0 标定后收紧。"
	case det.tokenDeltaChecked == 0:
		res.Status = probeStatusWarn
		res.Notes = "无可比真值块"
	case det.tokenDeltaAnomaly == 0:
		score = 20
		res.Status = probeStatusPass
	case det.tokenDeltaAnomaly < det.tokenDeltaChecked:
		score = 5
		res.Status = probeStatusWarn
		res.Notes = fmt.Sprintf("%d/%d 差分块偏离 Claude 真值区间", det.tokenDeltaAnomaly, det.tokenDeltaChecked)
	default:
		res.Status = probeStatusFail
		res.Notes = "全部差分块偏离 Claude 真值区间 — 后端疑似非 Claude 分词器"
	}
	res.ScoreDelta = score
	res.RequestBody = fmt.Sprintf("count_tokens x%d (base + blocks: %s)", len(reqBlocks)+1, strings.Join(reqBlocks, ","))
	res.Evidence = evidences
	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	det.addEvidence(res.Evidence...)
	return res
}

// ----------------------------------------------------------------------------
// P4 — extended thinking + SSE
// ----------------------------------------------------------------------------

func cdProbeThinkingStream(ctx context.Context, base, key string, det *detectionState) probeResult {
	res := probeResult{Probe: "P4", Name: "extended thinking + 流式签名"}
	start := time.Now()
	endpoint := base + "/v1/messages"

	body, _ := common.Marshal(map[string]any{
		"model":      thinkingProbeModel,
		"max_tokens": 1024,
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": thinkingBudget,
		},
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "Think step by step: what is 17 * 23? Show your reasoning, then answer."},
		},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	for k, v := range anthropicAuthHeaders(key) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := newDetectClient().Do(req)
	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	res.RequestMethod = "POST"
	res.RequestURL = endpoint
	res.RequestHeaders = map[string]string{
		"x-api-key":         "<redacted>",
		"anthropic-version": "2023-06-01",
		"Accept":            "text/event-stream",
	}
	res.RequestBody = string(body)
	if err != nil {
		res.Status = probeStatusFail
		res.Error = err.Error()
		det.addEvidence(evidenceRow{Field: "transport", Observed: err.Error(), Expected: "200", Conclusion: "bad"})
		return res
	}
	defer resp.Body.Close()
	res.StatusCode = resp.StatusCode
	res.ResponseHeaders = headerMap(resp.Header)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyEcho))
		res.ResponseBody = truncate(string(body), maxBodyEcho)
		res.Status = probeStatusFail
		res.Notes = fmt.Sprintf("HTTP %d", resp.StatusCode)
		det.addEvidence(evidenceRow{Field: "http_status", Observed: itoa(resp.StatusCode), Expected: "2xx", Conclusion: "bad"})
		return res
	}

	// Consume the SSE stream, capturing event types in order and reassembling
	// the assistant content blocks (especially the thinking block + signature).
	eventTypes := make([]string, 0, 32)
	var rawBuf strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1024*64), 1024*1024)

	// Reassembled state.
	type contentBlock struct {
		Type      string `json:"type"`
		Text      string `json:"text,omitempty"`
		Thinking  string `json:"thinking,omitempty"`
		Signature string `json:"signature,omitempty"`
	}
	blocks := make(map[int]*contentBlock)
	var eventType string

	// Spec invariants we want to verify:
	//   - message_start.message.stop_reason MUST be null (final stop_reason arrives later).
	//   - message_delta.usage is CUMULATIVE (warning box in streaming docs). For our
	//     short probe we'll see one message_delta containing usage; check that its
	//     output_tokens >= the small initial value reported in message_start.usage.
	msgStartStopReason := "<missing>"
	msgStartHasNull := false
	startOutputTokens := -1
	lastDeltaOutputTokens := -1
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if rawBuf.Len() < maxBodyEcho {
			rawBuf.WriteString(line)
			rawBuf.WriteByte('\n')
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(line[len("event:"):])
			eventTypes = append(eventTypes, eventType)
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataLine := strings.TrimSpace(line[len("data:"):])
		if dataLine == "" || dataLine == "[DONE]" {
			continue
		}
		switch eventType {
		case "message_start":
			// data: {"type":"message_start","message":{...,"stop_reason":null,"usage":{"input_tokens":N,"output_tokens":1}}}
			var raw map[string]any
			if err := common.UnmarshalJsonStr(dataLine, &raw); err == nil {
				if msg, ok := raw["message"].(map[string]any); ok {
					// stop_reason field: must be present AND null
					if v, present := msg["stop_reason"]; present {
						if v == nil {
							msgStartHasNull = true
							msgStartStopReason = "null"
						} else {
							msgStartStopReason = fmt.Sprintf("%v", v)
						}
					}
					if u, ok := msg["usage"].(map[string]any); ok {
						if ot, ok := u["output_tokens"].(float64); ok {
							startOutputTokens = int(ot)
						}
					}
				}
			}
		case "message_delta":
			// data: {"type":"message_delta","delta":{"stop_reason":...},"usage":{"output_tokens":N}}
			var payload struct {
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := common.UnmarshalJsonStr(dataLine, &payload); err == nil {
				lastDeltaOutputTokens = payload.Usage.OutputTokens
			}
		case "content_block_start":
			var payload struct {
				Index        int          `json:"index"`
				ContentBlock contentBlock `json:"content_block"`
			}
			if err := common.UnmarshalJsonStr(dataLine, &payload); err == nil {
				cb := payload.ContentBlock
				blocks[payload.Index] = &cb
			}
		case "content_block_delta":
			var payload struct {
				Index int `json:"index"`
				Delta struct {
					Type      string `json:"type"`
					Text      string `json:"text"`
					Thinking  string `json:"thinking"`
					Signature string `json:"signature"`
				} `json:"delta"`
			}
			if err := common.UnmarshalJsonStr(dataLine, &payload); err == nil {
				if blocks[payload.Index] == nil {
					blocks[payload.Index] = &contentBlock{}
				}
				switch payload.Delta.Type {
				case "thinking_delta":
					blocks[payload.Index].Thinking += payload.Delta.Thinking
					blocks[payload.Index].Type = "thinking"
				case "signature_delta":
					blocks[payload.Index].Signature += payload.Delta.Signature
					if blocks[payload.Index].Type == "" {
						blocks[payload.Index].Type = "thinking"
					}
				case "text_delta":
					blocks[payload.Index].Text += payload.Delta.Text
					if blocks[payload.Index].Type == "" {
						blocks[payload.Index].Type = "text"
					}
				}
			}
		}
	}
	res.ResponseBody = truncate(rawBuf.String(), maxBodyEcho)
	det.sseMsgStartStopNull = msgStartHasNull
	// Cumulative-usage check: if both numbers are present, last message_delta.usage
	// should be >= message_start.usage. Real Claude: ~equal or greater. Relay that
	// emits per-chunk increments will have small or zero last delta.
	det.sseDeltaUsageCumOk = startOutputTokens >= 0 && lastDeltaOutputTokens >= 0 &&
		lastDeltaOutputTokens >= startOutputTokens && lastDeltaOutputTokens >= 5

	// Check SSE sequence completeness.
	hasMessageStart := false
	for _, e := range eventTypes {
		switch e {
		case "message_start":
			hasMessageStart = true
		case "message_stop":
			det.sseHasMessageStop = true
		case "ping":
			det.sseHasPing = true
		}
	}
	det.sseSequenceHash = fingerprint.HashSSESequence(eventTypes)

	// Find the thinking block (if any) and inspect signature.
	var thinkingBlock *contentBlock
	for idx := 0; idx < len(blocks); idx++ {
		if b, ok := blocks[idx]; ok && b.Type == "thinking" {
			thinkingBlock = b
			break
		}
	}
	if thinkingBlock == nil {
		// Try any block with a signature, regardless of declared type.
		for idx := 0; idx < len(blocks); idx++ {
			if b, ok := blocks[idx]; ok && b.Signature != "" {
				thinkingBlock = b
				thinkingBlock.Type = "thinking"
				break
			}
		}
	}

	sigLen := 0
	sigLooksLikeBase64 := false
	if thinkingBlock != nil {
		sigLen = len(thinkingBlock.Signature)
		sigLooksLikeBase64 = sigLen > 0 && reBase64ish.MatchString(thinkingBlock.Signature)
		det.thinkingSig = thinkingBlock.Signature
		// Build the ordered list of blocks for P5.
		ordered := make([]any, 0, len(blocks))
		for idx := 0; idx < 32; idx++ {
			b, ok := blocks[idx]
			if !ok {
				continue
			}
			switch b.Type {
			case "thinking":
				ordered = append(ordered, map[string]any{
					"type":      "thinking",
					"thinking":  b.Thinking,
					"signature": b.Signature,
				})
			case "text":
				if b.Text != "" {
					ordered = append(ordered, map[string]any{"type": "text", "text": b.Text})
				}
			}
		}
		det.thinkingBlock = ordered
	}

	// Scoring (max 38):
	//   thinking.signature shape (len>=200, base64ish)   15
	//   SSE message_stop + ping cycle                     5
	//   message_start.message.stop_reason == null         3
	//   message_delta.usage cumulative                    5
	score := 0
	switch {
	case thinkingBlock != nil && sigLen >= 200 && sigLooksLikeBase64:
		score += 15
		res.Status = probeStatusPass
	case thinkingBlock != nil && sigLen >= 50:
		score += 5
		res.Status = probeStatusWarn
	default:
		res.Status = probeStatusFail
	}
	if det.sseHasMessageStop && det.sseHasPing {
		score += 5
	} else if det.sseHasMessageStop {
		score += 2
	}
	if det.sseMsgStartStopNull {
		score += 3
	}
	if det.sseDeltaUsageCumOk {
		score += 5
	}
	res.ScoreDelta = score

	res.Evidence = []evidenceRow{
		{Field: "sse.message_start", Observed: cond(hasMessageStart, "yes", "no"), Expected: "yes", Conclusion: cond(hasMessageStart, "ok", "bad")},
		{Field: "sse.message_start.stop_reason", Observed: msgStartStopReason, Expected: "null (spec)", Conclusion: cond(det.sseMsgStartStopNull, "ok", "warn")},
		{Field: "sse.message_delta.usage cumulative", Observed: fmt.Sprintf("start=%d → delta=%d", startOutputTokens, lastDeltaOutputTokens), Expected: "delta ≥ start (spec: cumulative)", Conclusion: cond(det.sseDeltaUsageCumOk, "ok", "warn")},
		{Field: "sse.message_stop", Observed: cond(det.sseHasMessageStop, "yes", "no"), Expected: "yes", Conclusion: cond(det.sseHasMessageStop, "ok", "warn")},
		{Field: "sse.ping", Observed: cond(det.sseHasPing, "yes", "no"), Expected: "yes (direct, ~15s)", Conclusion: cond(det.sseHasPing, "ok", "warn")},
		{Field: "thinking.signature.length", Observed: itoa(sigLen), Expected: ">=200", Conclusion: cond(sigLen >= 200, "ok", "bad")},
		{Field: "thinking.signature.base64", Observed: cond(sigLooksLikeBase64, "yes", "no"), Expected: "yes", Conclusion: cond(sigLooksLikeBase64, "ok", "bad")},
		{Field: "sse.sequence_hash", Observed: det.sseSequenceHash[:min(12, len(det.sseSequenceHash))], Expected: "stable", Conclusion: "ok"},
	}
	det.addEvidence(res.Evidence...)
	return res
}

// ----------------------------------------------------------------------------
// P5 — signature roundtrip（金标准：上游签名只有真 Anthropic 能验过）
// ----------------------------------------------------------------------------

func cdProbeSignatureRoundtrip(ctx context.Context, base, key string, det *detectionState) probeResult {
	res := probeResult{Probe: "P5", Name: "thinking.signature roundtrip"}
	start := time.Now()
	endpoint := base + "/v1/messages"

	if det.thinkingBlock == nil {
		res.Status = probeStatusSkip
		res.Notes = "P4 未拿到 thinking block，无法 roundtrip"
		det.signatureRoundtrip = "fail"
		det.addEvidence(evidenceRow{Field: "signature_roundtrip", Observed: "skipped", Expected: "pass", Conclusion: "bad"})
		return res
	}

	body, _ := common.Marshal(map[string]any{
		"model":      thinkingProbeModel,
		"max_tokens": 256,
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": thinkingBudget,
		},
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "Think step by step: what is 17 * 23? Show your reasoning, then answer.",
			},
			map[string]any{
				"role":    "assistant",
				"content": det.thinkingBlock,
			},
			map[string]any{
				"role":    "user",
				"content": "Now multiply that result by 2.",
			},
		},
	})

	respBody, status, respHeaders, err := doPostJSON(ctx, endpoint, body, anthropicAuthHeaders(key))
	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	res.RequestMethod = "POST"
	res.RequestURL = endpoint
	res.RequestHeaders = map[string]string{"x-api-key": "<redacted>", "anthropic-version": "2023-06-01"}
	res.RequestBody = string(body)
	res.StatusCode = status
	res.ResponseHeaders = headerMap(respHeaders)
	res.ResponseBody = truncate(string(respBody), maxBodyEcho)
	if err != nil {
		res.Status = probeStatusFail
		res.Error = err.Error()
		det.signatureRoundtrip = "fail"
		det.addEvidence(evidenceRow{Field: "signature_roundtrip", Observed: err.Error(), Expected: "200", Conclusion: "bad"})
		return res
	}
	if status >= 200 && status < 300 {
		res.Status = probeStatusPass
		// 注意：签名 roundtrip「透传必过」——被测上游只要把请求原样转发给真
		// Claude 后端就能通过，这无法区分直连 vs 中转。故此处**不再加正分**，
		// 也不在 channel() 里当「直连」证据。它只在 fail 分支作负向铁证。
		res.ScoreDelta = 0
		res.Notes = "签名可验证（注意：透明中转转发也会通过，仅说明后端是 Claude 协议，不代表直连）"
		det.signatureRoundtrip = "pass"
		// Backend forensics — harvest model echo and inference_geo from the
		// validated roundtrip body. These leak through transparent relays.
		var p5 struct {
			Model string `json:"model"`
			Usage struct {
				ServiceTier  string `json:"service_tier"`
				InferenceGeo string `json:"inference_geo"`
			} `json:"usage"`
		}
		_ = common.Unmarshal(respBody, &p5)
		if p5.Model != "" {
			det.modelEchoVariant = p5.Model
			if !containsString(det.modelIds, p5.Model) {
				det.modelIds = append(det.modelIds, p5.Model)
			}
		}
		if p5.Usage.InferenceGeo != "" {
			det.hasInferenceGeo = true
			det.inferenceGeoValue = p5.Usage.InferenceGeo
		}
		if p5.Usage.ServiceTier != "" && det.serviceTier == "" {
			det.serviceTier = p5.Usage.ServiceTier
		}
		det.addEvidence(evidenceRow{Field: "signature_roundtrip", Observed: "200", Expected: "200 (server verified sig)", Conclusion: "ok"})
		return res
	}

	// Inspect error message — Anthropic typically returns
	// "Invalid `thinking.signature`" / "could not be verified".
	low := strings.ToLower(string(respBody))
	switch {
	case strings.Contains(low, "signature") && (strings.Contains(low, "invalid") || strings.Contains(low, "verif")):
		res.Status = probeStatusFail
		res.Notes = "上游报告签名无效 — 真 Claude 不应在这里失败"
		det.signatureRoundtrip = "fail"
	case strings.Contains(low, "thinking") && strings.Contains(low, "not supported"):
		res.Status = probeStatusFail
		res.Notes = "上游声称不支持 thinking — 几乎肯定不是真 Claude"
		det.signatureRoundtrip = "fail"
	default:
		res.Status = probeStatusFail
		res.Notes = fmt.Sprintf("HTTP %d", status)
		det.signatureRoundtrip = "fail"
	}
	det.addEvidence(evidenceRow{Field: "signature_roundtrip", Observed: fmt.Sprintf("HTTP %d", status), Expected: "200", Conclusion: "bad"})
	return res
}

// ----------------------------------------------------------------------------
// P6 — 错误响应形态（Anthropic vs OpenAI）
// ----------------------------------------------------------------------------

func cdProbeErrorShape(ctx context.Context, base, key string, det *detectionState) probeResult {
	res := probeResult{Probe: "P6", Name: "错误响应形态"}
	start := time.Now()
	endpoint := base + "/v1/messages"
	body, _ := common.Marshal(map[string]any{
		"model":      "claude-nonexistent-model-zzz",
		"max_tokens": 16,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	})
	respBody, status, respHeaders, err := doPostJSON(ctx, endpoint, body, anthropicAuthHeaders(key))
	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	res.RequestMethod = "POST"
	res.RequestURL = endpoint
	res.RequestHeaders = map[string]string{"x-api-key": "<redacted>", "anthropic-version": "2023-06-01"}
	res.RequestBody = string(body)
	res.StatusCode = status
	res.ResponseHeaders = headerMap(respHeaders)
	res.ResponseBody = truncate(string(respBody), maxBodyEcho)
	if err != nil {
		res.Status = probeStatusFail
		res.Error = err.Error()
		return res
	}
	if status < 400 {
		// Some shady upstreams return 200 + fabricated content for any model — also bad.
		res.Status = probeStatusFail
		res.Notes = "不存在模型却返回 2xx — 严重伪造嫌疑"
		det.errorShape = "fabricated"
		det.addEvidence(evidenceRow{Field: "invalid_model.status", Observed: itoa(status), Expected: "4xx", Conclusion: "bad"})
		return res
	}
	// Try anthropic shape — note official examples include a top-level request_id
	// field on error responses (see /docs/en/api/errors).
	var anth struct {
		Type      string `json:"type"`
		RequestId string `json:"request_id"`
		Error     struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = common.Unmarshal(respBody, &anth)
	isAnthropic := anth.Type == "error" && anth.Error.Type != "" && anth.Error.Message != ""
	det.errorBodyHasReqId = reReqId.MatchString(anth.RequestId)

	// Validate error.type is in the known enum.
	knownErrorTypes := map[string]struct{}{
		"invalid_request_error": {}, "authentication_error": {}, "billing_error": {},
		"permission_error": {}, "not_found_error": {}, "request_too_large": {},
		"rate_limit_error": {}, "api_error": {}, "overloaded_error": {}, "timeout_error": {},
	}
	_, errorTypeKnown := knownErrorTypes[anth.Error.Type]

	// Try OpenAI shape.
	var oai struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Param   any    `json:"param"`
		} `json:"error"`
	}
	_ = common.Unmarshal(respBody, &oai)
	isOpenAI := !isAnthropic && oai.Error.Message != "" && oai.Error.Type != ""

	// Scoring (max 8):
	//   Anthropic-shaped envelope                5
	//   error.type from known enum               2 (already implies anthropic)
	//   error envelope includes request_id req_01  3
	score := 0
	switch {
	case isAnthropic:
		det.errorShape = "anthropic"
		score += 5
		if errorTypeKnown {
			score += 2
		}
		if det.errorBodyHasReqId {
			score += 3
		}
		res.Status = probeStatusPass
	case isOpenAI:
		det.errorShape = "openai"
		res.Status = probeStatusFail
		res.Notes = "错误体是 OpenAI 形态 — 上游不是 Anthropic"
	default:
		det.errorShape = "unknown"
		res.Status = probeStatusWarn
		res.Notes = "错误体既不是 Anthropic 也不是 OpenAI 形态"
	}
	res.ScoreDelta = score
	res.Evidence = []evidenceRow{
		{Field: "error.shape", Observed: det.errorShape, Expected: "anthropic", Conclusion: cond(det.errorShape == "anthropic", "ok", "bad")},
		{Field: "error.type (anthropic.error.type)", Observed: anth.Error.Type, Expected: "invalid_request_error|authentication_error|...", Conclusion: cond(errorTypeKnown, "ok", cond(anth.Error.Type != "", "warn", "bad"))},
		{Field: "error.request_id", Observed: anth.RequestId, Expected: "req_01[ULID 22 Crockford]", Conclusion: cond(det.errorBodyHasReqId, "ok", "warn")},
	}
	det.addEvidence(res.Evidence...)
	return res
}

// ----------------------------------------------------------------------------
// P7 — 能力端点探测（batches / files）
// ----------------------------------------------------------------------------

func cdProbeCapabilityEndpoints(ctx context.Context, base, key string, det *detectionState) probeResult {
	res := probeResult{Probe: "P7", Name: "Anthropic 独占能力端点"}
	start := time.Now()

	// /v1/messages/batches: POST with empty requests array; direct Anthropic
	// returns 400 with anthropic-style error; relays without batches return 404.
	bEndpoint := base + "/v1/messages/batches"
	bBody, _ := common.Marshal(map[string]any{"requests": []any{}})
	bResp, bStatus, bHeaders, _ := doPostJSON(ctx, bEndpoint, bBody, mergeMap(anthropicAuthHeaders(key), map[string]string{
		"anthropic-beta": "message-batches-2024-09-24",
	}))
	det.batchesAnthropic = looksAnthropicError(bResp) && bStatus >= 400 && bStatus < 500 && bStatus != 404
	det.batches404 = bStatus == 404

	fEndpoint := base + "/v1/files"
	fResp, fStatus, fHeaders, _ := doSimpleGet(ctx, fEndpoint, map[string]string{
		"x-api-key":         key,
		"anthropic-version": "2023-06-01",
		"anthropic-beta":    "files-api-2025-04-14",
	})
	det.filesAnthropic = (fStatus >= 200 && fStatus < 300) || looksAnthropicError(fResp)
	det.files404 = fStatus == 404

	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	res.RequestMethod = "POST + GET"
	res.RequestURL = bEndpoint + " ; " + fEndpoint
	res.RequestHeaders = map[string]string{
		"batches:anthropic-beta": "message-batches-2024-09-24",
		"files:anthropic-beta":   "files-api-2025-04-14",
	}
	res.RequestBody = `// batches: ` + string(bBody) + ` // files: GET`
	res.StatusCode = bStatus
	res.ResponseHeaders = mergeMap(headerMap(bHeaders), prefixMap("files.", headerMap(fHeaders)))
	res.ResponseBody = "// batches:\n" + truncate(string(bResp), maxBodyEcho/2) + "\n\n// files:\n" + truncate(string(fResp), maxBodyEcho/2)

	score := 0
	switch {
	case det.batchesAnthropic && det.filesAnthropic:
		score = 0 // already credited via verdict tree; capability check pure-signal
		res.Status = probeStatusPass
		res.Notes = "batches + files 都返回 Anthropic 行为 → 强信号：Anthropic 直连"
	case det.batches404 && det.files404:
		res.Status = probeStatusWarn
		res.Notes = "batches + files 都 404 → 中转 / Bedrock / Vertex"
	default:
		res.Status = probeStatusWarn
	}
	res.ScoreDelta = score

	res.Evidence = []evidenceRow{
		{Field: "/v1/messages/batches", Observed: fmt.Sprintf("HTTP %d", bStatus), Expected: "Anthropic error or 200", Conclusion: cond(det.batchesAnthropic, "ok", "warn")},
		{Field: "/v1/files", Observed: fmt.Sprintf("HTTP %d", fStatus), Expected: "Anthropic error or 200", Conclusion: cond(det.filesAnthropic, "ok", "warn")},
	}
	det.addEvidence(res.Evidence...)
	return res
}

// ----------------------------------------------------------------------------
// P8 — prompt-cache 写入 + 命中 roundtrip
// ----------------------------------------------------------------------------
//
// Anthropic 协议层独占信号：cache_control:{type:"ephemeral",ttl:"5m"} 标记一
// 个 prefix 后，首次调用应有 usage.cache_creation_input_tokens > 0；紧跟其后
// 用完全相同的 prefix 再调一次，应有 usage.cache_read_input_tokens > 0，且
// usage.cache_creation.{ephemeral_5m,ephemeral_1h}_input_tokens 嵌套对象存
// 在。中转想完整伪造必须 ① 自实现 Anthropic 分词器，② 自实现 prefix-cache
// 命中逻辑，③ 还要让两次 usage 数字自洽。代价比 thinking.signature 略低，
// 但更难假装到对得上。
//
// Prompt 体积：构造一段 ≥ 2200 tokens（覆盖 Sonnet/Opus 1024 + Haiku 2048
// 两个门槛）的稳定 system text，cache_control 打在 system 块上。

func cdProbeCacheRoundtrip(ctx context.Context, base, key string, det *detectionState) probeResult {
	res := probeResult{Probe: "P8", Name: "prompt-cache 写入+命中 roundtrip"}
	start := time.Now()
	endpoint := base + "/v1/messages"

	// 拼一段稳定、可重复的填充文字。每段 ~50 tokens，重复 50 次约 2500 tokens。
	chunk := "The quick brown fox jumps over the lazy dog. Pack my box with five dozen liquor jugs. " +
		"How vexingly quick daft zebras jump! Sphinx of black quartz, judge my vow. "
	var sb strings.Builder
	sb.Grow(len(chunk) * 50)
	for i := 0; i < 50; i++ {
		sb.WriteString(chunk)
	}
	fillerSystem := sb.String()

	makeBody := func() []byte {
		body, _ := common.Marshal(map[string]any{
			"model":      thinkingProbeModel,
			"max_tokens": 16,
			"system": []map[string]any{
				{
					"type":          "text",
					"text":          fillerSystem,
					"cache_control": map[string]any{"type": "ephemeral", "ttl": "5m"},
				},
			},
			"messages": []map[string]any{
				{"role": "user", "content": "Reply: PONG"},
			},
		})
		return body
	}

	type cacheUsage struct {
		InputTokens              int    `json:"input_tokens"`
		OutputTokens             int    `json:"output_tokens"`
		CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int    `json:"cache_read_input_tokens"`
		ServiceTier              string `json:"service_tier"`
		InferenceGeo             string `json:"inference_geo"`
		CacheCreation            struct {
			Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
			Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation"`
	}
	type cacheResp struct {
		Model string     `json:"model"`
		Usage cacheUsage `json:"usage"`
	}

	body1 := makeBody()
	resp1, status1, headers1, err1 := doPostJSON(ctx, endpoint, body1, anthropicAuthHeaders(key))
	if err1 != nil || status1 < 200 || status1 >= 300 {
		res.LatencyMs = int(time.Since(start) / time.Millisecond)
		res.RequestMethod = "POST x2"
		res.RequestURL = endpoint
		res.RequestHeaders = map[string]string{"x-api-key": "<redacted>", "anthropic-version": "2023-06-01"}
		res.RequestBody = "// call 1:\n" + truncate(string(body1), maxBodyEcho/2)
		res.StatusCode = status1
		res.ResponseHeaders = headerMap(headers1)
		res.ResponseBody = "// call 1:\n" + truncate(string(resp1), maxBodyEcho/2)
		res.Status = probeStatusFail
		if err1 != nil {
			res.Error = err1.Error()
		} else {
			res.Notes = fmt.Sprintf("call 1 HTTP %d — 上游未实现 cache_control 或拒绝 system 块", status1)
		}
		det.addEvidence(evidenceRow{Field: "cache.call1", Observed: fmt.Sprintf("HTTP %d", status1), Expected: "200", Conclusion: "bad"})
		return res
	}
	var first cacheResp
	_ = common.Unmarshal(resp1, &first)

	// 第二次：完全相同 prefix，期望命中缓存。
	body2 := makeBody()
	resp2, status2, headers2, err2 := doPostJSON(ctx, endpoint, body2, anthropicAuthHeaders(key))
	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	res.RequestMethod = "POST x2"
	res.RequestURL = endpoint
	res.RequestHeaders = map[string]string{"x-api-key": "<redacted>", "anthropic-version": "2023-06-01"}
	res.RequestBody = "// call 1:\n" + truncate(string(body1), maxBodyEcho/4) + "\n\n// call 2: (same body)"
	res.StatusCode = status2
	res.ResponseHeaders = mergeMap(headerMap(headers1), prefixMap("call2.", headerMap(headers2)))
	res.ResponseBody = "// call 1:\n" + truncate(string(resp1), maxBodyEcho/2) +
		"\n\n// call 2:\n" + truncate(string(resp2), maxBodyEcho/2)
	if err2 != nil || status2 < 200 || status2 >= 300 {
		res.Status = probeStatusFail
		if err2 != nil {
			res.Error = err2.Error()
		} else {
			res.Notes = fmt.Sprintf("call 2 HTTP %d", status2)
		}
		det.addEvidence(evidenceRow{Field: "cache.call2", Observed: fmt.Sprintf("HTTP %d", status2), Expected: "200", Conclusion: "bad"})
		return res
	}
	var second cacheResp
	_ = common.Unmarshal(resp2, &second)

	// Backend forensics — model echo + inference_geo + service_tier leak through
	// transparent relays in the cache response bodies.
	for _, r := range []cacheResp{first, second} {
		if r.Model != "" {
			det.modelEchoVariant = r.Model
			if !containsString(det.modelIds, r.Model) {
				det.modelIds = append(det.modelIds, r.Model)
			}
		}
		if r.Usage.InferenceGeo != "" {
			det.hasInferenceGeo = true
			det.inferenceGeoValue = r.Usage.InferenceGeo
		}
		if r.Usage.ServiceTier != "" && det.serviceTier == "" {
			det.serviceTier = r.Usage.ServiceTier
		}
	}

	det.cacheCreatePresent = first.Usage.CacheCreationInputTokens > 0
	det.cacheReadPresent = second.Usage.CacheReadInputTokens > 0
	det.cacheNestedBreakdown = first.Usage.CacheCreation.Ephemeral5m > 0 ||
		second.Usage.CacheCreation.Ephemeral5m > 0
	// 数字自洽：第二次的 cache_read 应该 ≈ 第一次的 cache_create。
	creationMatchesRead := false
	if det.cacheCreatePresent && det.cacheReadPresent {
		delta := first.Usage.CacheCreationInputTokens - second.Usage.CacheReadInputTokens
		if delta < 0 {
			delta = -delta
		}
		// 允许 ±5% 偏差（cache_control 边界 token 计数可能差几个）
		tol := first.Usage.CacheCreationInputTokens / 20
		if tol < 5 {
			tol = 5
		}
		creationMatchesRead = delta <= tol
	}

	// Scoring (max 25):
	//   call 1 cache_creation > 0                  10
	//   call 2 cache_read     > 0                  10
	//   cache_creation nested ephemeral_5m present  3
	//   numbers self-consistent (creation ≈ read)   2
	score := 0
	if det.cacheCreatePresent {
		score += 10
	}
	if det.cacheReadPresent {
		score += 10
	}
	if det.cacheNestedBreakdown {
		score += 3
	}
	if creationMatchesRead {
		score += 2
	}
	switch {
	case det.cacheCreatePresent && det.cacheReadPresent:
		res.Status = probeStatusPass
	case det.cacheCreatePresent || det.cacheReadPresent:
		res.Status = probeStatusWarn
	default:
		res.Status = probeStatusFail
		res.Notes = "两次调用都没有 cache_creation/cache_read 字段 — 上游可能丢弃了 cache_control"
	}
	res.ScoreDelta = score

	res.Evidence = []evidenceRow{
		{Field: "call1.usage.cache_creation_input_tokens", Observed: itoa(first.Usage.CacheCreationInputTokens), Expected: ">0", Conclusion: cond(det.cacheCreatePresent, "ok", "bad")},
		{Field: "call2.usage.cache_read_input_tokens", Observed: itoa(second.Usage.CacheReadInputTokens), Expected: ">0 (cache hit)", Conclusion: cond(det.cacheReadPresent, "ok", "bad")},
		{Field: "call1.usage.cache_creation.ephemeral_5m_input_tokens", Observed: itoa(first.Usage.CacheCreation.Ephemeral5m), Expected: ">0 (new schema)", Conclusion: cond(first.Usage.CacheCreation.Ephemeral5m > 0, "ok", "warn")},
		{Field: "creation ≈ read (self-consistency)", Observed: fmt.Sprintf("%d vs %d", first.Usage.CacheCreationInputTokens, second.Usage.CacheReadInputTokens), Expected: "±5%", Conclusion: cond(creationMatchesRead, "ok", "warn")},
		{Field: "call2.usage.input_tokens", Observed: itoa(second.Usage.InputTokens), Expected: "small (most went to cache_read)", Conclusion: cond(second.Usage.InputTokens < first.Usage.InputTokens, "ok", "warn")},
	}
	det.addEvidence(res.Evidence...)
	return res
}

// ----------------------------------------------------------------------------
// P9 — Prefill rejection（Sonnet 4.6 / Opus 4.6 / Opus 4.7 协议层独占）
// ----------------------------------------------------------------------------
//
// 文档原文："Prefilling assistant messages is not supported for this model."
// 真 Anthropic 对 sonnet-4-6 / opus-4-6 / opus-4-7 发带 trailing assistant 的
// 请求时必然返回 400 + anthropic-shaped error，message 文本就是上面那句。中
// 转/伪 Claude 一般 ① 200 + 编出来的回答（最大破绽）② 转 OpenAI 的 prefill
// 行为（assistant 内容被当成续写）③ 400 但错误文案不一致。

func cdProbePrefillRejection(ctx context.Context, base, key string, det *detectionState) probeResult {
	res := probeResult{Probe: "P9", Name: "Prefill 锁定模型的拒绝行为"}
	start := time.Now()
	endpoint := base + "/v1/messages"

	// 用 Sonnet 4.6（也是我们前面 P4 用过的）— 受 prefill 锁定。
	body, _ := common.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 32,
		"messages": []map[string]any{
			{"role": "user", "content": "Write a 3-word slogan for a coffee shop."},
			{"role": "assistant", "content": "Here it is:"},
		},
	})
	respBody, status, respHeaders, err := doPostJSON(ctx, endpoint, body, anthropicAuthHeaders(key))
	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	res.RequestMethod = "POST"
	res.RequestURL = endpoint
	res.RequestHeaders = map[string]string{"x-api-key": "<redacted>", "anthropic-version": "2023-06-01"}
	res.RequestBody = string(body)
	res.StatusCode = status
	res.ResponseHeaders = headerMap(respHeaders)
	res.ResponseBody = truncate(string(respBody), maxBodyEcho)
	if err != nil {
		res.Status = probeStatusFail
		res.Error = err.Error()
		det.addEvidence(evidenceRow{Field: "prefill.transport", Observed: err.Error(), Expected: "400", Conclusion: "bad"})
		return res
	}

	// 期望：400 + anthropic envelope + message 含 "Prefilling assistant messages"
	if status == 200 {
		res.Status = probeStatusFail
		res.Notes = "Sonnet 4.6 不应允许 prefill 续写 — 上游接受并续写 = 强伪造信号"
		det.prefillRejected = false
		det.addEvidence(evidenceRow{Field: "prefill.status", Observed: "200 (继续生成)", Expected: "400 invalid_request_error", Conclusion: "bad"})
		return res
	}
	if status < 400 || status >= 500 {
		res.Status = probeStatusWarn
		res.Notes = fmt.Sprintf("意外状态 HTTP %d", status)
		det.addEvidence(evidenceRow{Field: "prefill.status", Observed: itoa(status), Expected: "400", Conclusion: "warn"})
		return res
	}

	var parsed struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = common.Unmarshal(respBody, &parsed)
	isAnth := parsed.Type == "error" && parsed.Error.Type == "invalid_request_error"
	msgMatches := strings.Contains(strings.ToLower(parsed.Error.Message), "prefilling assistant")

	score := 0
	switch {
	case isAnth && msgMatches:
		score = 10
		res.Status = probeStatusPass
		det.prefillRejected = true
	case isAnth:
		score = 5
		res.Status = probeStatusWarn
		res.Notes = "400 + anthropic 形态，但拒绝文案不完全匹配 — 可能是中转的转译"
	default:
		score = 0
		res.Status = probeStatusFail
		res.Notes = "拒绝形态不是 Anthropic 标准"
	}
	res.ScoreDelta = score
	res.Evidence = []evidenceRow{
		{Field: "prefill.status", Observed: itoa(status), Expected: "400", Conclusion: cond(status == 400, "ok", "warn")},
		{Field: "prefill.error.type", Observed: parsed.Error.Type, Expected: "invalid_request_error", Conclusion: cond(parsed.Error.Type == "invalid_request_error", "ok", "warn")},
		{Field: "prefill.error.message", Observed: truncate(parsed.Error.Message, 200), Expected: "Prefilling assistant messages is not supported for this model.", Conclusion: cond(msgMatches, "ok", "warn")},
	}
	det.addEvidence(res.Evidence...)
	return res
}

// ----------------------------------------------------------------------------
// P10 — anthropic-version 头形态拒绝
// ----------------------------------------------------------------------------
//
// 文档明确：anthropic-version 是必传头，2023-06-01 是当前唯一稳定值。
// 真 Anthropic 收到 1999-01-01 这种伪造值时返 400 + invalid_request_error。
// 大多数中转完全忽略这个头，仍返 200 — 这是一个 0 token 的强信号。

func cdProbeVersionHeaderReject(ctx context.Context, base, key string, det *detectionState) probeResult {
	res := probeResult{Probe: "P10", Name: "anthropic-version 头拒绝伪造值"}
	start := time.Now()
	endpoint := base + "/v1/messages"
	body, _ := common.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 8,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	})
	respBody, status, respHeaders, err := doPostJSON(ctx, endpoint, body, map[string]string{
		"x-api-key":         key,
		"anthropic-version": "1999-01-01", // 伪造
		"Content-Type":      "application/json",
	})
	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	res.RequestMethod = "POST"
	res.RequestURL = endpoint
	res.RequestHeaders = map[string]string{"x-api-key": "<redacted>", "anthropic-version": "1999-01-01"}
	res.RequestBody = string(body)
	res.StatusCode = status
	res.ResponseHeaders = headerMap(respHeaders)
	res.ResponseBody = truncate(string(respBody), maxBodyEcho)
	if err != nil {
		res.Status = probeStatusFail
		res.Error = err.Error()
		return res
	}
	if status == 200 {
		res.Status = probeStatusFail
		res.Notes = "1999-01-01 这种伪造 anthropic-version 仍返回 200 — 上游不验证此头，强中转/伪造嫌疑"
		det.versionHeaderRejectsBogus = false
		det.addEvidence(evidenceRow{Field: "anthropic-version.bogus.status", Observed: "200", Expected: "400 invalid_request_error", Conclusion: "bad"})
		return res
	}
	// 期望 400，错误形态匹配 Anthropic envelope
	var parsed struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = common.Unmarshal(respBody, &parsed)
	isAnth := parsed.Type == "error" && parsed.Error.Type == "invalid_request_error"
	low := strings.ToLower(parsed.Error.Message)
	msgMentionsVersion := strings.Contains(low, "version") || strings.Contains(low, "anthropic-version")

	// Scoring (max 8):
	//   非 200 + Anthropic envelope + 文案提到 version
	score := 0
	switch {
	case isAnth && msgMentionsVersion:
		score = 8
		res.Status = probeStatusPass
		det.versionHeaderRejectsBogus = true
	case isAnth:
		score = 5
		res.Status = probeStatusWarn
		res.Notes = "拒绝了，但错误文案没明确提到 version — 可能是中转的转译"
		det.versionHeaderRejectsBogus = true
	default:
		score = 2
		res.Status = probeStatusWarn
		res.Notes = "拒绝了，但错误形态不是 Anthropic 标准"
	}
	res.ScoreDelta = score
	res.Evidence = []evidenceRow{
		{Field: "anthropic-version.bogus.status", Observed: itoa(status), Expected: "400", Conclusion: cond(status == 400, "ok", "warn")},
		{Field: "error.type", Observed: parsed.Error.Type, Expected: "invalid_request_error", Conclusion: cond(parsed.Error.Type == "invalid_request_error", "ok", "warn")},
		{Field: "error.message mentions version", Observed: cond(msgMentionsVersion, "yes", "no"), Expected: "yes", Conclusion: cond(msgMentionsVersion, "ok", "warn")},
	}
	det.addEvidence(res.Evidence...)
	return res
}

// ----------------------------------------------------------------------------
// P11 — thinking + tool_choice:"any" 互斥拒绝
// ----------------------------------------------------------------------------
//
// 文档原文："Only tool_choice:{type:"auto"} (default) or "none" work [with thinking].
// Using "any" or "tool" returns an error."
// 这条规则极其冷门，中转想要刚好把它实现对的概率极低。

func cdProbeThinkingToolChoiceReject(ctx context.Context, base, key string, det *detectionState) probeResult {
	res := probeResult{Probe: "P11", Name: "thinking + tool_choice:any 互斥"}
	start := time.Now()
	endpoint := base + "/v1/messages"
	body, _ := common.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 16,
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": 1024,
		},
		"tool_choice": map[string]any{"type": "any"},
		"tools": []map[string]any{
			{
				"name":        "echo",
				"description": "echo back text",
				"input_schema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				},
			},
		},
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	respBody, status, respHeaders, err := doPostJSON(ctx, endpoint, body, anthropicAuthHeaders(key))
	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	res.RequestMethod = "POST"
	res.RequestURL = endpoint
	res.RequestHeaders = map[string]string{"x-api-key": "<redacted>", "anthropic-version": "2023-06-01"}
	res.RequestBody = string(body)
	res.StatusCode = status
	res.ResponseHeaders = headerMap(respHeaders)
	res.ResponseBody = truncate(string(respBody), maxBodyEcho)
	if err != nil {
		res.Status = probeStatusFail
		res.Error = err.Error()
		return res
	}
	if status == 200 {
		res.Status = probeStatusFail
		res.Notes = "thinking + tool_choice:any 不应被接受 — 真 Anthropic 必返 400。上游接受 = 不验证此约束 = 中转/伪造"
		det.thinkingToolChoiceRejected = false
		det.addEvidence(evidenceRow{Field: "thinking_tool_choice.status", Observed: "200", Expected: "400", Conclusion: "bad"})
		return res
	}
	var parsed struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = common.Unmarshal(respBody, &parsed)
	isAnth := parsed.Type == "error" && parsed.Error.Type == "invalid_request_error"
	low := strings.ToLower(parsed.Error.Message)
	msgMatches := (strings.Contains(low, "tool_choice") || strings.Contains(low, "tool choice")) &&
		(strings.Contains(low, "thinking") || strings.Contains(low, "any"))

	score := 0
	switch {
	case isAnth && msgMatches:
		score = 8
		res.Status = probeStatusPass
		det.thinkingToolChoiceRejected = true
	case isAnth:
		score = 5
		res.Status = probeStatusWarn
		res.Notes = "拒绝了，但错误文案没明确提到 thinking/tool_choice 关系"
		det.thinkingToolChoiceRejected = true
	default:
		score = 2
		res.Status = probeStatusWarn
		res.Notes = "拒绝了，但错误形态不是 Anthropic 标准"
	}
	res.ScoreDelta = score
	res.Evidence = []evidenceRow{
		{Field: "thinking+tool_choice:any.status", Observed: itoa(status), Expected: "400", Conclusion: cond(status == 400, "ok", "warn")},
		{Field: "error.type", Observed: parsed.Error.Type, Expected: "invalid_request_error", Conclusion: cond(parsed.Error.Type == "invalid_request_error", "ok", "warn")},
		{Field: "error.message mentions thinking/tool_choice", Observed: cond(msgMatches, "yes", "no"), Expected: "yes", Conclusion: cond(msgMatches, "ok", "warn")},
	}
	det.addEvidence(res.Evidence...)
	return res
}

// ----------------------------------------------------------------------------
// P12 — thinking.budget_tokens 最小值拒绝
// ----------------------------------------------------------------------------
//
// 文档隐含：budget_tokens 最小 1024。发 100 应返 400 + 关于 minimum 的具体错误。

func cdProbeBudgetTokensMinReject(ctx context.Context, base, key string, det *detectionState) probeResult {
	res := probeResult{Probe: "P12", Name: "thinking.budget_tokens 最小值校验"}
	start := time.Now()
	endpoint := base + "/v1/messages"
	body, _ := common.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 2048,
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": 100, // 低于 1024 最小值
		},
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	respBody, status, respHeaders, err := doPostJSON(ctx, endpoint, body, anthropicAuthHeaders(key))
	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	res.RequestMethod = "POST"
	res.RequestURL = endpoint
	res.RequestHeaders = map[string]string{"x-api-key": "<redacted>", "anthropic-version": "2023-06-01"}
	res.RequestBody = string(body)
	res.StatusCode = status
	res.ResponseHeaders = headerMap(respHeaders)
	res.ResponseBody = truncate(string(respBody), maxBodyEcho)
	if err != nil {
		res.Status = probeStatusFail
		res.Error = err.Error()
		return res
	}
	if status == 200 {
		res.Status = probeStatusFail
		res.Notes = "budget_tokens=100 不应被接受 — 真 Anthropic 必返 400 (minimum 1024)。上游接受 = 不验证 = 伪造"
		det.budgetTokensMinRejected = false
		det.addEvidence(evidenceRow{Field: "budget_tokens.min.status", Observed: "200", Expected: "400", Conclusion: "bad"})
		return res
	}
	var parsed struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = common.Unmarshal(respBody, &parsed)
	isAnth := parsed.Type == "error" && parsed.Error.Type == "invalid_request_error"
	low := strings.ToLower(parsed.Error.Message)
	msgMatches := strings.Contains(low, "budget_tokens") &&
		(strings.Contains(low, "1024") || strings.Contains(low, "minimum") || strings.Contains(low, "at least"))

	score := 0
	switch {
	case isAnth && msgMatches:
		score = 6
		res.Status = probeStatusPass
		det.budgetTokensMinRejected = true
	case isAnth:
		score = 4
		res.Status = probeStatusWarn
		res.Notes = "拒绝了，但错误文案没明确提到 1024/minimum"
		det.budgetTokensMinRejected = true
	default:
		score = 1
		res.Status = probeStatusWarn
		res.Notes = "拒绝了，但错误形态不是 Anthropic 标准"
	}
	res.ScoreDelta = score
	res.Evidence = []evidenceRow{
		{Field: "budget_tokens=100.status", Observed: itoa(status), Expected: "400", Conclusion: cond(status == 400, "ok", "warn")},
		{Field: "error.type", Observed: parsed.Error.Type, Expected: "invalid_request_error", Conclusion: cond(parsed.Error.Type == "invalid_request_error", "ok", "warn")},
		{Field: "error.message mentions 1024/minimum", Observed: cond(msgMatches, "yes", "no"), Expected: "yes", Conclusion: cond(msgMatches, "ok", "warn")},
	}
	det.addEvidence(res.Evidence...)
	return res
}

// ----------------------------------------------------------------------------
// P13 — server tool (web_search_20260209) 接受度
// ----------------------------------------------------------------------------
//
// Anthropic server-side web_search 工具仅在 Direct + Claude Platform on AWS 提供。
// Bedrock / Vertex / Foundry 通常 400 拒绝该工具 schema，或显式列为不支持。
// 把 tool_choice 设成 "none" 让真渠道也只返几个 token，成本接近 0。
// 这个 probe 不参与"真伪打分"，仅用于后端渠道判定 — 任何 4xx 都不扣分。

func cdProbeWebSearchAcceptance(ctx context.Context, base, key string, det *detectionState) probeResult {
	res := probeResult{Probe: "P13", Name: "server tool web_search 接受度（后端探测）"}
	start := time.Now()
	endpoint := base + "/v1/messages"

	body, _ := common.Marshal(map[string]any{
		"model":       "claude-sonnet-4-6",
		"max_tokens":  16,
		"tool_choice": map[string]any{"type": "none"},
		"tools": []map[string]any{
			{
				"type":     "web_search_20260209",
				"name":     "web_search",
				"max_uses": 1,
			},
		},
		"messages": []map[string]any{{"role": "user", "content": "Reply: hi"}},
	})
	respBody, status, respHeaders, err := doPostJSON(ctx, endpoint, body, anthropicAuthHeaders(key))
	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	res.RequestMethod = "POST"
	res.RequestURL = endpoint
	res.RequestHeaders = map[string]string{"x-api-key": "<redacted>", "anthropic-version": "2023-06-01"}
	res.RequestBody = string(body)
	res.StatusCode = status
	res.ResponseHeaders = headerMap(respHeaders)
	res.ResponseBody = truncate(string(respBody), maxBodyEcho)
	if err != nil {
		res.Status = probeStatusFail
		res.Error = err.Error()
		return res
	}

	if status >= 200 && status < 300 {
		// Direct / Platform on AWS 接受 server tool schema。
		det.webSearchSupported = true
		// 同步收集 model 字段
		var p struct {
			Model string `json:"model"`
			Usage struct {
				InferenceGeo string `json:"inference_geo"`
			} `json:"usage"`
		}
		_ = common.Unmarshal(respBody, &p)
		if p.Model != "" {
			det.modelEchoVariant = p.Model
			if !containsString(det.modelIds, p.Model) {
				det.modelIds = append(det.modelIds, p.Model)
			}
		}
		if p.Usage.InferenceGeo != "" {
			det.hasInferenceGeo = true
			det.inferenceGeoValue = p.Usage.InferenceGeo
		}
		res.Status = probeStatusPass
		res.ScoreDelta = 4
		res.Notes = "上游接受 web_search server tool → 后端是 Direct 或 Claude Platform on AWS"
	} else {
		// 400 + 错误提到 tool 类型 → Bedrock / Vertex / 严格中转
		var parsed struct {
			Type  string `json:"type"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = common.Unmarshal(respBody, &parsed)
		low := strings.ToLower(parsed.Error.Message)
		if strings.Contains(low, "tool") || strings.Contains(low, "web_search") {
			det.webSearchRejected = true
			res.Status = probeStatusWarn
			res.Notes = "上游显式拒绝 web_search server tool → 后端疑似 Bedrock / Vertex / Foundry"
		} else {
			res.Status = probeStatusWarn
			res.Notes = fmt.Sprintf("HTTP %d，错误未明确指向 tool — 信号不强", status)
		}
	}
	res.Evidence = []evidenceRow{
		{Field: "server_tool.status", Observed: itoa(status), Expected: "200 (Direct/Platform) or 400 (Bedrock/Vertex)", Conclusion: "ok"},
		{Field: "web_search_supported", Observed: cond(det.webSearchSupported, "yes", "no"), Expected: "yes iff Direct/Platform", Conclusion: cond(det.webSearchSupported, "ok", "warn")},
		{Field: "web_search_rejected_explicit", Observed: cond(det.webSearchRejected, "yes", "no"), Expected: "yes iff Bedrock/Vertex/Foundry", Conclusion: "ok"},
	}
	det.addEvidence(res.Evidence...)
	return res
}

// ----------------------------------------------------------------------------
// P14 — bogus anthropic-beta header 静默处理
// ----------------------------------------------------------------------------
//
// Anthropic 直连对未知 anthropic-beta 值采取 silent-ignore 策略（200 + 普通响应）。
// 严格代理层 / 中转 / Bedrock-Vertex 通常会校验 beta 名单，伪造值会触发 400。
// 这个 probe 不参与"真伪打分"，仅用于"前端层"判定（不是后端）。

func cdProbeBogusBetaHandling(ctx context.Context, base, key string, det *detectionState) probeResult {
	res := probeResult{Probe: "P14", Name: "bogus anthropic-beta 处理（前端层探测）"}
	start := time.Now()
	endpoint := base + "/v1/messages"

	body, _ := common.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 8,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
	})
	respBody, status, respHeaders, err := doPostJSON(ctx, endpoint, body, map[string]string{
		"x-api-key":         key,
		"anthropic-version": "2023-06-01",
		"anthropic-beta":    "bogus-fake-2099-99-99",
		"Content-Type":      "application/json",
	})
	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	res.RequestMethod = "POST"
	res.RequestURL = endpoint
	res.RequestHeaders = map[string]string{
		"x-api-key":         "<redacted>",
		"anthropic-version": "2023-06-01",
		"anthropic-beta":    "bogus-fake-2099-99-99",
	}
	res.RequestBody = string(body)
	res.StatusCode = status
	res.ResponseHeaders = headerMap(respHeaders)
	res.ResponseBody = truncate(string(respBody), maxBodyEcho)
	if err != nil {
		res.Status = probeStatusFail
		res.Error = err.Error()
		return res
	}

	if status >= 200 && status < 300 {
		det.bogusBetaIgnored = true
		res.Status = probeStatusPass
		res.ScoreDelta = 3
		res.Notes = "上游静默忽略未知 anthropic-beta → 符合 Anthropic 直连策略"
	} else {
		det.bogusBetaRejected = true
		res.Status = probeStatusWarn
		res.Notes = fmt.Sprintf("上游对未知 anthropic-beta 返回 HTTP %d → 前端层严格校验（中转/Bedrock/Vertex 行为）", status)
	}

	res.Evidence = []evidenceRow{
		{Field: "bogus_beta.status", Observed: itoa(status), Expected: "200 (Direct silent-ignore)", Conclusion: cond(det.bogusBetaIgnored, "ok", "warn")},
		{Field: "frontend_layer_hint", Observed: cond(det.bogusBetaIgnored, "Direct-like", "strict relay / Bedrock / Vertex"), Expected: "Direct-like", Conclusion: cond(det.bogusBetaIgnored, "ok", "warn")},
	}
	det.addEvidence(res.Evidence...)
	return res
}

// ----------------------------------------------------------------------------
// P15 — 吞吐降级探针（抓「按 opus 收钱、实际走 haiku」）
// ----------------------------------------------------------------------------
//
// 对**宣称模型**发一个确定性长输出的流式请求，测首字延迟 TTFB 与
// output tokens/sec。各档位模型生成速度差异大（haiku 远快于 opus）：宣称 opus
// 但实测吞吐落在更快的廉价档 → 降级嫌疑。吞吐受网络/负载影响有噪声，故仅作
// **旗标**（baseline 未标定时绝不下铁结论）。
func cdProbeThroughputDowngrade(ctx context.Context, base, key, model string, det *detectionState) probeResult {
	res := probeResult{Probe: "P15", Name: "吞吐降级探针"}
	start := time.Now()
	endpoint := base + "/v1/messages"

	// 确定性长输出：要求复述一段固定文本足够多次，逼出稳定的生成时长。
	body, _ := common.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 512,
		"stream":     true,
		"messages": []map[string]any{
			{"role": "user", "content": "Count from 1 to 200, one number per line, no commentary."},
		},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	for k, v := range anthropicAuthHeaders(key) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")

	res.RequestMethod = "POST"
	res.RequestURL = endpoint
	res.RequestHeaders = map[string]string{"x-api-key": "<redacted>", "anthropic-version": "2023-06-01", "Accept": "text/event-stream"}
	res.RequestBody = string(body)

	resp, err := newDetectClient().Do(req)
	if err != nil {
		res.Status = probeStatusFail
		res.Error = err.Error()
		res.LatencyMs = int(time.Since(start) / time.Millisecond)
		det.addEvidence(evidenceRow{Field: "throughput.transport", Observed: err.Error(), Expected: "200", Conclusion: "warn"})
		return res
	}
	defer resp.Body.Close()
	res.StatusCode = resp.StatusCode
	res.ResponseHeaders = headerMap(resp.Header)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyEcho))
		res.ResponseBody = truncate(string(b), maxBodyEcho)
		res.Status = probeStatusWarn
		res.Notes = fmt.Sprintf("HTTP %d — 无法测吞吐", resp.StatusCode)
		det.addEvidence(evidenceRow{Field: "throughput.http_status", Observed: itoa(resp.StatusCode), Expected: "2xx", Conclusion: "warn"})
		return res
	}

	// 流式读取：记录首个 content delta 的时刻（TTFB）与最终累计 output_tokens。
	var ttfb time.Duration
	gotFirst := false
	outputTokens := 0
	genStart := time.Now() // 首字时刻，稍后覆盖
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 1024*64), 1024*1024)
	var eventType string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(line[len("event:"):])
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataLine := strings.TrimSpace(line[len("data:"):])
		if dataLine == "" || dataLine == "[DONE]" {
			continue
		}
		switch eventType {
		case "content_block_delta":
			if !gotFirst {
				ttfb = time.Since(start)
				genStart = time.Now()
				gotFirst = true
			}
		case "message_delta":
			var payload struct {
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := common.UnmarshalJsonStr(dataLine, &payload); err == nil && payload.Usage.OutputTokens > 0 {
				outputTokens = payload.Usage.OutputTokens
			}
		}
	}
	genElapsed := time.Since(genStart)
	res.LatencyMs = int(time.Since(start) / time.Millisecond)
	det.throughputModel = model
	det.throughputTTFBms = int(ttfb / time.Millisecond)

	tokPerSec := 0.0
	if outputTokens > 0 && genElapsed > 0 {
		tokPerSec = float64(outputTokens) / genElapsed.Seconds()
	}
	det.throughputTokPerSec = tokPerSec

	v := fingerprint.ClassifyThroughputTier(model, tokPerSec)
	det.throughputDowngrade = v.DowngradeSuspect
	det.throughputDecisive = v.Decisive

	conclusion := "ok"
	switch {
	case tokPerSec <= 0:
		res.Status = probeStatusWarn
		res.Notes = "未测到有效吞吐（无 usage / 流中断）"
		conclusion = "warn"
	case v.ClaimedTier == "":
		res.Status = probeStatusWarn
		res.Notes = fmt.Sprintf("宣称模型 %s 无法归档，跳过降级判定", model)
		conclusion = "warn"
	case v.DowngradeSuspect && v.Decisive:
		res.Status = probeStatusFail
		res.Notes = fmt.Sprintf("宣称 %s（%s 档）实测 %.0f tok/s 落入廉价档 %v — 高度疑似降级", model, v.ClaimedTier, tokPerSec, v.FitTiers)
		conclusion = "bad"
	case v.DowngradeSuspect:
		res.Status = probeStatusWarn
		res.Notes = fmt.Sprintf("宣称 %s 实测 %.0f tok/s 偏快（基线未标定，仅作旗标）", model, tokPerSec)
		conclusion = "warn"
	default:
		res.Status = probeStatusPass
	}

	res.Evidence = []evidenceRow{
		{Field: "throughput.ttfb_ms", Observed: itoa(det.throughputTTFBms), Expected: "因模型而异", Conclusion: "ok"},
		{Field: "throughput.output_tokens", Observed: itoa(outputTokens), Expected: ">0", Conclusion: cond(outputTokens > 0, "ok", "warn")},
		{Field: "throughput.tok_per_sec", Observed: fmt.Sprintf("%.1f", tokPerSec), Expected: fmt.Sprintf("%s 档区间", v.ClaimedTier), Conclusion: conclusion},
		{Field: "throughput.fit_tiers", Observed: strings.Join(v.FitTiers, ","), Expected: v.ClaimedTier, Conclusion: conclusion},
		{Field: "throughput.calibrated", Observed: cond(fingerprint.BaselineCalibrated(), "yes", "no（占位区间，仅旗标）"), Expected: "yes", Conclusion: cond(fingerprint.BaselineCalibrated(), "ok", "warn")},
	}
	det.addEvidence(res.Evidence...)
	return res
}

// looksAnthropicError returns true if the body parses as Anthropic's error
// envelope (type=error + nested error.type/message).
func looksAnthropicError(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var parsed struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := common.Unmarshal(body, &parsed); err != nil {
		return false
	}
	return parsed.Type == "error" && parsed.Error.Type != "" && parsed.Error.Message != ""
}

// ----------------------------------------------------------------------------
// Low-level HTTP helpers (returns body, status, headers, err).
// ----------------------------------------------------------------------------

func doSimpleGet(ctx context.Context, url string, headers map[string]string) ([]byte, int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := newDetectClient().Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	return body, resp.StatusCode, resp.Header, nil
}

func doPostJSON(ctx context.Context, url string, body []byte, headers map[string]string) ([]byte, int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := newDetectClient().Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	return respBody, resp.StatusCode, resp.Header, nil
}

// ----------------------------------------------------------------------------
// small utilities
// ----------------------------------------------------------------------------

func itoa(n int) string               { return fmt.Sprintf("%d", n) }
func cond(c bool, a, b string) string { if c { return a }; return b }
func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func mergeMap(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
func prefixMap(p string, m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[p+k] = v
	}
	return out
}
