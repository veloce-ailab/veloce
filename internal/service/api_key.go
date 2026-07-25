package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

const apiKeyPrefix = "sk-"

var (
	ErrAPIKeyNotFound      = errors.New("api key not found")
	ErrAPIKeyIPRestricted  = errors.New("api key is not allowed from this IP")
	ErrAPIKeyQuotaExceeded = errors.New("api key quota exceeded")
)

func GenerateAPIKey() (string, string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}

	raw := apiKeyPrefix + base64.RawURLEncoding.EncodeToString(b[:])
	return raw, HashAPIKey(raw), nil
}

func HashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func APIKeyPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) <= 12 {
		return raw
	}
	return raw[:8] + "..." + raw[len(raw)-4:]
}

func FindUserByAPIKey(raw, clientIP string) (*model.User, *model.APIKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil, ErrAPIKeyNotFound
	}

	apiKey, err := findAPIKeyByRaw(raw)
	if err == nil {
		if !apiKey.Enabled {
			return nil, nil, ErrAPIKeyNotFound
		}
		if !APIKeyAllowsIP(apiKey, clientIP) {
			return nil, nil, ErrAPIKeyIPRestricted
		}
		markAPIKeyUsed(apiKey)
		return &apiKey.User, apiKey, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, err
	}

	hash := HashAPIKey(raw)
	apiKey, err = findAPIKeyByHash(hash)
	if err == nil {
		if strings.TrimSpace(apiKey.APIKey) == "" {
			if updateErr := model.DB.Model(apiKey).Update("api_key", raw).Error; updateErr != nil {
				return nil, nil, updateErr
			}
			apiKey.APIKey = raw
		}
		if !apiKey.Enabled {
			return nil, nil, ErrAPIKeyNotFound
		}
		if !APIKeyAllowsIP(apiKey, clientIP) {
			return nil, nil, ErrAPIKeyIPRestricted
		}
		markAPIKeyUsed(apiKey)
		return &apiKey.User, apiKey, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, err
	}

	user, err := findLegacyUserByAPIKey(raw)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, err
		}
		user, err = findLegacyUserByAPIKey(hash)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil, ErrAPIKeyNotFound
			}
			return nil, nil, err
		}
	}
	if user.APIKey != raw {
		if err := model.DB.Model(user).Update("api_key", raw).Error; err != nil {
			return nil, nil, err
		}
		user.APIKey = raw
	}

	apiKey, err = ensureLegacyAPIKey(user, raw, hash, APIKeyPrefix(raw))
	if err != nil {
		return nil, nil, err
	}
	markAPIKeyUsed(apiKey)
	return user, apiKey, nil
}

func APIKeyAllowsModel(apiKey *model.APIKey, modelName string) bool {
	if apiKey == nil {
		return true
	}
	modelName = normalizeAPIKeyModelName(modelName)
	allowed := ParseList(apiKey.AllowedModels)
	if len(allowed) == 0 {
		return true
	}
	for _, item := range allowed {
		if normalizeAPIKeyModelName(item) == modelName {
			return true
		}
	}
	return false
}

func normalizeAPIKeyModelName(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= len("models/") && strings.EqualFold(value[:len("models/")], "models/") {
		return strings.TrimSpace(value[len("models/"):])
	}
	return value
}

func APIKeyAllowsIP(apiKey *model.APIKey, clientIP string) bool {
	if apiKey == nil {
		return true
	}
	allowed := ParseList(apiKey.AllowedIPs)
	if len(allowed) == 0 {
		return true
	}

	client := net.ParseIP(strings.TrimSpace(clientIP))
	if client == nil {
		return false
	}
	for _, item := range allowed {
		if itemIP := net.ParseIP(item); itemIP != nil {
			if itemIP.Equal(client) {
				return true
			}
			continue
		}
		_, network, err := net.ParseCIDR(item)
		if err == nil && network.Contains(client) {
			return true
		}
	}
	return false
}

func APIKeyAllowsUserChannel(apiKey *model.APIKey, userChannelID *uint) bool {
	if PersonalModeEnabled() {
		return true
	}
	if apiKey == nil {
		return true
	}
	allowed := ParseUintList(apiKey.AllowedUserChannels)
	if len(allowed) != 1 || userChannelID == nil {
		return false
	}
	return allowed[0] == *userChannelID
}

func APIKeyQuotaExceeded(apiKey *model.APIKey, cost decimal.Decimal) (bool, error) {
	return APIKeyQuotaExceededInTx(model.DB, apiKey, cost)
}

func APIKeyQuotaExceededInTx(tx *gorm.DB, apiKey *model.APIKey, cost decimal.Decimal) (bool, error) {
	if PersonalModeEnabledInTx(tx) {
		return false, nil
	}
	if apiKey == nil || apiKey.QuotaLimit.LessThanOrEqual(decimal.Zero) {
		return false, nil
	}
	used, err := APIKeyUsageCostSince(tx, apiKey.ID, apiKey.UserID, apiKey.UsageResetAt)
	if err != nil {
		return false, err
	}
	return apiKeyQuotaExceeded(apiKey, used, cost), nil
}

func APIKeyUsageCost(tx *gorm.DB, apiKeyID uint, userID uint) (decimal.Decimal, error) {
	var apiKey model.APIKey
	if tx == nil {
		tx = model.DB
	}
	if apiKeyID == 0 || userID == 0 {
		return decimal.Zero, nil
	}
	if err := tx.Select("usage_reset_at").Where("id = ? AND user_id = ?", apiKeyID, userID).First(&apiKey).Error; err != nil {
		return decimal.Zero, err
	}
	return APIKeyUsageCostSince(tx, apiKeyID, userID, apiKey.UsageResetAt)
}

func APIKeyUsageCostSince(tx *gorm.DB, apiKeyID uint, userID uint, usageResetAt *time.Time) (decimal.Decimal, error) {
	if tx == nil {
		tx = model.DB
	}
	if apiKeyID == 0 || userID == 0 {
		return decimal.Zero, nil
	}
	summary, err := model.SummarizeTokenLogs(model.TokenLogFilter{APIKeyID: &apiKeyID, UserID: &userID, Since: usageResetAt})
	if err != nil {
		return decimal.Zero, err
	}
	return summary.TotalCost, nil
}

func apiKeyQuotaExceeded(apiKey *model.APIKey, used decimal.Decimal, cost decimal.Decimal) bool {
	if apiKey == nil || apiKey.QuotaLimit.LessThanOrEqual(decimal.Zero) {
		return false
	}
	return used.Add(cost).GreaterThan(apiKey.QuotaLimit) || used.GreaterThanOrEqual(apiKey.QuotaLimit)
}

func ParseList(raw string) []string {
	seen := map[string]struct{}{}
	items := []string{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		items = append(items, item)
	}
	sort.Strings(items)
	return items
}

func ParseUintList(raw string) []uint {
	seen := map[uint]struct{}{}
	items := []uint{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parsed, err := strconv.ParseUint(item, 10, 0)
		if err != nil || parsed == 0 {
			continue
		}
		value := uint(parsed)
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		items = append(items, value)
	}
	sort.Slice(items, func(i, j int) bool { return items[i] < items[j] })
	return items
}

func JoinList(items []string) string {
	return strings.Join(ParseList(strings.Join(items, ",")), ",")
}

func JoinUintList(items []uint) string {
	raw := make([]string, 0, len(items))
	for _, item := range items {
		raw = append(raw, strconv.FormatUint(uint64(item), 10))
	}
	parsed := ParseUintList(strings.Join(raw, ","))
	out := make([]string, 0, len(parsed))
	for _, item := range parsed {
		out = append(out, strconv.FormatUint(uint64(item), 10))
	}
	return strings.Join(out, ",")
}

func findAPIKeyByRaw(raw string) (*model.APIKey, error) {
	var apiKey model.APIKey
	if err := model.DB.Preload("User").Where("api_key = ?", raw).First(&apiKey).Error; err != nil {
		return nil, err
	}
	return &apiKey, nil
}

func findAPIKeyByHash(hash string) (*model.APIKey, error) {
	var apiKey model.APIKey
	if err := model.DB.Preload("User").Where("key_hash = ?", hash).First(&apiKey).Error; err != nil {
		return nil, err
	}
	return &apiKey, nil
}

func findLegacyUserByAPIKey(stored string) (*model.User, error) {
	var user model.User
	if err := model.DB.Where("api_key = ?", stored).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func ensureLegacyAPIKey(user *model.User, raw, hash, prefix string) (*model.APIKey, error) {
	var apiKey model.APIKey
	err := model.DB.Where("key_hash = ?", hash).First(&apiKey).Error
	if err == nil {
		if strings.TrimSpace(apiKey.APIKey) == "" {
			if updateErr := model.DB.Model(&apiKey).Update("api_key", raw).Error; updateErr != nil {
				return nil, updateErr
			}
			apiKey.APIKey = raw
		}
		return &apiKey, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	apiKey = model.APIKey{
		UserID:    user.ID,
		Name:      "Legacy key",
		APIKey:    raw,
		KeyHash:   hash,
		KeyPrefix: prefix,
		Enabled:   true,
	}
	if err := model.DB.Create(&apiKey).Error; err != nil {
		return nil, err
	}
	apiKey.User = *user
	return &apiKey, nil
}

func markAPIKeyUsed(apiKey *model.APIKey) {
	now := time.Now()
	model.DB.Model(apiKey).Update("last_used_at", now)
	apiKey.LastUsedAt = &now
}
