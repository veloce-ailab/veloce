package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
)

const (
	communityKnowledgeAPIBaseURL = "https://veloce-community.flweb.cn/api/v1"
	maxCommunityKnowledgeImport  = 32 << 20
)

var communityKnowledgeHTTPClient = &http.Client{Timeout: 20 * time.Second}

type communityKnowledgeBasePayload struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type communityKnowledgeContentPayload struct {
	Files []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Content string `json:"content"`
	} `json:"files"`
}

// importCommunityKnowledgeBase copies the published plain-text documents to
// the current user's private knowledge base. The upstream origin is fixed,
// so clients cannot use this endpoint as an arbitrary URL fetcher.
func (api *advancedChatAPI) importCommunityKnowledgeBase(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !CommunityFeatureEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Community is disabled"})
		return
	}
	if !advancedChatFileStorageEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "File storage is disabled"})
		return
	}

	communityID := strings.TrimSpace(c.Param("id"))
	if communityID == "" || len(communityID) > 120 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid community knowledge base id"})
		return
	}
	basePayload := communityKnowledgeBasePayload{}
	if err := fetchCommunityKnowledgeJSON(c.Request.Context(), "/knowledge-bases/"+url.PathEscape(communityID), &basePayload); err != nil {
		writeCommunityKnowledgeImportError(c, err)
		return
	}
	contentPayload := communityKnowledgeContentPayload{}
	if err := fetchCommunityKnowledgeJSON(c.Request.Context(), "/knowledge-bases/"+url.PathEscape(communityID)+"/content", &contentPayload); err != nil {
		writeCommunityKnowledgeImportError(c, err)
		return
	}

	base, valid := advancedChatKnowledgeBaseFromInput(c, user.ID, advancedChatKnowledgeBaseInput{
		Name:        basePayload.Name,
		Description: basePayload.Description,
	})
	if !valid {
		return
	}
	if len(contentPayload.Files) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Community knowledge base has no importable files"})
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

	createdFiles := make([]string, 0, len(contentPayload.Files))
	createdDocuments := 0
	cleanup := func() {
		for _, fileID := range createdFiles {
			deleteAdvancedChatKnowledgeFile(user.ID, fileID)
		}
		_ = model.DB.Where("id = ? AND user_id = ?", base.ID, user.ID).Delete(&AdvancedChatKnowledgeDocument{}).Error
		_ = model.DB.Where("id = ? AND user_id = ?", base.ID, user.ID).Delete(&AdvancedChatKnowledgeBase{}).Error
	}
	for index, item := range contentPayload.Files {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = fmt.Sprintf("community-document-%d.md", index+1)
		}
		if path.Ext(name) == "" {
			name += ".md"
		}
		file, status, message, err := storeAdvancedChatFile(user.ID, advancedChatFileStoreInput{
			Name:               name,
			MIMEType:           "text/markdown; charset=utf-8",
			Data:               []byte(content),
			Source:             advancedChatKnowledgeDocumentSource,
			SourceKey:          fmt.Sprintf("community-knowledge:%s:%s:%d", base.ID, strings.TrimSpace(item.ID), index),
			RequireAllowedType: true,
		})
		if err != nil {
			cleanup()
			c.JSON(status, gin.H{"error": message})
			return
		}
		createdFiles = append(createdFiles, file.ID)
		document := AdvancedChatKnowledgeDocument{
			ID:              newAdvancedChatID("akd"),
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
			cleanup()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save knowledge document"})
			return
		}
		createdDocuments++
	}
	if createdDocuments == 0 {
		cleanup()
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Community knowledge base has no importable text"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"knowledge_base": advancedChatKnowledgeBaseResponseFromModel(base, createdDocuments, 0, false)})
}

func fetchCommunityKnowledgeJSON(ctx context.Context, route string, output interface{}) error {
	requestContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, communityKnowledgeAPIBaseURL+route, nil)
	if err != nil {
		return err
	}
	resp, err := communityKnowledgeHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("community service unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errCommunityKnowledgeNotFound
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("community service returned HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxCommunityKnowledgeImport+1)).Decode(output); err != nil {
		return fmt.Errorf("invalid community response: %w", err)
	}
	return nil
}

var errCommunityKnowledgeNotFound = fmt.Errorf("community knowledge base not found")

func writeCommunityKnowledgeImportError(c *gin.Context, err error) {
	if err == errCommunityKnowledgeNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "Community knowledge base not found"})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": "Community knowledge base is temporarily unavailable"})
}
