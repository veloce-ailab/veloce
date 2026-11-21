package service

import (
	"context"
	"errors"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
)

const tokenLogClientCanceledKey = "token_log_client_canceled"
const clientCanceledLogStatus = 499

func clientRequestCanceled(c *gin.Context) bool {
	if c == nil {
		return false
	}
	return c.GetBool(tokenLogClientCanceledKey) || (c.Request != nil && errors.Is(c.Request.Context().Err(), context.Canceled))
}

// A canceled downstream request is intentionally free even if the upstream
// already generated chargeable tokens. Preserve observed usage for diagnostics.
func recordCanceledUsage(c *gin.Context, user *model.User, apiKey *model.APIKey, channel *model.Channel, modelConfig *model.ModelConfig, modelName string, usage usageTokenCounts, billingModel model.Model) {
	if c == nil || c.GetBool(tokenLogRecordedKey) || user == nil || channel == nil {
		return
	}
	c.Set(tokenLogClientCanceledKey, true)
	entry := canceledUsageLog(c, user, apiKey, channel, modelConfig, modelName, usage, billingModel)
	if err := model.RecordTokenLog(entry); err != nil {
		log.Printf("failed to record client-canceled usage: %v", err)
		return
	}
	markTokenLogRecorded(c)
}

func canceledUsageLog(c *gin.Context, user *model.User, apiKey *model.APIKey, channel *model.Channel, modelConfig *model.ModelConfig, modelName string, usage usageTokenCounts, billingModel model.Model) model.TokenLog {
	one := decimal.NewFromInt(1)
	entry := newUsageTokenLog(c, user, apiKey, channel, modelConfig, modelName, normalizeUsageTokenCounts(usage), billingModel, decimal.Zero, one, one, decimal.Zero)
	entry.Status = clientCanceledLogStatus
	entry.ErrorMessage = "client_canceled: client canceled or disconnected; request not charged"
	entry.PricingFormula = "client_canceled: no charge"
	return entry
}
