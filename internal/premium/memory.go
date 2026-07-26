package premium

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/config"
	"github.com/veloce-ailab/veloce/internal/model"
	communityservice "github.com/veloce-ailab/veloce/internal/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	memoryScopeGlobal = "global"
	memoryScopeAgent  = "agent"

	memoryToolList   = "memory_list"
	memoryToolRead   = "memory_read"
	memoryToolUpsert = "memory_upsert"
	memoryToolPatch  = "memory_patch"
	memoryToolDelete = "memory_delete"

	maxMemoryDocumentBytes    = 512 * 1024
	maxMemoryReadBytes        = 200 * 1024
	maxAttachedMemoryBytes    = 32 * 1024
	maxAttachedMemoryDocBytes = 8 * 1024
)

var allowedMemoryKinds = map[string]bool{
	"profile":     true,
	"preferences": true,
	"facts":       true,
	"projects":    true,
	"rules":       true,
	"scratch":     true,
	"custom":      true,
}

var attachedMemoryKinds = map[string]bool{
	"profile":     true,
	"preferences": true,
	"facts":       true,
	"projects":    true,
	"rules":       true,
}

type AdvancedChatMemoryDocument struct {
	ID          string     `gorm:"primaryKey;size:80" json:"id"`
	UserID      uint       `gorm:"index;uniqueIndex:idx_memory_user_scope_agent_kind;not null" json:"user_id"`
	User        model.User `gorm:"foreignKey:UserID" json:"-"`
	Scope       string     `gorm:"size:20;uniqueIndex:idx_memory_user_scope_agent_kind;not null" json:"scope"`
	AgentID     string     `gorm:"size:80;uniqueIndex:idx_memory_user_scope_agent_kind;not null;default:''" json:"agent_id"`
	Kind        string     `gorm:"size:40;uniqueIndex:idx_memory_user_scope_agent_kind;not null" json:"kind"`
	Title       string     `gorm:"size:160;not null" json:"title"`
	StoragePath string     `gorm:"type:text;not null" json:"-"`
	Size        int64      `gorm:"not null" json:"size"`
	Hash        string     `gorm:"size:64;not null" json:"hash"`
	Enabled     bool       `gorm:"not null;default:true" json:"enabled"`
	UpdatedBy   string     `gorm:"size:40;not null;default:'user'" json:"updated_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type memoryInput struct {
	Scope   string `json:"scope"`
	AgentID string `json:"agent_id"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Enabled *bool  `json:"enabled"`
}

type memoryResponse struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	AgentID   string    `json:"agent_id,omitempty"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Size      int64     `json:"size"`
	Hash      string    `json:"hash"`
	Enabled   bool      `json:"enabled"`
	UpdatedBy string    `json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type memoryContentResponse struct {
	memoryResponse
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

type memoryAPI struct{}

func initMemoryFeatures() error {
	return model.DB.AutoMigrate(&AdvancedChatMemoryDocument{})
}

func registerMemoryUserRoutes(group *gin.RouterGroup) {
	api := &memoryAPI{}
	group.GET("/advanced-chat/memories", api.list)
	group.GET("/advanced-chat/memories/:id", api.get)
	group.POST("/advanced-chat/memories", api.upsert)
	group.PUT("/advanced-chat/memories/:id", api.update)
	group.DELETE("/advanced-chat/memories/:id", api.delete)
}

func registerMemoryHooks() {
	communityservice.RegisterAdvancedChatStorageUsageWithDBHook(memoryStorageUsedBytesWithDB)
	communityservice.RegisterAdvancedChatRuntimeExtensionHook(memoryRuntimeExtension)
	communityservice.RegisterAdvancedChatToolHandler(memoryToolList, handleMemoryTool)
	communityservice.RegisterAdvancedChatToolHandler(memoryToolRead, handleMemoryTool)
	communityservice.RegisterAdvancedChatToolHandler(memoryToolUpsert, handleMemoryTool)
	communityservice.RegisterAdvancedChatToolHandler(memoryToolPatch, handleMemoryTool)
	communityservice.RegisterAdvancedChatToolHandler(memoryToolDelete, handleMemoryTool)
}

func (api *memoryAPI) list(c *gin.Context) {
	user, ok := currentPremiumUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	memories, err := listUserMemories(user.ID, c.Query("scope"), c.Query("agent_id"), true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list memories"})
		return
	}
	result := make([]memoryResponse, 0, len(memories))
	for _, memory := range memories {
		result = append(result, memoryResponseFromModel(memory))
	}
	c.JSON(http.StatusOK, gin.H{
		"memories":        result,
		"used_bytes":      communityservice.AdvancedChatFileStorageUsedBytes(user.ID),
		"total_bytes":     communityservice.AdvancedChatFileStorageTotalBytes(),
		"remaining_bytes": communityservice.AdvancedChatFileStorageRemainingBytes(user.ID),
	})
}

func (api *memoryAPI) get(c *gin.Context) {
	user, ok := currentPremiumUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	memory, content, truncated, err := loadMemoryContent(user.ID, c.Param("id"), maxMemoryReadBytes)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": "Memory not found"})
		return
	}
	c.JSON(http.StatusOK, memoryContentResponse{memoryResponse: memoryResponseFromModel(memory), Content: content, Truncated: truncated})
}

func (api *memoryAPI) upsert(c *gin.Context) {
	user, ok := currentPremiumUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input memoryInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	memory, status, message, err := upsertMemoryDocument(user.ID, input, "user")
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, memoryResponseFromModel(memory))
}

func (api *memoryAPI) update(c *gin.Context) {
	user, ok := currentPremiumUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input memoryInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	input.Scope = ""
	input.AgentID = ""
	memory, status, message, err := updateMemoryDocument(user.ID, c.Param("id"), input, "user")
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, memoryResponseFromModel(memory))
}

func (api *memoryAPI) delete(c *gin.Context) {
	user, ok := currentPremiumUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if err := deleteMemoryDocument(user.ID, c.Param("id")); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": "Failed to delete memory"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Memory deleted"})
}

func memoryRuntimeExtension(ctx context.Context, input communityservice.AdvancedChatRuntimeContext) (communityservice.AdvancedChatRuntimeExtension, error) {
	if communityservice.AdvancedChatMemoryToolsDisabled(input.DisabledToolGroups) {
		return communityservice.AdvancedChatRuntimeExtension{}, nil
	}
	memories, err := runtimeMemories(input.UserID, input.AgentID)
	if err != nil {
		return communityservice.AdvancedChatRuntimeExtension{}, err
	}
	return communityservice.AdvancedChatRuntimeExtension{
		SystemPrompt: memorySystemPrompt(memories),
		Tools:        memoryTools(),
	}, nil
}

func memorySystemPrompt(memories []AdvancedChatMemoryDocument) string {
	var builder strings.Builder
	builder.WriteString("Memory is available as Markdown documents. Use memory tools when the user gives durable preferences, facts, identity, project context, or asks to remember or forget something. Do not store secrets, passwords, payment data, or transient one-off instructions.")
	if len(memories) == 0 {
		builder.WriteString("\n\nNo saved memories are currently available.")
		return builder.String()
	}
	builder.WriteString("\n\nAvailable memory catalog:\n<available_memories>")
	for _, memory := range memories {
		builder.WriteString(fmt.Sprintf("\n  <memory id=%q scope=%q agent_id=%q kind=%q title=%q size=%q updated_at=%q />",
			xmlAttr(memory.ID), xmlAttr(memory.Scope), xmlAttr(memory.AgentID), xmlAttr(memory.Kind), xmlAttr(memory.Title), fmt.Sprintf("%d", memory.Size), memory.UpdatedAt.Format(time.RFC3339)))
	}
	builder.WriteString("\n</available_memories>")
	if attached := attachedMemoryPrompt(memories); attached != "" {
		builder.WriteString("\n\n")
		builder.WriteString(attached)
	}
	return builder.String()
}

func attachedMemoryPrompt(memories []AdvancedChatMemoryDocument) string {
	var builder strings.Builder
	total := 0
	count := 0
	for _, memory := range memories {
		if !attachedMemoryKinds[memory.Kind] || !memory.Enabled {
			continue
		}
		remaining := maxAttachedMemoryBytes - total
		if remaining <= 0 {
			break
		}
		limit := maxAttachedMemoryDocBytes
		if remaining < limit {
			limit = remaining
		}
		content, err := readMemoryFile(memory.StoragePath, limit)
		if err != nil || strings.TrimSpace(content) == "" {
			continue
		}
		if count == 0 {
			builder.WriteString("Attached memory Markdown. Treat this as durable user/assistant context and follow it unless the current user explicitly overrides it:\n<attached_memories>")
		}
		truncated := memory.Size > int64(limit)
		builder.WriteString(fmt.Sprintf("\n  <memory id=%q scope=%q agent_id=%q kind=%q title=%q truncated=%q>\n%s\n  </memory>",
			xmlAttr(memory.ID), xmlAttr(memory.Scope), xmlAttr(memory.AgentID), xmlAttr(memory.Kind), xmlAttr(memory.Title), fmt.Sprintf("%t", truncated), strings.TrimSpace(content)))
		total += len([]byte(content))
		count++
	}
	if count == 0 {
		return ""
	}
	builder.WriteString("\n</attached_memories>")
	return builder.String()
}

func memoryTools() []communityservice.ChatExecutorTool {
	return []communityservice.ChatExecutorTool{
		{
			Name:        memoryToolList,
			Description: "List saved Markdown memories available to this user and current assistant.",
			Schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"scope":    map[string]interface{}{"type": "string", "enum": []string{memoryScopeGlobal, memoryScopeAgent}},
					"agent_id": map[string]interface{}{"type": "string"},
				},
			},
		},
		{
			Name:        memoryToolRead,
			Description: "Read a saved Markdown memory document by id.",
			Schema: map[string]interface{}{
				"type":     "object",
				"required": []string{"memory_id"},
				"properties": map[string]interface{}{
					"memory_id": map[string]interface{}{"type": "string"},
				},
			},
		},
		{
			Name:        memoryToolUpsert,
			Description: "Create or replace a Markdown memory document. Use only for durable user facts, preferences, rules, or project context.",
			Schema: map[string]interface{}{
				"type":     "object",
				"required": []string{"scope", "kind", "content"},
				"properties": map[string]interface{}{
					"scope":    map[string]interface{}{"type": "string", "enum": []string{memoryScopeGlobal, memoryScopeAgent}},
					"agent_id": map[string]interface{}{"type": "string", "description": "Required for agent scope. Use default for the default assistant."},
					"kind":     map[string]interface{}{"type": "string", "enum": []string{"profile", "preferences", "facts", "projects", "rules", "scratch", "custom"}},
					"title":    map[string]interface{}{"type": "string"},
					"content":  map[string]interface{}{"type": "string", "description": "Full Markdown document content."},
				},
			},
		},
		{
			Name:        memoryToolPatch,
			Description: "Patch an existing Markdown memory by replacing exact text.",
			Schema: map[string]interface{}{
				"type":     "object",
				"required": []string{"memory_id", "old_text", "new_text"},
				"properties": map[string]interface{}{
					"memory_id": map[string]interface{}{"type": "string"},
					"old_text":  map[string]interface{}{"type": "string"},
					"new_text":  map[string]interface{}{"type": "string"},
				},
			},
		},
		{
			Name:        memoryToolDelete,
			Description: "Delete a saved memory document when the user asks to forget it.",
			Schema: map[string]interface{}{
				"type":     "object",
				"required": []string{"memory_id"},
				"properties": map[string]interface{}{
					"memory_id": map[string]interface{}{"type": "string"},
				},
			},
		},
	}
}

func handleMemoryTool(ctx context.Context, input communityservice.AdvancedChatToolCallInput) (string, error) {
	switch input.Name {
	case memoryToolList:
		memories, err := listUserMemories(input.UserID, stringArg(input.Arguments, "scope"), firstNonEmpty(stringArg(input.Arguments, "agent_id"), input.AgentID), true)
		if err != nil {
			return "", err
		}
		return memoriesJSON(memories, false)
	case memoryToolRead:
		memory, content, truncated, err := loadMemoryContent(input.UserID, stringArg(input.Arguments, "memory_id"), maxMemoryReadBytes)
		if err != nil {
			return "", err
		}
		data, _ := json.Marshal(gin.H{"memory": memoryResponseFromModel(memory), "content": content, "truncated": truncated})
		return string(data), nil
	case memoryToolUpsert:
		memory, _, _, err := upsertMemoryDocument(input.UserID, memoryInput{
			Scope:   stringArg(input.Arguments, "scope"),
			AgentID: firstNonEmpty(stringArg(input.Arguments, "agent_id"), input.AgentID),
			Kind:    stringArg(input.Arguments, "kind"),
			Title:   stringArg(input.Arguments, "title"),
			Content: stringArg(input.Arguments, "content"),
		}, "assistant")
		if err != nil {
			return "", err
		}
		return "Memory saved: " + memory.ID, nil
	case memoryToolPatch:
		memory, content, _, err := loadMemoryContent(input.UserID, stringArg(input.Arguments, "memory_id"), maxMemoryDocumentBytes)
		if err != nil {
			return "", err
		}
		oldText := stringArg(input.Arguments, "old_text")
		if oldText == "" || !strings.Contains(content, oldText) {
			return "", errors.New("old_text was not found in memory")
		}
		nextContent := strings.Replace(content, oldText, stringArg(input.Arguments, "new_text"), 1)
		_, _, _, err = updateMemoryDocument(input.UserID, memory.ID, memoryInput{Title: memory.Title, Content: nextContent}, "assistant")
		if err != nil {
			return "", err
		}
		return "Memory patched: " + memory.ID, nil
	case memoryToolDelete:
		if err := deleteMemoryDocument(input.UserID, stringArg(input.Arguments, "memory_id")); err != nil {
			return "", err
		}
		return "Memory deleted.", nil
	default:
		return "", errors.New("unsupported memory tool")
	}
}

func listUserMemories(userID uint, rawScope string, rawAgentID string, includeDisabled bool) ([]AdvancedChatMemoryDocument, error) {
	scope := normalizeMemoryScope(rawScope)
	agentID := normalizeMemoryAgentID(rawAgentID)
	query := model.DB.Where("user_id = ?", userID)
	if scope != "" {
		query = query.Where("scope = ?", scope)
	}
	if agentID != "" {
		query = query.Where("agent_id = ?", agentID)
	}
	if !includeDisabled {
		query = query.Where("enabled = ?", true)
	}
	var memories []AdvancedChatMemoryDocument
	if err := query.Order("scope ASC, agent_id ASC, kind ASC, updated_at DESC").Find(&memories).Error; err != nil {
		return nil, err
	}
	return memories, nil
}

func runtimeMemories(userID uint, agentID string) ([]AdvancedChatMemoryDocument, error) {
	agentID = normalizeMemoryAgentID(agentID)
	var memories []AdvancedChatMemoryDocument
	query := model.DB.Where("user_id = ? AND enabled = ? AND (scope = ? OR (scope = ? AND agent_id = ?))", userID, true, memoryScopeGlobal, memoryScopeAgent, agentID)
	if err := query.Order("scope ASC, kind ASC, updated_at DESC").Find(&memories).Error; err != nil {
		return nil, err
	}
	return memories, nil
}

func upsertMemoryDocument(userID uint, input memoryInput, updatedBy string) (AdvancedChatMemoryDocument, int, string, error) {
	scope := normalizeMemoryScope(input.Scope)
	if scope == "" {
		return AdvancedChatMemoryDocument{}, http.StatusBadRequest, "Invalid memory scope", errors.New("invalid scope")
	}
	agentID := ""
	if scope == memoryScopeAgent {
		agentID = normalizeMemoryAgentID(input.AgentID)
		if agentID == "" {
			return AdvancedChatMemoryDocument{}, http.StatusBadRequest, "Agent id is required", errors.New("agent id required")
		}
	}
	kind := normalizeMemoryKind(input.Kind)
	if kind == "" {
		return AdvancedChatMemoryDocument{}, http.StatusBadRequest, "Invalid memory kind", errors.New("invalid kind")
	}
	var existing AdvancedChatMemoryDocument
	err := model.DB.Where("user_id = ? AND scope = ? AND agent_id = ? AND kind = ?", userID, scope, agentID, kind).First(&existing).Error
	if err == nil {
		input.Title = firstNonEmpty(input.Title, existing.Title)
		return updateMemoryDocument(userID, existing.ID, input, updatedBy)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return AdvancedChatMemoryDocument{}, http.StatusInternalServerError, "Failed to load memory", err
	}
	content := normalizeMemoryContent(input.Content)
	if content == "" {
		return AdvancedChatMemoryDocument{}, http.StatusBadRequest, "Memory content is required", errors.New("empty content")
	}
	if len([]byte(content)) > maxMemoryDocumentBytes {
		return AdvancedChatMemoryDocument{}, http.StatusRequestEntityTooLarge, "Memory document is too large", errors.New("memory too large")
	}
	id, err := newMemoryID()
	if err != nil {
		return AdvancedChatMemoryDocument{}, http.StatusInternalServerError, "Failed to create memory id", err
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = defaultMemoryTitle(kind)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	data := []byte(content)
	storagePath := memoryStoragePath(userID, scope, agentID, kind)
	tempPath, err := writeMemoryTemp(storagePath, data)
	if err != nil {
		return AdvancedChatMemoryDocument{}, http.StatusInternalServerError, "Failed to write memory", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	hash := sha256.Sum256(data)
	memory := AdvancedChatMemoryDocument{
		ID:          id,
		UserID:      userID,
		Scope:       scope,
		AgentID:     agentID,
		Kind:        kind,
		Title:       truncateRunes(title, 160),
		StoragePath: storagePath,
		Size:        int64(len(data)),
		Hash:        hex.EncodeToString(hash[:]),
		Enabled:     enabled,
		UpdatedBy:   normalizeUpdatedBy(updatedBy),
	}
	storageLimit := communityservice.AdvancedChatFileStorageTotalBytes()
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		used, err := communityservice.AdvancedChatFileStorageUsedBytesWithDB(tx, userID)
		if err != nil {
			return err
		}
		if used+memory.Size > storageLimit {
			return errMemoryQuotaExceeded
		}
		return tx.Create(&memory).Error
	})
	if err != nil {
		if errors.Is(err, errMemoryQuotaExceeded) {
			return AdvancedChatMemoryDocument{}, http.StatusPaymentRequired, "Not enough file storage space", err
		}
		return AdvancedChatMemoryDocument{}, http.StatusInternalServerError, "Failed to save memory", err
	}
	if err := commitMemoryTemp(tempPath, storagePath); err != nil {
		_ = model.DB.Where("id = ? AND user_id = ?", memory.ID, userID).Delete(&AdvancedChatMemoryDocument{}).Error
		return AdvancedChatMemoryDocument{}, http.StatusInternalServerError, "Failed to commit memory", err
	}
	committed = true
	return memory, http.StatusOK, "", nil
}

var errMemoryQuotaExceeded = errors.New("memory quota exceeded")

func updateMemoryDocument(userID uint, id string, input memoryInput, updatedBy string) (AdvancedChatMemoryDocument, int, string, error) {
	var memory AdvancedChatMemoryDocument
	if err := model.DB.Where("id = ? AND user_id = ?", strings.TrimSpace(id), userID).First(&memory).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AdvancedChatMemoryDocument{}, http.StatusNotFound, "Memory not found", err
		}
		return AdvancedChatMemoryDocument{}, http.StatusInternalServerError, "Failed to load memory", err
	}
	content := normalizeMemoryContent(input.Content)
	if content == "" {
		loaded, err := readMemoryFile(memory.StoragePath, maxMemoryDocumentBytes)
		if err != nil {
			return AdvancedChatMemoryDocument{}, http.StatusInternalServerError, "Failed to read memory", err
		}
		content = loaded
	}
	if len([]byte(content)) > maxMemoryDocumentBytes {
		return AdvancedChatMemoryDocument{}, http.StatusRequestEntityTooLarge, "Memory document is too large", errors.New("memory too large")
	}
	data := []byte(content)
	oldStoragePath := memory.StoragePath
	nextKind := memory.Kind
	if strings.TrimSpace(input.Kind) != "" {
		nextKind = normalizeMemoryKind(input.Kind)
		if nextKind == "" {
			return AdvancedChatMemoryDocument{}, http.StatusBadRequest, "Invalid memory kind", errors.New("invalid kind")
		}
	}
	if nextKind != memory.Kind {
		var duplicate AdvancedChatMemoryDocument
		err := model.DB.Where("user_id = ? AND scope = ? AND agent_id = ? AND kind = ? AND id <> ?", userID, memory.Scope, memory.AgentID, nextKind, memory.ID).First(&duplicate).Error
		if err == nil {
			return AdvancedChatMemoryDocument{}, http.StatusConflict, "Memory kind already exists for this scope", errors.New("duplicate memory kind")
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return AdvancedChatMemoryDocument{}, http.StatusInternalServerError, "Failed to check memory kind", err
		}
		memory.StoragePath = memoryStoragePath(userID, memory.Scope, memory.AgentID, nextKind)
	}
	tempPath, err := writeMemoryTemp(memory.StoragePath, data)
	if err != nil {
		return AdvancedChatMemoryDocument{}, http.StatusInternalServerError, "Failed to write memory", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	hash := sha256.Sum256(data)
	updates := map[string]interface{}{
		"size":       int64(len(data)),
		"hash":       hex.EncodeToString(hash[:]),
		"updated_by": normalizeUpdatedBy(updatedBy),
		"updated_at": time.Now(),
	}
	if nextKind != memory.Kind {
		updates["kind"] = nextKind
		updates["storage_path"] = memory.StoragePath
	}
	if strings.TrimSpace(input.Title) != "" {
		updates["title"] = truncateRunes(input.Title, 160)
	}
	if input.Enabled != nil {
		updates["enabled"] = *input.Enabled
	}
	storageLimit := communityservice.AdvancedChatFileStorageTotalBytes()
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		used, err := communityservice.AdvancedChatFileStorageUsedBytesWithDB(tx, userID)
		if err != nil {
			return err
		}
		if used-memory.Size+int64(len(data)) > storageLimit {
			return errMemoryQuotaExceeded
		}
		return tx.Model(&AdvancedChatMemoryDocument{}).Where("id = ? AND user_id = ?", memory.ID, userID).Updates(updates).Error
	})
	if err != nil {
		if errors.Is(err, errMemoryQuotaExceeded) {
			return AdvancedChatMemoryDocument{}, http.StatusPaymentRequired, "Not enough file storage space", err
		}
		return AdvancedChatMemoryDocument{}, http.StatusInternalServerError, "Failed to save memory", err
	}
	if err := commitMemoryTemp(tempPath, memory.StoragePath); err != nil {
		return AdvancedChatMemoryDocument{}, http.StatusInternalServerError, "Failed to commit memory", err
	}
	if oldStoragePath != memory.StoragePath {
		_ = removeMemoryFile(oldStoragePath)
	}
	committed = true
	_ = model.DB.Where("id = ? AND user_id = ?", memory.ID, userID).First(&memory).Error
	return memory, http.StatusOK, "", nil
}

func deleteMemoryDocument(userID uint, id string) error {
	var memory AdvancedChatMemoryDocument
	if err := model.DB.Where("id = ? AND user_id = ?", strings.TrimSpace(id), userID).First(&memory).Error; err != nil {
		return err
	}
	if err := model.DB.Where("id = ? AND user_id = ?", memory.ID, userID).Delete(&AdvancedChatMemoryDocument{}).Error; err != nil {
		return err
	}
	_ = removeMemoryFile(memory.StoragePath)
	return nil
}

func loadMemoryContent(userID uint, id string, maxBytes int) (AdvancedChatMemoryDocument, string, bool, error) {
	var memory AdvancedChatMemoryDocument
	if err := model.DB.Where("id = ? AND user_id = ?", strings.TrimSpace(id), userID).First(&memory).Error; err != nil {
		return AdvancedChatMemoryDocument{}, "", false, err
	}
	content, err := readMemoryFile(memory.StoragePath, maxBytes)
	if err != nil {
		return AdvancedChatMemoryDocument{}, "", false, err
	}
	return memory, content, memory.Size > int64(maxBytes), nil
}

func memoriesJSON(memories []AdvancedChatMemoryDocument, includeContent bool) (string, error) {
	items := make([]memoryResponse, 0, len(memories))
	for _, memory := range memories {
		items = append(items, memoryResponseFromModel(memory))
	}
	data, err := json.Marshal(gin.H{"memories": items, "include_content": includeContent})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func memoryResponseFromModel(memory AdvancedChatMemoryDocument) memoryResponse {
	return memoryResponse{
		ID:        memory.ID,
		Scope:     memory.Scope,
		AgentID:   memory.AgentID,
		Kind:      memory.Kind,
		Title:     memory.Title,
		Size:      memory.Size,
		Hash:      memory.Hash,
		Enabled:   memory.Enabled,
		UpdatedBy: memory.UpdatedBy,
		CreatedAt: memory.CreatedAt,
		UpdatedAt: memory.UpdatedAt,
	}
}

func memoryStorageUsedBytes(userID uint) int64 {
	var used int64
	if err := model.DB.Model(&AdvancedChatMemoryDocument{}).Where("user_id = ?", userID).Select("COALESCE(SUM(size), 0)").Scan(&used).Error; err != nil {
		return 0
	}
	return used
}

func memoryStorageUsedBytesWithDB(db *gorm.DB, userID uint) (int64, error) {
	var used int64
	if err := db.Model(&AdvancedChatMemoryDocument{}).Where("user_id = ?", userID).Select("COALESCE(SUM(size), 0)").Scan(&used).Error; err != nil {
		return 0, err
	}
	return used, nil
}

func memoryStoragePath(userID uint, scope string, agentID string, kind string) string {
	if scope == memoryScopeAgent {
		return path.Join("advanced-chat", "memories", fmt.Sprintf("%d", userID), "agents", sanitizePathPart(agentID), sanitizePathPart(kind)+".md")
	}
	return path.Join("advanced-chat", "memories", fmt.Sprintf("%d", userID), "global", sanitizePathPart(kind)+".md")
}

func memoryAbsPath(relativePath string) (string, error) {
	relativePath = strings.TrimSpace(strings.ReplaceAll(relativePath, "\\", "/"))
	if relativePath == "" || strings.HasPrefix(relativePath, "/") || strings.Contains(relativePath, "\x00") {
		return "", errors.New("invalid memory path")
	}
	cleaned := path.Clean("/" + relativePath)
	cleaned = strings.TrimPrefix(cleaned, "/")
	root := strings.TrimSpace(config.DataPath)
	if root == "" {
		root = "data"
	}
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(absRoot, filepath.FromSlash(cleaned)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", errors.New("memory path escapes data root")
	}
	return target, nil
}

func writeMemoryTemp(relativePath string, data []byte) (string, error) {
	target, err := memoryAbsPath(relativePath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".tmp-memory-*")
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

func commitMemoryTemp(tempPath string, relativePath string) error {
	target, err := memoryAbsPath(relativePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.Rename(tempPath, target)
}

func readMemoryFile(relativePath string, maxBytes int) (string, error) {
	target, err := memoryAbsPath(relativePath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	if maxBytes > 0 && len(data) > maxBytes {
		data = data[:maxBytes]
	}
	return string(data), nil
}

func removeMemoryFile(relativePath string) error {
	target, err := memoryAbsPath(relativePath)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = os.Remove(filepath.Dir(target))
	return nil
}

func normalizeMemoryScope(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case memoryScopeGlobal:
		return memoryScopeGlobal
	case memoryScopeAgent:
		return memoryScopeAgent
	default:
		return ""
	}
}

func normalizeMemoryKind(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = sanitizePathPart(value)
	if !allowedMemoryKinds[value] {
		return ""
	}
	return value
}

func normalizeMemoryAgentID(value string) string {
	return sanitizePathPart(strings.TrimSpace(value))
}

func normalizeMemoryContent(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.TrimSpace(value)
}

func normalizeUpdatedBy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "assistant":
		return "assistant"
	case "system":
		return "system"
	default:
		return "user"
	}
}

func defaultMemoryTitle(kind string) string {
	switch kind {
	case "profile":
		return "Profile"
	case "preferences":
		return "Preferences"
	case "facts":
		return "Facts"
	case "projects":
		return "Projects"
	case "rules":
		return "Rules"
	case "scratch":
		return "Scratch"
	default:
		return "Memory"
	}
}

func sanitizePathPart(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('-')
		}
		if builder.Len() >= 80 {
			break
		}
	}
	return strings.Trim(builder.String(), "-_")
}

func newMemoryID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "acm-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(data)), nil
}

func stringArg(arguments map[string]interface{}, key string) string {
	if arguments == nil {
		return ""
	}
	value, ok := arguments[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func truncateRunes(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func xmlAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}
