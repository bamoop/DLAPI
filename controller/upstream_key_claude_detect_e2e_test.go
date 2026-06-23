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

// TestClaudeDetectE2E 用真实基准 key 驱动完整 15 探针，验证双轴结论。
//
// 需要网络 + 基准 key，默认 skip。运行：
//
//	CLAUDE_DETECT_E2E_BASE=https://api.derouter.ai/proxy \
//	CLAUDE_DETECT_E2E_KEY=sk-ant-... \
//	go test ./controller/ -run TestClaudeDetectE2E -v -count=1 -timeout=300s
func TestClaudeDetectE2E(t *testing.T) {
	base := os.Getenv("CLAUDE_DETECT_E2E_BASE")
	key := os.Getenv("CLAUDE_DETECT_E2E_KEY")
	if base == "" || key == "" {
		t.Skip("set CLAUDE_DETECT_E2E_BASE / CLAUDE_DETECT_E2E_KEY to run")
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/detect", ClaudeDetectUpstreamKey)

	body, _ := common.Marshal(map[string]any{
		"base_url":   base,
		"key":        key,
		"model_name": "claude-sonnet-4-6",
	})
	req := httptest.NewRequest(http.MethodPost, "/detect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}

	// 解析 SSE：抓出 summary 事件的 data 行。
	var summaryData string
	probeCount := 0
	sc := bufio.NewScanner(w.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	curEvent := ""
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event:") {
			curEvent = strings.TrimSpace(line[len("event:"):])
			if curEvent == "probe" {
				probeCount++
			}
			continue
		}
		if strings.HasPrefix(line, "data:") && curEvent == "summary" {
			summaryData = strings.TrimSpace(line[len("data:"):])
		}
	}
	if summaryData == "" {
		t.Fatalf("no summary event; probes seen=%d; raw head:\n%s", probeCount, truncate(w.Body.String(), 2000))
	}

	var env struct {
		Payload struct {
			BackendLabel      string `json:"backend_label"`
			BackendColor      string `json:"backend_color"`
			IntegrityLabel    string `json:"integrity_label"`
			IntegrityColor    string `json:"integrity_color"`
			Channel           string `json:"channel"`
			Score             int    `json:"score"`
			DowngradeSuspect  bool   `json:"downgrade_suspect"`
			BaselineCalibrated bool  `json:"baseline_calibrated"`
		} `json:"payload"`
	}
	if err := common.UnmarshalJsonStr(summaryData, &env); err != nil {
		t.Fatalf("parse summary: %v\ndata=%s", err, summaryData)
	}
	p := env.Payload

	t.Logf("probes=%d score=%d", probeCount, p.Score)
	t.Logf("后端: %s [%s]", p.BackendLabel, p.BackendColor)
	t.Logf("链路: %s [%s]", p.IntegrityLabel, p.IntegrityColor)
	t.Logf("渠道: %s | 降级=%v | 已标定=%v", p.Channel, p.DowngradeSuspect, p.BaselineCalibrated)

	// 回归断言：基准 key 是「中转 · 后端真 Claude」。
	//  - 后端轴不能判红（它确实是真 Claude 后端）。
	//  - 链路轴必须显示中转（base host 非 api.anthropic.com）。
	if p.BackendColor == "red" {
		t.Errorf("后端轴误判为红（基准 key 后端是真 Claude）：%s", p.BackendLabel)
	}
	if !strings.Contains(p.IntegrityLabel, "中转") {
		t.Errorf("链路轴应识别为中转，实际：%s", p.IntegrityLabel)
	}
	if !strings.Contains(p.Channel, "中转") {
		t.Errorf("渠道应含「中转」，实际：%s", p.Channel)
	}
}
