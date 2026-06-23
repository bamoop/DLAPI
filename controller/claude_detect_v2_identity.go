package controller

import (
	"fmt"
	"regexp"
	"strings"
)

// 身份一致维度探针：D3 / D11。核心反降级（opus 偷换 haiku / 换成 GPT 等）。

// ---- D3 身份一致性 ----
// 显式：response.model 是否 == 宣称模型。
// 隐式：问「你是哪家厂商的什么模型」，解析 vendor/family，与宣称对照。
func probeIdentityConsistency(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D3", Name: "身份一致性", Dimension: "身份一致"}
	reqFamily := modelFamily(p.model)

	// 显式 echo 检查。
	res, status, _, err := ccAsk(p, 64, textOnlyDirective+"In one short line, state which company built you and your model name.", nil)
	if err != nil || status < 200 || status >= 300 {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail = map[string]any{"http_status": status, "error": errStr(err)}
		out.Diagnosis = &diagnosis{Category: "network", Title: "身份探测无响应", Suggestions: []string{errStr(err)}}
		return out
	}
	explicitScore := 0
	explicitReason := "mismatch"
	if res.Model == p.model {
		explicitScore = 100
		explicitReason = "exact_match"
	} else if modelFamily(res.Model) == reqFamily && reqFamily != "" {
		explicitScore = 80
		explicitReason = "family_match"
	} else if res.Model != "" {
		explicitScore = 30
		explicitReason = "model_echo_differs"
	}

	// 隐式解析。
	low := strings.ToLower(res.Text)
	vendor := parseVendor(low)
	familyKw := parseFamilyKeyword(low)
	implicitScore := 0
	implicitReason := "no_identity"
	switch {
	case vendor == "anthropic" && familyKw == reqFamily && reqFamily != "":
		implicitScore = 100
		implicitReason = "vendor_and_family_match"
	case vendor == "anthropic" && familyKw == "claude":
		implicitScore = 80
		implicitReason = "vendor_match_family_keyword"
	case vendor == "anthropic":
		implicitScore = 70
		implicitReason = "vendor_match"
	case vendor != "" && vendor != "anthropic":
		implicitScore = 0
		implicitReason = "vendor_mismatch:" + vendor
	default:
		implicitScore = 40
		implicitReason = "vendor_unknown"
	}

	score := (explicitScore + implicitScore) / 2
	out.Detail = map[string]any{
		"requested_model":  p.model,
		"requested_family": reqFamily,
		"explicit_check":   map[string]any{"response_model_field": res.Model, "score": explicitScore, "reason": explicitReason},
		"implicit_check":   map[string]any{"raw_response": truncate(res.Text, 200), "vendor": vendor, "family_keyword": familyKw, "score": implicitScore, "reason": implicitReason},
	}
	out.Score = score
	switch {
	case vendor != "" && vendor != "anthropic":
		out.Status = probeStatusV2Partial
		out.Diagnosis = &diagnosis{Category: "identity", Title: "厂商不符（疑似换模型）",
			Suggestions: []string{fmt.Sprintf("隐式身份解析出 vendor=%s，与宣称的 Anthropic 不符", vendor)}}
		out.signals = append(out.signals, suspicionSignal{
			Code: "VENDOR_MISMATCH", Title: "厂商身份不符", Tier: "strong",
			Description: "模型自报厂商与宣称不一致，疑似后端被换成非 Claude 模型",
			Evidence: "vendor=" + vendor, SourceProbe: "D3",
		})
	case score >= 90:
		out.Status = probeStatusV2Success
	case score >= 60:
		out.Status = probeStatusV2Success
	default:
		out.Status = probeStatusV2Partial
		out.Diagnosis = &diagnosis{Category: "identity", Title: "身份信号偏弱",
			Suggestions: []string{"显式/隐式身份未能稳定匹配宣称模型"}}
	}
	return out
}

// ---- D11 隐式身份（代码签名）----
// 杀手锏：要求写一段代码并在文件头注释里填 Tool/Date/Model-Version/Author。
// 真 Claude（尤其 Claude Code 语境）会在 author/model 字段写自己的身份；
// 被换成 haiku/其它会写出不同的 model 名或厂商，GPT 系会暴露非 Claude 特征。
func probeCodeSignature(p *detectV2Context) probeOutcome {
	out := probeOutcome{Code: "D11", Name: "隐式身份", Dimension: "身份一致"}
	reqFamily := modelFamily(p.model)
	prompt := textOnlyDirective +
		"Write a complete Python script for a small Date Difference Calculator. " +
		"It MUST start with a file header comment block containing exactly these fields, each on its own line: " +
		"Tool Name, Generated Date, Model/Version (the exact model that is generating this), Author, License, Python Version. " +
		"Fill them in truthfully based on who you actually are. Then write the code."
	res, status, _, err := ccAsk(p, 600, prompt, nil)
	if err != nil || status < 200 || status >= 300 {
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Detail = map[string]any{"http_status": status, "error": errStr(err)}
		out.Diagnosis = &diagnosis{Category: "network", Title: "代码签名探测无响应", Suggestions: []string{errStr(err)}}
		return out
	}

	header := res.Text
	if len(header) > 1200 {
		header = header[:1200]
	}
	low := strings.ToLower(header)
	modelLine := extractHeaderField(header, []string{"model/version", "model", "version"})
	authorLine := extractHeaderField(header, []string{"author"})
	out.Detail = map[string]any{
		"requested_family": reqFamily,
		"raw_response":     truncate(res.Text, 600),
		"model_line":       modelLine,
		"author_line":      authorLine,
	}

	declaredVendor := parseVendor(strings.ToLower(modelLine + " " + authorLine + " " + low))
	declaredFamily := parseFamilyKeyword(strings.ToLower(modelLine + " " + authorLine))
	authorIsClaude := containsAny(strings.ToLower(authorLine), []string{"claude", "anthropic"})

	switch {
	case declaredVendor != "" && declaredVendor != "anthropic":
		out.Status = probeStatusV2Partial
		out.Score = 0
		out.Diagnosis = &diagnosis{Category: "identity", Title: "代码签名暴露非 Claude 身份",
			Suggestions: []string{fmt.Sprintf("生成代码头部自报 vendor=%s，疑似后端换成非 Claude", declaredVendor)}}
		out.signals = append(out.signals, suspicionSignal{
			Code: "CODE_SIG_MISMATCH", Title: "代码签名身份不符", Tier: "strong",
			Description: "生成代码的文件头自报模型/作者非 Claude 系", Evidence: truncate(modelLine+" | "+authorLine, 120), SourceProbe: "D11",
		})
	case declaredFamily == reqFamily && reqFamily != "":
		out.Status = probeStatusV2Success
		out.Score = 100
		out.Detail["match"] = "model_family_confirmed"
		if authorIsClaude {
			out.signals = append(out.signals, suspicionSignal{
				Code: "AUTHOR_REAL_AI", Title: "Author 含真 AI 身份", Tier: "positive",
				Description: "代码注释 author 字段填写 Anthropic/Claude 自身身份，符合真 Claude 在被要求填 author 时的典型行为",
				Evidence: "Author: " + truncate(authorLine, 60), SourceProbe: "D11",
			})
		}
	case declaredFamily == "claude" || authorIsClaude:
		out.Status = probeStatusV2Success
		out.Score = 88
		out.Detail["match"] = "claude_family_no_exact_version"
	default:
		out.Status = probeStatusV2Partial
		out.Score = 50
		out.Diagnosis = &diagnosis{Category: "identity", Title: "代码签名未明确自报 Claude 身份",
			Suggestions: []string{"模型未在代码头部写出可识别的 Claude 模型/作者身份"}}
	}
	return out
}

// ---- 身份解析小工具 ----

// modelFamily 把模型名归到 family：claude-opus / claude-sonnet / claude-haiku / claude / ""。
func modelFamily(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return "claude-opus"
	case strings.Contains(m, "sonnet"):
		return "claude-sonnet"
	case strings.Contains(m, "haiku"):
		return "claude-haiku"
	case strings.Contains(m, "claude"):
		return "claude"
	default:
		return ""
	}
}

func parseVendor(low string) string {
	switch {
	case strings.Contains(low, "anthropic"):
		return "anthropic"
	case strings.Contains(low, "openai") || strings.Contains(low, "gpt-") || strings.Contains(low, "chatgpt"):
		return "openai"
	case strings.Contains(low, "google") || strings.Contains(low, "gemini") || strings.Contains(low, "deepmind"):
		return "google"
	case strings.Contains(low, "deepseek"):
		return "deepseek"
	case strings.Contains(low, "qwen") || strings.Contains(low, "alibaba") || strings.Contains(low, "通义"):
		return "alibaba"
	case strings.Contains(low, "mistral"):
		return "mistral"
	case strings.Contains(low, "meta") || strings.Contains(low, "llama"):
		return "meta"
	default:
		return ""
	}
}

func parseFamilyKeyword(low string) string {
	switch {
	case strings.Contains(low, "opus"):
		return "claude-opus"
	case strings.Contains(low, "sonnet"):
		return "claude-sonnet"
	case strings.Contains(low, "haiku"):
		return "claude-haiku"
	case strings.Contains(low, "claude"):
		return "claude"
	default:
		return ""
	}
}

var headerFieldRe = regexp.MustCompile(`(?im)^[\s#/*]*([A-Za-z][A-Za-z /._-]*?)\s*[:：]\s*(.+?)\s*$`)

// extractHeaderField 从代码头部注释里按候选字段名（小写匹配）抽出第一行值。
func extractHeaderField(text string, fieldNames []string) string {
	for _, m := range headerFieldRe.FindAllStringSubmatch(text, -1) {
		if len(m) < 3 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(m[1]))
		for _, fn := range fieldNames {
			if key == fn {
				return strings.TrimSpace(m[2])
			}
		}
	}
	return ""
}
