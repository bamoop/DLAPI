package service

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

type UpstreamClient struct {
	BaseURL              string
	SiteType             string // "newapi" or "sub2api"
	httpClient           *http.Client
	token                string // Bearer token for sub2api
	userId               int    // user id from login
	sub2apiGroupIdToName map[int]string // sub2api: group id -> group name
	extraHeaders         map[string]string
}

type UpstreamUserInfo struct {
	Id          int    `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        int    `json:"role"`
	Status      int    `json:"status"`
	Group       string `json:"group"`
	Quota       int64  `json:"quota"`
	UsedQuota   int64  `json:"used_quota"`
	Balance     string `json:"balance"`
}

type UpstreamGroupInfo struct {
	Ratio any    `json:"ratio"`
	Desc  string `json:"desc"`
}

type UpstreamToken struct {
	Id             int    `json:"id"`
	Name           string `json:"name"`
	Key            string `json:"key"`
	Status         int    `json:"status"`
	Group          string `json:"group"`
	RemainQuota    int64  `json:"remain_quota"`
	UsedQuota      int64  `json:"used_quota"`
	UnlimitedQuota bool   `json:"unlimited_quota"`
}

type UpstreamApiResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func NewUpstreamClient(baseURL, siteType string) *UpstreamClient {
	jar, _ := cookiejar.New(nil)
	baseURL = strings.TrimRight(baseURL, "/")
	return &UpstreamClient{
		BaseURL:  baseURL,
		SiteType: siteType,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
		},
	}
}

func (c *UpstreamClient) doRequest(method, path string, body []byte) ([]byte, error) {
	reqURL := c.BaseURL + path
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequest(method, reqURL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(method, reqURL, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range c.extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Try to extract error message from response body
		var errResp struct {
			Message string `json:"message"`
		}
		if common.Unmarshal(respBody, &errResp) == nil && errResp.Message != "" {
			return respBody, fmt.Errorf("%s", errResp.Message)
		}
		return respBody, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return respBody, nil
}

// Login authenticates with the upstream site. Adapts to site type automatically.
func (c *UpstreamClient) Login(username, password string) (*UpstreamUserInfo, error) {
	if c.SiteType == "sub2api" {
		return c.loginSub2API(username, password)
	}
	return c.loginNewAPI(username, password)
}

func (c *UpstreamClient) loginNewAPI(username, password string) (*UpstreamUserInfo, error) {
	loginReq := map[string]string{
		"username": username,
		"password": password,
	}
	body, err := common.Marshal(loginReq)
	if err != nil {
		return nil, err
	}

	respBody, err := c.doRequest("POST", "/api/user/login", body)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}

	var resp struct {
		UpstreamApiResponse
		Data UpstreamUserInfo `json:"data"`
	}
	if err := common.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse login response: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("login failed: %s", resp.Message)
	}

	u, _ := url.Parse(c.BaseURL)
	if len(c.httpClient.Jar.Cookies(u)) == 0 {
		return nil, fmt.Errorf("login succeeded but no session cookies received")
	}

	c.userId = resp.Data.Id
	c.extraHeaders = map[string]string{
		"New-Api-User": fmt.Sprintf("%d", resp.Data.Id),
	}
	return &resp.Data, nil
}

func (c *UpstreamClient) loginSub2API(email, password string) (*UpstreamUserInfo, error) {
	loginReq := map[string]string{
		"email":    email,
		"password": password,
	}
	body, err := common.Marshal(loginReq)
	if err != nil {
		return nil, err
	}

	respBody, err := c.doRequest("POST", "/api/v1/auth/login", body)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}

	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
			User         struct {
				Id       int     `json:"id"`
				Email    string  `json:"email"`
				Username string  `json:"username"`
				Role     string  `json:"role"`
				Balance  float64 `json:"balance"`
				Status   string  `json:"status"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := common.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse login response: %w", err)
	}
	if resp.Data.AccessToken == "" {
		if resp.Message != "" && resp.Message != "success" {
			return nil, fmt.Errorf("login failed: %s", resp.Message)
		}
		return nil, fmt.Errorf("login failed: no access token received")
	}

	c.token = resp.Data.AccessToken
	c.userId = resp.Data.User.Id

	info := &UpstreamUserInfo{
		Id:       resp.Data.User.Id,
		Email:    resp.Data.User.Email,
		Username: resp.Data.User.Username,
		Balance:  fmt.Sprintf("%.2f", resp.Data.User.Balance),
	}
	if info.Username == "" {
		info.Username = info.Email
	}
	switch resp.Data.User.Role {
	case "admin":
		info.Role = 100
	default:
		info.Role = 1
	}
	if resp.Data.User.Status == "active" {
		info.Status = 1
	}

	return info, nil
}

// GetUserSelf fetches the current user's profile info.
func (c *UpstreamClient) GetUserSelf() (*UpstreamUserInfo, error) {
	if c.SiteType == "sub2api" {
		return c.getUserSelfSub2API()
	}
	return c.getUserSelfNewAPI()
}

func (c *UpstreamClient) getUserSelfNewAPI() (*UpstreamUserInfo, error) {
	respBody, err := c.doRequest("GET", "/api/user/self", nil)
	if err != nil {
		return nil, fmt.Errorf("get user self failed: %w", err)
	}

	var resp struct {
		UpstreamApiResponse
		Data UpstreamUserInfo `json:"data"`
	}
	if err := common.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse user self response: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("get user self failed: %s", resp.Message)
	}
	c.userId = resp.Data.Id
	return &resp.Data, nil
}

func (c *UpstreamClient) getUserSelfSub2API() (*UpstreamUserInfo, error) {
	respBody, err := c.doRequest("GET", "/api/v1/auth/me", nil)
	if err != nil {
		return nil, fmt.Errorf("get user info failed: %w", err)
	}

	var resp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := common.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse user info response: %w", err)
	}

	raw := resp.Data
	if raw == nil {
		return nil, fmt.Errorf("get user info failed: empty response data")
	}

	info := &UpstreamUserInfo{}
	if id, ok := raw["id"].(float64); ok {
		info.Id = int(id)
	}
	if v, ok := raw["username"].(string); ok {
		info.Username = v
	}
	if v, ok := raw["email"].(string); ok {
		info.Email = v
		if info.Username == "" {
			info.Username = v
		}
	}
	if v, ok := raw["display_name"].(string); ok {
		info.DisplayName = v
	}
	if v, ok := raw["role"].(string); ok {
		switch v {
		case "admin":
			info.Role = 100
		default:
			info.Role = 1
		}
	}
	if v, ok := raw["balance"].(float64); ok {
		info.Balance = fmt.Sprintf("%.5f", v)
	} else if v, ok := raw["balance"].(string); ok {
		info.Balance = v
	}

	c.userId = info.Id
	return info, nil
}

// GetGroups fetches available groups from the upstream site.
func (c *UpstreamClient) GetGroups() (map[string]UpstreamGroupInfo, error) {
	if c.SiteType == "sub2api" {
		return c.getGroupsSub2API()
	}
	return c.getGroupsNewAPI()
}

func (c *UpstreamClient) getGroupsNewAPI() (map[string]UpstreamGroupInfo, error) {
	respBody, err := c.doRequest("GET", "/api/user/self/groups", nil)
	if err != nil {
		return nil, fmt.Errorf("get groups failed: %w", err)
	}

	var resp struct {
		UpstreamApiResponse
		Data map[string]UpstreamGroupInfo `json:"data"`
	}
	if err := common.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse groups response: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("get groups failed: %s", resp.Message)
	}
	return resp.Data, nil
}

func (c *UpstreamClient) getGroupsSub2API() (map[string]UpstreamGroupInfo, error) {
	respBody, err := c.doRequest("GET", "/api/v1/groups/available", nil)
	if err != nil {
		return nil, fmt.Errorf("get groups failed: %w", err)
	}

	var resp struct {
		Code int              `json:"code"`
		Data []map[string]any `json:"data"`
	}
	if err := common.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse groups response: %w", err)
	}

	groups := make(map[string]UpstreamGroupInfo)
	// Also build an id->name map so we can match tokens by group_id later
	c.sub2apiGroupIdToName = make(map[int]string)
	for _, g := range resp.Data {
		name := ""
		if v, ok := g["name"].(string); ok {
			name = v
		}
		if name == "" {
			continue
		}
		var groupId int
		if v, ok := g["id"].(float64); ok {
			groupId = int(v)
		}
		if groupId > 0 {
			c.sub2apiGroupIdToName[groupId] = name
		}
		info := UpstreamGroupInfo{}
		if v, ok := g["rate_multiplier"]; ok {
			info.Ratio = v
		} else if v, ok := g["ratio"]; ok {
			info.Ratio = v
		}
		if v, ok := g["description"].(string); ok {
			info.Desc = v
		}
		groups[name] = info
	}
	return groups, nil
}

// ListTokens fetches all API tokens/keys.
func (c *UpstreamClient) ListTokens() ([]UpstreamToken, error) {
	if c.SiteType == "sub2api" {
		return c.listTokensSub2API()
	}
	return c.listTokensNewAPI()
}

func (c *UpstreamClient) listTokensNewAPI() ([]UpstreamToken, error) {
	var allTokens []UpstreamToken
	page := 1
	pageSize := 100
	for {
		path := fmt.Sprintf("/api/token/?p=%d&page_size=%d", page, pageSize)
		respBody, err := c.doRequest("GET", path, nil)
		if err != nil {
			return nil, fmt.Errorf("list tokens failed: %w", err)
		}

		var resp struct {
			UpstreamApiResponse
			Data struct {
				Items []UpstreamToken `json:"items"`
				Total int             `json:"total"`
			} `json:"data"`
		}
		if err := common.Unmarshal(respBody, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse tokens response: %w", err)
		}
		if !resp.Success {
			return nil, fmt.Errorf("list tokens failed: %s", resp.Message)
		}

		allTokens = append(allTokens, resp.Data.Items...)
		if len(allTokens) >= resp.Data.Total || len(resp.Data.Items) < pageSize {
			break
		}
		page++
	}
	return allTokens, nil
}

func (c *UpstreamClient) listTokensSub2API() ([]UpstreamToken, error) {
	respBody, err := c.doRequest("GET", "/api/v1/keys?page=1&page_size=100", nil)
	if err != nil {
		return nil, fmt.Errorf("list keys failed: %w", err)
	}

	var resp struct {
		Code int `json:"code"`
		Data struct {
			Items []map[string]any `json:"items"`
			Total int              `json:"total"`
		} `json:"data"`
	}
	if err := common.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse keys response: %w", err)
	}

	var tokens []UpstreamToken
	for _, raw := range resp.Data.Items {
		t := UpstreamToken{}
		if v, ok := raw["id"].(float64); ok {
			t.Id = int(v)
		}
		if v, ok := raw["name"].(string); ok {
			t.Name = v
		}
		if v, ok := raw["key"].(string); ok {
			t.Key = v
		}
		if v, ok := raw["status"].(string); ok {
			if v == "active" {
				t.Status = 1
			}
		}
		// Map group_id (int) to group name
		if v, ok := raw["group_id"].(float64); ok {
			groupId := int(v)
			if name, exists := c.sub2apiGroupIdToName[groupId]; exists {
				t.Group = name
			} else {
				t.Group = fmt.Sprintf("group_%d", groupId)
			}
		}
		tokens = append(tokens, t)
	}
	return tokens, nil
}

// CreateToken creates a new API token for a specific group.
func (c *UpstreamClient) CreateToken(name, group string) (*UpstreamToken, error) {
	if c.SiteType == "sub2api" {
		return c.createTokenSub2API(name, group)
	}
	return c.createTokenNewAPI(name, group)
}

func (c *UpstreamClient) createTokenNewAPI(name, group string) (*UpstreamToken, error) {
	tokenReq := map[string]any{
		"name":            name,
		"group":           group,
		"unlimited_quota": true,
		"expired_time":    -1,
	}
	body, err := common.Marshal(tokenReq)
	if err != nil {
		return nil, err
	}

	respBody, err := c.doRequest("POST", "/api/token/", body)
	if err != nil {
		return nil, fmt.Errorf("create token failed: %w", err)
	}

	var resp struct {
		UpstreamApiResponse
		Data UpstreamToken `json:"data"`
	}
	if err := common.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse create token response: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("create token failed: %s", resp.Message)
	}
	return &resp.Data, nil
}

func (c *UpstreamClient) createTokenSub2API(name, group string) (*UpstreamToken, error) {
	// Find group_id from name
	groupId := 0
	for id, n := range c.sub2apiGroupIdToName {
		if n == group {
			groupId = id
			break
		}
	}
	if groupId == 0 {
		return nil, fmt.Errorf("group %q not found", group)
	}

	tokenReq := map[string]any{
		"name":     name,
		"group_id": groupId,
	}
	body, err := common.Marshal(tokenReq)
	if err != nil {
		return nil, err
	}

	respBody, err := c.doRequest("POST", "/api/v1/keys", body)
	if err != nil {
		return nil, fmt.Errorf("create key failed: %w", err)
	}

	var resp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := common.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse create key response: %w", err)
	}

	t := &UpstreamToken{Group: group, Status: 1}
	if resp.Data != nil {
		if v, ok := resp.Data["id"].(float64); ok {
			t.Id = int(v)
		}
		if v, ok := resp.Data["key"].(string); ok {
			t.Key = v
		}
		if v, ok := resp.Data["name"].(string); ok {
			t.Name = v
		}
	}
	return t, nil
}

// GetTokenKey fetches the full API key for a token by ID.
func (c *UpstreamClient) GetTokenKey(tokenId int) (string, error) {
	if c.SiteType == "sub2api" {
		return "", fmt.Errorf("sub2api tokens already include full key")
	}

	path := fmt.Sprintf("/api/token/%d/key", tokenId)
	respBody, err := c.doRequest("POST", path, nil)
	if err != nil {
		return "", fmt.Errorf("get token key failed: %w", err)
	}

	var resp struct {
		UpstreamApiResponse
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := common.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("failed to parse token key response: %w", err)
	}
	if !resp.Success {
		return "", fmt.Errorf("get token key failed: %s", resp.Message)
	}
	return resp.Data.Key, nil
}
