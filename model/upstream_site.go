package model

import (
	"time"

	"gorm.io/gorm"
)

type UpstreamSite struct {
	Id            int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Name          string `json:"name" gorm:"type:varchar(100);not null"`
	BaseURL       string `json:"base_url" gorm:"type:varchar(255);not null"`
	Username      string `json:"username" gorm:"type:varchar(100);not null"`
	EncryptedPwd  string `json:"-" gorm:"type:text;not null"`
	SiteType      string `json:"site_type" gorm:"type:varchar(20);default:'newapi'"`
	Status        int    `json:"status" gorm:"default:1"`
	Balance       string `json:"balance" gorm:"type:text"`
	CachedGroups  string `json:"cached_groups" gorm:"type:text"`
	CachedTokens  string `json:"cached_tokens" gorm:"type:text"`
	LastSyncTime  int64  `json:"last_sync_time" gorm:"bigint"`
	LastSyncError string `json:"last_sync_error" gorm:"type:text"`
	CreatedTime   int64  `json:"created_time" gorm:"bigint"`
	Remark        string `json:"remark" gorm:"type:varchar(255)"`
}

func GetUpstreamSites(page, pageSize int) (sites []*UpstreamSite, total int64, err error) {
	tx := DB.Model(&UpstreamSite{})
	err = tx.Count(&total).Error
	if err != nil {
		return
	}
	err = tx.Order("id desc").Limit(pageSize).Offset((page - 1) * pageSize).Find(&sites).Error
	return
}

func GetUpstreamSiteById(id int) (*UpstreamSite, error) {
	var site UpstreamSite
	err := DB.First(&site, id).Error
	if err != nil {
		return nil, err
	}
	return &site, nil
}

func CreateUpstreamSite(site *UpstreamSite) error {
	site.CreatedTime = time.Now().Unix()
	return DB.Create(site).Error
}

func UpdateUpstreamSite(site *UpstreamSite) error {
	return DB.Save(site).Error
}

func UpdateUpstreamSiteSyncData(id int, updates map[string]any) error {
	return DB.Model(&UpstreamSite{}).Where("id = ?", id).Updates(updates).Error
}

func DeleteUpstreamSite(id int) error {
	return DB.Delete(&UpstreamSite{}, id).Error
}

func GetAllActiveUpstreamSites() ([]*UpstreamSite, error) {
	var sites []*UpstreamSite
	err := DB.Where("status = ?", 1).Find(&sites).Error
	return sites, err
}

func SearchUpstreamSites(keyword string, page, pageSize int) (sites []*UpstreamSite, total int64, err error) {
	tx := DB.Model(&UpstreamSite{})
	if keyword != "" {
		tx = tx.Where("name LIKE ? OR base_url LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	err = tx.Count(&total).Error
	if err != nil {
		return
	}
	err = tx.Order("id desc").Limit(pageSize).Offset((page - 1) * pageSize).Find(&sites).Error
	return
}

func (s *UpstreamSite) UpdateSyncResult(err error, updates map[string]any) {
	if updates == nil {
		updates = make(map[string]any)
	}
	updates["last_sync_time"] = time.Now().Unix()
	if err != nil {
		updates["last_sync_error"] = err.Error()
	} else {
		updates["last_sync_error"] = ""
	}
	_ = UpdateUpstreamSiteSyncData(s.Id, updates)
}

func (s *UpstreamSite) BeforeCreate(tx *gorm.DB) error {
	if s.CreatedTime == 0 {
		s.CreatedTime = time.Now().Unix()
	}
	return nil
}
