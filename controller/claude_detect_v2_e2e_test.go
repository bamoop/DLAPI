package controller

import (
	"bufio"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
)

// TestClaudeDetectV2E2E 驱动 v2 引擎跑真实基准 key，打印 ztest 式报告并做断言。
//
//	CLAUDE_DETECT_E2E_BASE=https://api.derouter.ai/proxy \
//	CLAUDE_DETECT_E2E_KEY=sk-ant-... \
//	CLAUDE_DETECT_E2E_MODEL=claude-opus-4-7 \
//	go test ./controller/ -run TestClaudeDetectV2E2E -v -count=1 -timeout=600s
func TestClaudeDetectV2E2E(t *testing.T) {
	base := os.Getenv("CLAUDE_DETECT_E2E_BASE")
	key := os.Getenv("CLAUDE_DETECT_E2E_KEY")
	if base == "" || key == "" {
		t.Skip("set CLAUDE_DETECT_E2E_BASE / CLAUDE_DETECT_E2E_KEY to run")
	}
	model := os.Getenv("CLAUDE_DETECT_E2E_MODEL")
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/detect", ClaudeDetectUpstreamKeyV2)

	body, _ := common.Marshal(map[string]any{"base_url": base, "key": key, "model_name": model})
	req := httptest.NewRequest(http.MethodPost, "/detect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}

	var summaryData string
	probeCount := 0
	sc := bufio.NewScanner(w.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	cur := ""
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event:") {
			cur = strings.TrimSpace(line[len("event:"):])
			if cur == "probe" {
				probeCount++
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			d := strings.TrimSpace(line[len("data:"):])
			if cur == "summary" {
				summaryData = d
			}
			if cur == "probe" {
				var pe struct {
					Payload struct {
						Code   string `json:"probe_code"`
						Status string `json:"status"`
						Score  int    `json:"score"`
					} `json:"payload"`
				}
				if common.UnmarshalJsonStr(d, &pe) == nil {
					t.Logf("probe %-4s %-8s %d", pe.Payload.Code, pe.Payload.Status, pe.Payload.Score)
				}
			}
		}
	}
	if summaryData == "" {
		t.Fatalf("no summary; probes=%d", probeCount)
	}

	var env struct {
		Payload struct {
			CompositeScore int    `json:"composite_score"`
			RiskLevel      string `json:"risk_level"`
			Verdict        struct {
				Label       string   `json:"label"`
				Headline    string   `json:"headline"`
				KeyFindings []string `json:"key_findings"`
			} `json:"verdict"`
			DimensionGroups []struct {
				Name         string `json:"name"`
				ScorePercent int    `json:"score_percent"`
			} `json:"dimension_groups"`
			RiskAlerts []struct {
				Severity    string `json:"severity"`
				Title       string `json:"title"`
				SourceProbe string `json:"source_probe"`
			} `json:"risk_alerts"`
		} `json:"payload"`
	}
	if err := common.UnmarshalJsonStr(summaryData, &env); err != nil {
		t.Fatalf("parse summary: %v\n%s", err, summaryData)
	}
	p := env.Payload
	t.Logf("=== composite=%d risk=%s verdict=%s", p.CompositeScore, p.RiskLevel, p.Verdict.Label)
	t.Logf("headline: %s", p.Verdict.Headline)
	for _, g := range p.DimensionGroups {
		t.Logf("  [%s] %d%%", g.Name, g.ScorePercent)
	}
	for _, a := range p.RiskAlerts {
		t.Logf("  ALERT[%s] %s (%s)", a.Severity, a.Title, a.SourceProbe)
	}
	for _, kf := range p.Verdict.KeyFindings {
		t.Logf("  finding: %s", kf)
	}
	if probeCount == 0 {
		t.Error("no probes ran")
	}
}
