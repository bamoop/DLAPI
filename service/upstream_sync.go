package service

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

type GroupTokenInfo struct {
	TokenId   int    `json:"token_id"`
	TokenName string `json:"token_name"`
	Key       string `json:"key"`
	Status    int    `json:"status"`
	Group     string `json:"group"`
}

// normalizeKey makes sure an API key returned by an upstream newapi-style
// list endpoint is in the form that downstream consumers actually use:
// prefixed with "sk-". newAPI's list endpoint stores the raw base64 portion
// only; clients send "sk-<raw>" in the Authorization header. We normalize
// here so the value persisted to DB is directly usable for display, copy,
// and testing. Masked values (those containing "*") are left as-is.
func normalizeKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	if strings.Contains(raw, "*") {
		return raw
	}
	if strings.HasPrefix(raw, "sk-") {
		return raw
	}
	return "sk-" + raw
}

type SyncResult struct {
	Balance  string                       `json:"balance"`
	Groups   map[string]UpstreamGroupInfo `json:"groups"`
	Tokens   map[string]GroupTokenInfo    `json:"tokens"`
	UserInfo *UpstreamUserInfo            `json:"user_info"`
}

func SyncUpstreamSite(site *model.UpstreamSite) (*SyncResult, error) {
	password, err := common.DecryptAES(site.EncryptedPwd)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt password: %w", err)
	}

	client := NewUpstreamClient(site.BaseURL, site.SiteType)

	_, err = client.Login(site.Username, password)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}

	userInfo, err := client.GetUserSelf()
	if err != nil {
		return nil, fmt.Errorf("get user info failed: %w", err)
	}

	groups, err := client.GetGroups()
	if err != nil {
		return nil, fmt.Errorf("get groups failed: %w", err)
	}

	existingTokens, err := client.ListTokens()
	if err != nil {
		return nil, fmt.Errorf("list tokens failed: %w", err)
	}

	tokensByGroup := make(map[string]*UpstreamToken)
	for i := range existingTokens {
		t := &existingTokens[i]
		if t.Status == 1 {
			if _, exists := tokensByGroup[t.Group]; !exists {
				tokensByGroup[t.Group] = t
			}
		}
	}

	groupTokens := make(map[string]GroupTokenInfo)

	for groupName := range groups {
		if groupName == "auto" {
			continue
		}
		existing, hasToken := tokensByGroup[groupName]
		if !hasToken {
			newToken, createErr := client.CreateToken(groupName, groupName)
			if createErr != nil {
				groupTokens[groupName] = GroupTokenInfo{
					Group:  groupName,
					Status: -1,
				}
				continue
			}
			existing = newToken
		}

		key := existing.Key
		if site.SiteType != "sub2api" && (key == "" || strings.Contains(key, "*")) {
			fetchedKey, keyErr := client.GetTokenKey(existing.Id)
			if keyErr == nil {
				key = fetchedKey
			}
		}
		if site.SiteType != "sub2api" {
			key = normalizeKey(key)
		}

		groupTokens[groupName] = GroupTokenInfo{
			TokenId:   existing.Id,
			TokenName: existing.Name,
			Key:       key,
			Status:    existing.Status,
			Group:     groupName,
		}
	}

	balanceStr := userInfo.Balance
	if balanceStr == "" {
		balanceStr = fmt.Sprintf("%.2f", float64(userInfo.Quota)/500000.0)
	}

	groupsJSON, _ := common.Marshal(groups)
	tokensJSON, _ := common.Marshal(groupTokens)

	updates := map[string]any{
		"balance":       balanceStr,
		"cached_groups": string(groupsJSON),
		"cached_tokens": string(tokensJSON),
	}
	site.UpdateSyncResult(nil, updates)

	return &SyncResult{
		Balance:  balanceStr,
		Groups:   groups,
		Tokens:   groupTokens,
		UserInfo: userInfo,
	}, nil
}

func TestUpstreamLogin(baseURL, username, password, siteType string) (*UpstreamUserInfo, error) {
	client := NewUpstreamClient(baseURL, siteType)
	_, err := client.Login(username, password)
	if err != nil {
		return nil, err
	}
	return client.GetUserSelf()
}

func CreateGroupTokenForSite(site *model.UpstreamSite, groupName string) (*GroupTokenInfo, error) {
	password, err := common.DecryptAES(site.EncryptedPwd)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt password: %w", err)
	}

	client := NewUpstreamClient(site.BaseURL, site.SiteType)
	_, err = client.Login(site.Username, password)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}

	newToken, err := client.CreateToken(groupName, groupName)
	if err != nil {
		return nil, fmt.Errorf("create token failed: %w", err)
	}

	key := newToken.Key
	if site.SiteType != "sub2api" && (key == "" || strings.Contains(key, "*")) {
		fetchedKey, keyErr := client.GetTokenKey(newToken.Id)
		if keyErr == nil {
			key = fetchedKey
		}
	}
	if site.SiteType != "sub2api" {
		key = normalizeKey(key)
	}

	info := &GroupTokenInfo{
		TokenId:   newToken.Id,
		TokenName: newToken.Name,
		Key:       key,
		Status:    newToken.Status,
		Group:     groupName,
	}

	var cachedTokens map[string]GroupTokenInfo
	if site.CachedTokens != "" {
		_ = common.UnmarshalJsonStr(site.CachedTokens, &cachedTokens)
	}
	if cachedTokens == nil {
		cachedTokens = make(map[string]GroupTokenInfo)
	}
	cachedTokens[groupName] = *info
	tokensJSON, _ := common.Marshal(cachedTokens)
	_ = model.UpdateUpstreamSiteSyncData(site.Id, map[string]any{
		"cached_tokens": string(tokensJSON),
	})

	return info, nil
}
