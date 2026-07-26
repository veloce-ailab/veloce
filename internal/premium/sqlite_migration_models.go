package premium

import "github.com/veloce-ailab/veloce/internal/model"

func init() {
	model.RegisterSQLiteMigrationModels(
		&AdvancedChatMemoryDocument{},
	)
}
