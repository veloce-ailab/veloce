package service

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const advancedChatKnowledgeDocumentSource = "knowledge_base"

type AdvancedChatKnowledgeBase struct {
	ID                     string     `gorm:"primaryKey;size:80" json:"id"`
	UserID                 uint       `gorm:"uniqueIndex:idx_advanced_chat_knowledge_user_name;index;not null" json:"user_id"`
	User                   model.User `gorm:"foreignKey:UserID" json:"-"`
	Name                   string     `gorm:"uniqueIndex:idx_advanced_chat_knowledge_user_name;size:120;not null" json:"name"`
	Description            string     `gorm:"type:text;not null;default:''" json:"description"`
	EmbeddingModelName     string     `gorm:"size:100;not null;default:''" json:"embedding_model_name"`
	EmbeddingUserChannelID uint       `gorm:"index" json:"embedding_user_channel_id"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

type AdvancedChatKnowledgeDocument struct {
	ID              string     `gorm:"primaryKey;size:80" json:"id"`
	KnowledgeBaseID string     `gorm:"index;size:80;not null" json:"knowledge_base_id"`
	UserID          uint       `gorm:"index;not null" json:"user_id"`
	FileID          string     `gorm:"uniqueIndex;size:80;not null" json:"file_id"`
	Name            string     `gorm:"size:255;not null" json:"name"`
	MIMEType        string     `gorm:"size:120;not null" json:"mime_type"`
	Size            int64      `gorm:"not null" json:"size"`
	TextAvailable   bool       `gorm:"not null;default:false" json:"text_available"`
	EmbeddingStatus string     `gorm:"index;size:20;not null;default:'pending'" json:"embedding_status"`
	EmbeddingError  string     `gorm:"type:text;not null;default:''" json:"embedding_error,omitempty"`
	EmbeddingModel  string     `gorm:"size:120;not null;default:''" json:"embedding_model,omitempty"`
	EmbeddingDim    int        `gorm:"not null;default:0" json:"embedding_dimensions"`
	ChunkCount      int        `gorm:"not null;default:0" json:"chunk_count"`
	EmbeddedAt      *time.Time `json:"embedded_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type advancedChatKnowledgeBaseInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type advancedChatKnowledgeBaseResponse struct {
	ID                     string    `json:"id"`
	Name                   string    `json:"name"`
	Description            string    `json:"description"`
	DocumentCount          int       `json:"document_count"`
	StorageBytes           int64     `json:"storage_bytes"`
	EmbeddingModelName     string    `json:"embedding_model_name,omitempty"`
	EmbeddingUserChannelID uint      `json:"embedding_user_channel_id,omitempty"`
	Vectorized             bool      `json:"vectorized"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type advancedChatKnowledgeDocumentResponse struct {
	ID              string    `json:"id"`
	FileID          string    `json:"file_id"`
	Name            string    `json:"name"`
	Type            string    `json:"type"`
	Size            int64     `json:"size"`
	TextAvailable   bool      `json:"text_available"`
	EmbeddingStatus string    `json:"embedding_status"`
	EmbeddingError  string    `json:"embedding_error,omitempty"`
	ChunkCount      int       `json:"chunk_count"`
	DownloadURL     string    `json:"download_url"`
	CreatedAt       time.Time `json:"created_at"`
}

func (api *advancedChatAPI) listKnowledgeBases(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var bases []AdvancedChatKnowledgeBase
	if err := model.DB.Where("user_id = ?", user.ID).Order("updated_at DESC, created_at DESC").Find(&bases).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list knowledge bases"})
		return
	}
	var documents []AdvancedChatKnowledgeDocument
	if err := model.DB.Where("user_id = ?", user.ID).Find(&documents).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list knowledge documents"})
		return
	}
	counts := make(map[string]int)
	sizes := make(map[string]int64)
	for _, document := range documents {
		counts[document.KnowledgeBaseID]++
		sizes[document.KnowledgeBaseID] += document.Size
	}
	result := make([]advancedChatKnowledgeBaseResponse, 0, len(bases))
	for _, base := range bases {
		result = append(result, advancedChatKnowledgeBaseResponseFromModel(base, counts[base.ID], sizes[base.ID], advancedChatKnowledgeBaseIsVectorized(base)))
	}
	c.JSON(http.StatusOK, gin.H{"knowledge_bases": result})
}

func (api *advancedChatAPI) createKnowledgeBase(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatKnowledgeBaseInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	base, valid := advancedChatKnowledgeBaseFromInput(c, user.ID, input)
	if !valid {
		return
	}
	if err := model.DB.Create(&base).Error; err != nil {
		if isAdvancedChatUniqueConstraintError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "Knowledge base name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create knowledge base"})
		return
	}
	c.JSON(http.StatusOK, advancedChatKnowledgeBaseResponseFromModel(base, 0, 0, false))
}

func (api *advancedChatAPI) updateKnowledgeBase(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	base, found := loadAdvancedChatKnowledgeBase(c, user.ID, c.Param("id"))
	if !found {
		return
	}
	var input advancedChatKnowledgeBaseInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	next, valid := advancedChatKnowledgeBaseFromInput(c, user.ID, input)
	if !valid {
		return
	}
	if err := model.DB.Model(base).Updates(map[string]interface{}{"name": next.Name, "description": next.Description}).Error; err != nil {
		if isAdvancedChatUniqueConstraintError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "Knowledge base name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update knowledge base"})
		return
	}
	base.Name = next.Name
	base.Description = next.Description
	var documents []AdvancedChatKnowledgeDocument
	_ = model.DB.Where("knowledge_base_id = ? AND user_id = ?", base.ID, user.ID).Find(&documents).Error
	var size int64
	for _, document := range documents {
		size += document.Size
	}
	c.JSON(http.StatusOK, advancedChatKnowledgeBaseResponseFromModel(*base, len(documents), size, advancedChatKnowledgeBaseIsVectorized(*base)))
}

func (api *advancedChatAPI) deleteKnowledgeBase(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	base, found := loadAdvancedChatKnowledgeBase(c, user.ID, c.Param("id"))
	if !found {
		return
	}
	var documents []AdvancedChatKnowledgeDocument
	if err := model.DB.Where("knowledge_base_id = ? AND user_id = ?", base.ID, user.ID).Find(&documents).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load knowledge documents"})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("knowledge_base_id = ? AND user_id = ?", base.ID, user.ID).Delete(&AdvancedChatKnowledgeChunk{}).Error; err != nil {
			return err
		}
		if err := tx.Where("knowledge_base_id = ? AND user_id = ?", base.ID, user.ID).Delete(&AdvancedChatKnowledgeDocument{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ? AND user_id = ?", base.ID, user.ID).Delete(&AdvancedChatKnowledgeBase{}).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete knowledge base"})
		return
	}
	for _, document := range documents {
		deleteAdvancedChatKnowledgeFile(user.ID, document.FileID)
	}
	c.JSON(http.StatusOK, gin.H{"message": "Knowledge base deleted", "used_bytes": advancedChatFileStorageUsedBytes(user.ID), "remaining_bytes": advancedChatFileStorageRemainingBytes(user.ID)})
}

func (api *advancedChatAPI) listKnowledgeDocuments(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	base, found := loadAdvancedChatKnowledgeBase(c, user.ID, c.Param("id"))
	if !found {
		return
	}
	var documents []AdvancedChatKnowledgeDocument
	if err := model.DB.Where("knowledge_base_id = ? AND user_id = ?", base.ID, user.ID).Order("created_at DESC").Find(&documents).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list knowledge documents"})
		return
	}
	result := make([]advancedChatKnowledgeDocumentResponse, 0, len(documents))
	for _, document := range documents {
		result = append(result, advancedChatKnowledgeDocumentResponseFromModel(document))
	}
	c.JSON(http.StatusOK, gin.H{"documents": result, "used_bytes": advancedChatFileStorageUsedBytes(user.ID), "total_bytes": advancedChatFileStorageTotalBytes(), "remaining_bytes": advancedChatFileStorageRemainingBytes(user.ID)})
}

func (api *advancedChatAPI) uploadKnowledgeDocument(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	base, found := loadAdvancedChatKnowledgeBase(c, user.ID, c.Param("id"))
	if !found {
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
	if err != nil || int64(len(data)) > maxBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File is too large or could not be read"})
		return
	}

	documentID := newAdvancedChatID("akd")
	file, status, message, err := storeAdvancedChatFile(user.ID, advancedChatFileStoreInput{
		Name:               fileHeader.Filename,
		MIMEType:           fileHeader.Header.Get("Content-Type"),
		Data:               data,
		Source:             advancedChatKnowledgeDocumentSource,
		SourceKey:          "knowledge:" + base.ID + ":" + documentID,
		RequireAllowedType: true,
	})
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	document := AdvancedChatKnowledgeDocument{
		ID:              documentID,
		KnowledgeBaseID: base.ID,
		UserID:          user.ID,
		FileID:          file.ID,
		Name:            file.Name,
		MIMEType:        file.MIMEType,
		Size:            file.Size,
		TextAvailable:   strings.TrimSpace(file.TextExtract) != "",
		EmbeddingStatus: advancedChatKnowledgeEmbeddingPending,
	}
	if err := model.DB.Create(&document).Error; err != nil {
		deleteAdvancedChatKnowledgeFile(user.ID, file.ID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save knowledge document"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"document": advancedChatKnowledgeDocumentResponseFromModel(document), "used_bytes": advancedChatFileStorageUsedBytes(user.ID), "total_bytes": advancedChatFileStorageTotalBytes(), "remaining_bytes": advancedChatFileStorageRemainingBytes(user.ID)})
}

func (api *advancedChatAPI) deleteKnowledgeDocument(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	base, found := loadAdvancedChatKnowledgeBase(c, user.ID, c.Param("id"))
	if !found {
		return
	}
	var document AdvancedChatKnowledgeDocument
	if err := model.DB.Where("id = ? AND knowledge_base_id = ? AND user_id = ?", c.Param("document_id"), base.ID, user.ID).First(&document).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Knowledge document not found"})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("document_id = ? AND user_id = ?", document.ID, user.ID).Delete(&AdvancedChatKnowledgeChunk{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ? AND user_id = ?", document.ID, user.ID).Delete(&AdvancedChatKnowledgeDocument{}).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete knowledge document"})
		return
	}
	deleteAdvancedChatKnowledgeFile(user.ID, document.FileID)
	c.JSON(http.StatusOK, gin.H{"message": "Knowledge document deleted", "used_bytes": advancedChatFileStorageUsedBytes(user.ID), "remaining_bytes": advancedChatFileStorageRemainingBytes(user.ID)})
}

func advancedChatKnowledgeBaseFromInput(c *gin.Context, userID uint, input advancedChatKnowledgeBaseInput) (AdvancedChatKnowledgeBase, bool) {
	name := strings.TrimSpace(input.Name)
	description := strings.TrimSpace(input.Description)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Knowledge base name is required"})
		return AdvancedChatKnowledgeBase{}, false
	}
	if len([]rune(name)) > 120 || len([]rune(description)) > 5000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Knowledge base name or description is too long"})
		return AdvancedChatKnowledgeBase{}, false
	}
	return AdvancedChatKnowledgeBase{ID: newAdvancedChatID("akb"), UserID: userID, Name: name, Description: description}, true
}

func loadAdvancedChatKnowledgeBase(c *gin.Context, userID uint, id string) (*AdvancedChatKnowledgeBase, bool) {
	var base AdvancedChatKnowledgeBase
	if err := model.DB.Where("id = ? AND user_id = ?", strings.TrimSpace(id), userID).First(&base).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Knowledge base not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load knowledge base"})
		}
		return nil, false
	}
	return &base, true
}

func advancedChatKnowledgeBaseResponseFromModel(base AdvancedChatKnowledgeBase, documentCount int, storageBytes int64, vectorized bool) advancedChatKnowledgeBaseResponse {
	return advancedChatKnowledgeBaseResponse{ID: base.ID, Name: base.Name, Description: base.Description, DocumentCount: documentCount, StorageBytes: storageBytes, EmbeddingModelName: base.EmbeddingModelName, EmbeddingUserChannelID: base.EmbeddingUserChannelID, Vectorized: vectorized, CreatedAt: base.CreatedAt, UpdatedAt: base.UpdatedAt}
}

func advancedChatKnowledgeDocumentResponseFromModel(document AdvancedChatKnowledgeDocument) advancedChatKnowledgeDocumentResponse {
	return advancedChatKnowledgeDocumentResponse{ID: document.ID, FileID: document.FileID, Name: document.Name, Type: document.MIMEType, Size: document.Size, TextAvailable: document.TextAvailable, EmbeddingStatus: document.EmbeddingStatus, EmbeddingError: document.EmbeddingError, ChunkCount: document.ChunkCount, DownloadURL: "/api/user/advanced-chat/files/" + document.FileID + "/download", CreatedAt: document.CreatedAt}
}

func deleteAdvancedChatKnowledgeFile(userID uint, fileID string) {
	var file AdvancedChatFile
	if err := model.DB.Where("id = ? AND user_id = ? AND source = ?", fileID, userID, advancedChatKnowledgeDocumentSource).First(&file).Error; err != nil {
		return
	}
	if err := model.DB.Where("id = ? AND user_id = ?", file.ID, userID).Delete(&AdvancedChatFile{}).Error; err == nil {
		_ = removeAdvancedChatStoragePath(file.StoragePath)
	}
}
