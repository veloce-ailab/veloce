package service

import (
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestFilterUserChannelGroupAccess(t *testing.T) {
	previousDB := model.DB
	t.Cleanup(func() { model.DB = previousDB })

	database, err := gorm.Open(sqlite.Open("file:user-channel-group-access-test?mode=memory&cache=shared"), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&model.Group{}, &model.User{}, &model.UserGroupMembership{}, &model.UserChannel{}, &model.UserChannelGroupAccess{}); err != nil {
		t.Fatal(err)
	}
	model.DB = database

	standard := model.Group{Name: "standard"}
	premium := model.Group{Name: "premium"}
	if err := database.Create(&standard).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&premium).Error; err != nil {
		t.Fatal(err)
	}
	user := model.User{Username: "member", Email: "member@example.com"}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&model.UserGroupMembership{UserID: user.ID, GroupID: standard.ID}).Error; err != nil {
		t.Fatal(err)
	}

	openChannel := model.UserChannel{Name: "open", Enabled: true}
	standardChannel := model.UserChannel{Name: "standard-only", Enabled: true}
	premiumChannel := model.UserChannel{Name: "premium-only", Enabled: true}
	for _, channel := range []*model.UserChannel{&openChannel, &standardChannel, &premiumChannel} {
		if err := database.Create(channel).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Create(&model.UserChannelGroupAccess{UserChannelID: standardChannel.ID, GroupID: standard.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&model.UserChannelGroupAccess{UserChannelID: premiumChannel.ID, GroupID: premium.ID}).Error; err != nil {
		t.Fatal(err)
	}

	query, err := filterUserChannelGroupAccess(database.Model(&model.UserChannel{}), &user)
	if err != nil {
		t.Fatal(err)
	}
	var channels []model.UserChannel
	if err := query.Order("name ASC").Find(&channels).Error; err != nil {
		t.Fatal(err)
	}
	if len(channels) != 2 || channels[0].Name != "open" || channels[1].Name != "standard-only" {
		t.Fatalf("accessible channels = %#v", channels)
	}
}
