package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

var (
	Environment              string
	Port                     string
	DBDriver                 string
	DBPath                   string
	DBDSN                    string
	DBMaxOpenConns           int
	DBMaxIdleConns           int
	DBConnMaxLifetimeSeconds int
	DataPath                 string
	JWTSecret                string
	OIDCIssuer               string
	OIDCClientID             string
	OIDCSecret               string
	OIDCRedirect             string

	BootstrapAdminEmails   string
	BootstrapAdminOIDCSubs string
)

func Init() {
	err := godotenv.Load()
	if err != nil {
		log.Println("No .env file found, using system environment variables")
	}

	Environment = getEnv("APP_ENV", "development")
	Port = getEnv("PORT", "8080")
	DBDriver = strings.ToLower(strings.TrimSpace(getEnv("DB_DRIVER", "sqlite")))
	DBPath = getEnv("DB_PATH", "flai.db")
	DBDSN = strings.TrimSpace(getEnv("DB_DSN", os.Getenv("DATABASE_URL")))
	DBMaxOpenConns = getEnvPositiveInt("DB_MAX_OPEN_CONNS", 25)
	DBMaxIdleConns = getEnvPositiveInt("DB_MAX_IDLE_CONNS", 10)
	if DBMaxIdleConns > DBMaxOpenConns {
		log.Printf("DB_MAX_IDLE_CONNS cannot exceed DB_MAX_OPEN_CONNS; using %d", DBMaxOpenConns)
		DBMaxIdleConns = DBMaxOpenConns
	}
	DBConnMaxLifetimeSeconds = getEnvNonNegativeInt("DB_CONN_MAX_LIFETIME_SECONDS", 3600)
	DataPath = getEnv("DATA_PATH", "data")
	JWTSecret = resolveJWTSecret(Environment)
	OIDCIssuer = os.Getenv("OIDC_ISSUER")
	OIDCClientID = os.Getenv("OIDC_CLIENT_ID")
	OIDCSecret = os.Getenv("OIDC_CLIENT_SECRET")
	OIDCRedirect = os.Getenv("OIDC_REDIRECT_URL")
	BootstrapAdminEmails = os.Getenv("BOOTSTRAP_ADMIN_EMAILS")
	BootstrapAdminOIDCSubs = os.Getenv("BOOTSTRAP_ADMIN_OIDC_SUBS")
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvPositiveInt(key string, fallback int) int {
	value := getEnvNonNegativeInt(key, fallback)
	if value == 0 {
		log.Printf("%s must be greater than zero; using %d", key, fallback)
		return fallback
	}
	return value
}

func getEnvNonNegativeInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		log.Printf("invalid %s value %q; using %d", key, raw, fallback)
		return fallback
	}
	return value
}

// placeholderJWTSecret is the value shipped in .env.example. It is published in
// the repository, so it must never be used to sign real sessions.
const placeholderJWTSecret = "change-me-please"

// resolveJWTSecret returns the configured signing secret. An unset or
// placeholder secret is fatal outside development; in development it falls back
// to a random per-process secret rather than the published placeholder, since
// anyone could otherwise mint an admin token against such a deployment. The
// tradeoff is that sessions do not survive a restart until JWT_SECRET is set.
func resolveJWTSecret(env string) string {
	secret := strings.TrimSpace(getEnv("JWT_SECRET", ""))
	if secret != "" && secret != placeholderJWTSecret {
		return secret
	}
	if requiresSecureSecrets(env) {
		log.Fatal("JWT_SECRET must be set to a secure value outside development")
	}
	generated, err := randomSecret()
	if err != nil {
		log.Fatalf("failed to generate a development JWT secret: %v", err)
	}
	log.Printf("WARNING: JWT_SECRET is not set; signing sessions with a random secret for this process only. Sessions will be invalidated on restart. Set JWT_SECRET to a strong value.")
	return generated
}

func randomSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func requiresSecureSecrets(env string) bool {
	return !IsDevelopmentLike(env)
}

func IsDevelopmentLike(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "", "development", "dev", "local", "test":
		return true
	default:
		return false
	}
}

func IsBootstrapAdmin(email, oidcSub string) bool {
	return csvContains(BootstrapAdminEmails, email, false) ||
		csvContains(BootstrapAdminOIDCSubs, oidcSub, true)
}

func csvContains(raw, value string, caseSensitive bool) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}

	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if caseSensitive {
			if item == value {
				return true
			}
			continue
		}
		if strings.EqualFold(item, value) {
			return true
		}
	}

	return false
}
