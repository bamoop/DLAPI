package controller

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// Claude 检测引擎 v2 — 行为/能力级检测（对标 ztest.ai）
// ----------------------------------------------------------------------------
// 取代旧 P1-P15 协议取证套件。设计理念：协议层信号（签名/头）会被透传中转
// 原样带过，对「opus 偷换 haiku」「注入隐藏提示词」「降级廉价模型」无能为力。
// v2 改测行为/能力：让模型自报身份(D11)、做能力题(D10/D16)、量 token 注入(S1)，
// 直击 TG 套壳站的掺水/降级。打分见 claude_detect_score.go。
// ============================================================================

// ---- Claude Code 伪装 ----
// ztest 用 claude_code 兼容模式发探针（system prompt = 官方 CLI）。很多中转对
// Claude Code 请求与普通请求走不同路由/注入，且 D11/S2/S3 依赖这个身份语境。
const claudeCodeSystemPrompt = "You are Claude Code, Anthropic's official CLI for Claude."

// claudeCodeBetaHeader 是 Claude Code 客户端常带的 beta 头（宽松版，避免被严格中转拒绝）。
const claudeCodeUserAgent = "claude-cli/1.0 (external)"

// ccAuthHeaders 在标准 anthropic 鉴权头基础上，附加 Claude Code 客户端特征头。
func ccAuthHeaders(key string) map[string]string {
	h := anthropicAuthHeaders(key)
	h["User-Agent"] = claudeCodeUserAgent
	return h
}

// textOnlyDirective：Claude Code 伪装会让模型倾向调用 Write/工具（返回
// stop_reason=tool_use 且无 text）。对需要文本答案的探针（D5/D10/D11/D16/D3/S2/S3）
// 加这个前缀，强制把答案直接写在对话里、不调用工具。D7 需要 tool_use，不加。
const textOnlyDirective = "Output your answer directly as plain text in this chat. Do NOT call any tools and do NOT write files. "

// ccMessageBody 构造一个带 Claude Code system prompt 的 /v1/messages 请求体。
// system 用顶层 system 字段（Anthropic 原生）。extra 允许覆盖/追加字段。
func ccMessageBody(model string, maxTokens int, userContent any, extra map[string]any) []byte {
	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     claudeCodeSystemPrompt,
		"messages": []map[string]any{
			{"role": "user", "content": userContent},
		},
	}
	for k, v := range extra {
		body[k] = v
	}
	buf, _ := common.Marshal(body)
	return buf
}

// anthropicMsgResult 是解析后的 /v1/messages 响应（探针通用）。
type anthropicMsgResult struct {
	Id         string
	Model      string
	StopReason string
	Text       string // 拼接所有 text content block
	Usage      anthropicUsage
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// parseAnthropicMessage 解析非流式 /v1/messages 响应体，抽出文本与 usage。
func parseAnthropicMessage(body []byte) anthropicMsgResult {
	var raw struct {
		Id         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage anthropicUsage `json:"usage"`
	}
	_ = common.Unmarshal(body, &raw)
	var sb strings.Builder
	for _, c := range raw.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return anthropicMsgResult{
		Id: raw.Id, Model: raw.Model, StopReason: raw.StopReason,
		Text: sb.String(), Usage: raw.Usage,
	}
}

// ccAsk 发一个带 Claude Code 伪装的简单单轮请求，返回解析结果 + 原始状态。
// userContent 一般是字符串；maxTokens 控制输出上限。
func ccAsk(p *detectV2Context, maxTokens int, userContent any, extra map[string]any) (anthropicMsgResult, int, []byte, error) {
	body := ccMessageBody(p.model, maxTokens, userContent, extra)
	respBody, status, _, err := doPostJSON(p.ctx, p.base+"/v1/messages", body, ccAuthHeaders(p.key))
	if err != nil {
		return anthropicMsgResult{}, status, respBody, err
	}
	return parseAnthropicMessage(respBody), status, respBody, nil
}

// detectV2Context 贯穿一次检测的共享上下文。
type detectV2Context struct {
	ctx         context.Context
	base        string // 已 TrimRight
	key         string
	model       string // 宣称/被测模型
	channelType string // "anthropic"（目前只测 anthropic 协议）
}

// probeV2Func 是 v2 探针签名。
type probeV2Func func(p *detectV2Context) probeOutcome

// runProbeV2Safely 包裹探针，panic 转成 partial 结果而非中断 SSE 流。
func runProbeV2Safely(code, name, dimension string, fn probeV2Func, p *detectV2Context) (out probeOutcome) {
	defer func() {
		if r := recover(); r != nil {
			out = probeOutcome{
				Code: code, Name: name, Dimension: dimension,
				Status: probeStatusV2Partial, Score: 0,
				Diagnosis: &diagnosis{
					Category:    "engine",
					Title:       "探针执行异常",
					Suggestions: []string{fmt.Sprintf("panic: %v", r)},
				},
				Detail: map[string]any{"stack": string(debug.Stack())},
			}
		}
	}()
	return fn(p)
}

// v2ProbeReg 是探针注册项。
type v2ProbeReg struct {
	Code      string
	Name      string
	Dimension string
	Fn        probeV2Func
}

// claudeDetectV2Probes 返回全部 v2 探针，按维度组织、固定顺序执行。
// 各探针实现分散在 claude_detect_v2_*.go。
func claudeDetectV2Probes() []v2ProbeReg {
	return []v2ProbeReg{
		// 协议合规
		{"HB", "接口心跳", "协议合规", probeHeartbeat},
		{"D1", "协议连通性", "协议合规", probeConnectivity},
		// 身份一致（核心反降级）
		{"D3", "身份一致性", "身份一致", probeIdentityConsistency},
		{"D11", "隐式身份", "身份一致", probeCodeSignature},
		// 安全性（直击掺水）
		{"S1", "Token 注入", "安全性", probeTokenInjection},
		{"S2", "提示词提取", "安全性", probePromptExtraction},
		{"S3", "指令覆盖", "安全性", probeInstructionOverride},
		{"S4", "错误信息泄露", "安全性", probeErrorLeak},
		// 能力验证（廉价模型会翻车）
		{"D10", "思维链", "能力验证", probeChainOfThought},
		{"D16", "能力指纹", "能力验证", probeCapabilityFingerprint},
		{"D7", "结构化输出", "能力验证", probeToolUse},
		{"D19", "文档识别", "能力验证", probeDocExtraction},
		// 协议合规（补齐）
		{"D2", "响应结构", "协议合规", probeResponseStructure},
		{"D17", "响应签名", "协议合规", probeResponseSignature},
		{"D18", "缓存字段完备性", "协议合规", probeCacheFields},
		// 性能
		{"D8", "响应时延", "性能", probeLatency},
		{"D9", "性能稳定性", "性能", probeStability},
		{"S5", "流完整性", "性能", probeStreamIntegrity},
		// 内容完整性
		{"D5", "内容 Canary", "内容完整性", probeContentCanary},
		// 多模态
		{"D13", "多模态", "能力验证", probeMultimodal},
	}
}

// ClaudeDetectUpstreamKeyV2 是 v2 引擎入口（替换旧 ClaudeDetectUpstreamKey 的检测主体）。
// Routing: POST /api/upstream/key-test/claude-detect
func ClaudeDetectUpstreamKeyV2(c *gin.Context) {
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

	pctx := &detectV2Context{
		ctx:         c.Request.Context(),
		base:        strings.TrimRight(baseURL, "/"),
		key:         key,
		model:       req.ModelName,
		channelType: "anthropic",
	}

	probes := claudeDetectV2Probes()
	emit("start", gin.H{
		"key_hint":     keyHint,
		"base_url":     pctx.base,
		"target_model": req.ModelName,
		"engine":       "v2",
		"probe_total":  len(probes),
	})

	outcomes := make([]probeOutcome, 0, len(probes))
	for i, reg := range probes {
		start := time.Now()
		o := runProbeV2Safely(reg.Code, reg.Name, reg.Dimension, reg.Fn, pctx)
		// 兜底字段（探针可能没填全）。
		o.Code, o.Name, o.Dimension = reg.Code, reg.Name, reg.Dimension
		if o.LatencyMs == 0 {
			o.LatencyMs = int(time.Since(start) / time.Millisecond)
		}
		outcomes = append(outcomes, o)
		emit("probe", gin.H{
			"index":       i + 1,
			"total":       len(probes),
			"probe_code":  o.Code,
			"probe_name":  o.Name,
			"dimension":   o.Dimension,
			"status":      o.Status,
			"score":       o.Score,
			"label":       o.Label,
			"latency_ms":  o.LatencyMs,
			"details":     o.Detail,
			"diagnosis":   o.Diagnosis,
		})
	}

	summary := aggregateDetect(outcomes)
	emit("summary", summary)
}
