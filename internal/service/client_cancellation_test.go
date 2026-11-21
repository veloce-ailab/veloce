package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
)

func TestCanceledUsageLogIsFreeAndMarked(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	if clientRequestCanceled(c) {
		t.Fatal("live request marked canceled")
	}
	cancel()
	if !clientRequestCanceled(c) {
		t.Fatal("canceled request not detected")
	}
	entry := canceledUsageLog(c, &model.User{ID: 1}, nil, &model.Channel{ID: 2}, &model.ModelConfig{}, "model", usageTokenCounts{InputTokens: 20000, OutputTokens: 123}, model.Model{InputPrice: decimal.NewFromInt(5)})
	if entry.Status != 499 || !entry.Cost.IsZero() || !entry.BaseCost.IsZero() || entry.InputTokens != 20000 || entry.OutputTokens != 123 || entry.ErrorMessage == "" {
		t.Fatalf("unexpected cancellation log %+v", entry)
	}
	// Marking the log already recorded lets this verify the no-billing path
	// without any storage dependency: even a nil model DB must not be touched.
	c.Set(tokenLogRecordedKey, true)
	s := NewProxyService()
	cost, _, _, err := s.settleUsage(c, &model.User{}, nil, &model.Channel{}, &model.ModelConfig{}, "model", usageTokenCounts{InputTokens: 20000}, model.Model{}, true)
	if err != nil || !cost.IsZero() {
		t.Fatalf("canceled request settled: cost=%v err=%v", cost, err)
	}
	cost, _, _, err = s.billServerUsage(c, &model.User{}, nil, &model.Channel{}, &model.ModelConfig{}, "model", usageTokenCounts{InputTokens: 20000}, true)
	if err != nil || !cost.IsZero() {
		t.Fatalf("canceled server request settled: cost=%v err=%v", cost, err)
	}
}

func TestUpstreamDeadlineDoesNotMeanClientCanceled(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// An upstream error has its own context. The live downstream request must
	// remain billable rather than confusing provider cancellation with a user.
	_ = ctx
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if clientRequestCanceled(c) {
		t.Fatal("upstream failure treated as client cancellation")
	}
	deadline, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	c.Request = c.Request.WithContext(deadline)
	if clientRequestCanceled(c) {
		t.Fatal("request deadline is not explicit cancellation")
	}
}

type failingCancellationWriter struct{ gin.ResponseWriter }

func (w failingCancellationWriter) Write([]byte) (int, error) {
	return 0, errors.New("client disconnected")
}

func TestDownstreamWriteFailureMarksCancellation(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	wrapped := &errorBodyCaptureWriter{ResponseWriter: failingCancellationWriter{c.Writer}, context: c}
	if _, err := wrapped.Write([]byte("data: {}\n\n")); err == nil {
		t.Fatal("missing write failure")
	}
	if !clientRequestCanceled(c) {
		t.Fatal("failed downstream write must cancel billing")
	}
}
