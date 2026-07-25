package premium

import "github.com/veloce-ailab/veloce/internal/model"

func init() {
	model.RegisterSQLiteMigrationModels(
		&SubscriptionPlan{},
		&UserSubscription{},
		&PremiumRedeemCode{},
		&PremiumRedemptionLog{},
		&MetaModel{},
		&AdvancedChatMemoryDocument{},
	)
}
