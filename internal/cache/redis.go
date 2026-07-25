// Package cache owns the application's optional Redis connection.
package cache

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/redis/go-redis/v9"
)

const (
	redisSettingEnabled  = "redis_enabled"
	redisSettingAddress  = "redis_address"
	redisSettingUsername = "redis_username"
	redisSettingPassword = "redis_password"
	redisSettingDatabase = "redis_database"
	redisSettingTLS      = "redis_tls_enabled"
)

// RedisConfig is the resolved Redis connection configuration. Values in the
// REDIS_* environment variables take precedence over system settings.
type RedisConfig struct {
	Enabled  bool
	Address  string
	Username string
	Password string
	Database int
	TLS      bool
}

var (
	redisMu     sync.RWMutex
	redisClient *redis.Client
)

// ResolveRedisConfig loads Redis configuration from system settings, applying
// process environment overrides. REDIS_URL overrides the individual Redis
// connection environment variables when it is set.
func ResolveRedisConfig() (RedisConfig, error) {
	return resolveRedisConfig(model.GetSystemSetting, os.LookupEnv)
}

func resolveRedisConfig(setting func(string, string) string, lookupEnv func(string) (string, bool)) (RedisConfig, error) {
	config := RedisConfig{
		Enabled:  settingBool(setting(redisSettingEnabled, "false")),
		Address:  strings.TrimSpace(setting(redisSettingAddress, "127.0.0.1:6379")),
		Username: setting(redisSettingUsername, ""),
		Password: setting(redisSettingPassword, ""),
		TLS:      settingBool(setting(redisSettingTLS, "false")),
	}

	database, err := parseDatabase(setting(redisSettingDatabase, "0"))
	if err != nil {
		return RedisConfig{}, fmt.Errorf("invalid Redis database system setting: %w", err)
	}
	config.Database = database

	if rawURL, ok := lookupEnv("REDIS_URL"); ok && strings.TrimSpace(rawURL) != "" {
		options, err := redis.ParseURL(strings.TrimSpace(rawURL))
		if err != nil {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_URL: %w", err)
		}
		config.Address = options.Addr
		config.Username = options.Username
		config.Password = options.Password
		config.Database = options.DB
		config.TLS = options.TLSConfig != nil
	} else {
		if value, ok := lookupEnv("REDIS_ADDR"); ok {
			config.Address = strings.TrimSpace(value)
		}
		if value, ok := lookupEnv("REDIS_USERNAME"); ok {
			config.Username = value
		}
		if value, ok := lookupEnv("REDIS_PASSWORD"); ok {
			config.Password = value
		}
		if value, ok := lookupEnv("REDIS_DB"); ok {
			database, err := parseDatabase(value)
			if err != nil {
				return RedisConfig{}, fmt.Errorf("invalid REDIS_DB: %w", err)
			}
			config.Database = database
		}
		if value, ok := lookupEnv("REDIS_TLS"); ok {
			enabled, err := strconv.ParseBool(strings.TrimSpace(value))
			if err != nil {
				return RedisConfig{}, fmt.Errorf("invalid REDIS_TLS: %w", err)
			}
			config.TLS = enabled
		}
	}

	if value, ok := lookupEnv("REDIS_ENABLED"); ok {
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return RedisConfig{}, fmt.Errorf("invalid REDIS_ENABLED: %w", err)
		}
		config.Enabled = enabled
	}
	if config.Enabled && config.Address == "" {
		return RedisConfig{}, fmt.Errorf("Redis address must be set when Redis is enabled")
	}
	return config, nil
}

func settingBool(value string) bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && enabled
}

func parseDatabase(value string) (int, error) {
	database, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || database < 0 {
		return 0, fmt.Errorf("must be a non-negative integer")
	}
	return database, nil
}

// Connect establishes and validates the configured Redis connection. Redis is
// optional, so a disabled configuration leaves Client nil.
func Connect(ctx context.Context) error {
	config, err := ResolveRedisConfig()
	if err != nil {
		return err
	}
	if !config.Enabled {
		return Close()
	}

	options := &redis.Options{
		Addr:         config.Address,
		Username:     config.Username,
		Password:     config.Password,
		DB:           config.Database,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	}
	if config.TLS {
		options.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	client := redis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return fmt.Errorf("connect to Redis at %q: %w", config.Address, err)
	}

	redisMu.Lock()
	previous := redisClient
	redisClient = client
	redisMu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	log.Printf("Redis connected: address=%q database=%d tls=%t", config.Address, config.Database, config.TLS)
	return nil
}

// Client returns the active Redis client, or nil when Redis is disabled.
func Client() *redis.Client {
	redisMu.RLock()
	defer redisMu.RUnlock()
	return redisClient
}

// Close releases the active Redis connection.
func Close() error {
	redisMu.Lock()
	client := redisClient
	redisClient = nil
	redisMu.Unlock()
	if client == nil {
		return nil
	}
	return client.Close()
}
