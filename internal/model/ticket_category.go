package model

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// TicketCategory is an administrator-managed category shown when users open
// support tickets.
type TicketCategory struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	Slug      string         `gorm:"uniqueIndex;size:80;not null" json:"slug"`
	Name      string         `gorm:"size:100;not null" json:"name"`
	Enabled   bool           `gorm:"default:true;index" json:"enabled"`
	SortOrder int            `gorm:"default:0;index" json:"sort_order"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

var defaultTicketCategories = []TicketCategory{
	{Slug: "general", Name: "一般咨询", SortOrder: 10},
	{Slug: "technical", Name: "技术问题", SortOrder: 20},
	{Slug: "account", Name: "账号问题", SortOrder: 30},
	{Slug: "billing", Name: "账单与充值", SortOrder: 40},
	{Slug: "suggestion", Name: "功能建议", SortOrder: 50},
}

func EnsureDefaultTicketCategories(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	for _, category := range defaultTicketCategories {
		if err := db.Where(TicketCategory{Slug: category.Slug}).Attrs(category).FirstOrCreate(&category).Error; err != nil {
			return err
		}
	}
	return nil
}

func NormalizeTicketCategorySlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "-")
	value = strings.ReplaceAll(value, "_", "-")
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			builder.WriteRune(char)
		}
	}
	return strings.Trim(builder.String(), "-")
}
