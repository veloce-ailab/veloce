package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/veloce-ailab/veloce/internal/service"
)

func AuthMiddleware(authService *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := tokenFromRequest(c)
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization is required"})
			c.Abort()
			return
		}

		var user model.User

		if strings.HasPrefix(token, "sk-") {
			// API Key authentication
			apiKeyUser, apiKey, err := service.FindUserByAPIKey(token, c.ClientIP())
			if err != nil {
				if errors.Is(err, service.ErrAPIKeyIPRestricted) {
					c.JSON(http.StatusForbidden, gin.H{"error": "API key is not allowed from this IP"})
					c.Abort()
					return
				}
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
				c.Abort()
				return
			}
			user = *apiKeyUser
			c.Set("api_key", apiKey)
		} else {
			// JWT authentication (for dashboard)
			userID, _, err := authService.VerifyJWT(token)
			if err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid session"})
				c.Abort()
				return
			}
			if err := model.DB.First(&user, userID).Error; err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "User not found"})
				c.Abort()
				return
			}
			if err := service.EnsureFirstAdmin(&user); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize admin user"})
				c.Abort()
				return
			}
		}
		if user.IsDisabled {
			c.JSON(http.StatusForbidden, gin.H{"error": "Account is disabled"})
			c.Abort()
			return
		}

		c.Set("user", &user)
		c.Next()
	}
}

// ExternalAPIMiddleware gates token-based OpenAI-compatible API endpoints
// without affecting dashboard sessions or the browser chat workspace.
func ExternalAPIMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.EqualFold(strings.TrimSpace(model.GetSystemSetting("token_api_enabled", "true")), "true") {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Token API access is disabled by the administrator"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func tokenFromRequest(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if authHeader != "" {
		parts := strings.Fields(authHeader)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return parts[1]
		}
		return ""
	}
	if apiKey := strings.TrimSpace(c.GetHeader("x-api-key")); apiKey != "" {
		return apiKey
	}
	return strings.TrimSpace(c.Query("key"))
}

func AdminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		val, exists := c.Get("user")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			c.Abort()
			return
		}

		user, ok := val.(*model.User)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			c.Abort()
			return
		}
		if !canAccessAdminPath(user, c.Request.URL.Path) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
			c.Abort()
			return
		}

		c.Next()
	}
}

// StaffMiddleware grants access to staff-only routes that are not part of the
// main administrative surface, such as the support queue.
func StaffMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := currentUserFromContext(c)
		if !ok || !model.IsStaffUser(user) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Staff access required"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func currentUserFromContext(c *gin.Context) (*model.User, bool) {
	value, exists := c.Get("user")
	user, ok := value.(*model.User)
	return user, exists && ok && user != nil
}

func canAccessAdminPath(user *model.User, path string) bool {
	if user == nil {
		return false
	}
	if user.IsAdmin || model.NormalizeUserRole(user.Role) == model.UserRoleAdmin {
		return true
	}
	role := model.NormalizeUserRole(user.Role)
	if role == model.UserRoleOperations {
		return hasAdminPathPrefix(path, "/api/channels", "/api/models", "/api/channel-models", "/api/user-channels", "/api/logs", "/api/audit-logs", "/api/stats", "/api/channel-usage", "/api/status-monitors", "/api/nodes", "/api/scheduled-jobs")
	}
	if role == model.UserRoleSales {
		return hasAdminPathPrefix(path, "/api/users", "/api/groups", "/api/referral-commissions", "/api/stats")
	}
	return false
}

func hasAdminPathPrefix(path string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
