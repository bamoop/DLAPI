package controller

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func GetUpstreamSites(c *gin.Context) {
	page := common.GetPageQuery(c)
	keyword := c.Query("keyword")

	var sites []*model.UpstreamSite
	var total int64
	var err error

	if keyword != "" {
		sites, total, err = model.SearchUpstreamSites(keyword, page.GetPage(), page.GetPageSize())
	} else {
		sites, total, err = model.GetUpstreamSites(page.GetPage(), page.GetPageSize())
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}

	common.ApiSuccess(c, gin.H{
		"items": sites,
		"total": total,
		"page":  page.GetPage(),
	})
}

func AddUpstreamSite(c *gin.Context) {
	var req struct {
		Name     string `json:"name"`
		BaseURL  string `json:"base_url"`
		Username string `json:"username"`
		Password string `json:"password"`
		SiteType string `json:"site_type"`
		Remark   string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "invalid request")
		return
	}
	if req.Name == "" || req.BaseURL == "" || req.Username == "" || req.Password == "" {
		common.ApiErrorMsg(c, "name, base_url, username, password are required")
		return
	}
	if req.SiteType == "" {
		req.SiteType = "newapi"
	}

	userInfo, err := service.TestUpstreamLogin(req.BaseURL, req.Username, req.Password, req.SiteType)
	if err != nil {
		common.ApiErrorMsg(c, "login test failed: "+err.Error())
		return
	}

	encryptedPwd, err := common.EncryptAES(req.Password)
	if err != nil {
		common.ApiErrorMsg(c, "failed to encrypt password")
		return
	}

	site := &model.UpstreamSite{
		Name:         req.Name,
		BaseURL:      req.BaseURL,
		Username:     req.Username,
		EncryptedPwd: encryptedPwd,
		SiteType:     req.SiteType,
		Status:       1,
		Remark:       req.Remark,
	}

	if err := model.CreateUpstreamSite(site); err != nil {
		common.ApiError(c, err)
		return
	}

	go func() {
		result, syncErr := service.SyncUpstreamSite(site)
		if syncErr != nil {
			site.UpdateSyncResult(syncErr, nil)
		}
		_ = result
	}()

	common.ApiSuccess(c, gin.H{
		"site":      site,
		"user_info": userInfo,
	})
}

func UpdateUpstreamSite(c *gin.Context) {
	var req struct {
		Id       int    `json:"id"`
		Name     string `json:"name"`
		BaseURL  string `json:"base_url"`
		Username string `json:"username"`
		Password string `json:"password"`
		SiteType string `json:"site_type"`
		Status   int    `json:"status"`
		Remark   string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "invalid request")
		return
	}
	if req.Id == 0 {
		common.ApiErrorMsg(c, "id is required")
		return
	}

	site, err := model.GetUpstreamSiteById(req.Id)
	if err != nil {
		common.ApiErrorMsg(c, "site not found")
		return
	}

	if req.Name != "" {
		site.Name = req.Name
	}
	if req.BaseURL != "" {
		site.BaseURL = req.BaseURL
	}
	if req.Username != "" {
		site.Username = req.Username
	}
	if req.Password != "" {
		encryptedPwd, err := common.EncryptAES(req.Password)
		if err != nil {
			common.ApiErrorMsg(c, "failed to encrypt password")
			return
		}
		site.EncryptedPwd = encryptedPwd
	}
	if req.SiteType != "" {
		site.SiteType = req.SiteType
	}
	if req.Status != 0 {
		site.Status = req.Status
	}
	if req.Remark != "" {
		site.Remark = req.Remark
	}

	if err := model.UpdateUpstreamSite(site); err != nil {
		common.ApiError(c, err)
		return
	}

	common.ApiSuccess(c, site)
}

func UpdateUpstreamSiteRemark(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ApiErrorMsg(c, "invalid id")
		return
	}
	var req struct {
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "invalid request")
		return
	}
	if err := model.UpdateUpstreamSiteSyncData(id, map[string]any{"remark": req.Remark}); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

func DeleteUpstreamSite(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ApiErrorMsg(c, "invalid id")
		return
	}

	if err := model.DeleteUpstreamSite(id); err != nil {
		common.ApiError(c, err)
		return
	}

	common.ApiSuccess(c, nil)
}

func SyncUpstreamSite(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ApiErrorMsg(c, "invalid id")
		return
	}

	site, err := model.GetUpstreamSiteById(id)
	if err != nil {
		common.ApiErrorMsg(c, "site not found")
		return
	}

	result, err := service.SyncUpstreamSite(site)
	if err != nil {
		site.UpdateSyncResult(err, nil)
		common.ApiErrorMsg(c, "sync failed: "+err.Error())
		return
	}

	common.ApiSuccess(c, result)
}

func TestUpstreamSiteConnection(c *gin.Context) {
	var req struct {
		BaseURL  string `json:"base_url"`
		Username string `json:"username"`
		Password string `json:"password"`
		SiteType string `json:"site_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "invalid request")
		return
	}
	if req.SiteType == "" {
		req.SiteType = "newapi"
	}

	userInfo, err := service.TestUpstreamLogin(req.BaseURL, req.Username, req.Password, req.SiteType)
	if err != nil {
		common.ApiErrorMsg(c, "connection test failed: "+err.Error())
		return
	}

	common.ApiSuccess(c, userInfo)
}

func GetUpstreamGroups(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ApiErrorMsg(c, "invalid id")
		return
	}

	site, err := model.GetUpstreamSiteById(id)
	if err != nil {
		common.ApiErrorMsg(c, "site not found")
		return
	}

	var groups map[string]service.UpstreamGroupInfo
	if site.CachedGroups != "" {
		_ = common.UnmarshalJsonStr(site.CachedGroups, &groups)
	}

	var tokens map[string]service.GroupTokenInfo
	if site.CachedTokens != "" {
		_ = common.UnmarshalJsonStr(site.CachedTokens, &tokens)
	}

	common.ApiSuccess(c, gin.H{
		"groups": groups,
		"tokens": tokens,
	})
}

func CreateGroupToken(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ApiErrorMsg(c, "invalid id")
		return
	}
	groupName := c.Param("group")
	if groupName == "" {
		common.ApiErrorMsg(c, "group name is required")
		return
	}

	site, err := model.GetUpstreamSiteById(id)
	if err != nil {
		common.ApiErrorMsg(c, "site not found")
		return
	}

	tokenInfo, err := service.CreateGroupTokenForSite(site, groupName)
	if err != nil {
		common.ApiErrorMsg(c, "create token failed: "+err.Error())
		return
	}

	common.ApiSuccess(c, tokenInfo)
}

func SearchUpstreamKeys(c *gin.Context) {
	keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword")))

	sites, err := model.GetAllActiveUpstreamSites()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	type KeyResult struct {
		SiteId      int    `json:"site_id"`
		SiteName    string `json:"site_name"`
		BaseURL     string `json:"base_url"`
		GroupName   string `json:"group_name"`
		Ratio       any    `json:"ratio"`
		Desc        string `json:"desc"`
		Key         string `json:"key"`
		TokenStatus int    `json:"token_status"`
	}

	var results []KeyResult
	for _, site := range sites {
		var groups map[string]service.UpstreamGroupInfo
		if site.CachedGroups != "" {
			_ = common.UnmarshalJsonStr(site.CachedGroups, &groups)
		}
		var tokens map[string]service.GroupTokenInfo
		if site.CachedTokens != "" {
			_ = common.UnmarshalJsonStr(site.CachedTokens, &tokens)
		}
		for groupName, groupInfo := range groups {
			if keyword != "" {
				nameLower := strings.ToLower(groupName)
				descLower := strings.ToLower(groupInfo.Desc)
				if !strings.Contains(nameLower, keyword) && !strings.Contains(descLower, keyword) {
					continue
				}
			}
			r := KeyResult{
				SiteId:    site.Id,
				SiteName:  site.Name,
				BaseURL:   site.BaseURL,
				GroupName: groupName,
				Ratio:     groupInfo.Ratio,
				Desc:      groupInfo.Desc,
			}
			if token, ok := tokens[groupName]; ok {
				r.Key = token.Key
				r.TokenStatus = token.Status
			}
			results = append(results, r)
		}
	}

	if results == nil {
		results = []KeyResult{}
	}

	common.ApiSuccess(c, gin.H{
		"results": results,
		"total":   len(results),
	})
}

func SyncAllUpstreamSites(c *gin.Context) {
	sites, err := model.GetAllActiveUpstreamSites()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	results := make(map[int]string)
	for _, site := range sites {
		_, syncErr := service.SyncUpstreamSite(site)
		if syncErr != nil {
			results[site.Id] = "failed: " + syncErr.Error()
		} else {
			results[site.Id] = "success"
		}
	}

	common.ApiSuccess(c, gin.H{
		"results": results,
		"total":   len(sites),
	})
}
