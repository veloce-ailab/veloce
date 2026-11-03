package model

import "time"

// RiskRule configures one synchronous block rule or asynchronous score rule.
type RiskRule struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Name       string    `gorm:"size:120;not null" json:"name"`
	Source     string    `gorm:"size:32;not null;index" json:"source"`
	MatchType  string    `gorm:"size:16;not null" json:"match_type"`
	Pattern    string    `gorm:"type:text" json:"pattern"`
	Mode       string    `gorm:"size:16;not null" json:"mode"`
	RiskPoints float64   `gorm:"type:decimal(12,4);not null;default:0" json:"risk_points"`
	LLMModel   string    `gorm:"size:120" json:"llm_model"`
	Enabled    bool      `gorm:"not null;default:true;index" json:"enabled"`
	Priority   int       `gorm:"not null;default:0;index" json:"priority"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// UserRiskState stores the current score and risk suspension for one user.
type UserRiskState struct {
	UserID      uint       `gorm:"primaryKey" json:"user_id"`
	RiskScore   float64    `gorm:"type:decimal(12,4);not null;default:0" json:"risk_score"`
	Banned      bool       `gorm:"not null;default:false;index" json:"banned"`
	BannedAt    *time.Time `json:"banned_at,omitempty"`
	Reason      string     `gorm:"size:255" json:"reason,omitempty"`
	LastDecayAt time.Time  `json:"last_decay_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// UserRiskEvent is an audit record. It intentionally stores no prompt or response text.
type UserRiskEvent struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     uint      `gorm:"index;not null" json:"user_id"`
	RuleID     *uint     `gorm:"index" json:"rule_id,omitempty"`
	RuleName   string    `gorm:"size:120" json:"rule_name"`
	Source     string    `gorm:"size:32;index" json:"source"`
	Delta      float64   `gorm:"type:decimal(12,4);not null;default:0" json:"delta"`
	ScoreAfter float64   `gorm:"type:decimal(12,4);not null;default:0" json:"score_after"`
	Threshold  float64   `gorm:"type:decimal(12,4);not null;default:0" json:"threshold"`
	Action     string    `gorm:"size:32;index" json:"action"`
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
}
