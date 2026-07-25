package api

import (
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func TestConfigurationExportAndImportRoundTrip(t *testing.T) {
	previousDB := model.DB
	t.Cleanup(func() { model.DB = previousDB })

	database, err := gorm.Open(sqlite.Open("file:configuration-export-test?mode=memory&cache=shared"), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&model.SystemSetting{}, &model.UserChannel{}, &model.Channel{}, &model.Model{}, &model.ModelConfig{}); err != nil {
		t.Fatal(err)
	}
	model.DB = database
	if err := database.Create(&model.SystemSetting{Key: "site_name", Value: "Source"}).Error; err != nil {
		t.Fatal(err)
	}
	userChannel := model.UserChannel{Name: "public", Multiplier: decimal.NewFromInt(1), RoutingAlgorithm: "priority", Enabled: true}
	if err := database.Create(&userChannel).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{Name: "provider", Type: "openai", BaseURL: "https://api.example.com", APIKey: "secret", UserChannelID: &userChannel.ID, Multiplier: decimal.NewFromInt(1), Priority: 1, Weight: 1, Enabled: true, PriceSyncCron: "0 */6 * * *"}
	if err := database.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	modelItem := model.Model{ModelName: "gpt-test", Provider: "openai", Enabled: true, InputPrice: decimal.RequireFromString("1.5"), OutputPrice: decimal.RequireFromString("3")}
	if err := database.Create(&modelItem).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&model.ModelConfig{ChannelID: channel.ID, ModelID: modelItem.ID, UpstreamModelName: "gpt-test-upstream", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}

	sections, err := normalizeConfigurationSections([]string{configurationSectionSettings, configurationSectionChannels, configurationSectionModels, configurationSectionPrices})
	if err != nil {
		t.Fatal(err)
	}
	exported, err := buildConfigurationExport(sections)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.SystemSettings) != 1 || len(exported.Channels) != 1 || len(exported.Models) != 1 || len(exported.ModelPrices) != 1 || len(exported.ModelConfigs) != 1 {
		t.Fatalf("unexpected export counts: %+v", exported)
	}
	if exported.Channels[0].APIKey != "secret" || exported.ModelPrices[0].InputPrice.String() != "1.5" {
		t.Fatalf("export did not preserve configuration values: %+v", exported)
	}

	if err := database.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.ModelConfig{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.Channel{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.UserChannel{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.Model{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.SystemSetting{}).Error; err != nil {
		t.Fatal(err)
	}

	if err := importConfiguration(exported, sections); err != nil {
		t.Fatal(err)
	}
	var restoredChannel model.Channel
	if err := database.Where("name = ?", "provider").First(&restoredChannel).Error; err != nil {
		t.Fatal(err)
	}
	if restoredChannel.APIKey != "secret" {
		t.Fatalf("restored API key = %q, want secret", restoredChannel.APIKey)
	}
	var restoredModel model.Model
	if err := database.Where("model_name = ?", "gpt-test").First(&restoredModel).Error; err != nil {
		t.Fatal(err)
	}
	if !restoredModel.InputPrice.Equal(decimal.RequireFromString("1.5")) {
		t.Fatalf("restored input price = %s, want 1.5", restoredModel.InputPrice)
	}
	var configCount int64
	if err := database.Model(&model.ModelConfig{}).Count(&configCount).Error; err != nil {
		t.Fatal(err)
	}
	if configCount != 1 {
		t.Fatalf("restored model config count = %d, want 1", configCount)
	}
}
