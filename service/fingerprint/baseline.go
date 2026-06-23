package fingerprint

import "strings"

// ============================================================================
// Claude 真值基线（baseline）—— 反套壳/反降级检测的外部锚点
// ----------------------------------------------------------------------------
// 设计动机见 plan：现有 P3 token 探针「自己比自己」，套壳站内部自洽即满分。
// 真正的 tokenizer 指纹必须有一个**独立于被测上游**的真值锚点。
//
// 约束：Anthropic 对 Claude 3+ 不提供离线 tokenizer，本项目也无 Claude 分词器。
// 所以真值不是「绝对 token 数」，而是**差分增量 Δ**：
//
//	对固定语料 base 和 base+BLOCK 分别调 count_tokens，
//	Δ(BLOCK) = tokens(base+BLOCK) - tokens(base)。
//
// 关键性质：若上游是中转且注入了固定前缀 P（如基准 key 注入的缓存/提示词），
// 则两次计数都带 P，做差时 P 抵消 → Δ 不受注入污染。不同分词器（GPT/Gemini/
// 廉价模型）对同一 CJK/代码/数字块给出不同 Δ → 后端换模型即露馅。
//
// !!! 以下 Δ 真值与吞吐区间均为占位/保守值（Stage 0 未标定）。
// !!! 落地流程：用基准 key 跑探针 → 实测 Δ 与各档吞吐 → 回填本文件 →
// !!! 把 baselineCalibrated 置为 true 收紧容差。在此之前探针只给弱提示，不下铁结论。
// ============================================================================

// baselineCalibrated 标记真值是否已用基准 key 实测固化。
//
// 已于 Stage 0（2026-06，基准 key api.derouter.ai/proxy）标定：
//   - tokenizer 差分 Δ：确定性实测，可靠 → P3 全量生效（差分全偏离即判后端可疑）。
//   - 吞吐档位：单样本保守区间，opus/sonnet 重叠 → P15 只在「宣称 opus/sonnet 实测达
//     haiku 级（≥260 tok/s）」时下 decisive 红；其余至多黄旗。多样本后可收紧区间。
const baselineCalibrated = true

// ----------------------------------------------------------------------------
// Tokenizer 差分语料 —— 探针与基线必须共用同一组常量字符串。
// ----------------------------------------------------------------------------

// 语料块。base 为公共前缀；每个 *Block 追加在 base 之后单独计数。
// 故意挑选三类对不同分词器区分度高的内容：CJK、源代码、长数字串。
const (
	// 中性英文基底，单独计一次作为减数。
	tokBase = "The quick brown fox jumps over the lazy dog. " +
		"Pack my box with five dozen liquor jugs. " +
		"How vexingly quick daft zebras jump."

	// CJK 块：中文分词在 Claude 与 GPT/Gemini 间差异显著。
	tokCJKBlock = "中文分词在不同的大语言模型之间存在显著差异，" +
		"这是辨别后端真实模型的有效信号之一。" +
		"日本語のトークン化も同様に違いが出ます。"

	// 代码块：标点/缩进/标识符的合并规则各家不同。
	tokCodeBlock = "func fibonacci(n int) int {\n" +
		"\tif n < 2 {\n\t\treturn n\n\t}\n" +
		"\treturn fibonacci(n-1) + fibonacci(n-2)\n}\n" +
		"const x = [1,2,3].map(i => i*i).reduce((a,b)=>a+b, 0);"

	// 数字块：连续数字的切分粒度差异大。
	tokDigitBlock = "3141592653589793238462643383279502884197169399375105 " +
		"2718281828459045235360287471352662497757247093699959 " +
		"1618033988749894848204586834365638117720309179805762"
)

// TokenCorpusBlock 是一个差分语料块的标识 + 文本。
type TokenCorpusBlock struct {
	Name string // "cjk" | "code" | "digit"
	Text string // 追加在 tokBase 之后的内容
}

// TokenCorpusBase 返回公共前缀文本（减数）。
func TokenCorpusBase() string { return tokBase }

// TokenCorpusBlocks 返回全部差分块，顺序稳定。探针按此顺序对
// (tokBase) 与 (tokBase+block.Text) 分别调用 count_tokens。
func TokenCorpusBlocks() []TokenCorpusBlock {
	return []TokenCorpusBlock{
		{Name: "cjk", Text: tokCJKBlock},
		{Name: "code", Text: tokCodeBlock},
		{Name: "digit", Text: tokDigitBlock},
	}
}

// tokenDeltaTruth 是某个语料块在**真 Claude 分词器**下的期望增量。
// Low/High 是容差区间（含边界）。占位值偏宽，标定后收紧。
type tokenDeltaTruth struct {
	Low  int
	High int
}

// claudeTokenDeltaTruth：各差分块的真值增量区间。
//
// 已用基准 key（api.derouter.ai/proxy，Claude Max 反代）实测固化（Stage 0, 2026-06）。
// 实测 Δ：cjk=60, code=75, digit=56（base input_tokens=33239，注入 3.3w token 在做差时抵消，
// 验证了差分锚点不受注入污染）。区间在实测值上下留 ±6 容差，吸收 count_tokens 抖动。
// Claude 分词器同源，Δ 与具体型号无关；非 Claude 后端（GPT/Gemini/廉价模型）画像会偏离。
var claudeTokenDeltaTruth = map[string]tokenDeltaTruth{
	"cjk":   {Low: 54, High: 66}, // 实测 60（CJK+日文混合）
	"code":  {Low: 69, High: 81}, // 实测 75（源代码）
	"digit": {Low: 50, High: 62}, // 实测 56（长数字串）
}

// TokenDeltaVerdict 是一次差分比对的结论。
type TokenDeltaVerdict struct {
	Block    string
	Observed int
	Low      int
	High     int
	InRange  bool
	HasTruth bool // 该块是否有真值区间可比
}

// ClassifyTokenDelta 比对单个块的实测增量与 Claude 真值区间。
// 未标定（baselineCalibrated=false）时 InRange 仍照常计算，但调用方
// 应只将其作为弱提示。
func ClassifyTokenDelta(block string, observed int) TokenDeltaVerdict {
	t, ok := claudeTokenDeltaTruth[block]
	v := TokenDeltaVerdict{Block: block, Observed: observed, HasTruth: ok}
	if !ok {
		return v
	}
	v.Low, v.High = t.Low, t.High
	v.InRange = observed >= t.Low && observed <= t.High
	return v
}

// BaselineCalibrated 暴露标定状态给 controller 层决定证据强弱。
func BaselineCalibrated() bool { return baselineCalibrated }

// ----------------------------------------------------------------------------
// 吞吐画像 —— 抓「按 opus 收钱、实际走 haiku」的降级。
// ----------------------------------------------------------------------------
//
// 不同档位模型的生成速度差异大（haiku 远快于 opus）。对宣称模型实测
// output tokens/sec 与 TTFB，落档不符即降级嫌疑。吞吐受网络/负载影响有
// 噪声，故仅作**旗标**，区间留足 margin。

// ThroughputTier 是一个速度档位的画像。
type ThroughputTier struct {
	Tier         string // "opus" | "sonnet" | "haiku"
	MinTokPerSec float64
	MaxTokPerSec float64
}

// claudeThroughputTiers：各档位 output tokens/sec 区间。
//
// Stage 0 单次实测（基准 key, 2026-06）：sonnet≈129, opus≈147, haiku≈385 tok/s。
// 重要：opus 与 sonnet 吞吐高度重叠（147 vs 129），无法靠速度区分；可靠分界只有
// 「haiku 远快于 opus/sonnet」。故区间故意做成：opus/sonnet 共享一个低速带、haiku 一个高速带，
// 各自留足 margin（吞吐受网络/负载/反代缓冲影响，单样本不足以收紧）。
// 当前仅能可靠抓「宣称 opus/sonnet 实测却是 haiku 级飞快」这一类降级。
// !!! 这是单样本保守区间；多跑几轮取分布后可收紧。详见 baselineCalibrated 注释。
var claudeThroughputTiers = []ThroughputTier{
	{Tier: "opus", MinTokPerSec: 40, MaxTokPerSec: 220},
	{Tier: "sonnet", MinTokPerSec: 40, MaxTokPerSec: 220},
	{Tier: "haiku", MinTokPerSec: 260, MaxTokPerSec: 600},
}

// tierOfModel 把模型名归到 opus/sonnet/haiku 档；未知返回 ""。
func tierOfModel(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return "opus"
	case strings.Contains(m, "sonnet"):
		return "sonnet"
	case strings.Contains(m, "haiku"):
		return "haiku"
	default:
		return ""
	}
}

// ThroughputVerdict 是吞吐比对结论。
type ThroughputVerdict struct {
	ClaimedModel   string
	ClaimedTier    string
	ObservedTokSec float64
	// FitTiers 列出实测吞吐落在哪些档位区间内（可能多个，因占位区间重叠）。
	FitTiers []string
	// DowngradeSuspect 为 true 表示宣称档位与实测吞吐不符，且实测明显落在
	// 更快的廉价档（如宣称 opus 实测落 haiku 且不落 opus）。
	DowngradeSuspect bool
	// Decisive 仅在已标定且信号明确时为 true；否则调用方只作弱提示。
	Decisive bool
}

// ClassifyThroughputTier 判定宣称模型的实测吞吐是否暴露降级。
// observedTokSec <= 0（没测到有效吞吐）时返回空结论。
func ClassifyThroughputTier(claimedModel string, observedTokSec float64) ThroughputVerdict {
	v := ThroughputVerdict{
		ClaimedModel:   claimedModel,
		ClaimedTier:    tierOfModel(claimedModel),
		ObservedTokSec: observedTokSec,
	}
	if observedTokSec <= 0 || v.ClaimedTier == "" {
		return v
	}
	claimedRange := ThroughputTier{}
	for _, t := range claudeThroughputTiers {
		if observedTokSec >= t.MinTokPerSec && observedTokSec <= t.MaxTokPerSec {
			v.FitTiers = append(v.FitTiers, t.Tier)
		}
		if t.Tier == v.ClaimedTier {
			claimedRange = t
		}
	}
	// 宣称档位区间外，且实测更快（超过宣称档上限）→ 降级嫌疑。
	fitsClaimed := observedTokSec >= claimedRange.MinTokPerSec && observedTokSec <= claimedRange.MaxTokPerSec
	if !fitsClaimed && observedTokSec > claimedRange.MaxTokPerSec {
		v.DowngradeSuspect = true
	}
	// 仅在标定后、且确实落在某个更廉价档时才下铁结论。
	v.Decisive = baselineCalibrated && v.DowngradeSuspect && len(v.FitTiers) > 0
	return v
}
