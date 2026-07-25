package service

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"
	"gorm.io/gorm"
)

const (
	advancedChatSkillPackageMaxArchiveBytes = 50 * 1024 * 1024
	advancedChatSkillPackageMaxFiles        = 500
	advancedChatSkillPackageMaxFileBytes    = 2 * 1024 * 1024
	advancedChatSkillPackageMaxTotalBytes   = 50 * 1024 * 1024

	advancedChatSkillSourceUploaded  = "uploaded"
	advancedChatSkillSourceConnector = "connector"

	advancedChatActivateSkillToolName     = "activate_skill"
	advancedChatReadSkillResourceToolName = "read_skill_resource"
	advancedChatSkillResourceMaxBytes     = 256 * 1024
)

type AdvancedChatSkillPackage struct {
	ID             string     `gorm:"primaryKey;size:80" json:"id"`
	UserID         uint       `gorm:"index;not null" json:"user_id"`
	User           model.User `gorm:"foreignKey:UserID" json:"-"`
	OrganizationID uint       `gorm:"index" json:"organization_id,omitempty"`
	WorkspaceID    uint       `gorm:"index" json:"workspace_id,omitempty"`
	OwnerUserID    uint       `gorm:"index" json:"owner_user_id,omitempty"`
	Visibility     string     `gorm:"size:20;not null;default:'personal';index" json:"visibility"`
	Name           string     `gorm:"size:160;not null" json:"name"`
	SourceName     string     `gorm:"size:255;not null" json:"source_name"`
	StoragePath    string     `gorm:"type:text;not null" json:"-"`
	Size           int64      `gorm:"not null" json:"size"`
	FileCount      int        `gorm:"not null" json:"file_count"`
	Hash           string     `gorm:"index;size:64;not null" json:"hash"`
	Status         string     `gorm:"size:40;not null" json:"status"`
	ErrorText      string     `gorm:"type:text;not null" json:"error_text"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type AdvancedChatPackagedSkill struct {
	ID             string                   `gorm:"primaryKey;size:80" json:"id"`
	UserID         uint                     `gorm:"index;not null" json:"user_id"`
	User           model.User               `gorm:"foreignKey:UserID" json:"-"`
	OrganizationID uint                     `gorm:"index" json:"organization_id,omitempty"`
	WorkspaceID    uint                     `gorm:"index" json:"workspace_id,omitempty"`
	OwnerUserID    uint                     `gorm:"index" json:"owner_user_id,omitempty"`
	Visibility     string                   `gorm:"size:20;not null;default:'personal';index" json:"visibility"`
	PackageID      string                   `gorm:"index;size:80;not null" json:"package_id"`
	Package        AdvancedChatSkillPackage `gorm:"foreignKey:PackageID" json:"-"`
	Name           string                   `gorm:"size:120;not null" json:"name"`
	Description    string                   `gorm:"type:text;not null" json:"description"`
	Source         string                   `gorm:"size:40;not null" json:"source"`
	SkillPath      string                   `gorm:"type:text;not null" json:"skill_path"`
	RootPath       string                   `gorm:"type:text;not null" json:"root_path"`
	MetadataJSON   string                   `gorm:"type:text;not null" json:"-"`
	AllowedTools   string                   `gorm:"type:text;not null" json:"-"`
	Compatibility  string                   `gorm:"type:text;not null" json:"-"`
	Enabled        bool                     `gorm:"not null;default:true" json:"enabled"`
	Size           int64                    `gorm:"not null" json:"size"`
	Hash           string                   `gorm:"index;size:64;not null" json:"hash"`
	CreatedAt      time.Time                `json:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
}

type advancedChatSkillPackageResponse struct {
	ID         string                           `json:"id"`
	Name       string                           `json:"name"`
	SourceName string                           `json:"source_name"`
	Size       int64                            `json:"size"`
	FileCount  int                              `json:"file_count"`
	Hash       string                           `json:"hash"`
	Status     string                           `json:"status"`
	ErrorText  string                           `json:"error_text,omitempty"`
	Skills     []advancedChatPackagedSkillBrief `json:"skills"`
	CreatedAt  time.Time                        `json:"created_at"`
	UpdatedAt  time.Time                        `json:"updated_at"`
}

type advancedChatSkillPackageDetailResponse struct {
	advancedChatSkillPackageResponse
	Files []advancedChatSkillPackageFileResponse `json:"files"`
}

type advancedChatSkillPackageFileResponse struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Skill   bool   `json:"skill"`
	ModTime string `json:"mod_time,omitempty"`
}

type advancedChatSkillPackageFileContentResponse struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
}

type advancedChatPackagedSkillBrief struct {
	ID          string    `json:"id"`
	PackageID   string    `json:"package_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Source      string    `json:"source"`
	SkillPath   string    `json:"skill_path"`
	RootPath    string    `json:"root_path"`
	Enabled     bool      `json:"enabled"`
	Size        int64     `json:"size"`
	Hash        string    `json:"hash"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type advancedChatSkillDetailResponse struct {
	advancedChatPackagedSkillBrief
	PackageName       string                                 `json:"package_name"`
	PackageSourceName string                                 `json:"package_source_name"`
	Files             []advancedChatSkillPackageFileResponse `json:"files"`
}

type advancedChatSkillManifest struct {
	Name          string                 `yaml:"name" json:"name"`
	Description   string                 `yaml:"description" json:"description"`
	License       interface{}            `yaml:"license" json:"license,omitempty"`
	AllowedTools  interface{}            `yaml:"allowed-tools" json:"allowed-tools,omitempty"`
	Compatibility interface{}            `yaml:"compatibility" json:"compatibility,omitempty"`
	Metadata      map[string]interface{} `yaml:"metadata" json:"metadata,omitempty"`
}

type advancedChatSkillFileCandidate struct {
	SkillPath     string
	RootPath      string
	Name          string
	Description   string
	MetadataJSON  string
	AllowedTools  string
	Compatibility string
	Size          int64
	Hash          string
}

type advancedChatSkillExtractStats struct {
	Size      int64
	FileCount int
	Hash      string
}

type advancedChatUploadedSkillSyncPackage struct {
	ID     string                               `json:"id"`
	Hash   string                               `json:"hash"`
	Files  []advancedChatUploadedSkillSyncFile  `json:"files"`
	Skills []advancedChatUploadedSkillSyncSkill `json:"skills"`
}

type advancedChatUploadedSkillSyncFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type advancedChatUploadedSkillSyncSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SkillPath   string   `json:"skill_path"`
	RootPath    string   `json:"root_path"`
	Resources   []string `json:"resource_paths"`
}

func (api *advancedChatAPI) listSkillPackages(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var packages []AdvancedChatSkillPackage
	if err := model.DB.Where("user_id = ?", user.ID).Order("created_at DESC").Find(&packages).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list skill packages"})
		return
	}
	packageIDs := make([]string, 0, len(packages))
	for _, pkg := range packages {
		packageIDs = append(packageIDs, pkg.ID)
	}
	skillsByPackage := map[string][]AdvancedChatPackagedSkill{}
	if len(packageIDs) > 0 {
		var skills []AdvancedChatPackagedSkill
		if err := model.DB.Where("user_id = ? AND package_id IN ?", user.ID, packageIDs).Order("created_at ASC").Find(&skills).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list packaged skills"})
			return
		}
		for _, skill := range skills {
			skillsByPackage[skill.PackageID] = append(skillsByPackage[skill.PackageID], skill)
		}
	}
	responses := make([]advancedChatSkillPackageResponse, 0, len(packages))
	for _, pkg := range packages {
		responses = append(responses, advancedChatSkillPackageResponseFromModel(pkg, skillsByPackage[pkg.ID]))
	}
	c.JSON(http.StatusOK, gin.H{
		"packages":        responses,
		"used_bytes":      advancedChatFileStorageUsedBytes(user.ID),
		"total_bytes":     advancedChatFileStorageTotalBytes(),
		"remaining_bytes": advancedChatFileStorageRemainingBytes(user.ID),
	})
}

func (api *advancedChatAPI) getSkillPackage(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	pkg, skills, found := loadAdvancedChatSkillPackageForResponse(c, user.ID)
	if !found {
		return
	}
	files, err := listAdvancedChatSkillPackageFiles(pkg, skills)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list skill package files"})
		return
	}
	c.JSON(http.StatusOK, advancedChatSkillPackageDetailResponse{
		advancedChatSkillPackageResponse: advancedChatSkillPackageResponseFromModel(pkg, skills),
		Files:                            files,
	})
}

func (api *advancedChatAPI) readSkillPackageFile(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	pkg, _, found := loadAdvancedChatSkillPackageForResponse(c, user.ID)
	if !found {
		return
	}
	filePath := strings.TrimSpace(c.Query("path"))
	if filePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File path is required"})
		return
	}
	normalized, err := normalizeAdvancedChatSkillPackagePath(filePath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file path"})
		return
	}
	root, err := advancedChatStorageAbsPath(pkg.StoragePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load skill package"})
		return
	}
	target := filepath.Join(root, filepath.FromSlash(normalized))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file path"})
		return
	}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found"})
		return
	}
	content, truncated, err := readAdvancedChatSkillPackageFilePreview(pkg, normalized, advancedChatSkillResourceMaxBytes)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read skill package file"})
		return
	}
	c.JSON(http.StatusOK, advancedChatSkillPackageFileContentResponse{
		Path:      normalized,
		Content:   content,
		Size:      info.Size(),
		Truncated: truncated,
	})
}

func (api *advancedChatAPI) getSkill(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	skill, pkg, found := loadAdvancedChatSkillForResponse(c, user.ID)
	if !found {
		return
	}
	files, err := listAdvancedChatPackagedSkillFiles(pkg, skill)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list skill files"})
		return
	}
	c.JSON(http.StatusOK, advancedChatSkillDetailResponse{
		advancedChatPackagedSkillBrief: advancedChatPackagedSkillBriefFromModel(skill),
		PackageName:                    pkg.Name,
		PackageSourceName:              pkg.SourceName,
		Files:                          files,
	})
}

func (api *advancedChatAPI) readSkillFile(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	skill, pkg, found := loadAdvancedChatSkillForResponse(c, user.ID)
	if !found {
		return
	}
	filePath := strings.TrimSpace(c.Query("path"))
	if filePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File path is required"})
		return
	}
	packagePath, err := advancedChatSkillPackagePathForSkillFile(skill, filePath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file path"})
		return
	}
	root, err := advancedChatStorageAbsPath(pkg.StoragePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load skill"})
		return
	}
	target := filepath.Join(root, filepath.FromSlash(packagePath))
	info, err := os.Stat(target)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found"})
		return
	}
	content, truncated, err := readAdvancedChatSkillPackageFilePreview(pkg, packagePath, advancedChatSkillResourceMaxBytes)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read skill file"})
		return
	}
	c.JSON(http.StatusOK, advancedChatSkillPackageFileContentResponse{
		Path:      filePath,
		Content:   content,
		Size:      info.Size(),
		Truncated: truncated,
	})
}

func (api *advancedChatAPI) uploadSkillPackage(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !advancedChatFileStorageEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "File storage is disabled"})
		return
	}
	if err := c.Request.ParseMultipartForm(advancedChatSkillPackageMaxArchiveBytes); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid multipart form"})
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File is required"})
		return
	}
	if fileHeader.Size > advancedChatSkillPackageMaxArchiveBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Skill package archive is too large"})
		return
	}
	source, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to open file"})
		return
	}
	defer source.Close()
	data, err := io.ReadAll(io.LimitReader(source, advancedChatSkillPackageMaxArchiveBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read file"})
		return
	}
	if len(data) == 0 || len(data) > advancedChatSkillPackageMaxArchiveBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Skill package archive is too large"})
		return
	}
	pkg, skills, status, message, err := storeAdvancedChatSkillPackage(user.ID, fileHeader.Filename, data)
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	organizationID, workspaceID := advancedChatEnterpriseScope(c)
	if organizationID != 0 {
		visibility := model.NormalizeResourceVisibility(c.PostForm("visibility"))
		if visibility == model.ResourceVisibilityWorkspace && workspaceID == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "A workspace is required for workspace visibility"})
			return
		}
		updates := map[string]interface{}{"organization_id": organizationID, "workspace_id": workspaceID, "owner_user_id": user.ID, "visibility": visibility}
		if err := model.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&AdvancedChatSkillPackage{}).Where("id = ?", pkg.ID).Updates(updates).Error; err != nil {
				return err
			}
			return tx.Model(&AdvancedChatPackagedSkill{}).Where("package_id = ?", pkg.ID).Updates(updates).Error
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to scope skill package"})
			return
		}
		pkg.OrganizationID, pkg.WorkspaceID, pkg.OwnerUserID, pkg.Visibility = organizationID, workspaceID, user.ID, visibility
		for i := range skills {
			skills[i].OrganizationID, skills[i].WorkspaceID, skills[i].OwnerUserID, skills[i].Visibility = organizationID, workspaceID, user.ID, visibility
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"package":         advancedChatSkillPackageResponseFromModel(pkg, skills),
		"used_bytes":      advancedChatFileStorageUsedBytes(user.ID),
		"total_bytes":     advancedChatFileStorageTotalBytes(),
		"remaining_bytes": advancedChatFileStorageRemainingBytes(user.ID),
	})
}

func (api *advancedChatAPI) deleteSkillPackage(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	packageID := strings.TrimSpace(c.Param("id"))
	var pkg AdvancedChatSkillPackage
	if err := model.DB.Where("id = ? AND user_id = ?", packageID, user.ID).First(&pkg).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Skill package not found"})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("package_id = ? AND user_id = ?", packageID, user.ID).Delete(&AdvancedChatPackagedSkill{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ? AND user_id = ?", packageID, user.ID).Delete(&AdvancedChatSkillPackage{}).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete skill package"})
		return
	}
	_ = removeAdvancedChatStorageDir(pkg.StoragePath)
	c.JSON(http.StatusOK, gin.H{
		"message":         "Skill package deleted",
		"used_bytes":      advancedChatFileStorageUsedBytes(user.ID),
		"total_bytes":     advancedChatFileStorageTotalBytes(),
		"remaining_bytes": advancedChatFileStorageRemainingBytes(user.ID),
	})
}

func storeAdvancedChatSkillPackage(userID uint, sourceName string, archiveData []byte) (AdvancedChatSkillPackage, []AdvancedChatPackagedSkill, int, string, error) {
	archiveHash := hexHash(archiveData)
	packageID := newAdvancedChatID("acsp")
	storagePath := path.Join("advanced-chat", "skill-packages", strconv.FormatUint(uint64(userID), 10), packageID, archiveHash)
	target, err := advancedChatStorageAbsPath(storagePath)
	if err != nil {
		return AdvancedChatSkillPackage{}, nil, http.StatusInternalServerError, "Failed to prepare skill package storage", err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return AdvancedChatSkillPackage{}, nil, http.StatusInternalServerError, "Failed to prepare skill package storage", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(target)
		}
	}()

	stats, err := extractAdvancedChatSkillArchive(sourceName, archiveData, target)
	if err != nil {
		return AdvancedChatSkillPackage{}, nil, http.StatusBadRequest, err.Error(), err
	}
	if stats.Size > advancedChatFileStorageRemainingBytes(userID) {
		return AdvancedChatSkillPackage{}, nil, http.StatusPaymentRequired, "Not enough file storage space", errAdvancedChatFileInsufficient
	}
	candidates, err := scanAdvancedChatSkillPackage(target)
	if err != nil {
		return AdvancedChatSkillPackage{}, nil, http.StatusBadRequest, err.Error(), err
	}
	if len(candidates) == 0 {
		return AdvancedChatSkillPackage{}, nil, http.StatusBadRequest, "Skill package must contain at least one SKILL.md with name and description", errors.New("no skills found")
	}
	pkgName := strings.TrimSpace(sourceName)
	if len(candidates) == 1 {
		pkgName = candidates[0].Name
	}
	if pkgName == "" {
		pkgName = "Skill package"
	}
	pkg := AdvancedChatSkillPackage{
		ID:          packageID,
		UserID:      userID,
		Name:        truncateRunes(pkgName, 160),
		SourceName:  truncateRunes(sourceName, 255),
		StoragePath: storagePath,
		Size:        stats.Size,
		FileCount:   stats.FileCount,
		Hash:        stats.Hash,
		Status:      "ready",
	}
	skills := make([]AdvancedChatPackagedSkill, 0, len(candidates))
	for _, candidate := range candidates {
		skills = append(skills, AdvancedChatPackagedSkill{
			ID:            newAdvancedChatID("acsk"),
			UserID:        userID,
			PackageID:     packageID,
			Name:          truncateRunes(candidate.Name, 120),
			Description:   candidate.Description,
			Source:        advancedChatSkillSourceUploaded,
			SkillPath:     candidate.SkillPath,
			RootPath:      candidate.RootPath,
			MetadataJSON:  candidate.MetadataJSON,
			AllowedTools:  candidate.AllowedTools,
			Compatibility: candidate.Compatibility,
			Enabled:       true,
			Size:          candidate.Size,
			Hash:          candidate.Hash,
		})
	}
	storageLimit := advancedChatFileStorageTotalBytes()
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var used int64
		if err := tx.Model(&AdvancedChatFile{}).Where("user_id = ?", userID).Select("COALESCE(SUM(size), 0)").Scan(&used).Error; err != nil {
			return err
		}
		var packageUsed int64
		if err := tx.Model(&AdvancedChatSkillPackage{}).Where("user_id = ?", userID).Select("COALESCE(SUM(size), 0)").Scan(&packageUsed).Error; err != nil {
			return err
		}
		if used+packageUsed+stats.Size > storageLimit {
			return errAdvancedChatFileInsufficient
		}
		if err := tx.Create(&pkg).Error; err != nil {
			return err
		}
		return tx.Create(&skills).Error
	})
	if err != nil {
		if errors.Is(err, errAdvancedChatFileInsufficient) {
			return AdvancedChatSkillPackage{}, nil, http.StatusPaymentRequired, "Not enough file storage space", err
		}
		return AdvancedChatSkillPackage{}, nil, http.StatusInternalServerError, "Failed to save skill package", err
	}
	cleanup = false
	return pkg, skills, http.StatusOK, "", nil
}

func extractAdvancedChatSkillArchive(sourceName string, data []byte, target string) (advancedChatSkillExtractStats, error) {
	lower := strings.ToLower(strings.TrimSpace(sourceName))
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractAdvancedChatSkillZip(data, target)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractAdvancedChatSkillTarGz(data, target)
	default:
		return advancedChatSkillExtractStats{}, errors.New("Skill package must be a .zip, .tar.gz, or .tgz archive")
	}
}

func extractAdvancedChatSkillZip(data []byte, target string) (advancedChatSkillExtractStats, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return advancedChatSkillExtractStats{}, errors.New("Invalid zip archive")
	}
	stats := advancedChatSkillExtractStats{Hash: hexHash(data)}
	for _, file := range reader.File {
		mode := file.FileInfo().Mode()
		if mode&os.ModeSymlink != 0 || file.FileInfo().IsDir() {
			if file.FileInfo().IsDir() {
				continue
			}
			return stats, errors.New("Skill package may not contain symlinks")
		}
		relativePath, err := normalizeAdvancedChatSkillPackagePath(file.Name)
		if err != nil {
			return stats, err
		}
		if file.UncompressedSize64 > advancedChatSkillPackageMaxFileBytes {
			return stats, fmt.Errorf("Skill package file %s is too large", relativePath)
		}
		source, err := file.Open()
		if err != nil {
			return stats, err
		}
		written, err := writeAdvancedChatSkillPackageFile(target, relativePath, source)
		_ = source.Close()
		if err != nil {
			return stats, err
		}
		stats.FileCount++
		stats.Size += written
		if err := validateAdvancedChatSkillPackageStats(stats); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func extractAdvancedChatSkillTarGz(data []byte, target string) (advancedChatSkillExtractStats, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return advancedChatSkillExtractStats{}, errors.New("Invalid tar.gz archive")
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	stats := advancedChatSkillExtractStats{Hash: hexHash(data)}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stats, errors.New("Invalid tar.gz archive")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, tar.TypeRegA:
		default:
			return stats, errors.New("Skill package may only contain regular files")
		}
		relativePath, err := normalizeAdvancedChatSkillPackagePath(header.Name)
		if err != nil {
			return stats, err
		}
		if header.Size > advancedChatSkillPackageMaxFileBytes {
			return stats, fmt.Errorf("Skill package file %s is too large", relativePath)
		}
		written, err := writeAdvancedChatSkillPackageFile(target, relativePath, reader)
		if err != nil {
			return stats, err
		}
		stats.FileCount++
		stats.Size += written
		if err := validateAdvancedChatSkillPackageStats(stats); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func normalizeAdvancedChatSkillPackagePath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\x00") {
		return "", errors.New("Skill package file paths must be relative")
	}
	cleaned := path.Clean("/" + value)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." {
		return "", errors.New("Skill package file path is invalid")
	}
	if len([]rune(cleaned)) > 500 {
		return "", errors.New("Skill package file path is too long")
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("Skill package file paths must not escape the archive root")
		}
	}
	return cleaned, nil
}

func writeAdvancedChatSkillPackageFile(root string, relativePath string, source io.Reader) (int64, error) {
	target := filepath.Join(root, filepath.FromSlash(relativePath))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return 0, errors.New("Skill package file path escapes storage root")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	written, err := io.Copy(file, io.LimitReader(source, advancedChatSkillPackageMaxFileBytes+1))
	if err != nil {
		return written, err
	}
	if written > advancedChatSkillPackageMaxFileBytes {
		return written, fmt.Errorf("Skill package file %s is too large", relativePath)
	}
	return written, nil
}

func validateAdvancedChatSkillPackageStats(stats advancedChatSkillExtractStats) error {
	if stats.FileCount > advancedChatSkillPackageMaxFiles {
		return fmt.Errorf("Skill package contains too many files; max %d", advancedChatSkillPackageMaxFiles)
	}
	if stats.Size > advancedChatSkillPackageMaxTotalBytes {
		return fmt.Errorf("Skill package is too large after extraction; max %d bytes", advancedChatSkillPackageMaxTotalBytes)
	}
	return nil
}

func scanAdvancedChatSkillPackage(root string) ([]advancedChatSkillFileCandidate, error) {
	candidates := []advancedChatSkillFileCandidate{}
	err := filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if entry.Type()&os.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !strings.EqualFold(entry.Name(), "SKILL.md") {
			return nil
		}
		relativePath, err := filepath.Rel(root, filePath)
		if err != nil {
			return nil
		}
		relativePath = filepath.ToSlash(relativePath)
		data, err := os.ReadFile(filePath)
		if err != nil || len(data) == 0 || len(data) > advancedChatSkillPackageMaxFileBytes {
			return nil
		}
		manifest, metadataJSON, allowedTools, compatibility, ok := parseAdvancedChatSkillManifest(string(data))
		if !ok {
			return nil
		}
		name := strings.TrimSpace(manifest.Name)
		description := strings.TrimSpace(manifest.Description)
		if name == "" || description == "" {
			return nil
		}
		rootPath := path.Dir(relativePath)
		if rootPath == "." {
			rootPath = ""
		}
		candidates = append(candidates, advancedChatSkillFileCandidate{
			SkillPath:     relativePath,
			RootPath:      rootPath,
			Name:          name,
			Description:   description,
			MetadataJSON:  metadataJSON,
			AllowedTools:  allowedTools,
			Compatibility: compatibility,
			Size:          int64(len(data)),
			Hash:          hexHash(data),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(candidates, func(i, j int) bool {
		return strings.ToLower(candidates[i].SkillPath) < strings.ToLower(candidates[j].SkillPath)
	})
	return candidates, nil
}

func parseAdvancedChatSkillManifest(content string) (advancedChatSkillManifest, string, string, string, bool) {
	content = strings.TrimPrefix(content, "\ufeff")
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return advancedChatSkillManifest{}, "", "", "", false
	}
	body := content[3:]
	body = strings.TrimPrefix(body, "\r\n")
	body = strings.TrimPrefix(body, "\n")
	end := strings.Index(body, "\n---")
	if end < 0 {
		return advancedChatSkillManifest{}, "", "", "", false
	}
	raw := body[:end]
	var manifest advancedChatSkillManifest
	if err := yaml.Unmarshal([]byte(raw), &manifest); err != nil {
		return advancedChatSkillManifest{}, "", "", "", false
	}
	metadataJSON := marshalSkillManifestField(manifest.Metadata)
	return manifest, metadataJSON, marshalSkillManifestField(manifest.AllowedTools), marshalSkillManifestField(manifest.Compatibility), true
}

func marshalSkillManifestField(value interface{}) string {
	if value == nil {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func advancedChatSkillPackageResponseFromModel(pkg AdvancedChatSkillPackage, skills []AdvancedChatPackagedSkill) advancedChatSkillPackageResponse {
	briefs := make([]advancedChatPackagedSkillBrief, 0, len(skills))
	for _, skill := range skills {
		briefs = append(briefs, advancedChatPackagedSkillBriefFromModel(skill))
	}
	return advancedChatSkillPackageResponse{
		ID:         pkg.ID,
		Name:       pkg.Name,
		SourceName: pkg.SourceName,
		Size:       pkg.Size,
		FileCount:  pkg.FileCount,
		Hash:       pkg.Hash,
		Status:     pkg.Status,
		ErrorText:  pkg.ErrorText,
		Skills:     briefs,
		CreatedAt:  pkg.CreatedAt,
		UpdatedAt:  pkg.UpdatedAt,
	}
}

func advancedChatPackagedSkillBriefFromModel(skill AdvancedChatPackagedSkill) advancedChatPackagedSkillBrief {
	return advancedChatPackagedSkillBrief{
		ID:          skill.ID,
		PackageID:   skill.PackageID,
		Name:        skill.Name,
		Description: skill.Description,
		Source:      skill.Source,
		SkillPath:   skill.SkillPath,
		RootPath:    skill.RootPath,
		Enabled:     skill.Enabled,
		Size:        skill.Size,
		Hash:        skill.Hash,
		CreatedAt:   skill.CreatedAt,
		UpdatedAt:   skill.UpdatedAt,
	}
}

func loadAdvancedChatSkillForResponse(c *gin.Context, userID uint) (AdvancedChatPackagedSkill, AdvancedChatSkillPackage, bool) {
	skillID := strings.TrimSpace(c.Param("id"))
	if skillID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Skill id is required"})
		return AdvancedChatPackagedSkill{}, AdvancedChatSkillPackage{}, false
	}
	var skill AdvancedChatPackagedSkill
	if err := model.DB.Where("id = ? AND user_id = ? AND enabled = ?", skillID, userID, true).First(&skill).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Skill not found"})
			return AdvancedChatPackagedSkill{}, AdvancedChatSkillPackage{}, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load skill"})
		return AdvancedChatPackagedSkill{}, AdvancedChatSkillPackage{}, false
	}
	var pkg AdvancedChatSkillPackage
	if err := model.DB.Where("id = ? AND user_id = ? AND status = ?", skill.PackageID, userID, "ready").First(&pkg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Skill package not found"})
			return AdvancedChatPackagedSkill{}, AdvancedChatSkillPackage{}, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load skill package"})
		return AdvancedChatPackagedSkill{}, AdvancedChatSkillPackage{}, false
	}
	return skill, pkg, true
}

func loadAdvancedChatSkillPackageForResponse(c *gin.Context, userID uint) (AdvancedChatSkillPackage, []AdvancedChatPackagedSkill, bool) {
	packageID := strings.TrimSpace(c.Param("id"))
	if packageID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Skill package id is required"})
		return AdvancedChatSkillPackage{}, nil, false
	}
	var pkg AdvancedChatSkillPackage
	if err := model.DB.Where("id = ? AND user_id = ?", packageID, userID).First(&pkg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Skill package not found"})
			return AdvancedChatSkillPackage{}, nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load skill package"})
		return AdvancedChatSkillPackage{}, nil, false
	}
	var skills []AdvancedChatPackagedSkill
	if err := model.DB.Where("package_id = ? AND user_id = ?", packageID, userID).Order("created_at ASC").Find(&skills).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list packaged skills"})
		return AdvancedChatSkillPackage{}, nil, false
	}
	return pkg, skills, true
}

func listAdvancedChatPackagedSkillFiles(pkg AdvancedChatSkillPackage, skill AdvancedChatPackagedSkill) ([]advancedChatSkillPackageFileResponse, error) {
	root, err := advancedChatStorageAbsPath(pkg.StoragePath)
	if err != nil {
		return nil, err
	}
	base := root
	if strings.TrimSpace(skill.RootPath) != "" {
		base = filepath.Join(root, filepath.FromSlash(strings.Trim(strings.TrimSpace(skill.RootPath), "/")))
	}
	files := []advancedChatSkillPackageFileResponse{}
	err = filepath.WalkDir(base, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(base, filePath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, err := normalizeAdvancedChatSkillPackagePath(rel); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, advancedChatSkillPackageFileResponse{
			Path:    rel,
			Size:    info.Size(),
			Skill:   strings.EqualFold(rel, "SKILL.md"),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Skill != files[j].Skill {
			return files[i].Skill
		}
		return files[i].Path < files[j].Path
	})
	return files, nil
}

func advancedChatSkillPackagePathForSkillFile(skill AdvancedChatPackagedSkill, relativePath string) (string, error) {
	relativePath, err := normalizeAdvancedChatSkillPackagePath(relativePath)
	if err != nil {
		return "", err
	}
	base := strings.Trim(strings.TrimSpace(skill.RootPath), "/")
	if base != "" {
		return path.Join(base, relativePath), nil
	}
	return relativePath, nil
}

func listAdvancedChatSkillPackageFiles(pkg AdvancedChatSkillPackage, skills []AdvancedChatPackagedSkill) ([]advancedChatSkillPackageFileResponse, error) {
	root, err := advancedChatStorageAbsPath(pkg.StoragePath)
	if err != nil {
		return nil, err
	}
	skillPaths := map[string]bool{}
	for _, skill := range skills {
		if path := strings.TrimSpace(skill.SkillPath); path != "" {
			skillPaths[path] = true
		}
	}
	files := []advancedChatSkillPackageFileResponse{}
	err = filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, err := normalizeAdvancedChatSkillPackagePath(rel); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, advancedChatSkillPackageFileResponse{
			Path:    rel,
			Size:    info.Size(),
			Skill:   skillPaths[rel],
			ModTime: info.ModTime().Format(time.RFC3339),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		leftSkill := files[i].Skill
		rightSkill := files[j].Skill
		if leftSkill != rightSkill {
			return leftSkill
		}
		return files[i].Path < files[j].Path
	})
	return files, nil
}

func readAdvancedChatSkillPackageFilePreview(pkg AdvancedChatSkillPackage, relativePath string, maxBytes int) (string, bool, error) {
	if maxBytes <= 0 {
		maxBytes = advancedChatSkillResourceMaxBytes
	}
	relativePath, err := normalizeAdvancedChatSkillPackagePath(relativePath)
	if err != nil {
		return "", false, err
	}
	root, err := advancedChatStorageAbsPath(pkg.StoragePath)
	if err != nil {
		return "", false, err
	}
	target := filepath.Join(root, filepath.FromSlash(relativePath))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", false, errors.New("skill file path escapes package root")
	}
	file, err := os.Open(target)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return "", false, err
	}
	truncated := len(data) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}
	return string(data), truncated, nil
}

func removeAdvancedChatStorageDir(relativePath string) error {
	if strings.TrimSpace(relativePath) == "" {
		return nil
	}
	target, err := advancedChatStorageAbsPath(relativePath)
	if err != nil {
		return err
	}
	return os.RemoveAll(target)
}

func advancedChatSkillPackageStorageUsedBytes(userID uint) int64 {
	var used int64
	if err := model.DB.Model(&AdvancedChatSkillPackage{}).
		Where("user_id = ?", userID).
		Select("COALESCE(SUM(size), 0)").
		Scan(&used).Error; err != nil {
		return 0
	}
	return used
}

func advancedChatUploadedSkillsSyncPayload(userID uint) (map[string]interface{}, error) {
	var packages []AdvancedChatSkillPackage
	if err := model.DB.Where("user_id = ? AND status = ?", userID, "ready").Order("created_at ASC").Find(&packages).Error; err != nil {
		return nil, err
	}
	if len(packages) == 0 {
		return map[string]interface{}{"packages": []advancedChatUploadedSkillSyncPackage{}}, nil
	}
	packageIDs := make([]string, 0, len(packages))
	for _, pkg := range packages {
		packageIDs = append(packageIDs, pkg.ID)
	}
	var skills []AdvancedChatPackagedSkill
	if err := model.DB.Where("user_id = ? AND enabled = ? AND package_id IN ?", userID, true, packageIDs).Order("created_at ASC").Find(&skills).Error; err != nil {
		return nil, err
	}
	skillsByPackage := map[string][]AdvancedChatPackagedSkill{}
	for _, skill := range skills {
		skillsByPackage[skill.PackageID] = append(skillsByPackage[skill.PackageID], skill)
	}
	result := make([]advancedChatUploadedSkillSyncPackage, 0, len(packages))
	for _, pkg := range packages {
		files, err := advancedChatSkillPackageSyncFiles(pkg)
		if err != nil {
			return nil, err
		}
		syncSkills := make([]advancedChatUploadedSkillSyncSkill, 0, len(skillsByPackage[pkg.ID]))
		for _, skill := range skillsByPackage[pkg.ID] {
			resources, _ := listAdvancedChatPackagedSkillResources(pkg, skill)
			syncSkills = append(syncSkills, advancedChatUploadedSkillSyncSkill{
				ID:          skill.ID,
				Name:        skill.Name,
				Description: skill.Description,
				SkillPath:   skill.SkillPath,
				RootPath:    skill.RootPath,
				Resources:   resources,
			})
		}
		result = append(result, advancedChatUploadedSkillSyncPackage{
			ID:     pkg.ID,
			Hash:   pkg.Hash,
			Files:  files,
			Skills: syncSkills,
		})
	}
	return map[string]interface{}{"packages": result}, nil
}

func advancedChatSkillPackageSyncFiles(pkg AdvancedChatSkillPackage) ([]advancedChatUploadedSkillSyncFile, error) {
	root, err := advancedChatStorageAbsPath(pkg.StoragePath)
	if err != nil {
		return nil, err
	}
	files := []advancedChatUploadedSkillSyncFile{}
	totalBytes := int64(0)
	err = filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, err := normalizeAdvancedChatSkillPackagePath(rel); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > advancedChatSkillPackageMaxFileBytes {
			return fmt.Errorf("skill package file is too large: %s", rel)
		}
		totalBytes += info.Size()
		if totalBytes > advancedChatSkillPackageMaxTotalBytes {
			return errors.New("skill package is too large")
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		files = append(files, advancedChatUploadedSkillSyncFile{
			Path:    rel,
			Content: base64.StdEncoding.EncodeToString(data),
		})
		if len(files) > advancedChatSkillPackageMaxFiles {
			return errors.New("skill package contains too many files")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func advancedChatSkillCatalogPrompt(skills []advancedChatRuntimeSkill, workspaceSkills []advancedChatWorkspaceSkill) string {
	items := []string{}
	for _, skill := range skills {
		if strings.TrimSpace(skill.ID) == "" || strings.TrimSpace(skill.Name) == "" {
			continue
		}
		items = append(items, fmt.Sprintf(`<skill id="%s" source="%s">
  <name>%s</name>
  <description>%s</description>
</skill>`, xmlEscape(skill.ID), xmlEscape(firstNonEmptyText(skill.Source, advancedChatSkillSourceUploaded)), xmlEscape(skill.Name), xmlEscape(skill.Description)))
	}
	for _, skill := range workspaceSkills {
		id := strings.TrimSpace(skill.ID)
		if id == "" {
			id = skill.Path
		}
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			name = id
		}
		description := connectorSkillDescription(skill)
		items = append(items, fmt.Sprintf(`<skill id="%s" source="%s">
  <name>%s</name>
  <description>%s</description>
</skill>`, xmlEscape("connector:"+id), advancedChatSkillSourceConnector, xmlEscape(name), xmlEscape(description)))
	}
	if len(items) == 0 {
		return ""
	}
	return strings.TrimSpace(`Available Skills are listed below. Each Skill is only a directory entry here: name and description only.
Read this directory and decide whether a Skill is relevant to the current task. If a Skill is relevant, call activate_skill with its id before relying on that Skill's instructions. Use read_skill_resource only after activate_skill shows the resource list.

<available_skills>
` + strings.Join(items, "\n") + `
</available_skills>`)
}

func advancedChatSkillTools(hasCatalog bool) []ChatExecutorTool {
	if !hasCatalog {
		return nil
	}
	return []ChatExecutorTool{
		{
			Name:        advancedChatActivateSkillToolName,
			Description: "Load the full SKILL.md instructions and resource listing for a selected Skill from the available_skills directory.",
			Schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"skill_id": map[string]interface{}{"type": "string", "description": "Skill id from available_skills."},
				},
				"required": []string{"skill_id"},
			},
		},
		{
			Name:        advancedChatReadSkillResourceToolName,
			Description: "Read a bundled resource file from an activated uploaded Skill by relative path.",
			Schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"skill_id":  map[string]interface{}{"type": "string", "description": "Uploaded Skill id from available_skills."},
					"path":      map[string]interface{}{"type": "string", "description": "Resource path relative to the Skill root, such as references/api.md or scripts/tool.py."},
					"max_bytes": map[string]interface{}{"type": "integer", "minimum": 1, "maximum": advancedChatSkillResourceMaxBytes},
				},
				"required": []string{"skill_id", "path"},
			},
		},
	}
}

func activateAdvancedChatSkill(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice, workspacePath string, workspaceSkills []advancedChatWorkspaceSkill, arguments map[string]interface{}) (string, error) {
	skillID := strings.TrimSpace(stringMapValue(arguments, "skill_id"))
	if skillID == "" {
		return "", errors.New("skill_id is required")
	}
	if strings.HasPrefix(skillID, "connector:") {
		localID := strings.TrimPrefix(skillID, "connector:")
		for _, skill := range workspaceSkills {
			if localID == skill.ID || localID == skill.Path {
				if strings.TrimSpace(skill.Content) == "" {
					loaded, err := readAdvancedChatWorkspaceSkillForRun(ctx, userID, device, workspacePath, skill)
					if err != nil {
						return "", err
					}
					skill = loaded
				}
				return formatActivatedConnectorSkill(skill), nil
			}
		}
		return "", errors.New("connector skill not found")
	}
	skill, pkg, err := loadAdvancedChatPackagedSkillForUse(userID, skillID)
	if err != nil {
		return "", err
	}
	content, err := readAdvancedChatPackagedSkillFile(pkg, skill.SkillPath, advancedChatSkillResourceMaxBytes)
	if err != nil {
		return "", err
	}
	resources, _ := listAdvancedChatPackagedSkillResources(pkg, skill)
	return formatActivatedPackagedSkill(skill, content, resources), nil
}

func readAdvancedChatSkillResource(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice, workspacePath string, workspaceSkills []advancedChatWorkspaceSkill, arguments map[string]interface{}) (string, error) {
	skillID := strings.TrimSpace(stringMapValue(arguments, "skill_id"))
	resourcePath := strings.TrimSpace(stringMapValue(arguments, "path"))
	maxBytes := intFromArguments(arguments, "max_bytes", advancedChatSkillResourceMaxBytes)
	if maxBytes <= 0 || maxBytes > advancedChatSkillResourceMaxBytes {
		maxBytes = advancedChatSkillResourceMaxBytes
	}
	if strings.HasPrefix(skillID, "connector:") {
		localID := strings.TrimPrefix(skillID, "connector:")
		for _, skill := range workspaceSkills {
			if localID == skill.ID || localID == skill.Path {
				return readAdvancedChatWorkspaceSkillResourceForRun(ctx, userID, device, workspacePath, skill, resourcePath, maxBytes)
			}
		}
		return "", errors.New("connector skill not found")
	}
	skill, pkg, err := loadAdvancedChatPackagedSkillForUse(userID, skillID)
	if err != nil {
		return "", err
	}
	resourcePath, err = normalizeAdvancedChatSkillPackagePath(resourcePath)
	if err != nil {
		return "", err
	}
	base := strings.Trim(strings.TrimSpace(skill.RootPath), "/")
	if base != "" {
		resourcePath = path.Join(base, resourcePath)
	}
	if resourcePath == skill.SkillPath {
		return "", errors.New("use activate_skill to read SKILL.md")
	}
	content, err := readAdvancedChatPackagedSkillFile(pkg, resourcePath, maxBytes)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("<skill_resource skill_id=%q path=%q>\n%s\n</skill_resource>", skill.ID, resourcePath, content), nil
}

func loadAdvancedChatPackagedSkillForUse(userID uint, skillID string) (AdvancedChatPackagedSkill, AdvancedChatSkillPackage, error) {
	var skill AdvancedChatPackagedSkill
	if err := model.DB.Where("id = ? AND user_id = ? AND enabled = ?", strings.TrimSpace(skillID), userID, true).First(&skill).Error; err != nil {
		return AdvancedChatPackagedSkill{}, AdvancedChatSkillPackage{}, errors.New("skill not found")
	}
	var pkg AdvancedChatSkillPackage
	if err := model.DB.Where("id = ? AND user_id = ? AND status = ?", skill.PackageID, userID, "ready").First(&pkg).Error; err != nil {
		return AdvancedChatPackagedSkill{}, AdvancedChatSkillPackage{}, errors.New("skill package not found")
	}
	return skill, pkg, nil
}

func readAdvancedChatPackagedSkillFile(pkg AdvancedChatSkillPackage, relativePath string, maxBytes int) (string, error) {
	relativePath, err := normalizeAdvancedChatSkillPackagePath(relativePath)
	if err != nil {
		return "", err
	}
	root, err := advancedChatStorageAbsPath(pkg.StoragePath)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(relativePath))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", errors.New("skill file path escapes package root")
	}
	file, err := os.Open(target)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return "", err
	}
	text := string(data)
	if len(data) > maxBytes {
		text = string(data[:maxBytes]) + "\n...(truncated)"
	}
	return text, nil
}

func listAdvancedChatPackagedSkillResources(pkg AdvancedChatSkillPackage, skill AdvancedChatPackagedSkill) ([]string, error) {
	root, err := advancedChatStorageAbsPath(pkg.StoragePath)
	if err != nil {
		return nil, err
	}
	base := root
	if strings.TrimSpace(skill.RootPath) != "" {
		base = filepath.Join(root, filepath.FromSlash(skill.RootPath))
	}
	resources := []string{}
	err = filepath.WalkDir(base, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(base, filePath)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.EqualFold(rel, "SKILL.md") {
			return nil
		}
		resources = append(resources, rel)
		if len(resources) >= 100 {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(resources)
	return resources, err
}

func formatActivatedPackagedSkill(skill AdvancedChatPackagedSkill, content string, resources []string) string {
	resourceLines := []string{}
	for _, resource := range resources {
		resourceLines = append(resourceLines, "  <file>"+xmlEscape(resource)+"</file>")
	}
	return fmt.Sprintf(`<skill_content id="%s" name="%s" source="%s">
%s
<skill_resources>
%s
</skill_resources>
</skill_content>`, xmlEscape(skill.ID), xmlEscape(skill.Name), xmlEscape(skill.Source), strings.TrimSpace(content), strings.Join(resourceLines, "\n"))
}

func formatActivatedConnectorSkill(skill advancedChatWorkspaceSkill) string {
	note := ""
	if skill.Truncated {
		note = "\nNote: this connector skill file was truncated by the connector size limit."
	}
	resourceLines := []string{}
	for _, resource := range skill.Resources {
		resourceLines = append(resourceLines, "  <file>"+xmlEscape(resource)+"</file>")
	}
	return fmt.Sprintf(`<skill_content id="%s" name="%s" source="%s" path="%s">%s
%s
<skill_resources>
%s
</skill_resources>
</skill_content>`, xmlEscape("connector:"+firstNonEmptyText(skill.ID, skill.Path)), xmlEscape(skill.Name), advancedChatSkillSourceConnector, xmlEscape(skill.Path), note, strings.TrimSpace(skill.Content), strings.Join(resourceLines, "\n"))
}

func connectorSkillDescription(skill advancedChatWorkspaceSkill) string {
	manifest, _, _, _, ok := parseAdvancedChatSkillManifest(skill.Content)
	if ok && strings.TrimSpace(manifest.Description) != "" {
		return strings.TrimSpace(manifest.Description)
	}
	if strings.TrimSpace(skill.Path) != "" {
		return "Local connector skill from " + skill.Path
	}
	return "Local connector skill."
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(strings.TrimSpace(value))
}

func intFromArguments(arguments map[string]interface{}, key string, fallback int) int {
	value, ok := arguments[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return fallback
}

func truncateRunes(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return value
}
