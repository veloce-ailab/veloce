package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORSConfig configures which browser origins may call the API from another
// origin. Native shells (Capacitor/iOS/Android webviews) and self-hosted web
// frontends run on their own origin, so the gateway has to answer preflight
// requests with explicit headers.
type CORSConfig struct {
	// AllowedOrigins holds exact origins such as https://app.example.com, or a
	// single "*" to allow any origin. These endpoints authenticate with bearer
	// tokens only and never rely on cookies, so a wildcard does not expose a
	// session to a hostile site.
	AllowedOrigins []string
}

// DefaultCORSAllowedHeaders lists the headers the Veloce gateway accepts from
// browser clients: OpenAI-style bearer tokens plus the Anthropic-style headers
// used by Claude-compatible clients.
var DefaultCORSAllowedHeaders = []string{
	"Authorization",
	"Content-Type",
	"Accept",
	"X-Api-Key",
	"Anthropic-Version",
	"Anthropic-Beta",
	"OpenAI-Organization",
	"OpenAI-Project",
}

// NewCORS returns a middleware that answers CORS preflight requests and adds
// the response headers browsers need to read API responses.
func NewCORS(config CORSConfig) gin.HandlerFunc {
	allowAll := false
	allowed := make(map[string]struct{}, len(config.AllowedOrigins))
	for _, origin := range config.AllowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		if origin == "*" {
			allowAll = true
			continue
		}
		allowed[strings.TrimRight(origin, "/")] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := strings.TrimSpace(c.GetHeader("Origin"))
		if origin == "" {
			// 非浏览器客户端（curl、服务端调用）不需要 CORS 响应头
			c.Next()
			return
		}

		if !allowAll {
			if _, ok := allowed[origin]; !ok {
				if c.Request.Method == http.MethodOptions {
					c.AbortWithStatus(http.StatusNoContent)
					return
				}
				c.Next()
				return
			}
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
		} else {
			c.Header("Access-Control-Allow-Origin", "*")
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", strings.Join(DefaultCORSAllowedHeaders, ", "))
		c.Header("Access-Control-Max-Age", "600")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
