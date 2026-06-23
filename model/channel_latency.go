package model

// ChannelLatencyTestRun captures one invocation of the Claude latency/RPM
// test panel: configuration + aggregated metrics. Individual per-request
// rows live in ChannelLatencyTestRequest.
type ChannelLatencyTestRun struct {
	Id        int64 `json:"id" gorm:"primaryKey"`
	ChannelId int   `json:"channel_id" gorm:"index"`
	KeyIndex  int   `json:"key_index"`
	KeyHint   string `json:"key_hint" gorm:"type:varchar(64)"`

	// Configuration snapshot
	PromptPresetId   string `json:"prompt_preset_id" gorm:"type:varchar(32)"` // "short"|"medium"|"long"|"custom"
	PromptText       string `json:"prompt_text" gorm:"type:text"`
	CacheBreakpoints string `json:"cache_breakpoints" gorm:"type:text"`       // JSON array of [{position:int}]
	Concurrency      int    `json:"concurrency"`
	ModelName        string `json:"model_name" gorm:"type:varchar(128)"`

	// Aggregated metrics (populated when the run finishes)
	StartedAt          int64 `json:"started_at"`
	FinishedAt         int64 `json:"finished_at"`
	TotalRequests      int   `json:"total_requests"`
	SuccessRequests    int   `json:"success_requests"`
	FailedRequests     int   `json:"failed_requests"`
	EffectiveRPM       int   `json:"effective_rpm"`        // success count up until the 30% sliding-window failure threshold
	AvgLatencyMs       int   `json:"avg_latency_ms"`
	P50LatencyMs       int   `json:"p50_latency_ms"`
	P95LatencyMs       int   `json:"p95_latency_ms"`
	MaxLatencyMs       int   `json:"max_latency_ms"`
	CacheHitCount      int   `json:"cache_hit_count"`
	CacheCreationCount int   `json:"cache_creation_count"`

	// Fingerprint snapshot for this run (mirrors UpstreamFingerprint).
	// We embed denormalised because the run is meant to be a frozen
	// record — the channel's live fingerprint may change later.
	FingerprintComposite string `json:"fingerprint_composite" gorm:"type:varchar(80);index"`
	FingerprintHeaders   string `json:"fingerprint_headers" gorm:"type:varchar(80)"`
	FingerprintErrors    string `json:"fingerprint_errors" gorm:"type:varchar(80)"`
	FingerprintModels    string `json:"fingerprint_models" gorm:"type:varchar(80)"`

	Status       string `json:"status" gorm:"type:varchar(16)"` // "running" | "done" | "aborted" | "failed"
	ErrorMessage string `json:"error_message,omitempty" gorm:"type:text"`
}

// ChannelLatencyTestRequest is one HTTP attempt inside a Run.
type ChannelLatencyTestRequest struct {
	Id        int64 `json:"id" gorm:"primaryKey"`
	RunId     int64 `json:"run_id" gorm:"index"`
	Sequence  int   `json:"sequence"`         // 0-based dispatch index
	StartedAt int64 `json:"started_at"`       // unix ms
	EndedAt   int64 `json:"ended_at"`         // unix ms

	LatencyMs    int  `json:"latency_ms"`
	StatusCode   int  `json:"status_code"`
	Success      bool `json:"success"`
	InputTokens  int  `json:"input_tokens"`
	OutputTokens int  `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens"`

	// Captured for the request/response detail view. Bodies are bounded
	// (see latencytest service) so the table doesn't balloon.
	RequestSnippet  string `json:"request_snippet" gorm:"type:text"`
	ResponseSnippet string `json:"response_snippet" gorm:"type:text"`
	ResponseHeaders string `json:"response_headers" gorm:"type:text"` // JSON object
	ErrorMessage    string `json:"error_message,omitempty" gorm:"type:text"`
}

const channelLatencyTestRunsMaxPerChannel = 50

// SaveChannelLatencyTestRun upserts the run and prunes oldest rows so each
// channel retains at most channelLatencyTestRunsMaxPerChannel runs.
func SaveChannelLatencyTestRun(run *ChannelLatencyTestRun) error {
	if err := DB.Save(run).Error; err != nil {
		return err
	}
	return pruneOldLatencyRuns(run.ChannelId)
}

func pruneOldLatencyRuns(channelId int) error {
	var ids []int64
	err := DB.Model(&ChannelLatencyTestRun{}).
		Where("channel_id = ?", channelId).
		Order("started_at DESC, id DESC").
		Offset(channelLatencyTestRunsMaxPerChannel).
		Limit(500).
		Pluck("id", &ids).Error
	if err != nil || len(ids) == 0 {
		return err
	}
	// Cascade-delete the children first to keep the schema portable
	// (no FK is declared; we do it manually for SQLite/MySQL/PostgreSQL).
	if err := DB.Where("run_id IN ?", ids).Delete(&ChannelLatencyTestRequest{}).Error; err != nil {
		return err
	}
	return DB.Where("id IN ?", ids).Delete(&ChannelLatencyTestRun{}).Error
}

func InsertChannelLatencyTestRequests(rows []*ChannelLatencyTestRequest) error {
	if len(rows) == 0 {
		return nil
	}
	return DB.CreateInBatches(rows, 100).Error
}

func ListChannelLatencyTestRuns(channelId int, limit int) ([]*ChannelLatencyTestRun, error) {
	if limit <= 0 || limit > channelLatencyTestRunsMaxPerChannel {
		limit = channelLatencyTestRunsMaxPerChannel
	}
	var rows []*ChannelLatencyTestRun
	err := DB.Where("channel_id = ?", channelId).
		Order("started_at DESC, id DESC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

func GetChannelLatencyTestRun(runId int64) (*ChannelLatencyTestRun, error) {
	var run ChannelLatencyTestRun
	err := DB.First(&run, runId).Error
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func GetChannelLatencyTestRequests(runId int64) ([]*ChannelLatencyTestRequest, error) {
	var rows []*ChannelLatencyTestRequest
	err := DB.Where("run_id = ?", runId).
		Order("sequence ASC").
		Find(&rows).Error
	return rows, err
}
