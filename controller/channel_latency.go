package controller

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/latencytest"

	"github.com/gin-gonic/gin"
)

type startLatencyTestRequest struct {
	KeyIndex     int                      `json:"key_index"`
	PromptPreset string                   `json:"prompt_preset"`
	PromptText   string                   `json:"prompt_text"`
	Breakpoints  []latencytest.Breakpoint `json:"breakpoints"`
	Concurrency  int                      `json:"concurrency"`
	ModelName    string                   `json:"model_name"`
}

// StartChannelLatencyTest streams the run as Server-Sent Events. The client
// POSTs JSON config; the server holds the connection open and writes
// `event: <type>` + `data: <json>` per Anthropic-style SSE convention.
func StartChannelLatencyTest(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	var req startLatencyTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if req.KeyIndex == 0 {
		// Treat the JSON default (0) as "auto" only when the caller did not
		// supply a key_index. We can't distinguish absent from explicit 0
		// here without a *int — accept the asymmetry: if the channel is
		// multi-key, an explicit 0 still resolves to key #0 because the
		// frontend sends -1 for "auto".
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 1
	}

	cfg := latencytest.Config{
		ChannelId:    id,
		KeyIndex:     req.KeyIndex,
		PromptPreset: req.PromptPreset,
		PromptText:   req.PromptText,
		Breakpoints:  req.Breakpoints,
		Concurrency:  req.Concurrency,
		ModelName:    req.ModelName,
	}

	events, err := latencytest.Run(c.Request.Context(), cfg)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}

	// SSE plumbing
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "streaming unsupported"})
		return
	}

	c.Stream(func(w io.Writer) bool {
		evt, ok := <-events
		if !ok {
			return false
		}
		payload, err := common.Marshal(evt)
		if err != nil {
			return true // skip malformed but keep stream alive
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, payload)
		flusher.Flush()
		return true
	})
}

// GetLatencyTestPresets returns the catalogue of preset prompts that the
// frontend uses to populate the prompt selector.
func GetLatencyTestPresets(c *gin.Context) {
	type lite struct {
		Id              string `json:"id"`
		Label           string `json:"label"`
		ApproxTokens    int    `json:"approx_tokens"`
		Description     string `json:"description"`
		TextLength      int    `json:"text_length"`
		BreakpointHints []int  `json:"breakpoint_hints"`
	}
	out := make([]lite, 0, len(latencytest.PromptPresets))
	for _, p := range latencytest.PromptPresets {
		out = append(out, lite{
			Id:              p.Id,
			Label:           p.Label,
			ApproxTokens:    p.ApproxTokens,
			Description:     p.Description,
			TextLength:      len(p.Text),
			BreakpointHints: p.BreakpointHints,
		})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": out})
}

// GetLatencyTestPresetText returns the full text of a single preset (the
// list endpoint only exposes length to keep that payload small).
func GetLatencyTestPresetText(c *gin.Context) {
	preset := latencytest.PresetById(c.Param("id"))
	if preset == nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "preset not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"id":              preset.Id,
		"label":           preset.Label,
		"text":            preset.Text,
		"breakpoint_hints": preset.BreakpointHints,
	}})
}

// ListChannelLatencyTestRuns returns the recent saved runs for a channel.
func ListChannelLatencyTestRuns(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	limit := 20
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	rows, err := model.ListChannelLatencyTestRuns(id, limit)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": rows})
}

// GetChannelLatencyTestRun returns a single run + its per-request rows.
func GetChannelLatencyTestRun(c *gin.Context) {
	runId, err := strconv.ParseInt(c.Param("run_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	run, err := model.GetChannelLatencyTestRun(runId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	rows, err := model.GetChannelLatencyTestRequests(runId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"run":      run,
		"requests": rows,
	}})
}
