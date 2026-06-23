package model

import (
	"github.com/QuantumNous/new-api/common"
)

// ChannelFingerprintHistory captures one manual fingerprint probe per row.
// Used by the admin UI to show how an upstream's fingerprint has evolved
// over time, and to allow comparison across keys within a multi-key channel.
type ChannelFingerprintHistory struct {
	Id        int64  `json:"id" gorm:"primaryKey"`
	ChannelId int    `json:"channel_id" gorm:"index"`
	KeyIndex  int    `json:"key_index"`                                  // multi-key channels: which key was used; 0 for single-key
	KeyHint   string `json:"key_hint" gorm:"type:varchar(64)"`           // short masked label (e.g. "sk-...abcd") for display
	Source    string `json:"source" gorm:"type:varchar(16);index"`       // "manual" | "auto"
	ProbedAt  int64  `json:"probed_at" gorm:"index"`
	DurationMs       int    `json:"duration_ms"`
	HeaderSetHash    string `json:"header_set_hash" gorm:"type:varchar(80)"`
	ErrorShapeHash   string `json:"error_shape_hash" gorm:"type:varchar(80)"`
	ModelSetHash     string `json:"model_set_hash" gorm:"type:varchar(80)"`
	CompositeHash    string `json:"composite_hash" gorm:"type:varchar(80);index"`
	ProbeVersion     int    `json:"probe_version"`
	ErrorMessage     string `json:"error_message,omitempty" gorm:"type:text"`
}

const channelFingerprintHistoryMaxRows = 200

func InsertChannelFingerprintHistory(record *ChannelFingerprintHistory) error {
	if err := DB.Create(record).Error; err != nil {
		return err
	}
	return pruneChannelFingerprintHistory(record.ChannelId)
}

// pruneChannelFingerprintHistory keeps only the most recent
// channelFingerprintHistoryMaxRows rows per channel. SQLite/MySQL/PostgreSQL
// all support the subquery form used here.
func pruneChannelFingerprintHistory(channelId int) error {
	var ids []int64
	err := DB.Model(&ChannelFingerprintHistory{}).
		Where("channel_id = ?", channelId).
		Order("probed_at DESC, id DESC").
		Offset(channelFingerprintHistoryMaxRows).
		Limit(1000).
		Pluck("id", &ids).Error
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if err := DB.Where("id IN ?", ids).Delete(&ChannelFingerprintHistory{}).Error; err != nil {
		common.SysError("failed to prune channel fingerprint history: " + err.Error())
		return err
	}
	return nil
}

func ListChannelFingerprintHistory(channelId int, limit int) ([]*ChannelFingerprintHistory, error) {
	if limit <= 0 || limit > channelFingerprintHistoryMaxRows {
		limit = channelFingerprintHistoryMaxRows
	}
	var rows []*ChannelFingerprintHistory
	err := DB.Where("channel_id = ?", channelId).
		Order("probed_at DESC, id DESC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}
