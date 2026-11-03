package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	riskSettingEnabled          = "risk_enabled"
	riskSettingThresholdFormula = "risk_threshold_formula"
	riskSettingDecayTimePerHour = "risk_decay_time_per_hour"
	riskSettingDecaySpend       = "risk_decay_spend_formula"
	riskSettingLLMModel         = "risk_llm_model"
	riskDefaultThreshold        = "100"
	riskMaxLLMInputBytes        = 12 * 1024
	riskMaxBufferedStreamBytes  = 32 * 1024 * 1024
)

type RiskSettings struct {
	Enabled           bool    `json:"enabled"`
	ThresholdFormula  string  `json:"threshold_formula"`
	DecayTimePerHour  float64 `json:"decay_time_per_hour"`
	DecaySpendFormula string  `json:"decay_spend_formula"`
	LLMModel          string  `json:"llm_model"`
}

type riskMetrics struct {
	TotalSpend    float64
	TotalRecharge float64
	TotalTokens   float64
	InputTokens   float64
	OutputTokens  float64
	RequestCount  float64
	GroupID       float64
	ExpiresAt     time.Time
}

var (
	riskMetricsMu    sync.Mutex
	riskMetricsCache = map[uint]riskMetrics{}
	riskQueueOnce    sync.Once
	riskQueue        = make(chan riskJob, 256)
)

type riskJob struct {
	UserID       uint
	Rule         *model.RiskRule
	Text         string
	Cost         float64
	InputTokens  int
	OutputTokens int
	ApplyDecay   bool
}

func CurrentRiskSettings() RiskSettings {
	return RiskSettings{
		Enabled:           riskSettingBool(model.GetSystemSetting(riskSettingEnabled, "false")),
		ThresholdFormula:  strings.TrimSpace(model.GetSystemSetting(riskSettingThresholdFormula, riskDefaultThreshold)),
		DecayTimePerHour:  riskSettingFloat(model.GetSystemSetting(riskSettingDecayTimePerHour, "0")),
		DecaySpendFormula: strings.TrimSpace(model.GetSystemSetting(riskSettingDecaySpend, "0")),
		LLMModel:          strings.TrimSpace(model.GetSystemSetting(riskSettingLLMModel, "")),
	}
}

func riskSettingBool(value string) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && parsed
}

func riskSettingFloat(value string) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0
	}
	return parsed
}

func enabledRiskRules() []model.RiskRule {
	if model.DB == nil {
		return nil
	}
	var rules []model.RiskRule
	if err := model.DB.Where("enabled = ?", true).Order("priority DESC, id ASC").Find(&rules).Error; err != nil {
		log.Printf("failed to load risk rules: %v", err)
		return nil
	}
	return rules
}

// RiskRequestGate checks an existing risk suspension and User-Agent rules before
// proxy target resolution. Score-only matches are queued so they don't delay the request.
func RiskRequestGate(userID uint, userAgent string) (bool, string) {
	settings := CurrentRiskSettings()
	if !settings.Enabled || userID == 0 || model.DB == nil {
		return false, ""
	}
	applyRiskTimeDecay(userID, settings)
	if banned, reason := riskUserSuspended(userID, settings); banned {
		return true, riskSuspensionMessage(reason)
	}
	for _, rule := range enabledRiskRules() {
		if rule.Source != "user_agent" || !riskRuleMatches(rule, userAgent) {
			continue
		}
		if rule.Mode == "block" {
			recordRiskBlock(userID, rule)
			return true, firstNonEmptyRisk(rule.Name, "Request blocked by risk policy")
		}
		queueRiskJob(riskJob{UserID: userID, Rule: &rule})
	}
	return false, ""
}

// RiskCheckInputText checks normalized inbound text. Synchronous block rules run
// inline; score and LLM rules are sent to the bounded background worker.
func RiskCheckInputText(userID uint, text string) (bool, string) {
	settings := CurrentRiskSettings()
	if !settings.Enabled || userID == 0 {
		return false, ""
	}
	for _, rule := range enabledRiskRules() {
		switch rule.Source {
		case "input_text":
			if !riskRuleMatches(rule, text) {
				continue
			}
			if rule.Mode == "block" {
				recordRiskBlock(userID, rule)
				return true, firstNonEmptyRisk(rule.Name, "Request blocked by risk policy")
			}
			queueRiskJob(riskJob{UserID: userID, Rule: &rule})
		case "llm_input":
			queueRiskJob(riskJob{UserID: userID, Rule: &rule, Text: clipRiskText(text)})
		}
	}
	return false, ""
}

// RiskCheckOutputText is called only after the full response has been buffered
// when any synchronous output rule is enabled. It never persists response text.
func RiskCheckOutputText(userID uint, text string) (bool, string) {
	settings := CurrentRiskSettings()
	if !settings.Enabled || userID == 0 {
		return false, ""
	}
	for _, rule := range enabledRiskRules() {
		switch rule.Source {
		case "output_text":
			if !riskRuleMatches(rule, text) {
				continue
			}
			if rule.Mode == "block" {
				recordRiskBlock(userID, rule)
				return true, firstNonEmptyRisk(rule.Name, "Response blocked by risk policy")
			}
			queueRiskJob(riskJob{UserID: userID, Rule: &rule})
		case "llm_output":
			queueRiskJob(riskJob{UserID: userID, Rule: &rule, Text: clipRiskText(text)})
		}
	}
	return false, ""
}

func RiskNeedsSyncOutputBuffer() bool {
	if !CurrentRiskSettings().Enabled {
		return false
	}
	for _, rule := range enabledRiskRules() {
		if rule.Source == "output_text" && rule.Mode == "block" {
			return true
		}
	}
	return false
}

// QueueRiskUsage applies configured spend/token-based score decay asynchronously.
func QueueRiskUsage(userID uint, cost decimal.Decimal, inputTokens, outputTokens int) {
	if userID == 0 || !CurrentRiskSettings().Enabled {
		return
	}
	if cost.IsZero() && inputTokens == 0 && outputTokens == 0 {
		return
	}
	queueRiskJob(riskJob{
		UserID:       userID,
		Cost:         decimalToRiskFloat(cost),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		ApplyDecay:   true,
	})
}

func RiskSyncOutputBufferLimit() int64 { return riskMaxBufferedStreamBytes }

func riskRuleMatches(rule model.RiskRule, text string) bool {
	switch rule.MatchType {
	case "contains":
		return rule.Pattern != "" && strings.Contains(strings.ToLower(text), strings.ToLower(rule.Pattern))
	case "regex":
		compiled, err := regexp.Compile(rule.Pattern)
		return err == nil && compiled.MatchString(text)
	default:
		return false
	}
}

func riskUserSuspended(userID uint, settings RiskSettings) (bool, string) {
	var state model.UserRiskState
	if err := model.DB.First(&state, "user_id = ?", userID).Error; err != nil {
		return false, ""
	}
	if state.Banned {
		return true, state.Reason
	}
	var user model.User
	if err := model.DB.Select("id", "group_id").First(&user, userID).Error; err != nil {
		return false, ""
	}
	threshold := riskThresholdForUser(&user, state.RiskScore, settings)
	if state.RiskScore < threshold {
		return false, ""
	}
	now := time.Now()
	reason := "Risk threshold reached"
	model.DB.Model(&state).Updates(map[string]interface{}{"banned": true, "banned_at": &now, "reason": reason, "updated_at": now})
	recordRiskEvent(model.UserRiskEvent{UserID: userID, RuleName: "Risk threshold", Source: "threshold", ScoreAfter: state.RiskScore, Threshold: threshold, Action: "auto_banned", CreatedAt: now})
	return true, reason
}

func applyRiskTimeDecay(userID uint, settings RiskSettings) {
	if settings.DecayTimePerHour <= 0 || model.DB == nil {
		return
	}
	var existing model.UserRiskState
	if err := model.DB.Select("user_id").First(&existing, "user_id = ?", userID).Error; err != nil {
		return
	}
	tx := model.DB.Begin()
	if tx.Error != nil {
		return
	}
	defer tx.Rollback()
	state, err := lockOrCreateRiskState(tx, userID)
	if err != nil {
		return
	}
	now := time.Now()
	if state.LastDecayAt.IsZero() {
		state.LastDecayAt = now
		state.UpdatedAt = now
		_ = tx.Save(&state).Error
		_ = tx.Commit().Error
		return
	}
	elapsedHours := now.Sub(state.LastDecayAt).Hours()
	if elapsedHours <= 0 {
		return
	}
	delta := -math.Min(state.RiskScore, elapsedHours*settings.DecayTimePerHour)
	state.RiskScore = roundRisk(state.RiskScore + delta)
	state.LastDecayAt = now
	state.UpdatedAt = now
	if err := tx.Save(&state).Error; err != nil {
		return
	}
	if delta < 0 {
		if err := tx.Create(&model.UserRiskEvent{UserID: userID, RuleName: "Time decay", Source: "decay_time", Delta: delta, ScoreAfter: state.RiskScore, Action: "decayed", CreatedAt: now}).Error; err != nil {
			return
		}
	}
	_ = tx.Commit().Error
}

func queueRiskJob(job riskJob) {
	if job.UserID == 0 {
		return
	}
	riskQueueOnce.Do(func() {
		for worker := 0; worker < 2; worker++ {
			go func() {
				for next := range riskQueue {
					processRiskJob(next)
				}
			}()
		}
	})
	select {
	case riskQueue <- job:
	default:
		log.Printf("risk evaluation queue full; dropping asynchronous event for user %d", job.UserID)
	}
}

func processRiskJob(job riskJob) {
	settings := CurrentRiskSettings()
	if !settings.Enabled {
		return
	}
	if job.ApplyDecay {
		applyUsageRiskDecay(job, settings)
		return
	}
	if job.Rule == nil {
		return
	}
	var currentRule model.RiskRule
	if model.DB == nil || model.DB.First(&currentRule, job.Rule.ID).Error != nil || !currentRule.Enabled || currentRule.Mode != "score" {
		return
	}
	job.Rule = &currentRule
	delta := job.Rule.RiskPoints
	if strings.HasPrefix(job.Rule.Source, "llm_") {
		percent, err := runRiskLLMJudge(job.Rule, job.Text, settings)
		if err != nil {
			log.Printf("LLM risk assessment failed for rule %d: %v", job.Rule.ID, err)
			return
		}
		if percent < 0 {
			percent = 0
		}
		if percent > 100 {
			percent = 100
		}
		delta *= percent / 100
	}
	if delta > 0 {
		applyRiskDelta(job.UserID, job.Rule.ID, job.Rule.Name, job.Rule.Source, delta)
	}
}

func applyUsageRiskDecay(job riskJob, settings RiskSettings) {
	if strings.TrimSpace(settings.DecaySpendFormula) == "" {
		return
	}
	state := loadRiskState(job.UserID)
	if state.RiskScore <= 0 {
		return
	}
	variables := map[string]float64{
		"cost":           job.Cost,
		"input_tokens":   float64(job.InputTokens),
		"output_tokens":  float64(job.OutputTokens),
		"total_tokens":   float64(job.InputTokens + job.OutputTokens),
		"tokens":         float64(job.InputTokens + job.OutputTokens),
		"risk_score":     state.RiskScore,
		"total_spend":    0,
		"total_recharge": 0,
		"group_id":       0,
	}
	if user, err := riskUser(job.UserID); err == nil {
		variables["group_id"] = float64(user.GroupID)
		metrics := getRiskMetrics(user)
		variables["total_spend"] = metrics.TotalSpend
		variables["total_recharge"] = metrics.TotalRecharge
	}
	decay, err := evalRiskFormula(settings.DecaySpendFormula, variables)
	if err != nil || decay <= 0 {
		return
	}
	applyRiskDelta(job.UserID, 0, "Usage decay", "decay_usage", -decay)
}

func applyRiskDelta(userID uint, ruleID uint, ruleName, source string, delta float64) {
	if model.DB == nil || userID == 0 || math.IsNaN(delta) || math.IsInf(delta, 0) {
		return
	}
	settings := CurrentRiskSettings()
	if !settings.Enabled {
		return
	}
	user, err := riskUser(userID)
	if err != nil {
		return
	}
	tx := model.DB.Begin()
	if tx.Error != nil {
		return
	}
	defer tx.Rollback()
	state, err := lockOrCreateRiskState(tx, userID)
	if err != nil {
		return
	}
	now := time.Now()
	previousScore := state.RiskScore
	scoreAfter := math.Max(0, roundRisk(state.RiskScore+delta))
	actualDelta := roundRisk(scoreAfter - previousScore)
	threshold := riskThresholdForUser(user, scoreAfter, settings)
	action := "scored"
	if delta < 0 {
		action = "decayed"
	}
	var bannedAt *time.Time
	if !state.Banned && threshold > 0 && scoreAfter >= threshold {
		state.Banned = true
		bannedAt = &now
		state.BannedAt = bannedAt
		state.Reason = firstNonEmptyRisk(ruleName, "Risk threshold reached")
		action = "auto_banned"
	}
	state.RiskScore = scoreAfter
	state.LastDecayAt = now
	state.UpdatedAt = now
	if err := tx.Save(&state).Error; err != nil {
		return
	}
	var ruleIDPtr *uint
	if ruleID != 0 {
		ruleIDPtr = &ruleID
	}
	if err := tx.Create(&model.UserRiskEvent{
		UserID: userID, RuleID: ruleIDPtr, RuleName: ruleName, Source: source,
		Delta: actualDelta, ScoreAfter: scoreAfter, Threshold: threshold,
		Action: action, CreatedAt: now,
	}).Error; err != nil {
		return
	}
	if err := tx.Commit().Error; err != nil {
		log.Printf("failed to apply risk score for user %d: %v", userID, err)
	}
}

func lockOrCreateRiskState(tx *gorm.DB, userID uint) (model.UserRiskState, error) {
	state := model.UserRiskState{UserID: userID, LastDecayAt: time.Now()}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error; err != nil {
		return state, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, "user_id = ?", userID).Error; err != nil {
		return state, err
	}
	return state, nil
}

func riskThresholdForUser(user *model.User, score float64, settings RiskSettings) float64 {
	if user == nil {
		return 100
	}
	metrics := getRiskMetrics(user)
	variables := map[string]float64{
		"total_spend":    metrics.TotalSpend,
		"total_recharge": metrics.TotalRecharge,
		"group_id":       float64(user.GroupID),
		"risk_score":     score,
		"total_tokens":   metrics.TotalTokens,
		"tokens":         metrics.TotalTokens,
		"input_tokens":   metrics.InputTokens,
		"output_tokens":  metrics.OutputTokens,
		"request_count":  metrics.RequestCount,
	}
	formula := strings.TrimSpace(settings.ThresholdFormula)
	if formula == "" {
		formula = riskDefaultThreshold
	}
	threshold, err := evalRiskFormula(formula, variables)
	if err != nil || threshold <= 0 || math.IsInf(threshold, 0) || math.IsNaN(threshold) {
		return 100
	}
	return threshold
}

func getRiskMetrics(user *model.User) riskMetrics {
	if user == nil {
		return riskMetrics{}
	}
	now := time.Now()
	riskMetricsMu.Lock()
	if cached, ok := riskMetricsCache[user.ID]; ok && now.Before(cached.ExpiresAt) {
		cached.GroupID = float64(user.GroupID)
		riskMetricsMu.Unlock()
		return cached
	}
	riskMetricsMu.Unlock()
	metrics := riskMetrics{GroupID: float64(user.GroupID)}
	userID := user.ID
	if summary, err := model.SummarizeTokenLogs(model.TokenLogFilter{UserID: &userID}); err == nil {
		metrics.TotalSpend, _ = summary.TotalCost.Float64()
		metrics.TotalTokens = float64(summary.TotalTokens)
		metrics.InputTokens = float64(summary.InputTokens)
		metrics.OutputTokens = float64(summary.OutputTokens)
		metrics.RequestCount = float64(summary.RequestCount)
	}
	if model.DB != nil {
		var recharge decimal.Decimal
		if err := model.DB.Model(&model.PaymentOrder{}).
			Where("user_id = ? AND status = ?", user.ID, "paid").
			Select("COALESCE(SUM(amount), 0)").Scan(&recharge).Error; err == nil {
			metrics.TotalRecharge, _ = recharge.Float64()
		}
	}
	metrics.ExpiresAt = now.Add(30 * time.Second)
	riskMetricsMu.Lock()
	riskMetricsCache[user.ID] = metrics
	riskMetricsMu.Unlock()
	return metrics
}

func riskUser(userID uint) (*model.User, error) {
	var user model.User
	if model.DB == nil {
		return nil, fmt.Errorf("database is unavailable")
	}
	if err := model.DB.Select("id", "group_id").First(&user, userID).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func loadRiskState(userID uint) model.UserRiskState {
	var state model.UserRiskState
	if model.DB != nil {
		_ = model.DB.First(&state, "user_id = ?", userID).Error
	}
	return state
}

func recordRiskBlock(userID uint, rule model.RiskRule) {
	if model.DB == nil {
		return
	}
	now := time.Now()
	threshold := 0.0
	if user, err := riskUser(userID); err == nil {
		threshold = riskThresholdForUser(user, loadRiskState(userID).RiskScore, CurrentRiskSettings())
	}
	recordRiskEvent(model.UserRiskEvent{
		UserID: userID, RuleID: &rule.ID, RuleName: rule.Name, Source: rule.Source,
		ScoreAfter: loadRiskState(userID).RiskScore, Threshold: threshold, Action: "blocked", CreatedAt: now,
	})
}

func recordRiskEvent(event model.UserRiskEvent) {
	if model.DB != nil {
		if err := model.DB.Create(&event).Error; err != nil {
			log.Printf("failed to record risk event for user %d: %v", event.UserID, err)
		}
	}
}

func riskSuspensionMessage(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "Account suspended by risk policy"
	}
	return "Account suspended by risk policy: " + reason
}

func firstNonEmptyRisk(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "Risk policy"
}

func clipRiskText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= riskMaxLLMInputBytes {
		return value
	}
	clipped := value[:riskMaxLLMInputBytes]
	for !utf8.ValidString(clipped) {
		clipped = clipped[:len(clipped)-1]
	}
	return clipped
}

func roundRisk(value float64) float64 {
	return math.Round(value*10000) / 10000
}

func decimalToRiskFloat(value decimal.Decimal) float64 {
	parsed, _ := value.Float64()
	return parsed
}

func validateRiskFormula(expression string, variables map[string]float64) error {
	_, err := evalRiskFormula(expression, variables)
	return err
}

func safeRiskContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
