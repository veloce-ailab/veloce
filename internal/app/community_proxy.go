package app

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const communityAPIBaseURL = "https://veloce-community.flweb.cn"

// proxyCommunityAPI exposes only the public read endpoints used by
// the embedded community browser. Keeping this request same-origin avoids a
// browser CORS dependency on the community deployment.
func proxyCommunityAPI(c *gin.Context) {
	requestContext, cancel := context.WithTimeout(c.Request.Context(), 12*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(requestContext)

	target, err := url.Parse(communityAPIBaseURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Community API is unavailable"})
		return
	}

	requestPath := strings.TrimPrefix(c.Request.URL.Path, "/api/community")
	if requestPath == "" {
		requestPath = "/characters"
	}
	target.Path = "/api/v1" + requestPath
	target.RawQuery = c.Request.URL.RawQuery

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, proxyErr error) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Community API is temporarily unavailable"})
	}
	proxy.Director = func(request *http.Request) {
		request.URL.Scheme = target.Scheme
		request.URL.Host = target.Host
		request.URL.Path = target.Path
		request.URL.RawPath = ""
		request.URL.RawQuery = target.RawQuery
		request.Host = target.Host
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}
