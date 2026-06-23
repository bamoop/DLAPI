package controller

import (
	"fmt"
	"regexp"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// 协议合规维度探针：HB / D1 / D2 / D17 / D18。

// reMsgIdV2：Anthropic 官方 message id 形态 ^msg_[A-Za-z0-9]{18,40}$（不含 '-'）。
var reMsgIdV2 = regexp.MustCompile(`^msg_[A-Za-z0-9]{18,40}$`)

// probeHeartbeat (HB) — 接口心跳：最小请求，确认 /v1/messages 可达且返回 2xx。
func probeHeartbeat(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "HB", Name: "接口心跳", Dimension: "协议合规"}
	start := time.Now()
	body := ccMessageBody(p.model, 16, "ping", nil)
	respBody, status, _, err := doPostJSON(p.ctx, p.base+"/v1/messages", body, ccAuthHeaders(p.key))
	out.LatencyMs = int(time.Since(start) / time.Millisecond)
	out.Detail = map[string]any{"http_status": status, "elapsed_ms": out.LatencyMs}
	if err != nil {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail["network_error"] = err.Error()
		out.Diagnosis = &diagnosis{Category: "network", Title: "连接失败",
			Suggestions: []string{err.Error()}}
		return out
	}
	if status >= 200 && status < 300 {
		out.Status = probeStatusV2Success
		out.Score = 100
		out.Detail["kind"] = "ok"
		return out
	}
	out.Status = probeStatusV2Partial
	out.Score = 0
	out.Detail["response_preview"] = truncate(string(respBody), 400)
	out.Diagnosis = &diagnosis{Category: "protocol", Title: fmt.Sprintf("心跳返回 HTTP %d", status),
		Suggestions: []string{"上游未在最小请求上返回 2xx"}}
	return out
}

// probeConnectivity (D1) — 协议连通性：claude_code 兼容模式发请求，
// 校验 200 + 响应体是合法 Anthropic message（有 id/model/content）。
func probeConnectivity(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D1", Name: "协议连通性", Dimension: "协议合规"}
	start := time.Now()
	body := ccMessageBody(p.model, 32, "Reply with the single word: pong", nil)
	respBody, status, _, err := doPostJSON(p.ctx, p.base+"/v1/messages", body, ccAuthHeaders(p.key))
	out.LatencyMs = int(time.Since(start) / time.Millisecond)
	out.Detail = map[string]any{
		"http_status":           status,
		"response_size_bytes":   len(respBody),
		"elapsed_ms":            out.LatencyMs,
		"response_preview":      truncate(string(respBody), 400),
	}
	if err != nil {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail["network_error"] = err.Error()
		out.Diagnosis = &diagnosis{Category: "network", Title: "连接失败", Suggestions: []string{err.Error()}}
		return out
	}
	if status < 200 || status >= 300 {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Diagnosis = &diagnosis{Category: "protocol", Title: fmt.Sprintf("HTTP %d", status),
			Suggestions: []string{"claude_code 兼容模式请求未返回 2xx"}}
		return out
	}
	var parsed struct {
		Id      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = common.Unmarshal(respBody, &parsed)
	validBody := parsed.Id != "" && parsed.Model != "" && len(parsed.Content) > 0
	out.Detail["model_echo"] = parsed.Model
	if validBody {
		out.Status = probeStatusV2Success
		out.Score = 100
		out.Label = "Claude Code 兼容模式可用"
		return out
	}
	out.Status = probeStatusV2Partial
	out.Score = 50
	out.Diagnosis = &diagnosis{Category: "protocol", Title: "响应体不完整",
		Suggestions: []string{"返回 200 但缺少 id/model/content 等 Anthropic 标准字段"}}
	return out
}

// probeResponseStructure (D2) — 响应结构：必需字段覆盖率。
func probeResponseStructure(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D2", Name: "响应结构", Dimension: "协议合规"}
	body := ccMessageBody(p.model, 32, "Reply with the single word: pong", nil)
	respBody, status, _, err := doPostJSON(p.ctx, p.base+"/v1/messages", body, ccAuthHeaders(p.key))
	if err != nil || status < 200 || status >= 300 {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail = map[string]any{"http_status": status, "error": errStr(err)}
		out.Diagnosis = &diagnosis{Category: "protocol", Title: "响应结构无法校验", Suggestions: []string{errStr(err)}}
		return out
	}
	// 用通用 map 检查必需字段是否存在且类型正确。
	var m map[string]any
	_ = common.Unmarshal(respBody, &m)
	required := []string{"id", "type", "role", "content", "model", "stop_reason", "usage"}
	matched := []string{}
	missing := []string{}
	typeErrors := []string{}
	for _, f := range required {
		v, ok := m[f]
		if !ok || v == nil {
			missing = append(missing, f)
			continue
		}
		matched = append(matched, f)
	}
	// content[0].type / content[0].text 深检。
	if content, ok := m["content"].([]any); ok && len(content) > 0 {
		if cb, ok := content[0].(map[string]any); ok {
			if _, ok := cb["type"]; ok {
				matched = append(matched, "content[0].type")
			} else {
				missing = append(missing, "content[0].type")
			}
		}
	}
	// usage 子字段。
	if usage, ok := m["usage"].(map[string]any); ok {
		for _, uf := range []string{"input_tokens", "output_tokens"} {
			if _, ok := usage[uf]; ok {
				matched = append(matched, "usage."+uf)
			} else {
				missing = append(missing, "usage."+uf)
			}
		}
	}
	// 关键：校验 id 字段格式。逆向渠道（如 Krio）自己拼装响应，伪造不出真
	// Anthropic message id，常返回 UUID（含 '-'）或其它非标准形态。这是协议层
	// 伪造的铁证，单独识别为强信号 PROTOCOL_FORGERY。
	idVal, _ := m["id"].(string)
	idForged := false
	if idVal != "" && !reMsgIdV2.MatchString(idVal) {
		idForged = true
		typeErrors = append(typeErrors, fmt.Sprintf("id=%q 不符 ^msg_[A-Za-z0-9]{18,40}$（Anthropic 官方 id 不含 '-'）", idVal))
	}

	total := len(matched) + len(missing)
	coverage := 100.0
	if total > 0 {
		coverage = float64(len(matched)) / float64(total) * 100
	}
	out.Detail = map[string]any{
		"matched_fields":  matched,
		"missing_fields":  missing,
		"type_errors":     typeErrors,
		"coverage_percent": roundF(coverage, 1),
		"id_observed":     idVal,
		"id_forged":       idForged,
	}

	if idForged {
		// id 伪造 → 协议层伪造铁证：封顶低分 + 强信号 + high 告警。
		out.Status = probeStatusV2Partial
		out.Score = 30
		out.Diagnosis = &diagnosis{Category: "protocol", Title: "协议字段伪造（id 非官方格式）",
			Suggestions: []string{fmt.Sprintf("id=%q 不是 Anthropic msg_ 格式，强烈暗示逆向/自拼装响应", idVal)}}
		out.signals = append(out.signals, suspicionSignal{
			Code: "PROTOCOL_FORGERY", Title: "协议字段伪造", Tier: "strong",
			Description: fmt.Sprintf("id 字段返回 %q，不符合 Anthropic msg_* / OpenAI chatcmpl-* 标准格式，强烈暗示协议层伪造响应", idVal),
			Evidence: "id=" + idVal, SourceProbe: "D2",
		})
		out.riskAlert = &riskAlert{
			Severity: "high", Title: "协议字段伪造", SourceProbe: "D2",
			Description: fmt.Sprintf("id 字段返回 %q，不符合 Anthropic 标准格式，强烈暗示协议层伪造响应（逆向渠道特征）", idVal),
		}
		return out
	}

	out.Score = int(coverage)
	switch {
	case len(missing) == 0:
		out.Status = probeStatusV2Success
		out.signals = append(out.signals, suspicionSignal{
			Code: "PROTOCOL_PERFECT", Title: "协议字段完整规范", Tier: "positive",
			Description: "Anthropic 标准字段全覆盖，格式无错", Evidence: "coverage=100%", SourceProbe: "D2",
		})
	case coverage >= 70:
		out.Status = probeStatusV2Partial
		out.Diagnosis = &diagnosis{Category: "protocol", Title: "部分协议字段缺失",
			Suggestions: []string{fmt.Sprintf("缺失字段: %v", missing)}}
	default:
		out.Status = probeStatusV2Partial
		out.Diagnosis = &diagnosis{Category: "protocol", Title: "协议字段大量缺失",
			Suggestions: []string{fmt.Sprintf("缺失字段: %v", missing)}}
	}
	return out
}

// probeResponseSignature (D17) — 响应签名：字段加权校验（id 形态 / type / role / model）。
func probeResponseSignature(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D17", Name: "响应签名", Dimension: "协议合规"}
	body := ccMessageBody(p.model, 16, "ping", nil)
	respBody, status, _, err := doPostJSON(p.ctx, p.base+"/v1/messages", body, ccAuthHeaders(p.key))
	if err != nil || status < 200 || status >= 300 {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail = map[string]any{"http_status": status, "error": errStr(err)}
		return out
	}
	r := parseAnthropicMessage(respBody)
	var raw struct {
		Type string `json:"type"`
		Role string `json:"role"`
	}
	_ = common.Unmarshal(respBody, &raw)

	type check struct {
		Field    string `json:"field"`
		Weight   int    `json:"weight"`
		Passed   bool   `json:"passed"`
		Observed string `json:"observed"`
		Expected string `json:"expected"`
	}
	checks := []check{
		{"id", 25, reMsgIdV2.MatchString(r.Id), r.Id, "^msg_[A-Za-z0-9]{18,40}$ (不含 '-')"},
		{"type", 15, raw.Type == "message", raw.Type, "\"message\""},
		{"role", 10, raw.Role == "assistant", raw.Role, "\"assistant\""},
		{"model", 25, r.Model == p.model || modelFamily(r.Model) == modelFamily(p.model), r.Model, "= " + p.model},
		{"stop_reason", 10, r.StopReason != "", r.StopReason, "non-empty"},
		{"usage.output_tokens", 15, r.Usage.OutputTokens > 0, fmt.Sprintf("%d", r.Usage.OutputTokens), ">0"},
	}
	score := 0
	for _, c := range checks {
		if c.Passed {
			score += c.Weight
		}
	}
	out.Detail = map[string]any{"http_status": status, "requested_model": p.model, "checks": checks}
	out.Score = score
	if score >= 90 {
		out.Status = probeStatusV2Success
	} else if score >= 60 {
		out.Status = probeStatusV2Partial
		out.Diagnosis = &diagnosis{Category: "protocol", Title: "响应签名部分字段异常", Suggestions: []string{"id/model/type 等字段未完全符合 Anthropic 规范"}}
	} else {
		out.Status = probeStatusV2Partial
		out.Diagnosis = &diagnosis{Category: "protocol", Title: "响应签名严重异常", Suggestions: []string{"多个关键字段不符合 Anthropic 官方形态，疑似伪造/改写"}}
	}
	return out
}

// probeCacheFields (D18) — 缓存字段完备性：usage 含 cache_creation/cache_read 字段。
func probeCacheFields(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D18", Name: "缓存字段完备性", Dimension: "协议合规"}
	body := ccMessageBody(p.model, 16, "ping", nil)
	respBody, status, _, err := doPostJSON(p.ctx, p.base+"/v1/messages", body, ccAuthHeaders(p.key))
	if err != nil || status < 200 || status >= 300 {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail = map[string]any{"http_status": status, "error": errStr(err)}
		return out
	}
	var raw struct {
		Usage map[string]any `json:"usage"`
	}
	_ = common.Unmarshal(respBody, &raw)
	missing := []string{}
	for _, f := range []string{"cache_creation_input_tokens", "cache_read_input_tokens"} {
		if _, ok := raw.Usage[f]; !ok {
			missing = append(missing, f)
		}
	}
	out.Detail = map[string]any{"http_status": status, "usage_observed": raw.Usage, "missing_fields": missing}
	if len(missing) == 0 {
		out.Status = probeStatusV2Success
		out.Score = 100
	} else {
		out.Status = probeStatusV2Partial
		out.Score = 50
		out.Diagnosis = &diagnosis{Category: "protocol", Title: "缓存字段缺失",
			Suggestions: []string{fmt.Sprintf("usage 缺少: %v（中转可能精简了 usage）", missing)}}
	}
	return out
}

var _ = time.Now
