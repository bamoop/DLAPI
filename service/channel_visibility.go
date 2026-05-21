package service

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

const rootOnlyMetaKey = "root_only"

func IsRootOnlyChannel(channel *model.Channel) bool {
	if channel == nil {
		return false
	}
	otherInfo := channel.GetOtherInfo()
	value, ok := otherInfo[rootOnlyMetaKey]
	if !ok {
		return false
	}
	boolValue, ok := value.(bool)
	return ok && boolValue
}

func CanViewChannelSensitive(role int, channel *model.Channel) bool {
	if role >= common.RoleRootUser {
		return true
	}
	return !IsRootOnlyChannel(channel)
}

func CanEditChannel(role int, channel *model.Channel) bool {
	return CanViewChannelSensitive(role, channel)
}

func SanitizeChannelForRole(role int, channel *model.Channel) *model.Channel {
	if channel == nil {
		return nil
	}
	cloned := *channel
	cloned.Key = ""
	if CanViewChannelSensitive(role, channel) {
		return &cloned
	}

	cloned.BaseURL = nil
	cloned.Setting = nil
	cloned.ParamOverride = nil
	cloned.HeaderOverride = nil
	cloned.Other = ""
	cloned.OtherSettings = ""
	cloned.OtherInfo = `{"root_only":true}`
	return &cloned
}

func SanitizeChannelListForRole(role int, channels []*model.Channel) []*model.Channel {
	if len(channels) == 0 {
		return channels
	}
	sanitized := make([]*model.Channel, 0, len(channels))
	for _, channel := range channels {
		sanitized = append(sanitized, SanitizeChannelForRole(role, channel))
	}
	return sanitized
}

func NormalizeChannelOtherInfoForRole(role int, channel *model.Channel) {
	if channel == nil {
		return
	}
	otherInfo := channel.GetOtherInfo()
	if role >= common.RoleRootUser {
		if len(otherInfo) == 0 && strings.TrimSpace(channel.OtherInfo) == "" {
			channel.OtherInfo = ""
		}
		return
	}
	delete(otherInfo, rootOnlyMetaKey)
	if len(otherInfo) == 0 {
		channel.OtherInfo = ""
		return
	}
	channel.SetOtherInfo(otherInfo)
}
