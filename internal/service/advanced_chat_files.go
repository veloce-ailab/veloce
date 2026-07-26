package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/veloce-ailab/veloce/internal/config"
	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	advancedChatFileTextExtractMaxChars  = 100000
	advancedChatFileMessageMaxChars      = 20000
	advancedChatVisionAttachmentMaxBytes = 20 << 20
	advancedChatGeneratedAssetTimeout    = 2 * time.Minute
)

var (
	errAdvancedChatFileStorageDisabled  = errors.New("agent chat file storage disabled")
	errAdvancedChatFileInsufficient     = errors.New("agent chat file storage quota exceeded")
	advancedChatAttachmentFileIDPattern = regexp.MustCompile(`file_id=(acf-[A-Za-z0-9_-]+)`)
)

type AdvancedChatFile struct {
	ID          string     `gorm:"primaryKey;size:80" json:"id"`
	UserID      uint       `gorm:"index;uniqueIndex:idx_advanced_chat_file_user_source;not null" json:"user_id"`
	User        model.User `gorm:"foreignKey:UserID" json:"-"`
	Name        string     `gorm:"size:255;not null" json:"name"`
	MIMEType    string     `gorm:"size:120;not null" json:"mime_type"`
	Size        int64      `gorm:"not null" json:"size"`
	Data        []byte     `gorm:"not null" json:"-"`
	StoragePath string     `gorm:"type:text;not null;default:''" json:"-"`
	TextExtract string     `gorm:"type:text;not null" json:"-"`
	Hash        string     `gorm:"index;size:64;not null" json:"-"`
	Source      string     `gorm:"size:60;not null" json:"source"`
	SourceKey   string     `gorm:"size:180;uniqueIndex:idx_advanced_chat_file_user_source;not null" json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type advancedChatFileResponse struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	Size          int64     `json:"size"`
	Source        string    `json:"source"`
	TextAvailable bool      `json:"text_available"`
	DownloadURL   string    `json:"download_url"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type advancedChatFileContentResponse struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
}

type advancedChatFileStoreInput struct {
	Name               string
	MIMEType           string
	Data               []byte
	Source             string
	SourceKey          string
	RequireAllowedType bool
}

type generatedAssetCandidate struct {
	Name       string
	MIMEType   string
	URL        string
	Base64Data string
}

func (api *advancedChatAPI) listFiles(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !advancedChatFileStorageEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "File storage is disabled"})
		return
	}

	var files []AdvancedChatFile
	if err := model.DB.Where("user_id = ? AND source <> ?", user.ID, advancedChatKnowledgeDocumentSource).Order("created_at DESC").Find(&files).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list files"})
		return
	}
	result := make([]advancedChatFileResponse, 0, len(files))
	for _, file := range files {
		result = append(result, advancedChatFileResponseFromModel(file))
	}
	c.JSON(http.StatusOK, gin.H{
		"files":           result,
		"used_bytes":      advancedChatFileStorageUsedBytes(user.ID),
		"total_bytes":     advancedChatFileStorageTotalBytes(),
		"remaining_bytes": advancedChatFileStorageRemainingBytes(user.ID),
	})
}

func (api *advancedChatAPI) uploadFile(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !advancedChatFileStorageEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "File storage is disabled"})
		return
	}
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid multipart form"})
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File is required"})
		return
	}
	maxBytes := int64(advancedChatAttachmentMaxMB()) * 1024 * 1024
	if fileHeader.Size > maxBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File is too large"})
		return
	}
	source, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to open file"})
		return
	}
	defer source.Close()
	data, err := io.ReadAll(io.LimitReader(source, maxBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read file"})
		return
	}
	if int64(len(data)) > maxBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File is too large"})
		return
	}

	contentType := fileHeader.Header.Get("Content-Type")
	file, status, message, err := storeAdvancedChatFile(user.ID, advancedChatFileStoreInput{
		Name:               fileHeader.Filename,
		MIMEType:           contentType,
		Data:               data,
		Source:             "upload",
		RequireAllowedType: true,
	})
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"file":            advancedChatFileResponseFromModel(file),
		"content":         advancedChatFileContentFromModel(file),
		"used_bytes":      advancedChatFileStorageUsedBytes(user.ID),
		"total_bytes":     advancedChatFileStorageTotalBytes(),
		"remaining_bytes": advancedChatFileStorageRemainingBytes(user.ID),
	})
}

func (api *advancedChatAPI) getFileContent(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !advancedChatFileStorageEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "File storage is disabled"})
		return
	}
	var file AdvancedChatFile
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).First(&file).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found"})
		return
	}
	c.JSON(http.StatusOK, advancedChatFileContentFromModel(file))
}

func (api *advancedChatAPI) downloadFile(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !advancedChatFileStorageEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "File storage is disabled"})
		return
	}
	var file AdvancedChatFile
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).First(&file).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found"})
		return
	}
	name := strings.ReplaceAll(file.Name, `"`, "")
	if name == "" {
		name = "file"
	}
	c.Header("Content-Disposition", `attachment; filename="`+name+`"; filename*=UTF-8''`+url.PathEscape(file.Name))
	data, err := advancedChatFileData(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file"})
		return
	}
	c.Data(http.StatusOK, file.MIMEType, data)
}

func (api *advancedChatAPI) deleteFile(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !advancedChatFileStorageEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "File storage is disabled"})
		return
	}
	var file AdvancedChatFile
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).First(&file).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found"})
		return
	}
	if file.Source == advancedChatKnowledgeDocumentSource {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Knowledge base documents must be deleted from their knowledge base"})
		return
	}
	if err := model.DB.Where("id = ? AND user_id = ?", file.ID, user.ID).Delete(&AdvancedChatFile{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete file"})
		return
	}
	_ = removeAdvancedChatStoragePath(file.StoragePath)
	c.JSON(http.StatusOK, gin.H{
		"message":         "File deleted",
		"used_bytes":      advancedChatFileStorageUsedBytes(user.ID),
		"total_bytes":     advancedChatFileStorageTotalBytes(),
		"remaining_bytes": advancedChatFileStorageRemainingBytes(user.ID),
	})
}

func storeAdvancedChatFile(userID uint, input advancedChatFileStoreInput) (AdvancedChatFile, int, string, error) {
	if !advancedChatFileStorageEnabled() {
		return AdvancedChatFile{}, http.StatusForbidden, "File storage is disabled", errAdvancedChatFileStorageDisabled
	}
	if len(input.Data) == 0 {
		return AdvancedChatFile{}, http.StatusBadRequest, "File is empty", errors.New("empty file")
	}
	name := sanitizeAdvancedChatFileName(input.Name, input.MIMEType)
	mimeType := normalizeAdvancedChatFileMIME(input.MIMEType, name, input.Data)
	if input.RequireAllowedType && !advancedChatMIMEAllowed(mimeType, advancedChatAttachmentAllowedTypes()) {
		return AdvancedChatFile{}, http.StatusBadRequest, "File type is not allowed", errors.New("file type blocked")
	}
	size := int64(len(input.Data))
	storageLimit := advancedChatFileStorageTotalBytes()
	if size > storageLimit {
		return AdvancedChatFile{}, http.StatusRequestEntityTooLarge, "File exceeds storage quota", errAdvancedChatFileInsufficient
	}

	id := newAdvancedChatID("acf")
	source := strings.TrimSpace(input.Source)
	if source == "" {
		source = "upload"
	}
	sourceKey := strings.TrimSpace(input.SourceKey)
	if sourceKey == "" {
		sourceKey = source + ":" + id
	}
	hash := sha256.Sum256(input.Data)
	storagePath := advancedChatFileStoragePath(userID, id, name)
	file := AdvancedChatFile{
		ID:          id,
		UserID:      userID,
		Name:        name,
		MIMEType:    mimeType,
		Size:        size,
		Data:        []byte{},
		StoragePath: storagePath,
		TextExtract: advancedChatFileTextExtract(input.Data, mimeType, name),
		Hash:        hex.EncodeToString(hash[:]),
		Source:      source,
		SourceKey:   sourceKey,
	}

	tempPath, err := writeAdvancedChatStorageFileTemp(storagePath, input.Data)
	if err != nil {
		return AdvancedChatFile{}, http.StatusInternalServerError, "Failed to save file", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()

	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var existing AdvancedChatFile
		if err := tx.Where("user_id = ? AND source_key = ?", userID, sourceKey).First(&existing).Error; err == nil {
			file = existing
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var used int64
		if err := tx.Model(&AdvancedChatFile{}).
			Where("user_id = ?", userID).
			Select("COALESCE(SUM(size), 0)").
			Scan(&used).Error; err != nil {
			return err
		}
		var packageUsed int64
		if err := tx.Model(&AdvancedChatSkillPackage{}).
			Where("user_id = ?", userID).
			Select("COALESCE(SUM(size), 0)").
			Scan(&packageUsed).Error; err != nil {
			return err
		}
		if used+packageUsed+size > storageLimit {
			return errAdvancedChatFileInsufficient
		}
		return tx.Create(&file).Error
	})
	if err != nil {
		if errors.Is(err, errAdvancedChatFileInsufficient) {
			return AdvancedChatFile{}, http.StatusPaymentRequired, "Not enough file storage space", err
		}
		if isAdvancedChatUniqueConstraintError(err) {
			var existing AdvancedChatFile
			if loadErr := model.DB.Where("user_id = ? AND source_key = ?", userID, sourceKey).First(&existing).Error; loadErr == nil {
				return existing, http.StatusOK, "", nil
			}
		}
		return AdvancedChatFile{}, http.StatusInternalServerError, "Failed to save file", err
	}
	if file.ID == id {
		if err := commitAdvancedChatStorageFile(tempPath, storagePath); err != nil {
			_ = model.DB.Where("id = ? AND user_id = ?", file.ID, userID).Delete(&AdvancedChatFile{}).Error
			return AdvancedChatFile{}, http.StatusInternalServerError, "Failed to save file", err
		}
		committed = true
	} else {
		_ = os.Remove(tempPath)
		committed = true
	}
	return file, http.StatusOK, "", nil
}

func advancedChatFileResponseFromModel(file AdvancedChatFile) advancedChatFileResponse {
	return advancedChatFileResponse{
		ID:            file.ID,
		Name:          file.Name,
		Type:          file.MIMEType,
		Size:          file.Size,
		Source:        file.Source,
		TextAvailable: strings.TrimSpace(file.TextExtract) != "",
		DownloadURL:   "/api/user/advanced-chat/files/" + url.PathEscape(file.ID) + "/download",
		CreatedAt:     file.CreatedAt,
		UpdatedAt:     file.UpdatedAt,
	}
}

func advancedChatFileContentFromModel(file AdvancedChatFile) advancedChatFileContentResponse {
	text := file.TextExtract
	truncated := false
	if len([]rune(text)) > advancedChatFileMessageMaxChars {
		text = string([]rune(text)[:advancedChatFileMessageMaxChars])
		truncated = true
	}
	return advancedChatFileContentResponse{
		ID:        file.ID,
		Text:      text,
		Binary:    strings.TrimSpace(file.TextExtract) == "",
		Truncated: truncated || len([]rune(file.TextExtract)) >= advancedChatFileTextExtractMaxChars,
	}
}

// ReadAdvancedChatFileData is used after enterprise shared-pool authorization
// has been evaluated by the enterprise API.
func ReadAdvancedChatFileData(file AdvancedChatFile) ([]byte, error) {
	return advancedChatFileData(file)
}

// StoreAdvancedChatPoolFile stores a file under an internal pool resource
// owner. Authorization is deliberately evaluated by the enterprise API.
func StoreAdvancedChatPoolFile(ownerUserID uint, name, mimeType string, data []byte) (AdvancedChatFile, int, string, error) {
	return storeAdvancedChatFile(ownerUserID, advancedChatFileStoreInput{
		Name:               name,
		MIMEType:           mimeType,
		Data:               data,
		Source:             "enterprise_pool",
		RequireAllowedType: true,
	})
}

func advancedChatExecutorMessage(userID uint, message advancedChatCompletionMessage) ChatExecutorMessage {
	executorMessage := ChatExecutorMessage{
		Role:    message.Role,
		Content: message.Content,
	}
	if message.Role != "user" || strings.TrimSpace(message.Content) == "" {
		return executorMessage
	}
	imageParts := advancedChatImageAttachmentParts(userID, message.Content)
	if len(imageParts) == 0 {
		return executorMessage
	}
	parts := make([]ChatExecutorContentPart, 0, len(imageParts)+1)
	parts = append(parts, ChatExecutorContentPart{Type: "text", Text: message.Content})
	parts = append(parts, imageParts...)
	executorMessage.Parts = parts
	return executorMessage
}

func advancedChatImageAttachmentParts(userID uint, content string) []ChatExecutorContentPart {
	ids := advancedChatFileIDsFromContent(content)
	if len(ids) == 0 {
		return nil
	}
	var files []AdvancedChatFile
	if err := model.DB.Where("user_id = ? AND id IN ?", userID, ids).Find(&files).Error; err != nil {
		return nil
	}
	byID := map[string]AdvancedChatFile{}
	for _, file := range files {
		byID[file.ID] = file
	}
	parts := make([]ChatExecutorContentPart, 0, len(ids))
	for _, id := range ids {
		file, ok := byID[id]
		if !ok || !strings.HasPrefix(strings.ToLower(file.MIMEType), "image/") || file.Size > advancedChatVisionAttachmentMaxBytes {
			continue
		}
		data, err := advancedChatFileData(file)
		if err != nil || int64(len(data)) != file.Size {
			continue
		}
		parts = append(parts, ChatExecutorContentPart{
			Type:     "image",
			MIMEType: file.MIMEType,
			Data:     base64.StdEncoding.EncodeToString(data),
		})
	}
	return parts
}

func advancedChatFileIDsFromContent(content string) []string {
	matches := advancedChatAttachmentFileIDPattern.FindAllStringSubmatch(content, -1)
	ids := make([]string, 0, len(matches))
	seen := map[string]struct{}{}
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		id := strings.TrimSpace(match[1])
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func advancedChatFileStorageEnabled() bool {
	if !advancedChatPremiumFeaturesAvailable() {
		return false
	}
	return advancedChatSettingBool(advancedChatFileStorageEnabledKey, true)
}

func advancedChatFileStorageAutoSaveImagesEnabled() bool {
	if !advancedChatPremiumFeaturesAvailable() {
		return false
	}
	return advancedChatSettingBool(advancedChatFileStorageAutoSaveImagesEnabledKey, false)
}

func advancedChatFileStorageAutoSaveVideosEnabled() bool {
	if !advancedChatPremiumFeaturesAvailable() {
		return false
	}
	return advancedChatSettingBool(advancedChatFileStorageAutoSaveVideosEnabledKey, false)
}

func advancedChatFileStorageTotalMB() int {
	value, err := strconv.Atoi(strings.TrimSpace(model.GetSystemSetting(advancedChatFileStorageTotalMBKey, strconv.Itoa(advancedChatDefaultFileStorageTotalMB))))
	if err != nil || value < 1 {
		return advancedChatDefaultFileStorageTotalMB
	}
	if value > 102400 {
		return 102400
	}
	return value
}

func advancedChatFileStorageTotalBytes() int64 {
	return int64(advancedChatFileStorageTotalMB()) * 1024 * 1024
}

func advancedChatStorageRoot() string {
	root := strings.TrimSpace(config.DataPath)
	if root == "" {
		root = "data"
	}
	return filepath.Clean(root)
}

func advancedChatStorageAbsPath(relativePath string) (string, error) {
	relativePath = strings.TrimSpace(strings.ReplaceAll(relativePath, "\\", "/"))
	if relativePath == "" || strings.HasPrefix(relativePath, "/") || strings.Contains(relativePath, "\x00") {
		return "", errors.New("invalid storage path")
	}
	cleaned := path.Clean("/" + relativePath)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" || cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("invalid storage path")
	}
	root, err := filepath.Abs(advancedChatStorageRoot())
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(cleaned)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", errors.New("storage path escapes data root")
	}
	return target, nil
}

func advancedChatFileStoragePath(userID uint, fileID string, name string) string {
	return path.Join("advanced-chat", "files", strconv.FormatUint(uint64(userID), 10), fileID, sanitizeAdvancedChatFileName(name, ""))
}

func writeAdvancedChatStorageFileTemp(relativePath string, data []byte) (string, error) {
	target, err := advancedChatStorageAbsPath(relativePath)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	return tempPath, nil
}

func commitAdvancedChatStorageFile(tempPath string, relativePath string) error {
	target, err := advancedChatStorageAbsPath(relativePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tempPath, target); err != nil {
		return err
	}
	return nil
}

func advancedChatFileData(file AdvancedChatFile) ([]byte, error) {
	if strings.TrimSpace(file.StoragePath) != "" {
		target, err := advancedChatStorageAbsPath(file.StoragePath)
		if err != nil {
			return nil, err
		}
		return os.ReadFile(target)
	}
	return file.Data, nil
}

func removeAdvancedChatStoragePath(relativePath string) error {
	if strings.TrimSpace(relativePath) == "" {
		return nil
	}
	target, err := advancedChatStorageAbsPath(relativePath)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = os.Remove(filepath.Dir(target))
	return nil
}

func advancedChatFileStorageUsedBytes(userID uint) int64 {
	used, err := advancedChatFileStorageUsedBytesWithDB(model.DB, userID)
	if err != nil {
		return 0
	}
	return used + ApplyAdvancedChatStorageUsageHooks(userID)
}

func advancedChatFileStorageUsedBytesWithDB(db *gorm.DB, userID uint) (int64, error) {
	if db == nil {
		return 0, errors.New("database is not initialized")
	}
	var used int64
	if err := db.Model(&AdvancedChatFile{}).
		Where("user_id = ?", userID).
		Select("COALESCE(SUM(size), 0)").
		Scan(&used).Error; err != nil {
		return 0, err
	}
	var packageUsed int64
	if err := db.Model(&AdvancedChatSkillPackage{}).
		Where("user_id = ?", userID).
		Select("COALESCE(SUM(size), 0)").
		Scan(&packageUsed).Error; err != nil {
		return 0, err
	}
	hookUsed, err := ApplyAdvancedChatStorageUsageWithDBHooks(db, userID)
	if err != nil {
		return 0, err
	}
	return used + packageUsed + hookUsed, nil
}

func advancedChatFileStorageRemainingBytes(userID uint) int64 {
	remaining := advancedChatFileStorageTotalBytes() - advancedChatFileStorageUsedBytes(userID)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func AdvancedChatFileStorageUsedBytes(userID uint) int64 {
	return advancedChatFileStorageUsedBytes(userID)
}

func AdvancedChatFileStorageUsedBytesWithDB(db *gorm.DB, userID uint) (int64, error) {
	return advancedChatFileStorageUsedBytesWithDB(db, userID)
}

func AdvancedChatFileStorageTotalBytes() int64 {
	return advancedChatFileStorageTotalBytes()
}

func AdvancedChatFileStorageRemainingBytes(userID uint) int64 {
	return advancedChatFileStorageRemainingBytes(userID)
}

func sanitizeAdvancedChatFileName(raw string, mimeType string) string {
	name := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	name = path.Base(name)
	name = strings.Trim(name, ". ")
	if name == "" || name == "/" {
		name = "file" + extensionForMIME(mimeType)
	}
	if len([]rune(name)) > 200 {
		ext := path.Ext(name)
		base := strings.TrimSuffix(name, ext)
		baseRunes := []rune(base)
		maxBase := 200 - len([]rune(ext))
		if maxBase < 1 {
			maxBase = 1
		}
		if len(baseRunes) > maxBase {
			base = string(baseRunes[:maxBase])
		}
		name = base + ext
	}
	return name
}

func normalizeAdvancedChatFileMIME(raw string, name string, data []byte) string {
	if mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(raw)); err == nil && mediaType != "" && mediaType != "application/octet-stream" {
		return strings.ToLower(mediaType)
	}
	if extType := mime.TypeByExtension(strings.ToLower(path.Ext(name))); extType != "" {
		if mediaType, _, err := mime.ParseMediaType(extType); err == nil && mediaType != "" {
			return strings.ToLower(mediaType)
		}
	}
	if len(data) > 0 {
		return strings.ToLower(http.DetectContentType(data[:minInt(len(data), 512)]))
	}
	return "application/octet-stream"
}

func advancedChatMIMEAllowed(mimeType string, allowedTypes []string) bool {
	for _, raw := range allowedTypes {
		allowed := strings.ToLower(strings.TrimSpace(raw))
		if allowed == "*/*" || allowed == mimeType {
			return true
		}
		if strings.HasSuffix(allowed, "/*") && strings.HasPrefix(mimeType, strings.TrimSuffix(allowed, "*")) {
			return true
		}
	}
	return false
}

func advancedChatFileTextExtract(data []byte, mimeType string, name string) string {
	if !advancedChatFileTextLike(mimeType, name) || !utf8.Valid(data) {
		return ""
	}
	text := string(data)
	if len([]rune(text)) > advancedChatFileTextExtractMaxChars {
		return string([]rune(text)[:advancedChatFileTextExtractMaxChars])
	}
	return text
}

func advancedChatFileTextLike(mimeType string, name string) bool {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if strings.HasPrefix(mimeType, "text/") {
		return true
	}
	switch mimeType {
	case "application/json", "application/xml", "application/javascript", "application/x-yaml":
		return true
	}
	return regexpTextFileExt(name)
}

func regexpTextFileExt(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".txt", ".json", ".csv", ".xml", ".yaml", ".yml", ".log", ".ini", ".toml":
		return true
	default:
		return false
	}
}

func extensionForMIME(mimeType string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(mimeType))
	if err != nil || mediaType == "" {
		return ""
	}
	extensions, err := mime.ExtensionsByType(mediaType)
	if err != nil || len(extensions) == 0 {
		return ""
	}
	return extensions[0]
}

func autoSaveAdvancedChatGeneratedAsset(ctx context.Context, input GeneratedAssetInput) {
	if input.UserID == 0 || !advancedChatFileStorageEnabled() {
		return
	}
	kind := strings.ToLower(strings.TrimSpace(input.Kind))
	switch kind {
	case "image":
		if !advancedChatFileStorageAutoSaveImagesEnabled() {
			return
		}
	case "video":
		if !advancedChatFileStorageAutoSaveVideosEnabled() {
			return
		}
	default:
		return
	}
	responseData := input.ResponseData
	if responseData == nil && len(input.ResponseBody) > 0 {
		_ = json.Unmarshal(input.ResponseBody, &responseData)
	}
	candidates := generatedAssetCandidates(kind, responseData)
	if len(candidates) == 0 {
		return
	}
	go func() {
		workCtx, cancel := context.WithTimeout(context.Background(), advancedChatGeneratedAssetTimeout)
		defer cancel()
		for index, candidate := range candidates {
			if err := saveGeneratedAdvancedChatFile(workCtx, input.UserID, kind, input.Source, input.ModelName, index, candidate); err != nil {
				log.Printf("agent chat generated asset auto-save skipped: user_id=%d kind=%s error=%v", input.UserID, kind, err)
			}
		}
		_ = ctx
	}()
}

func saveGeneratedAdvancedChatFile(ctx context.Context, userID uint, kind string, source string, modelName string, index int, candidate generatedAssetCandidate) error {
	var data []byte
	mimeType := candidate.MIMEType
	name := candidate.Name
	sourceKey := ""
	var err error
	if strings.TrimSpace(candidate.URL) != "" {
		sourceKey = generatedAssetSourceKey(kind, "url", candidate.URL)
		if advancedChatFileSourceKeyExists(userID, sourceKey) {
			return nil
		}
		data, mimeType, name, err = downloadGeneratedAdvancedChatFile(ctx, userID, candidate.URL, mimeType, name)
		if err != nil {
			return err
		}
	} else if strings.TrimSpace(candidate.Base64Data) != "" {
		data, mimeType, err = decodeGeneratedAssetData(candidate.Base64Data, mimeType)
		if err != nil {
			return err
		}
		sourceKey = generatedAssetSourceKey(kind, "data", hexHash(data))
		if advancedChatFileSourceKeyExists(userID, sourceKey) {
			return nil
		}
	} else {
		return errors.New("generated asset has no data")
	}
	mimeType = normalizeAdvancedChatFileMIME(mimeType, name, data)
	if kind == "image" && !strings.HasPrefix(mimeType, "image/") {
		return fmt.Errorf("generated asset is not an image: %s", mimeType)
	}
	if kind == "video" && !strings.HasPrefix(mimeType, "video/") {
		return fmt.Errorf("generated asset is not a video: %s", mimeType)
	}
	if strings.TrimSpace(name) == "" {
		name = generatedAssetName(kind, modelName, index, mimeType)
	} else {
		name = sanitizeAdvancedChatFileName(name, mimeType)
	}
	_, _, _, err = storeAdvancedChatFile(userID, advancedChatFileStoreInput{
		Name:      name,
		MIMEType:  mimeType,
		Data:      data,
		Source:    firstNonEmptyText(source, "generated_"+kind),
		SourceKey: sourceKey,
	})
	return err
}

// Generated assets are fetched from upstream-supplied URLs, so redirects must be
// re-validated instead of silently following them past the SSRF guard.
var generatedAssetDownloadClient = &http.Client{Timeout: 60 * time.Second, CheckRedirect: GuardedRedirectPolicy()}

func downloadGeneratedAdvancedChatFile(ctx context.Context, userID uint, rawURL string, mimeType string, name string) ([]byte, string, string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if err := ValidateOutboundHTTPURL(rawURL, CurrentURLGuardOptions()); err != nil {
		return nil, "", "", err
	}
	remaining := advancedChatFileStorageRemainingBytes(userID)
	if remaining <= 0 {
		return nil, "", "", errAdvancedChatFileInsufficient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	resp, err := generatedAssetDownloadClient.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, "", "", fmt.Errorf("download failed with HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, remaining+1))
	if err != nil {
		return nil, "", "", err
	}
	if int64(len(data)) > remaining {
		return nil, "", "", errAdvancedChatFileInsufficient
	}
	if mimeType == "" {
		mimeType = resp.Header.Get("Content-Type")
	}
	if name == "" {
		name = fileNameFromURL(rawURL)
	}
	return data, mimeType, name, nil
}

func decodeGeneratedAssetData(raw string, fallbackMIME string) ([]byte, string, error) {
	raw = strings.TrimSpace(raw)
	mimeType := fallbackMIME
	if strings.HasPrefix(raw, "data:") {
		head, body, ok := strings.Cut(raw, ",")
		if !ok {
			return nil, "", errors.New("invalid data URL")
		}
		if mediaType := strings.TrimPrefix(strings.Split(head, ";")[0], "data:"); mediaType != "" {
			mimeType = mediaType
		}
		raw = body
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil {
		return nil, "", err
	}
	return data, mimeType, nil
}

func generatedAssetCandidates(kind string, responseData map[string]interface{}) []generatedAssetCandidate {
	if responseData == nil {
		return nil
	}
	result := []generatedAssetCandidate{}
	seen := map[string]struct{}{}
	collectGeneratedAssetCandidates(kind, responseData, &result, seen, 0)
	return result
}

func collectGeneratedAssetCandidates(kind string, value interface{}, result *[]generatedAssetCandidate, seen map[string]struct{}, depth int) {
	if depth > 8 || value == nil {
		return
	}
	switch item := value.(type) {
	case []interface{}:
		for _, child := range item {
			collectGeneratedAssetCandidates(kind, child, result, seen, depth+1)
		}
	case map[string]interface{}:
		for _, candidate := range generatedAssetCandidatesFromMap(kind, item) {
			key := candidate.URL
			if key == "" {
				key = candidate.Base64Data
			}
			key = hexHash([]byte(key))
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			*result = append(*result, candidate)
		}
		for _, child := range item {
			switch child.(type) {
			case map[string]interface{}, []interface{}:
				collectGeneratedAssetCandidates(kind, child, result, seen, depth+1)
			}
		}
	}
}

func generatedAssetCandidatesFromMap(kind string, item map[string]interface{}) []generatedAssetCandidate {
	candidates := []generatedAssetCandidate{}
	mimeType := firstNonEmptyText(stringFromMap(item, "mime_type"), stringFromMap(item, "mimeType"), stringFromMap(item, "content_type"), stringFromMap(item, "contentType"))
	name := firstNonEmptyText(stringFromMap(item, "filename"), stringFromMap(item, "file_name"), stringFromMap(item, "name"))
	for _, key := range generatedAssetURLKeys(kind) {
		if raw := stringFromMap(item, key); raw != "" && validGeneratedAssetURL(raw) {
			candidates = append(candidates, generatedAssetCandidate{Name: firstNonEmptyText(name, fileNameFromURL(raw)), MIMEType: mimeType, URL: raw})
		}
	}
	for _, key := range generatedAssetBase64Keys(kind) {
		if raw := stringFromMap(item, key); raw != "" && !validGeneratedAssetURL(raw) && looksLikeGeneratedAssetData(raw) {
			candidates = append(candidates, generatedAssetCandidate{Name: name, MIMEType: mimeType, Base64Data: raw})
		}
	}
	return candidates
}

func generatedAssetURLKeys(kind string) []string {
	if kind == "video" {
		return []string{"url", "video", "video_url", "videoUrl", "output_url", "outputUrl"}
	}
	return []string{"url", "image", "image_url", "imageUrl", "output_url", "outputUrl"}
}

func generatedAssetBase64Keys(kind string) []string {
	if kind == "video" {
		return []string{"b64_json", "base64", "video_base64", "video", "output"}
	}
	return []string{"b64_json", "base64", "image_base64", "image", "output"}
}

func validGeneratedAssetURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func looksLikeGeneratedAssetData(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.HasPrefix(raw, "data:") || len(raw) > 100
}

func generatedAssetSourceKey(kind string, sourceType string, value string) string {
	hash := sha256.Sum256([]byte(value))
	return "generated:" + kind + ":" + sourceType + ":" + hex.EncodeToString(hash[:])
}

func advancedChatFileSourceKeyExists(userID uint, sourceKey string) bool {
	var count int64
	if err := model.DB.Model(&AdvancedChatFile{}).Where("user_id = ? AND source_key = ?", userID, sourceKey).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

func generatedAssetName(kind string, modelName string, index int, mimeType string) string {
	prefix := "generated-" + kind
	if cleaned := sanitizeGeneratedAssetNamePart(modelName); cleaned != "" {
		prefix += "-" + cleaned
	}
	if index > 0 {
		prefix += "-" + strconv.Itoa(index+1)
	}
	ext := extensionForMIME(mimeType)
	if ext == "" {
		if kind == "video" {
			ext = ".mp4"
		} else {
			ext = ".png"
		}
	}
	return prefix + ext
}

func sanitizeGeneratedAssetNamePart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			continue
		}
		if r == '-' || r == '_' {
			builder.WriteRune(r)
		}
	}
	result := strings.Trim(builder.String(), "-_")
	if len(result) > 40 {
		return result[:40]
	}
	return result
}

func fileNameFromURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	name := path.Base(parsed.Path)
	if name == "." || name == "/" {
		return ""
	}
	return name
}

func stringFromMap(item map[string]interface{}, key string) string {
	value, ok := item[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func hexHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
