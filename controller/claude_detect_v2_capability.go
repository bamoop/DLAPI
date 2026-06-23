package controller

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// 能力验证维度探针：D10 / D16 / D7 / D19。
// 廉价/降级模型在这些任务上会翻车，是「按 opus 收钱走 haiku」的有效旗标。

// ---- D10 思维链 ----
// 数学应用题，校验最终答案正确。降级到小模型时正确率下降。
func probeChainOfThought(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D10", Name: "思维链", Dimension: "能力验证"}
	// 确定性应用题：23 个苹果，送出 7，再买 12，平均分 4 篮 → 7。
	q := textOnlyDirective + "Solve step by step, then on the LAST line write 'FINAL: <number>'. " +
		"A basket has 23 apples. You give away 7, then buy 12 more, then split them equally into 4 baskets. " +
		"How many apples are in each basket?"
	res, status, _, err := ccAsk(p, 400, q, nil)
	if err != nil || status < 200 || status >= 300 {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail = map[string]any{"http_status": status, "error": errStr(err)}
		out.Diagnosis = &diagnosis{Category: "network", Title: "思维链无响应", Suggestions: []string{errStr(err)}}
		return out
	}
	finalNum := extractFinalNumber(res.Text)
	correct := finalNum == "7"
	out.Detail = map[string]any{
		"final_answer": truncate(res.Text, 400),
		"parsed_final": finalNum,
		"expected":     "7",
		"answer_correct": correct,
		"http_status":  status,
	}
	if correct {
		out.Status = probeStatusV2Success
		out.Score = 100
	} else {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Diagnosis = &diagnosis{Category: "capability", Title: "思维链答案错误",
			Suggestions: []string{fmt.Sprintf("期望 7，解析得 '%s' —— 可能后端被降级为弱模型", finalNum)}}
	}
	return out
}

// ---- D16 能力指纹 ----
// 两个子测，分数取平均：
//   constraints_json — 精确 minified JSON 输出（字符串反转 + 算术 + 固定 token）。
//   logic_grid       — 简单逻辑推理唯一解。
func probeCapabilityFingerprint(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D16", Name: "能力指纹", Dimension: "能力验证"}
	type subtest struct {
		Kind    string `json:"kind"`
		Raw     string `json:"raw_response"`
		Correct bool   `json:"correct"`
		Score   int    `json:"score"`
		Expected string `json:"expected"`
	}
	subs := []subtest{}

	// constraints_json: a=reverse('testz')='ztset', b=29+8=37, c='ZT-231C5CB5'
	jprompt := textOnlyDirective + "Reply with exactly one minified JSON object and no markdown, no code fence. " +
		"Schema: {\"a\": string, \"b\": number, \"c\": string}. " +
		"Set a to the reverse of 'testz'. Set b to 29 + 8. Set c to 'ZT-231C5CB5'."
	if r, st, _, err := ccAsk(p, 200, jprompt, nil); err == nil && st < 300 {
		var parsed struct {
			A string `json:"a"`
			B int    `json:"b"`
			C string `json:"c"`
		}
		clean := extractJSONObject(r.Text)
		_ = common.UnmarshalJsonStr(clean, &parsed)
		ok := parsed.A == "ztset" && parsed.B == 37 && parsed.C == "ZT-231C5CB5"
		sc := 0
		if ok {
			sc = 100
		}
		subs = append(subs, subtest{"constraints_json", truncate(r.Text, 200), ok, sc, "a=ztset,b=37,c=ZT-231C5CB5"})
	}

	// logic_grid: 唯一认证网关推理。设计成答案确定 = "C"。
	lprompt := textOnlyDirective + "Four API gateways A, B, C, D are tested; exactly one is certified. " +
		"Clues: (1) The certified one is not A. (2) If B is certified then D is too — but only one is certified, so B is not. " +
		"(3) D is not certified. Which gateway is certified? Answer with the single letter only."
	if r, st, _, err := ccAsk(p, 100, lprompt, nil); err == nil && st < 300 {
		ans := strings.ToUpper(strings.TrimSpace(extractFirstLetter(r.Text)))
		ok := ans == "C"
		sc := 0
		if ok {
			sc = 100
		}
		subs = append(subs, subtest{"logic_grid", truncate(r.Text, 120), ok, sc, "C"})
	}

	out.Detail = map[string]any{"subtests": subs}
	if len(subs) == 0 {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Diagnosis = &diagnosis{Category: "network", Title: "能力指纹无有效响应", Suggestions: []string{"子测全部失败"}}
		return out
	}
	sum := 0
	for _, s := range subs {
		sum += s.Score
	}
	avg := sum / len(subs)
	out.Score = avg
	switch {
	case avg >= 90:
		out.Status = probeStatusV2Success
		out.Label = "能力指纹整体通过"
	case avg >= 50:
		out.Status = probeStatusV2Partial
		out.Diagnosis = &diagnosis{Category: "capability", Title: "部分能力子测失败",
			Suggestions: []string{"约束输出或逻辑推理未全对，疑似能力偏弱"}}
	default:
		out.Status = probeStatusV2Partial
		out.Diagnosis = &diagnosis{Category: "capability", Title: "能力子测大面积失败",
			Suggestions: []string{"约束输出/逻辑推理均失败，疑似后端被降级"}}
	}
	return out
}

// ---- D7 结构化输出 / 工具调用 ----
// 给一个 tool 定义并要求调用，校验 tool_called + name + 参数 JSON/schema。
// 先 claude_code 模式，失败回退原生 Anthropic（不带 Claude Code system）。
// 很多中转不支持 tools 参数 → partial。
func probeToolUse(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D7", Name: "结构化输出", Dimension: "能力验证"}
	nonce := "e7b26bae"
	tool := map[string]any{
		"name":        "get_weather",
		"description": "Get the current weather for a city.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city":  map[string]any{"type": "string"},
				"nonce": map[string]any{"type": "string", "description": "echo this nonce: " + nonce},
			},
			"required": []string{"city", "nonce"},
		},
	}
	userMsg := "Call get_weather for city 'Tokyo' and set nonce to '" + nonce + "'."

	type attempt struct {
		Mode       string `json:"attempt_mode"`
		Status     int    `json:"http_status"`
		StopReason string `json:"stop_reason"`
		ErrKind    string `json:"error_kind,omitempty"`
		Err        string `json:"error,omitempty"`
	}
	attempts := []attempt{}

	validate := func(body []byte) (bool, bool, bool, bool, string) {
		// 返回 toolCalled, nameMatch, argsJSON, argsValueMatch, rawArgs
		var parsed struct {
			Content []struct {
				Type  string         `json:"type"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content"`
		}
		_ = common.Unmarshal(body, &parsed)
		for _, c := range parsed.Content {
			if c.Type == "tool_use" {
				raw, _ := common.Marshal(c.Input)
				nameOk := c.Name == "get_weather"
				city, _ := c.Input["city"].(string)
				n, _ := c.Input["nonce"].(string)
				valOk := strings.EqualFold(city, "Tokyo") && n == nonce
				return true, nameOk, true, valOk, string(raw)
			}
		}
		return false, false, false, false, ""
	}

	var toolCalled, nameMatch, argsJSON, argsVal bool
	var rawArgs string

	// attempt 1: claude_code 模式（带 Claude Code system）。
	body1, _ := common.Marshal(map[string]any{
		"model": p.model, "max_tokens": 400, "system": claudeCodeSystemPrompt,
		"tools": []any{tool}, "tool_choice": map[string]any{"type": "any"},
		"messages": []map[string]any{{"role": "user", "content": userMsg}},
	})
	rb1, st1, _, err1 := doPostJSON(p.ctx, p.base+"/v1/messages", body1, ccAuthHeaders(p.key))
	a1 := attempt{Mode: "claude_code", Status: st1}
	if err1 != nil {
		a1.ErrKind = "transport"
		a1.Err = err1.Error()
	} else if st1 >= 200 && st1 < 300 {
		toolCalled, nameMatch, argsJSON, argsVal, rawArgs = validate(rb1)
		a1.StopReason = parseStopReason(rb1)
	} else {
		a1.ErrKind = "http"
		a1.Err = fmt.Sprintf("HTTP %d", st1)
	}
	attempts = append(attempts, a1)

	// attempt 2: 原生 Anthropic fallback（无 Claude Code system）。
	if !toolCalled {
		body2, _ := common.Marshal(map[string]any{
			"model": p.model, "max_tokens": 400,
			"tools": []any{tool}, "tool_choice": map[string]any{"type": "any"},
			"messages": []map[string]any{{"role": "user", "content": userMsg}},
		})
		rb2, st2, _, err2 := doPostJSON(p.ctx, p.base+"/v1/messages", body2, anthropicAuthHeaders(p.key))
		a2 := attempt{Mode: "anthropic_native_fallback", Status: st2}
		if err2 != nil {
			a2.ErrKind = "transport"
			a2.Err = err2.Error()
		} else if st2 >= 200 && st2 < 300 {
			toolCalled, nameMatch, argsJSON, argsVal, rawArgs = validate(rb2)
			a2.StopReason = parseStopReason(rb2)
		} else {
			a2.ErrKind = "http"
			a2.Err = fmt.Sprintf("HTTP %d", st2)
		}
		attempts = append(attempts, a2)
	}

	out.Detail = map[string]any{
		"tool_name": "get_weather", "nonce": nonce,
		"validation": map[string]any{
			"tool_called": toolCalled, "tool_name_match": nameMatch,
			"arguments_valid_json": argsJSON, "arguments_value_match": argsVal,
			"raw_arguments": rawArgs,
		},
		"attempts": attempts,
	}
	switch {
	case toolCalled && nameMatch && argsVal:
		out.Status = probeStatusV2Success
		out.Score = 100
	case toolCalled && nameMatch:
		out.Status = probeStatusV2Partial
		out.Score = 60
		out.Diagnosis = &diagnosis{Category: "tool_use", Title: "工具调用参数不符",
			Suggestions: []string{"调用了正确工具但参数值/nonce 不匹配"}}
	default:
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Diagnosis = &diagnosis{Category: "tool_use", Title: "工具调用未生效",
			Suggestions: []string{"已尝试 claude_code → 原生 Anthropic fallback，全部未拿到 tool_use", "中转可能不支持 tools 参数"}}
	}
	return out
}

// ---- D19 文档识别 ----
// 在一段文档文本里埋一个 token，要求提取回显。检测中转是否截断/改写长文本。
func probeDocExtraction(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D19", Name: "文档识别", Dimension: "能力验证"}
	token := "ZTEST-TOKEN-a09bdb17"
	doc := "Below is a configuration document. " + strings.Repeat("This line is filler context describing the gateway setup and routing policy. ", 12) +
		"\nHIDDEN_VERIFICATION_TOKEN = " + token + "\n" +
		strings.Repeat("More filler explaining rate limits and caching behavior. ", 12)
	prompt := textOnlyDirective + "Read the following document and reply with ONLY the value of HIDDEN_VERIFICATION_TOKEN, nothing else.\n\n" + doc
	res, status, _, err := ccAsk(p, 100, prompt, nil)
	if err != nil || status < 200 || status >= 300 {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail = map[string]any{"http_status": status, "error": errStr(err)}
		return out
	}
	answer := strings.TrimSpace(res.Text)
	correct := strings.Contains(answer, token)
	out.Detail = map[string]any{"expected_token": token, "answer": truncate(answer, 120), "http_status": status}
	if correct {
		out.Status = probeStatusV2Success
		out.Score = 100
	} else if answer == "" {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail["note"] = "empty_response"
		out.Diagnosis = &diagnosis{Category: "capability", Title: "文档识别空响应", Suggestions: []string{"上游返回空文本"}}
	} else {
		out.Status = probeStatusV2Partial
		out.Score = 30
		out.Diagnosis = &diagnosis{Category: "capability", Title: "未能提取文档 token",
			Suggestions: []string{"长文本中的 token 未被正确提取，可能被截断/改写"}}
	}
	return out
}

// ---- 解析小工具 ----

func parseStopReason(body []byte) string {
	var r struct {
		StopReason string `json:"stop_reason"`
	}
	_ = common.Unmarshal(body, &r)
	return r.StopReason
}

// extractFinalNumber 找 "FINAL: <num>" 或文本中最后一个整数。
func extractFinalNumber(text string) string {
	low := strings.ToLower(text)
	if idx := strings.LastIndex(low, "final:"); idx >= 0 {
		rest := text[idx+len("final:"):]
		return firstIntToken(rest)
	}
	// 回退：最后一个整数。
	digits := ""
	last := ""
	for _, r := range text {
		if r >= '0' && r <= '9' {
			digits += string(r)
		} else {
			if digits != "" {
				last = digits
			}
			digits = ""
		}
	}
	if digits != "" {
		last = digits
	}
	return last
}

func firstIntToken(s string) string {
	out := ""
	started := false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			out += string(r)
			started = true
		} else if started {
			break
		}
	}
	return out
}

func extractFirstLetter(s string) string {
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			return string(r)
		}
	}
	return ""
}

// extractJSONObject 从可能带 markdown 的文本里抠出第一个 {...} JSON 对象。
func extractJSONObject(text string) string {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		return text[start : end+1]
	}
	return text
}
