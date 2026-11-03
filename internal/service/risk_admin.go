package service

import (
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RegisterRiskAdminRoutes mounts risk-management endpoints on the authenticated
// administrator API group (whose base path is /api).
func RegisterRiskAdminRoutes(admin *gin.RouterGroup) {
	risk := admin.Group("/risk")
	risk.GET("/settings", getRiskAdminSettings)
	risk.PUT("/settings", updateRiskAdminSettings)
	risk.GET("/rules", listRiskRules)
	risk.POST("/rules", createRiskRule)
	risk.PUT("/rules/:id", updateRiskRule)
	risk.DELETE("/rules/:id", deleteRiskRule)
	risk.GET("/users", listRiskUsers)
	risk.POST("/users/:id/unban", unbanRiskUser)
	risk.GET("/events", listRiskEvents)
}

func getRiskAdminSettings(c *gin.Context) {
	c.JSON(http.StatusOK, CurrentRiskSettings())
}

func updateRiskAdminSettings(c *gin.Context) {
	var input RiskSettings
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid risk settings"})
		return
	}
	input.ThresholdFormula = strings.TrimSpace(input.ThresholdFormula)
	input.DecaySpendFormula = strings.TrimSpace(input.DecaySpendFormula)
	input.LLMModel = strings.TrimSpace(input.LLMModel)
	if input.ThresholdFormula == "" {
		input.ThresholdFormula = riskDefaultThreshold
	}
	if input.DecaySpendFormula == "" {
		input.DecaySpendFormula = "0"
	}
	if input.DecayTimePerHour < 0 || math.IsNaN(input.DecayTimePerHour) || math.IsInf(input.DecayTimePerHour, 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Time decay must be a finite non-negative number"})
		return
	}
	thresholdVariables := map[string]float64{
		"total_spend": 100, "total_recharge": 100, "group_id": 1,
		"risk_score": 10, "total_tokens": 10000, "tokens": 10000,
		"input_tokens": 6000, "output_tokens": 4000, "request_count": 20,
	}
	if err := validateRiskFormula(input.ThresholdFormula, thresholdVariables); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid threshold formula: " + err.Error()})
		return
	}
	threshold, err := evalRiskFormula(input.ThresholdFormula, thresholdVariables)
	if err != nil || threshold <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Threshold formula must return a positive number"})
		return
	}
	decayVariables := map[string]float64{
		"cost": 1, "input_tokens": 100, "output_tokens": 100,
		"total_tokens": 200, "tokens": 200, "risk_score": 10, "group_id": 1,
		"total_spend": 100, "total_recharge": 100,
	}
	if err := validateRiskFormula(input.DecaySpendFormula, decayVariables); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid usage decay formula: " + err.Error()})
		return
	}
	if decay, err := evalRiskFormula(input.DecaySpendFormula, decayVariables); err != nil || decay < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Usage decay formula must return a non-negative number"})
		return
	}
	values := map[string]string{
		riskSettingEnabled:          strconv.FormatBool(input.Enabled),
		riskSettingThresholdFormula: input.ThresholdFormula,
		riskSettingDecayTimePerHour: strconv.FormatFloat(input.DecayTimePerHour, 'f', -1, 64),
		riskSettingDecaySpend:       input.DecaySpendFormula,
		riskSettingLLMModel:         input.LLMModel,
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		for key, value := range values {
			if err := model.SetSystemSettingWithDB(tx, key, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save risk settings"})
		return
	}
	c.JSON(http.StatusOK, CurrentRiskSettings())
}

func listRiskRules(c *gin.Context) {
	var rules []model.RiskRule
	if err := model.DB.Order("priority DESC, id ASC").Find(&rules).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load risk rules"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"rules": rules})
}

type riskRuleInput struct {
	Name       string  `json:"name"`
	Source     string  `json:"source"`
	MatchType  string  `json:"match_type"`
	Pattern    string  `json:"pattern"`
	Mode       string  `json:"mode"`
	RiskPoints float64 `json:"risk_points"`
	LLMModel   string  `json:"llm_model"`
	Enabled    bool    `json:"enabled"`
	Priority   int     `json:"priority"`
}

func (input riskRuleInput) model() (model.RiskRule, string) {
	input.Name = strings.TrimSpace(input.Name)
	input.Source = strings.TrimSpace(input.Source)
	input.MatchType = strings.TrimSpace(input.MatchType)
	input.Pattern = strings.TrimSpace(input.Pattern)
	input.Mode = strings.TrimSpace(input.Mode)
	input.LLMModel = strings.TrimSpace(input.LLMModel)
	if input.Name == "" || len(input.Name) > 120 {
		return model.RiskRule{}, "Rule name is required (max 120 characters)"
	}
	if input.RiskPoints < 0 || math.IsNaN(input.RiskPoints) || math.IsInf(input.RiskPoints, 0) || input.RiskPoints > 10000 {
		return model.RiskRule{}, "Risk points must be between 0 and 10000"
	}
	if input.Mode != "block" && input.Mode != "score" {
		return model.RiskRule{}, "Mode must be block or score"
	}
	sourceIsLLM := input.Source == "llm_input" || input.Source == "llm_output"
	if sourceIsLLM {
		if input.MatchType != "llm" || input.Mode != "score" {
			return model.RiskRule{}, "LLM rules must use LLM matching and asynchronous score mode"
		}
		if input.RiskPoints <= 0 {
			return model.RiskRule{}, "LLM maximum risk points must be greater than zero"
		}
	} else {
		if input.Source != "user_agent" && input.Source != "input_text" && input.Source != "output_text" {
			return model.RiskRule{}, "Unsupported event source"
		}
		if input.MatchType != "contains" && input.MatchType != "regex" {
			return model.RiskRule{}, "Match type must be contains or regex"
		}
		if input.Pattern == "" || len(input.Pattern) > 20000 {
			return model.RiskRule{}, "Pattern is required (max 20000 characters)"
		}
		if input.MatchType == "regex" {
			if _, err := regexp.Compile(input.Pattern); err != nil {
				return model.RiskRule{}, "Invalid regular expression: " + err.Error()
			}
		}
		if input.Mode == "score" && input.RiskPoints <= 0 {
			return model.RiskRule{}, "Risk points must be greater than zero for score rules"
		}
	}
	return model.RiskRule{
		Name: input.Name, Source: input.Source, MatchType: input.MatchType,
		Pattern: input.Pattern, Mode: input.Mode, RiskPoints: input.RiskPoints,
		LLMModel: input.LLMModel, Enabled: input.Enabled, Priority: input.Priority,
	}, ""
}

func createRiskRule(c *gin.Context) {
	var input riskRuleInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid risk rule"})
		return
	}
	rule, message := input.model()
	if message != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": message})
		return
	}
	if err := model.DB.Create(&rule).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create risk rule"})
		return
	}
	c.JSON(http.StatusCreated, rule)
}

func updateRiskRule(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid risk rule ID"})
		return
	}
	var existing model.RiskRule
	if err := model.DB.First(&existing, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Risk rule not found"})
		return
	}
	var input riskRuleInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid risk rule"})
		return
	}
	rule, message := input.model()
	if message != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": message})
		return
	}
	rule.ID = existing.ID
	rule.CreatedAt = existing.CreatedAt
	if err := model.DB.Save(&rule).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update risk rule"})
		return
	}
	c.JSON(http.StatusOK, rule)
}

func deleteRiskRule(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid risk rule ID"})
		return
	}
	result := model.DB.Delete(&model.RiskRule{}, id)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete risk rule"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Risk rule not found"})
		return
	}
	c.Status(http.StatusNoContent)
}

type riskUserResponse struct {
	UserID    uint       `json:"user_id"`
	Username  string     `json:"username"`
	Email     string     `json:"email"`
	GroupID   uint       `json:"group_id"`
	GroupName string     `json:"group_name"`
	RiskScore float64    `json:"risk_score"`
	Threshold float64    `json:"threshold"`
	Banned    bool       `json:"banned"`
	BannedAt  *time.Time `json:"banned_at,omitempty"`
	Reason    string     `json:"reason,omitempty"`
}

func listRiskUsers(c *gin.Context) {
	limit, offset := riskListWindow(c)
	search := strings.TrimSpace(c.Query("q"))
	query := model.DB.Table("user_risk_states").Joins("JOIN users ON users.id = user_risk_states.user_id").Joins("LEFT JOIN groups ON groups.id = users.group_id")
	if search != "" {
		pattern := "%" + search + "%"
		query = query.Where("users.username LIKE ? OR users.email LIKE ? OR CAST(users.id AS TEXT) LIKE ?", pattern, pattern, pattern)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count risk users"})
		return
	}
	var rows []riskUserResponse
	if err := query.Select("user_risk_states.user_id AS user_id, users.username AS username, users.email AS email, users.group_id AS group_id, groups.name AS group_name, user_risk_states.risk_score AS risk_score, user_risk_states.banned AS banned, user_risk_states.banned_at AS banned_at, user_risk_states.reason AS reason").
		Order("user_risk_states.banned DESC, user_risk_states.risk_score DESC, user_risk_states.updated_at DESC").
		Limit(limit).Offset(offset).Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load risk users"})
		return
	}
	for index := range rows {
		var user model.User
		user.ID = rows[index].UserID
		user.GroupID = rows[index].GroupID
		state := loadRiskState(user.ID)
		rows[index].Threshold = riskThresholdForUser(&user, rows[index].RiskScore, CurrentRiskSettings())
		rows[index].Reason = state.Reason
	}
	c.JSON(http.StatusOK, gin.H{"users": rows, "total": total})
}

func unbanRiskUser(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}
	tx := model.DB.Begin()
	if tx.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unban user"})
		return
	}
	defer tx.Rollback()
	var state model.UserRiskState
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, "user_id = ?", uint(id)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Risk state not found"})
		return
	}
	now := time.Now()
	oldScore := state.RiskScore
	state.RiskScore = 0
	state.Banned = false
	state.BannedAt = nil
	state.Reason = ""
	state.LastDecayAt = now
	state.UpdatedAt = now
	if err := tx.Save(&state).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unban user"})
		return
	}
	if err := tx.Create(&model.UserRiskEvent{
		UserID: state.UserID, RuleName: "Manual unban", Source: "admin",
		Delta: -oldScore, ScoreAfter: 0, Action: "manual_unban", CreatedAt: now,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record unban event"})
		return
	}
	if err := tx.Commit().Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unban user"})
		return
	}
	riskMetricsMu.Lock()
	delete(riskMetricsCache, state.UserID)
	riskMetricsMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"success": true})
}

type riskEventResponse struct {
	ID         uint      `json:"id"`
	UserID     uint      `json:"user_id"`
	Username   string    `json:"username"`
	RuleName   string    `json:"rule_name"`
	Source     string    `json:"source"`
	Delta      float64   `json:"delta"`
	ScoreAfter float64   `json:"score_after"`
	Threshold  float64   `json:"threshold"`
	Action     string    `json:"action"`
	CreatedAt  time.Time `json:"created_at"`
}

func listRiskEvents(c *gin.Context) {
	limit, offset := riskListWindow(c)
	query := model.DB.Table("user_risk_events").Joins("LEFT JOIN users ON users.id = user_risk_events.user_id")
	if rawUserID := strings.TrimSpace(c.Query("user_id")); rawUserID != "" {
		userID, err := strconv.ParseUint(rawUserID, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user_id"})
			return
		}
		query = query.Where("user_risk_events.user_id = ?", userID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count risk events"})
		return
	}
	var events []riskEventResponse
	if err := query.Select("user_risk_events.id AS id, user_risk_events.user_id AS user_id, users.username AS username, user_risk_events.rule_name AS rule_name, user_risk_events.source AS source, user_risk_events.delta AS delta, user_risk_events.score_after AS score_after, user_risk_events.threshold AS threshold, user_risk_events.action AS action, user_risk_events.created_at AS created_at").
		Order("user_risk_events.id DESC").Limit(limit).Offset(offset).Scan(&events).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load risk events"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"events": events, "total": total})
}

func parseRiskQueryInt(value string, fallback, minimum, maximum int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	if parsed < minimum {
		return minimum
	}
	if parsed > maximum {
		return maximum
	}
	return parsed
}

func riskListWindow(c *gin.Context) (int, int) {
	limitRaw := c.Query("page_size")
	if strings.TrimSpace(limitRaw) == "" {
		limitRaw = c.Query("limit")
	}
	limit := parseRiskQueryInt(limitRaw, 50, 1, 200)
	if rawOffset := strings.TrimSpace(c.Query("offset")); rawOffset != "" {
		return limit, parseRiskQueryInt(rawOffset, 0, 0, 1000000)
	}
	page := parseRiskQueryInt(c.Query("page"), 1, 1, 1000000)
	return limit, (page - 1) * limit
}
