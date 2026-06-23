package latencytest

import (
	"strings"
)

// PromptPreset describes a reusable test prompt with optional cache breakpoints.
type PromptPreset struct {
	Id            string `json:"id"`
	Label         string `json:"label"`
	ApproxTokens  int    `json:"approx_tokens"`
	Description   string `json:"description"`
	Text          string `json:"text"`
	BreakpointHints []int `json:"breakpoint_hints"` // suggested character offsets where breakpoints may go
}

// shortFiller / longFiller are deterministic blocks of plain English text. We
// keep them in code so the output is reproducible (same length, same content)
// across deploys — important when comparing cache hit rates between runs.
const sentenceTemplate = "The following is a stable test passage used to evaluate upstream cache behaviour. Each sentence repeats deterministically so that the prompt hash remains constant across runs and only intentional breakpoint markers change the cacheable prefix. "

func buildPrompt(targetSentences int) string {
	var b strings.Builder
	b.Grow(targetSentences * len(sentenceTemplate))
	for i := 0; i < targetSentences; i++ {
		b.WriteString(sentenceTemplate)
	}
	return b.String()
}

// PromptPresets is the static catalogue exposed to the admin UI.
//
// Per Anthropic's docs the minimum cacheable prefix is model-dependent:
//
//	Claude Sonnet 4.x / Opus 4.1   — 1,024 tokens
//	Claude Haiku 3.5               — 2,048 tokens
//	Claude Opus 4.5+ / Haiku 4.5+  — 4,096 tokens
//
// The sentence template below is 213 characters; English averages ~4
// chars/token, so each sentence ≈ 53 tokens.
//
//	sonnet  ~30  sentences ≈ 1,600 tokens  (clears the 1,024 threshold)
//	haiku35 ~45  sentences ≈ 2,400 tokens  (clears the 2,048 threshold)
//	opus45  ~85  sentences ≈ 4,500 tokens  (clears the 4,096 threshold)
//	xl      ~150 sentences ≈ 8,000 tokens  (any model + headroom for measuring)
var PromptPresets = []PromptPreset{
	{
		Id:           "sonnet",
		Label:        "Sonnet (≈1.6K tokens)",
		ApproxTokens: 1600,
		Description:  "Just over the 1,024-token threshold required by Sonnet 4.x. Will NOT activate caching on Opus 4.5+ / Haiku 4.5.",
		Text:         buildPrompt(30),
		BreakpointHints: []int{
			28 * len(sentenceTemplate),
		},
	},
	{
		Id:           "haiku35",
		Label:        "Haiku 3.5 (≈2.4K tokens)",
		ApproxTokens: 2400,
		Description:  "Above the 2,048-token threshold required by Haiku 3.5. Also works for Sonnet 4.x.",
		Text:         buildPrompt(45),
		BreakpointHints: []int{
			43 * len(sentenceTemplate),
		},
	},
	{
		Id:           "opus45",
		Label:        "Opus 4.5+ / Haiku 4.5 (≈4.5K tokens)",
		ApproxTokens: 4500,
		Description:  "Above the 4,096-token threshold required by Opus 4.5+ and Haiku 4.5. Works for every current model.",
		Text:         buildPrompt(85),
		BreakpointHints: []int{
			83 * len(sentenceTemplate),
		},
	},
	{
		Id:           "xl",
		Label:        "XL (≈8K tokens)",
		ApproxTokens: 8000,
		Description:  "Large prompt — useful for measuring sustained-throughput cache benefit; works on any model.",
		Text:         buildPrompt(150),
		BreakpointHints: []int{
			148 * len(sentenceTemplate),
		},
	},
}

// PresetById returns the preset by id, or nil if unknown.
func PresetById(id string) *PromptPreset {
	for i := range PromptPresets {
		if PromptPresets[i].Id == id {
			return &PromptPresets[i]
		}
	}
	return nil
}
