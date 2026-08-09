package model

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	PersonalCompanyStateDraft             = "draft"
	PersonalCompanyStateBootstrap         = "bootstrap"
	PersonalCompanyStateOperating         = "operating"
	PersonalCompanyStateAttentionRequired = "attention_required"
	PersonalCompanyStateSafeMode          = "safe_mode"
	PersonalCompanyStatePaused            = "paused"
	PersonalCompanyStateArchived          = "archived"

	PersonalCompanyAutonomyR0 = "r0"
	PersonalCompanyAutonomyR1 = "r1"
	PersonalCompanyAutonomyR2 = "r2"
)

// PersonalCompany is a user-owned operating boundary. It deliberately does
// not reuse enterprise organizations, memberships, or RBAC.
type PersonalCompany struct {
	ID                       uint            `gorm:"primaryKey" json:"id"`
	OwnerUserID              uint            `gorm:"uniqueIndex:idx_personal_company_studio;index;not null" json:"owner_user_id"`
	Name                     string          `gorm:"size:160;not null" json:"name"`
	State                    string          `gorm:"size:32;not null;default:'draft';index" json:"state"`
	Timezone                 string          `gorm:"size:80;not null;default:'UTC'" json:"timezone"`
	AutonomyLevel            string          `gorm:"size:8;not null;default:'r0'" json:"autonomy_level"`
	DailyBudget              decimal.Decimal `gorm:"type:decimal(20,6);not null;default:0" json:"daily_budget"`
	MonthlyBudget            decimal.Decimal `gorm:"type:decimal(20,6);not null;default:0" json:"monthly_budget"`
	BalanceFloor             decimal.Decimal `gorm:"type:decimal(20,6);not null;default:0" json:"balance_floor"`
	CharterRevisionID        *uint           `gorm:"index" json:"charter_revision_id,omitempty"`
	AgentGroupID             string          `gorm:"uniqueIndex:idx_personal_company_studio;size:80;index;not null;default:''" json:"agent_group_id,omitempty"`
	ConnectorDeviceID        string          `gorm:"size:80;not null;default:''" json:"connector_device_id,omitempty"`
	ConnectorWorkspacePath   string          `gorm:"type:text;not null;default:''" json:"connector_workspace_path,omitempty"`
	ConnectorCommandPrefixes string          `gorm:"type:text;not null;default:'[]'" json:"connector_command_prefixes,omitempty"`
	MaxConcurrentTasks       int             `gorm:"not null;default:1" json:"max_concurrent_tasks"`
	PausedAt                 *time.Time      `gorm:"index" json:"paused_at,omitempty"`
	CreatedAt                time.Time       `json:"created_at"`
	UpdatedAt                time.Time       `json:"updated_at"`
	DeletedAt                gorm.DeletedAt  `gorm:"index" json:"-"`
}

// CompanyCharterRevision is immutable once created. CompanyCharterRevisionID
// on PersonalCompany identifies the revision governing new work.
type CompanyCharterRevision struct {
	ID                uint            `gorm:"primaryKey" json:"id"`
	PersonalCompanyID uint            `gorm:"uniqueIndex:idx_company_charter_revision;index;not null" json:"personal_company_id"`
	PersonalCompany   PersonalCompany `gorm:"foreignKey:PersonalCompanyID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT" json:"-"`
	Revision          int             `gorm:"uniqueIndex:idx_company_charter_revision;not null" json:"revision"`
	Mission           string          `gorm:"type:text;not null" json:"mission"`
	Goals             string          `gorm:"type:text;not null;default:'[]'" json:"goals"`
	DataBoundaries    string          `gorm:"type:text;not null;default:'[]'" json:"data_boundaries"`
	ProhibitedActions string          `gorm:"type:text;not null;default:'[]'" json:"prohibited_actions"`
	ApprovalPolicy    string          `gorm:"type:text;not null;default:'{}'" json:"approval_policy"`
	CreatedByUserID   uint            `gorm:"index;not null" json:"created_by_user_id"`
	CreatedAt         time.Time       `json:"created_at"`
}

func NormalizePersonalCompanyState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case PersonalCompanyStateBootstrap, PersonalCompanyStateOperating, PersonalCompanyStateAttentionRequired,
		PersonalCompanyStateSafeMode, PersonalCompanyStatePaused, PersonalCompanyStateArchived:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return PersonalCompanyStateDraft
	}
}

func NormalizePersonalCompanyAutonomy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case PersonalCompanyAutonomyR1:
		return PersonalCompanyAutonomyR1
	case PersonalCompanyAutonomyR2:
		return PersonalCompanyAutonomyR2
	default:
		return PersonalCompanyAutonomyR0
	}
}
