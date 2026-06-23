package controller

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// 性能维度探针：D8 / D9 / S5。

// d8BaselineMs：响应时延基线（经验值，ztest 用 ~2200ms）。ratio>1.5 扣分。
const d8BaselineMs = 2200

// probeLatency (D8) — 响应时延：单次 latency vs 基线 ratio。
func probeLatency(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D8", Name: "响应时延", Dimension: "性能"}
	start := time.Now()
	body := ccMessageBody(p.model, 16, "ping", nil)
	_, status, _, err := doPostJSON(p.ctx, p.base+"/v1/messages", body, ccAuthHeaders(p.key))
	lat := int(time.Since(start) / time.Millisecond)
	out.LatencyMs = lat
	if err != nil || status < 200 || status >= 300 {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail = map[string]any{"latency_ms": lat, "http_status": status, "error": errStr(err)}
		return out
	}
	ratio := float64(lat) / float64(d8BaselineMs)
	out.Detail = map[string]any{"latency_ms": lat, "baseline_ms": d8BaselineMs, "ratio": roundF(ratio, 3)}
	switch {
	case ratio <= 1.5:
		out.Status = probeStatusV2Success
		out.Score = 100
		out.Detail["note"] = "1.5x 基线内"
		if ratio > 1.0 {
			out.Score = 90
		}
	case ratio <= 3.0:
		out.Status = probeStatusV2Success
		out.Score = 70
		out.Detail["note"] = "1.5x~3x 基线"
	default:
		out.Status = probeStatusV2Partial
		out.Score = 40
		out.Detail["note"] = ">3x 基线，明显偏慢"
		out.Diagnosis = &diagnosis{Category: "performance", Title: "响应明显偏慢",
			Suggestions: []string{fmt.Sprintf("ratio=%.1f，可能多层转发或后端拥塞", ratio)}}
	}
	return out
}

// probeStability (D9) — 性能稳定性：多次采样的成功率 + 延迟离散度。
func probeStability(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D9", Name: "性能稳定性", Dimension: "性能"}
	type sample struct {
		Seq       int    `json:"seq"`
		Ok        bool   `json:"ok"`
		LatencyMs int    `json:"latency_ms"`
		Status    int    `json:"http_status"`
		Err       string `json:"error,omitempty"`
	}
	const n = 4
	samples := make([]sample, 0, n)
	okCount := 0
	for i := 0; i < n; i++ {
		start := time.Now()
		body := ccMessageBody(p.model, 16, "ping", nil)
		_, status, _, err := doPostJSON(p.ctx, p.base+"/v1/messages", body, ccAuthHeaders(p.key))
		lat := int(time.Since(start) / time.Millisecond)
		s := sample{Seq: i, LatencyMs: lat, Status: status}
		if err == nil && status >= 200 && status < 300 {
			s.Ok = true
			okCount++
		} else {
			s.Err = errStr(err)
		}
		samples = append(samples, s)
	}
	out.Detail = map[string]any{"samples": samples}
	rate := float64(okCount) / float64(n)
	out.Detail["success_rate"] = roundF(rate, 2)
	switch {
	case okCount == n:
		out.Status = probeStatusV2Success
		out.Score = 100
	case rate >= 0.5:
		out.Status = probeStatusV2Partial
		out.Score = int(rate * 100)
		out.Diagnosis = &diagnosis{Category: "performance", Title: "稳定性不足",
			Suggestions: []string{fmt.Sprintf("成功率 %.0f%%，部分请求失败", rate*100)}}
	default:
		out.Status = probeStatusV2Partial
		out.Score = int(rate * 100)
		out.Diagnosis = &diagnosis{Category: "performance", Title: "稳定性差",
			Suggestions: []string{fmt.Sprintf("成功率仅 %.0f%%", rate*100)}}
	}
	return out
}

// probeStreamIntegrity (S5) — 流完整性：SSE 是否规范（is_sse / first_byte / chunk / done / 聚合一致）。
func probeStreamIntegrity(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "S5", Name: "流完整性", Dimension: "性能"}
	start := time.Now()
	body := ccMessageBody(p.model, 64, "Count from 1 to 10, one number per line.", map[string]any{"stream": true})
	req, _ := http.NewRequestWithContext(p.ctx, http.MethodPost, p.base+"/v1/messages", strings.NewReader(string(body)))
	for k, v := range ccAuthHeaders(p.key) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := newDetectClient().Do(req)
	if err != nil {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail = map[string]any{"error": err.Error()}
		return out
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	isSSE := strings.Contains(ct, "text/event-stream")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail = map[string]any{"http_status": resp.StatusCode, "is_sse": isSSE}
		return out
	}

	var firstByteMs int
	gotFirst := false
	chunkCount := 0
	bytesTotal := 0
	hasDone := false
	var aggregated strings.Builder
	var eventType string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !gotFirst && line != "" {
			firstByteMs = int(time.Since(start) / time.Millisecond)
			gotFirst = true
		}
		bytesTotal += len(line)
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(line[len("event:"):])
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		d := strings.TrimSpace(line[len("data:"):])
		if d == "[DONE]" {
			hasDone = true
			continue
		}
		if eventType == "content_block_delta" {
			chunkCount++
			var payload struct {
				Delta struct {
					Text string `json:"text"`
				} `json:"delta"`
			}
			if common.UnmarshalJsonStr(d, &payload) == nil {
				aggregated.WriteString(payload.Delta.Text)
			}
		}
		if eventType == "message_stop" {
			hasDone = true
		}
	}
	totalMs := int(time.Since(start) / time.Millisecond)
	aggLen := aggregated.Len()
	consistent := aggLen > 0
	out.Detail = map[string]any{
		"is_sse": isSSE, "first_byte_ms": firstByteMs, "total_ms": totalMs,
		"chunk_count": chunkCount, "bytes_total": bytesTotal,
		"has_done_signal": hasDone, "aggregated_text_len": aggLen,
		"aggregation_consistent": consistent,
	}
	score := 0
	if isSSE {
		score += 30
	}
	if chunkCount > 0 {
		score += 30
	}
	if hasDone {
		score += 20
	}
	if consistent {
		score += 20
	}
	out.Score = score
	switch {
	case score >= 90:
		out.Status = probeStatusV2Success
	case score >= 60:
		out.Status = probeStatusV2Success
	default:
		out.Status = probeStatusV2Partial
		out.Diagnosis = &diagnosis{Category: "performance", Title: "流式响应不规范",
			Suggestions: []string{"SSE 格式/分块/结束信号不完整，部分中转把流式伪装成一次性返回"}}
	}
	_ = io.Discard
	return out
}
