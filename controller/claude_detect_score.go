package controller

import (
	"math"
	"sort"
)

// ============================================================================
// Claude 检测引擎 v2 — 打分聚合（对标 ztest.ai）
// ----------------------------------------------------------------------------
// 方法论来自对 ztest.ai 的逆向（其 GET /api/reports/{id} 真实报告 +
// /api/system/score-levels 实测档位）。打分管线（已用真实样本验证）：
//
//	① 探针执行 → status ∈ {success, partial, skipped}, score ∈ [0,100]
//	② 组分 = floor(mean(组内 status!=skipped 的 score))   // partial 计入（双重拉低）
//	③ composite = round(mean(各组组分)) − Σ风险告警惩罚    // clamp[0,100]
//	④ 风险档位 = 从高到低匹配第一个 score>=threshold
//
// 关键证据：ztest 安全组 (30+100+100+100)/4=82.5 → 报告值 82（floor 而非 round）。
// composite：6 组等权均值 88.0 − 1 条 medium 告警 = 87。
// ============================================================================

const (
	probeStatusV2Success = "success"
	probeStatusV2Partial = "partial"
	probeStatusV2Skipped = "skipped"
	probeStatusV2Fail    = "fail"
)

// 维度组的稳定 key + 展示名 + 图标（与 ztest 对齐，便于人工对照）。
type dimensionGroupDef struct {
	Key   string
	Name  string
	Icon  string
	Codes []string // 属于该组的探针 code，决定展示顺序
}

// claudeDetectGroups：6 维度组定义。探针通过 Dimension 字段归组；
// 这里的 Codes 仅用于固定前端展示顺序与「应有探针」清单。
var claudeDetectGroups = []dimensionGroupDef{
	{Key: "protocol", Name: "协议合规", Icon: "protocol", Codes: []string{"HB", "D1", "D2", "D17", "D18"}},
	{Key: "identity", Name: "身份一致", Icon: "identity", Codes: []string{"D3", "D11"}},
	{Key: "capability", Name: "能力验证", Icon: "capability", Codes: []string{"D7", "D10", "D13", "D16", "D19"}},
	{Key: "content", Name: "内容完整性", Icon: "content", Codes: []string{"D5"}},
	{Key: "security", Name: "安全性", Icon: "security", Codes: []string{"S1", "S2", "S3", "S4"}},
	{Key: "performance", Name: "性能", Icon: "performance", Codes: []string{"D8", "D9", "S5"}},
}

// 维度组名 → key 的反查（探针 Dimension 用中文名声明，聚合时归到对应组）。
var dimensionNameToKey = func() map[string]string {
	m := map[string]string{}
	for _, g := range claudeDetectGroups {
		m[g.Name] = g.Key
	}
	return m
}()

// diagnosis 给前端展示「这个探针为什么没满分 + 怎么排查」。
type diagnosis struct {
	Category    string   `json:"category"`
	Title       string   `json:"title"`
	Suggestions []string `json:"suggestions"`
}

// riskAlert 是会扣 composite 分的风险告警（如 S1 大量 token 注入）。
type riskAlert struct {
	Severity    string `json:"severity"` // "low"|"medium"|"high"
	Title       string `json:"title"`
	Description string `json:"description"`
	SourceProbe string `json:"source_probe"`
}

// suspicionSignal 进入 proxy_suspicion，分 strong/medium/positive 三档展示。
type suspicionSignal struct {
	Code        string `json:"code"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Evidence    string `json:"evidence"`
	SourceProbe string `json:"source_probe"`
	Tier        string `json:"-"` // "strong"|"medium"|"positive"，仅聚合时用
}

// probeOutcome 是 v2 每个探针的统一返回。
type probeOutcome struct {
	Code      string         `json:"probe_code"`
	Name      string         `json:"probe_name"`
	Dimension string         `json:"dimension"` // 中文组名
	Status    string         `json:"status"`    // success|partial|skipped
	Score     int            `json:"score"`     // 0..100；skipped 时忽略
	Label     string         `json:"label,omitempty"`
	LatencyMs int            `json:"latency_ms,omitempty"`
	Detail    map[string]any `json:"details,omitempty"`
	Diagnosis *diagnosis     `json:"diagnosis,omitempty"`

	// 聚合用副产物（不直接决定组分，但进 summary）。
	riskAlert *riskAlert
	signals   []suspicionSignal
}

// ---- 风险告警惩罚（单样本推断，做成可调常量；后续多抓 ztest 报告校准）----
func riskAlertPenalty(severity string) int {
	switch severity {
	case "high":
		return 5
	case "medium":
		return 1
	default: // low
		return 0
	}
}

// ---- 风险档位（实测自 ztest /api/system/score-levels）----
type scoreLevel struct {
	Threshold int    `json:"threshold"`
	Code      string `json:"code"`
	Label     string `json:"label"`
	Color     string `json:"color"`
}

// claudeDetectLevels：阈值从高到低；匹配第一个 score>=Threshold。
var claudeDetectLevels = []scoreLevel{
	{Threshold: 80, Code: "low", Label: "推荐", Color: "hsl(155 68% 50%)"},
	{Threshold: 60, Code: "medium-low", Label: "良好", Color: "hsl(45 90% 52%)"},
	{Threshold: 40, Code: "medium", Label: "一般", Color: "hsl(28 90% 55%)"},
	{Threshold: 20, Code: "high", Label: "不建议", Color: "hsl(0 85% 55%)"},
	{Threshold: 0, Code: "critical", Label: "不可用", Color: "hsl(350 75% 45%)"},
}

func levelForScore(score int) scoreLevel {
	for _, l := range claudeDetectLevels { // 已按阈值降序
		if score >= l.Threshold {
			return l
		}
	}
	return claudeDetectLevels[len(claudeDetectLevels)-1]
}

// ---- 聚合结果结构 ----
type groupScore struct {
	Key          string             `json:"key"`
	Name         string             `json:"name"`
	Icon         string             `json:"icon"`
	ScorePercent int                `json:"score_percent"`
	Probes       []groupProbeRef    `json:"probes"`
}

type groupProbeRef struct {
	Code   string `json:"code"`
	Status string `json:"status"`
	Score  int    `json:"score"`
}

type detectVerdict struct {
	Level       string   `json:"level"`
	Label       string   `json:"label"`
	Headline    string   `json:"headline"`
	KeyFindings []string `json:"key_findings"`
}

type detectSummaryV2 struct {
	CompositeScore int                        `json:"composite_score"`
	RiskLevel      string                     `json:"risk_level"`
	Verdict        detectVerdict              `json:"verdict"`
	DimensionGroups []groupScore              `json:"dimension_groups"`
	RiskAlerts     []riskAlert                `json:"risk_alerts"`
	ProxySuspicion proxySuspicionOut          `json:"proxy_suspicion"`
}

type proxySuspicionOut struct {
	Signals         suspicionBuckets `json:"signals"`
	Suspicion       int              `json:"suspicion"`
	VerdictOverride bool             `json:"verdict_override"`
}

type suspicionBuckets struct {
	Strong   []suspicionSignal `json:"strong"`
	Medium   []suspicionSignal `json:"medium"`
	Positive []suspicionSignal `json:"positive"`
}

// aggregateDetect 把全部探针结果聚合成 ztest 式 summary。
func aggregateDetect(outcomes []probeOutcome) detectSummaryV2 {
	// 1) 按维度组归集探针分数。
	byKey := map[string][]probeOutcome{}
	for _, o := range outcomes {
		key := dimensionNameToKey[o.Dimension]
		if key == "" {
			continue // 未知维度，跳过（不影响打分）
		}
		byKey[key] = append(byKey[key], o)
	}

	// 2) 组分 = floor(mean(非 skipped 探针 score))。
	var groups []groupScore
	var groupScores []int
	for _, def := range claudeDetectGroups {
		members := byKey[def.Key]
		sum, cnt := 0, 0
		refs := make([]groupProbeRef, 0, len(members))
		// 按 def.Codes 顺序输出，未在 Codes 里的探针追加在后。
		ordered := orderByCodes(members, def.Codes)
		for _, o := range ordered {
			refs = append(refs, groupProbeRef{Code: o.Code, Status: o.Status, Score: o.Score})
			if o.Status == probeStatusV2Skipped {
				continue
			}
			sum += o.Score
			cnt++
		}
		gs := groupScore{Key: def.Key, Name: def.Name, Icon: def.Icon, Probes: refs}
		if cnt > 0 {
			gs.ScorePercent = int(math.Floor(float64(sum) / float64(cnt)))
			groupScores = append(groupScores, gs.ScorePercent)
		} else {
			gs.ScorePercent = 0 // 全 skipped/空：不计入 composite
		}
		groups = append(groups, gs)
	}

	// 3) composite = round(mean(有效组分)) − Σ风险惩罚，clamp[0,100]。
	composite := 0
	if len(groupScores) > 0 {
		s := 0
		for _, g := range groupScores {
			s += g
		}
		composite = int(math.Round(float64(s) / float64(len(groupScores))))
	}

	// 收集风险告警 + suspicion 信号。
	var alerts []riskAlert
	buckets := suspicionBuckets{Strong: []suspicionSignal{}, Medium: []suspicionSignal{}, Positive: []suspicionSignal{}}
	for _, o := range outcomes {
		if o.riskAlert != nil {
			alerts = append(alerts, *o.riskAlert)
			composite -= riskAlertPenalty(o.riskAlert.Severity)
		}
		for _, sig := range o.signals {
			switch sig.Tier {
			case "strong":
				buckets.Strong = append(buckets.Strong, sig)
			case "medium":
				buckets.Medium = append(buckets.Medium, sig)
			default:
				buckets.Positive = append(buckets.Positive, sig)
			}
		}
	}
	if composite < 0 {
		composite = 0
	}
	if composite > 100 {
		composite = 100
	}

	// suspicion 指数：strong*40 + medium*15，clamp[0,100]。
	suspicion := len(buckets.Strong)*40 + len(buckets.Medium)*15
	if suspicion > 100 {
		suspicion = 100
	}

	// verdict_override：命中强信号（协议伪造/无关回答/包装身份/厂商不符等）→ 直接
	// 压到 danger 档，不被一堆正常的能力/多模态高分稀释。这是逆向渠道的判别关键：
	// 它后端是真 Claude，能力探针全过，破绽只在协议伪造+包装身份这几处强信号。
	overridden := false
	if len(buckets.Strong) > 0 {
		overridden = true
		// 同时把综合分压到 danger 档（<40），与强信号数量挂钩。
		capScore := 40 - len(buckets.Strong)*5
		if capScore < 10 {
			capScore = 10
		}
		if composite > capScore {
			composite = capScore
		}
	}

	lvl := levelForScore(composite)
	verdict := buildVerdict(composite, lvl, groups, alerts, buckets)
	if overridden {
		verdict.Level = "danger"
		verdict.Label = "模型替换风险"
		verdict.Headline = "多项检测命中强信号，存在模型替换/协议伪造/包装身份风险，非官方直连形态"
	}

	return detectSummaryV2{
		CompositeScore:  composite,
		RiskLevel:       lvl.Code,
		Verdict:         verdict,
		DimensionGroups: groups,
		RiskAlerts:      alerts,
		ProxySuspicion:  proxySuspicionOut{Signals: buckets, Suspicion: suspicion, VerdictOverride: overridden},
	}
}

// orderByCodes 把 members 按 codes 给定顺序排列，剩余的按 Code 字典序追加。
func orderByCodes(members []probeOutcome, codes []string) []probeOutcome {
	idx := map[string]int{}
	for i, c := range codes {
		idx[c] = i
	}
	out := make([]probeOutcome, len(members))
	copy(out, members)
	sort.SliceStable(out, func(i, j int) bool {
		ri, oki := idx[out[i].Code]
		rj, okj := idx[out[j].Code]
		switch {
		case oki && okj:
			return ri < rj
		case oki:
			return true
		case okj:
			return false
		default:
			return out[i].Code < out[j].Code
		}
	})
	return out
}

// buildVerdict 生成总判定文案 + key_findings。
func buildVerdict(score int, lvl scoreLevel, groups []groupScore, alerts []riskAlert, buckets suspicionBuckets) detectVerdict {
	v := detectVerdict{Level: lvl.Code, Label: levelVerdictLabel(score), KeyFindings: []string{}}
	switch {
	case score >= 80:
		v.Headline = "检测信号整体一致性高，更接近官方直连或高质量代理"
	case score >= 60:
		v.Headline = "整体可用，但存在一处或多处偏离官方行为的信号，需留意"
	case score >= 40:
		v.Headline = "多项行为/能力检测偏离官方，疑似掺水或降级中转"
	case score >= 20:
		v.Headline = "大量关键检测失败，强烈不建议使用"
	default:
		v.Headline = "核心检测无法通过，渠道不可用或严重伪造"
	}
	// key_findings：优先放风险告警，再放强/中可疑信号，最后放正向亮点。
	for _, a := range alerts {
		v.KeyFindings = append(v.KeyFindings, a.SourceProbe+" "+a.Title+"："+a.Description)
	}
	for _, s := range buckets.Strong {
		v.KeyFindings = append(v.KeyFindings, s.SourceProbe+" "+s.Title+"："+s.Description)
	}
	if len(v.KeyFindings) == 0 {
		for _, s := range buckets.Positive {
			v.KeyFindings = append(v.KeyFindings, s.SourceProbe+" "+s.Title+"："+s.Description)
			if len(v.KeyFindings) >= 3 {
				break
			}
		}
	}
	return v
}

// levelVerdictLabel 给 verdict 一个更口语的标签。
func levelVerdictLabel(score int) string {
	switch {
	case score >= 80:
		return "高一致性（近似官方）"
	case score >= 60:
		return "基本可用（疑似中转）"
	case score >= 40:
		return "可疑（疑似掺水/降级）"
	case score >= 20:
		return "高风险"
	default:
		return "不可用"
	}
}
