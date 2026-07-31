package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/veloce-ailab/veloce/internal/model"
)

// CommunityFeatureEnabled 社区功能总开关（后台可关闭，默认开启）。
// 关闭后社区浏览代理与社区导入接口全部不可用。
func CommunityFeatureEnabled() bool {
	return model.GetSystemSetting("community_enabled", "true") != "false"
}

// communitySkillPayload 社区技能详情：content 为 SKILL.md 全文
type communitySkillPayload struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SourceName string `json:"source_name"`
}

// importCommunitySkill 把社区投稿的技能导入为当前用户的技能包：
// 下载社区保存的原始归档，再走与本地上传完全相同的校验和入库路径。
// 上游地址固定为社区站点，客户端无法借此抓取任意 URL。
func (api *advancedChatAPI) importCommunitySkill(c *gin.Context) {
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid community skill id"})
		return
	}
	payload := communitySkillPayload{}
	if err := fetchCommunityKnowledgeJSON(c.Request.Context(), "/skills/"+url.PathEscape(communityID), &payload); err != nil {
		if err == errCommunityKnowledgeNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "Community skill not found"})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "Community skill is temporarily unavailable"})
		return
	}
	archive, err := fetchCommunitySkillArchive(c.Request.Context(), communityID)
	if err != nil {
		if err == errCommunityKnowledgeNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "Community skill not found"})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "Community skill package is temporarily unavailable"})
		return
	}
	sourceName := strings.TrimSpace(payload.SourceName)
	if sourceName == "" {
		sourceName = strings.TrimSpace(payload.Name) + ".zip"
	}
	// storeAdvancedChatSkillPackage 会解包、校验每个 SKILL.md manifest 并落库，
	// 与手动上传技能包走完全相同的路径。
	pkg, skills, status, message, err := storeAdvancedChatSkillPackage(user.ID, sourceName, archive)
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"package":         advancedChatSkillPackageResponseFromModel(pkg, skills),
		"used_bytes":      advancedChatFileStorageUsedBytes(user.ID),
		"total_bytes":     advancedChatFileStorageTotalBytes(),
		"remaining_bytes": advancedChatFileStorageRemainingBytes(user.ID),
	})
}

func fetchCommunitySkillArchive(ctx context.Context, skillID string) ([]byte, error) {
	requestContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, communityKnowledgeAPIBaseURL+"/skills/"+url.PathEscape(skillID)+"/archive", nil)
	if err != nil {
		return nil, err
	}
	resp, err := communityKnowledgeHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("community service unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errCommunityKnowledgeNotFound
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("community service returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, advancedChatSkillPackageMaxArchiveBytes+1))
	if err != nil || len(data) == 0 || len(data) > advancedChatSkillPackageMaxArchiveBytes {
		return nil, fmt.Errorf("invalid community skill package")
	}
	return data, nil
}
