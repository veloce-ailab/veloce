package api

import (
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type CheckInAPI struct{}

type checkInStatusResponse struct {
	Enabled             bool                 `json:"enabled"`
	CheckedInToday      bool                 `json:"checked_in_today"`
	CurrentStreak       int                  `json:"current_streak"`
	TodayRewardPreview  string               `json:"today_reward_preview"`
	RandomEnabled       bool                 `json:"random_enabled"`
	RandomMin           string               `json:"random_min"`
	RandomMax           string               `json:"random_max"`
	NextStreakReward    *nextStreakReward    `json:"next_streak_reward,omitempty"`
	CurrencyDisplayName string               `json:"currency_display_name"`
	RecentRecords       []checkInRecordBrief `json:"recent_records"`
}

type nextStreakReward struct {
	Day    int    `json:"day"`
	Amount string `json:"amount"`
}

type checkInRecordBrief struct {
	CheckInDate  string `json:"check_in_date"`
	RewardAmount string `json:"reward_amount"`
	StreakDays   int    `json:"streak_days"`
	RewardKind   string `json:"reward_kind"`
}

type checkInClaimResponse struct {
	RewardAmount        string `json:"reward_amount"`
	RewardKind          string `json:"reward_kind"`
	StreakDays          int    `json:"streak_days"`
	Balance             string `json:"balance"`
	CurrencyDisplayName string `json:"currency_display_name"`
}

type checkInConfig struct {
	Enabled             bool
	DailyReward         decimal.Decimal
	Timezone            string
	StreakEnabled       bool
	StreakCycleDays     int
	StreakRewards       map[int]decimal.Decimal
	RandomEnabled       bool
	RandomMin           decimal.Decimal
	RandomMax           decimal.Decimal
	CurrencyDisplayName string
}

func (api *CheckInAPI) Status(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	cfg := currentCheckInConfig()
	today, _, err := checkInToday(cfg.Timezone)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid check-in timezone"})
		return
	}

	var todayCount int64
	model.DB.Model(&model.CheckInRecord{}).Where("user_id = ? AND check_in_date = ?", user.ID, today).Count(&todayCount)
	currentStreak := latestCheckInStreak(user.ID, today, cfg.Timezone)
	preview := cfg.DailyReward
	if cfg.RandomEnabled {
		preview = preview.Add(cfg.RandomMin)
	}
	var records []model.CheckInRecord
	model.DB.Where("user_id = ?", user.ID).Order("check_in_date DESC").Limit(7).Find(&records)
	briefs := make([]checkInRecordBrief, 0, len(records))
	for _, record := range records {
		briefs = append(briefs, checkInRecordBrief{
			CheckInDate:  record.CheckInDate,
			RewardAmount: record.RewardAmount.String(),
			StreakDays:   record.StreakDays,
			RewardKind:   record.RewardKind,
		})
	}
	c.JSON(http.StatusOK, checkInStatusResponse{
		Enabled:             cfg.Enabled,
		CheckedInToday:      todayCount > 0,
		CurrentStreak:       currentStreak,
		TodayRewardPreview:  preview.String(),
		RandomEnabled:       cfg.RandomEnabled,
		RandomMin:           cfg.RandomMin.String(),
		RandomMax:           cfg.RandomMax.String(),
		NextStreakReward:    nextConfiguredStreakReward(currentStreak, cfg),
		CurrencyDisplayName: cfg.CurrencyDisplayName,
		RecentRecords:       briefs,
	})
}

func (api *CheckInAPI) ListRecords(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var records []model.CheckInRecord
	query := model.DB.Model(&model.CheckInRecord{}).Where("user_id = ?", user.ID)
	var err error
	query, err = applyDateStringRange(query, c, "check_in_date")
	if writePaginationError(c, err) {
		return
	}
	if !wantsPaginatedResponse(c) {
		if err := query.Order("check_in_date DESC").Limit(100).Find(&records).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load check-in records"})
			return
		}
		briefs := make([]checkInRecordBrief, 0, len(records))
		for _, record := range records {
			briefs = append(briefs, checkInRecordBrief{
				CheckInDate:  record.CheckInDate,
				RewardAmount: record.RewardAmount.String(),
				StreakDays:   record.StreakDays,
				RewardKind:   record.RewardKind,
			})
		}
		c.JSON(http.StatusOK, briefs)
		return
	}

	page, pageSize := parsePagination(c)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count check-in records"})
		return
	}
	if err := query.Order("check_in_date DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&records).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load check-in records"})
		return
	}
	briefs := make([]checkInRecordBrief, 0, len(records))
	for _, record := range records {
		briefs = append(briefs, checkInRecordBrief{
			CheckInDate:  record.CheckInDate,
			RewardAmount: record.RewardAmount.String(),
			StreakDays:   record.StreakDays,
			RewardKind:   record.RewardKind,
		})
	}
	c.JSON(http.StatusOK, paginatedResponse{Items: briefs, Total: total, Page: page, PageSize: pageSize})
}

func (api *CheckInAPI) Claim(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	cfg := currentCheckInConfig()
	if !cfg.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Check-in is disabled"})
		return
	}
	today, yesterday, err := checkInToday(cfg.Timezone)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid check-in timezone"})
		return
	}
	var refreshed model.User
	var created model.CheckInRecord
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var existing model.CheckInRecord
		err := tx.Where("user_id = ? AND check_in_date = ?", user.ID, today).First(&existing).Error
		if err == nil {
			return errors.New("already checked in today")
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		streak := 1
		var previous model.CheckInRecord
		if err := tx.Where("user_id = ?", user.ID).Order("check_in_date DESC").First(&previous).Error; err == nil && previous.CheckInDate == yesterday {
			streak = previous.StreakDays + 1
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		reward, kind, err := computeCheckInReward(cfg, streak)
		if err != nil {
			return err
		}
		if reward.LessThan(decimal.Zero) {
			return errors.New("check-in reward must not be negative")
		}
		created = model.CheckInRecord{
			UserID:       user.ID,
			CheckInDate:  today,
			RewardAmount: reward,
			StreakDays:   streak,
			RewardKind:   kind,
		}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.User{}).Where("id = ?", user.ID).UpdateColumn("balance", gorm.Expr("balance + ?", reward)).Error; err != nil {
			return err
		}
		return tx.First(&refreshed, user.ID).Error
	})
	if err != nil {
		if strings.Contains(err.Error(), "already checked") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Already checked in today"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, checkInClaimResponse{
		RewardAmount:        created.RewardAmount.String(),
		RewardKind:          created.RewardKind,
		StreakDays:          created.StreakDays,
		Balance:             refreshed.Balance.String(),
		CurrencyDisplayName: cfg.CurrencyDisplayName,
	})
}

func currentCheckInConfig() checkInConfig {
	return checkInConfig{
		Enabled:             settingBool("checkin_enabled", false),
		DailyReward:         settingDecimal("checkin_daily_reward", "0"),
		Timezone:            firstNonEmptyString(settingString("checkin_timezone", "Asia/Shanghai"), "Asia/Shanghai"),
		StreakEnabled:       settingBool("checkin_streak_enabled", false),
		StreakCycleDays:     settingInt("checkin_streak_cycle_days", 7),
		StreakRewards:       settingStreakRewards("checkin_streak_rewards"),
		RandomEnabled:       settingBool("checkin_random_enabled", false),
		RandomMin:           settingDecimal("checkin_random_min", "0"),
		RandomMax:           settingDecimal("checkin_random_max", "0"),
		CurrencyDisplayName: firstNonEmptyString(settingString("payment_currency_display_name", "$"), "$"),
	}
}

func checkInToday(timezone string) (string, string, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return "", "", err
	}
	now := time.Now().In(loc)
	return now.Format("2006-01-02"), now.AddDate(0, 0, -1).Format("2006-01-02"), nil
}

func latestCheckInStreak(userID uint, today string, timezone string) int {
	var latest model.CheckInRecord
	if err := model.DB.Where("user_id = ?", userID).Order("check_in_date DESC").First(&latest).Error; err != nil {
		return 0
	}
	if latest.CheckInDate == today {
		return latest.StreakDays
	}
	_, yesterday, err := checkInToday(timezone)
	if err != nil {
		return latest.StreakDays
	}
	if latest.CheckInDate == yesterday {
		return latest.StreakDays
	}
	return 0
}

func computeCheckInReward(cfg checkInConfig, streak int) (decimal.Decimal, string, error) {
	reward := cfg.DailyReward
	kinds := []string{"daily"}
	if cfg.StreakEnabled {
		streakDay := streak
		if cfg.StreakCycleDays > 0 {
			streakDay = ((streak - 1) % cfg.StreakCycleDays) + 1
		}
		if bonus, exists := cfg.StreakRewards[streakDay]; exists {
			reward = reward.Add(bonus)
			kinds = append(kinds, "streak")
		}
	}
	if cfg.RandomEnabled && cfg.RandomMax.GreaterThanOrEqual(cfg.RandomMin) && cfg.RandomMax.GreaterThan(decimal.Zero) {
		randomReward, err := randomDecimalBetween(cfg.RandomMin, cfg.RandomMax)
		if err != nil {
			return decimal.Zero, "", err
		}
		reward = reward.Add(randomReward)
		kinds = append(kinds, "random")
	}
	return reward, strings.Join(kinds, "_"), nil
}

func randomDecimalBetween(min decimal.Decimal, max decimal.Decimal) (decimal.Decimal, error) {
	minCents := min.Mul(decimal.NewFromInt(100)).IntPart()
	maxCents := max.Mul(decimal.NewFromInt(100)).IntPart()
	if maxCents < minCents {
		return decimal.Zero, errors.New("invalid random reward range")
	}
	span := maxCents - minCents + 1
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(span))
	if err != nil {
		return decimal.Zero, err
	}
	return decimal.NewFromInt(minCents + value.Int64()).Div(decimal.NewFromInt(100)), nil
}

func nextConfiguredStreakReward(currentStreak int, cfg checkInConfig) *nextStreakReward {
	if !cfg.StreakEnabled || len(cfg.StreakRewards) == 0 {
		return nil
	}
	limit := cfg.StreakCycleDays
	if limit <= 0 {
		limit = currentStreak + 365
	}
	for offset := 1; offset <= limit; offset++ {
		day := currentStreak + offset
		streakDay := day
		if cfg.StreakCycleDays > 0 {
			streakDay = ((day - 1) % cfg.StreakCycleDays) + 1
		}
		if amount, exists := cfg.StreakRewards[streakDay]; exists {
			return &nextStreakReward{Day: streakDay, Amount: amount.String()}
		}
	}
	return nil
}

func settingDecimal(key string, fallback string) decimal.Decimal {
	value, err := decimal.NewFromString(strings.TrimSpace(model.GetSystemSetting(key, fallback)))
	if err != nil {
		value, _ = decimal.NewFromString(fallback)
	}
	return value
}

func settingInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(model.GetSystemSetting(key, strconv.Itoa(fallback))))
	if err != nil {
		return fallback
	}
	return value
}

func settingStreakRewards(key string) map[int]decimal.Decimal {
	raw := strings.TrimSpace(model.GetSystemSetting(key, "{}"))
	parsed := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return map[int]decimal.Decimal{}
	}
	result := map[int]decimal.Decimal{}
	for day, amount := range parsed {
		parsedDay, err := strconv.Atoi(strings.TrimSpace(day))
		if err != nil || parsedDay <= 0 {
			continue
		}
		parsedAmount, err := decimal.NewFromString(strings.TrimSpace(amount))
		if err != nil || parsedAmount.LessThan(decimal.Zero) {
			continue
		}
		result[parsedDay] = parsedAmount
	}
	return result
}
