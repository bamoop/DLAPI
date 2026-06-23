//go:build ignore

// Stage 0 标定工具 —— 用基准 key 实测 tokenizer 差分增量 Δ 与各模型吞吐档位，
// 输出可直接回填进 service/fingerprint/baseline.go 的真值。
//
// 复用 baseline.go 里导出的 *同一组* 语料常量（TokenCorpusBase/TokenCorpusBlocks），
// 保证标定出的 Δ 与运行时 P3 探针计算的 Δ 完全一致。
//
// 用法：
//
//	go run scripts/claude_baseline_calibrate.go \
//	    -base https://api.derouter.ai/proxy \
//	    -key  sk-ant-xxxxxxxx \
//	    -models claude-sonnet-4-6,claude-opus-4-7,claude-haiku-4-5-20251001
//
// -models 用于吞吐档位标定；只填手头 key 支持的型号即可（缺的档位用经验值留宽）。
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/service/fingerprint"
)

func authHeaders(key string) map[string]string {
	return map[string]string{
		"x-api-key":         key,
		"anthropic-version": "2023-06-01",
		"Content-Type":      "application/json",
	}
}

func countTokensOnce(ctx context.Context, base, key, model, content string) (int, error) {
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]any{{"role": "user", "content": content}},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/messages/count_tokens", bytes.NewReader(body))
	for k, v := range authHeaders(key) {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return -1, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(rb))
	}
	var ct struct {
		InputTokens int `json:"input_tokens"`
	}
	_ = json.Unmarshal(rb, &ct)
	return ct.InputTokens, nil
}

// countTokens 带重试 —— derouter 这类 Max 反代会间歇性 504/auth 抖动。
func countTokens(ctx context.Context, base, key, model, content string) (int, error) {
	var lastErr error
	for attempt := 1; attempt <= 5; attempt++ {
		n, err := countTokensOnce(ctx, base, key, model, content)
		if err == nil {
			return n, nil
		}
		lastErr = err
		fmt.Printf("    (重试 %d/5: %v)\n", attempt, err)
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}
	return -1, lastErr
}

func measureThroughput(ctx context.Context, base, key, model string) (ttfbMs int, tokPerSec float64, err error) {
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 512,
		"stream":     true,
		"messages":   []map[string]any{{"role": "user", "content": "Count from 1 to 200, one number per line, no commentary."}},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/messages", bytes.NewReader(body))
	for k, v := range authHeaders(key) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")
	start := time.Now()
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(rb))
	}
	var ttfb time.Duration
	gotFirst := false
	genStart := time.Now()
	outputTokens := 0
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var et string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event:") {
			et = strings.TrimSpace(line[6:])
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		d := strings.TrimSpace(line[5:])
		if d == "" || d == "[DONE]" {
			continue
		}
		switch et {
		case "content_block_delta":
			if !gotFirst {
				ttfb = time.Since(start)
				genStart = time.Now()
				gotFirst = true
			}
		case "message_delta":
			var p struct {
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(d), &p) == nil && p.Usage.OutputTokens > 0 {
				outputTokens = p.Usage.OutputTokens
			}
		}
	}
	elapsed := time.Since(genStart)
	if outputTokens > 0 && elapsed > 0 {
		tokPerSec = float64(outputTokens) / elapsed.Seconds()
	}
	return int(ttfb / time.Millisecond), tokPerSec, nil
}

func main() {
	base := flag.String("base", "", "上游 base url，如 https://api.derouter.ai/proxy")
	key := flag.String("key", "", "Anthropic key")
	models := flag.String("models", "claude-sonnet-4-6", "逗号分隔的模型，用于吞吐档位标定")
	flag.Parse()
	if *base == "" || *key == "" {
		fmt.Println("用法: go run scripts/claude_baseline_calibrate.go -base <url> -key <key> [-models a,b,c]")
		return
	}
	ctx := context.Background()

	fmt.Println("==== tokenizer 差分增量 Δ（回填 claudeTokenDeltaTruth）====")
	// 用第一个模型做 count_tokens（分词器与具体型号无关，取一个可用型号即可）。
	tokModel := strings.Split(*models, ",")[0]
	baseTok, err := countTokens(ctx, *base, *key, tokModel, fingerprint.TokenCorpusBase())
	if err != nil {
		fmt.Printf("base 计数失败: %v\n", err)
		return
	}
	fmt.Printf("base input_tokens = %d (model=%s)\n", baseTok, tokModel)
	for _, blk := range fingerprint.TokenCorpusBlocks() {
		full, ferr := countTokens(ctx, *base, *key, tokModel, fingerprint.TokenCorpusBase()+blk.Text)
		if ferr != nil {
			fmt.Printf("  %-6s 失败: %v\n", blk.Name, ferr)
			continue
		}
		fmt.Printf("  %-6s Δ = %d  ->  建议 {Low: %d, High: %d}\n", blk.Name, full-baseTok, full-baseTok-3, full-baseTok+3)
	}

	fmt.Println("\n==== 各模型吞吐（回填 claudeThroughputTiers）====")
	for _, m := range strings.Split(*models, ",") {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		ttfb, tps, terr := measureThroughput(ctx, *base, *key, m)
		if terr != nil {
			fmt.Printf("  %-30s 失败: %v\n", m, terr)
			continue
		}
		fmt.Printf("  %-30s TTFB=%dms  tok/s=%.1f\n", m, ttfb, tps)
	}
	fmt.Println("\n提示：每个模型多跑几次取区间；填入 baseline.go 后把 baselineCalibrated 改为 true。")
}
