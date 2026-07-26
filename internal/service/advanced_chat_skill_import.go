package service

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
)

// CommunityFeatureEnabled 社区功能总开关（后台可关闭，默认开启）。
// 关闭后社区浏览代理与社区导入接口全部不可用。
func CommunityFeatureEnabled() bool {
	return model.GetSystemSetting("community_enabled", "true") != "false"
}

// communitySkillPayload 社区技能详情：content 为 SKILL.md 全文
type communitySkillPayload struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Content string `json:"content"`
}

var communitySkillSlugPattern = regexp.MustCompile(`[^a-z0-9-]+`)

// importCommunitySkill 把社区投稿的技能导入为当前用户的技能包：
// 将 SKILL.md 打包成单技能 zip 后走与上传完全相同的存储与校验路径。
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
	if strings.TrimSpace(payload.Content) == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "Community skill has no importable content"})
		return
	}

	slug := communitySkillSlug(payload.Name)
	archive, err := buildSingleSkillArchive(slug, payload.Content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to package community skill"})
		return
	}
	// storeAdvancedChatSkillPackage 会解包、校验 SKILL.md manifest（name/description）
	// 并落库，行为与手动上传技能包一致
	pkg, skills, status, message, err := storeAdvancedChatSkillPackage(user.ID, slug+".zip", archive)
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

// communitySkillSlug 由技能名派生目录名（仅小写字母数字与连字符）
func communitySkillSlug(name string) string {
	slug := communitySkillSlugPattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "community-skill"
	}
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	return slug
}

// buildSingleSkillArchive 生成只包含 <slug>/SKILL.md 的内存 zip
func buildSingleSkillArchive(slug, content string) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create(slug + "/SKILL.md")
	if err != nil {
		return nil, err
	}
	if _, err := file.Write([]byte(content)); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
