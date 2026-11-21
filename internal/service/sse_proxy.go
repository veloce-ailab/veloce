package service

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Converted streams have a different encoding and length from the upstream.
// Do not forward entity framing, compressed lengths, or ETag validators.
func writeConvertedStreamHeaders(c *gin.Context, resp *http.Response) {
	for key, values := range resp.Header {
		switch http.CanonicalHeaderKey(key) {
		case "Content-Type", "Content-Length", "Content-Encoding", "Etag", "Content-Md5", "Digest":
			continue
		}
		if !shouldSkipProxyResponseHeader(key, true) {
			c.Writer.Header()[key] = values
		}
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(resp.StatusCode)
}

type streamMeterWriter struct{ meter *streamUsageMeter }

func (w streamMeterWriter) Write(data []byte) (int, error) {
	w.meter.consume(data)
	return len(data), nil
}
