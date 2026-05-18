// Package claude_code contains constants used to mimic the real Claude Code CLI
// client when forwarding requests upstream to Anthropic. These values are
// intentionally hardcoded — they must stay in sync with the latest Claude Code
// traffic to avoid being downgraded to third-party rate limits.
//
// Reference: ports the minimal header subset from sub2api
// (internal/pkg/claude/constants.go) — only the pieces needed for header
// injection. No body-rewriting helpers live here.
package claude_code

// CLICurrentVersion is the Claude Code CLI version we pretend to be. Keep this
// in lockstep with the User-Agent below.
const CLICurrentVersion = "2.1.92"

// anthropic-beta tokens that real Claude Code traffic typically advertises.
const (
	BetaOAuth               = "oauth-2025-04-20"
	BetaClaudeCode          = "claude-code-20250219"
	BetaInterleavedThinking = "interleaved-thinking-2025-05-14"
	BetaPromptCachingScope  = "prompt-caching-scope-2026-01-05"
	BetaEffort              = "effort-2025-11-24"
	BetaContextManagement   = "context-management-2025-06-27"
	BetaExtendedCacheTTL    = "extended-cache-ttl-2025-04-11"
)

// FullClaudeCodeMimicryBetas returns the ordered list of anthropic-beta tokens
// we inject when mimicking Claude Code. The order mirrors what the real CLI
// sends. Callers should merge this with any client-supplied anthropic-beta
// header so explicit client betas are not lost.
func FullClaudeCodeMimicryBetas() []string {
	return []string{
		BetaClaudeCode,
		BetaOAuth,
		BetaInterleavedThinking,
		BetaPromptCachingScope,
		BetaEffort,
		BetaContextManagement,
		BetaExtendedCacheTTL,
	}
}

// DefaultHeaders is the fingerprint of headers the real Claude Code CLI sends
// on every request. We inject these whenever a token has the
// claude_code_header flag enabled and the outgoing model is claude-*.
//
// User-Agent version MUST match CLICurrentVersion exactly — Anthropic uses the
// full set of headers as one fingerprint when deciding whether a request is
// "real" Claude Code traffic.
var DefaultHeaders = map[string]string{
	"User-Agent":                                "claude-cli/" + CLICurrentVersion + " (external, cli)",
	"X-Stainless-Lang":                          "js",
	"X-Stainless-Package-Version":               "0.70.0",
	"X-Stainless-OS":                            "Linux",
	"X-Stainless-Arch":                          "arm64",
	"X-Stainless-Runtime":                       "node",
	"X-Stainless-Runtime-Version":               "v24.13.0",
	"X-Stainless-Retry-Count":                   "0",
	"X-Stainless-Timeout":                       "600",
	"X-App":                                     "cli",
	"Anthropic-Dangerous-Direct-Browser-Access": "true",
}
