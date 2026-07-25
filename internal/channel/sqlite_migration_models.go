package channel

import "github.com/veloce-ailab/veloce/internal/model"

func init() {
	model.RegisterSQLiteMigrationModels(&MessageChannelIntegration{}, &MessageChannelMessage{})
}
