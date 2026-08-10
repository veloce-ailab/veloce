package model

import "time"

// UserBillingPreference stores the user's ordered funding sources as JSON.
type UserBillingPreference struct {
	UserID      uint      `gorm:"primaryKey" json:"user_id"`
	SourceOrder string    `gorm:"column:source_order;type:text" json:"source_order"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
