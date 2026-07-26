package service

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

// Desktop login uses a PKCE authorization-code flow that only the Veloce
// Desktop client consumes: the embedded authorize page mints a code for the
// signed-in user, and the desktop app exchanges it together with the code
// verifier for a JWT. Codes are single-use, short-lived, and kept in memory
// only, so a restart simply invalidates pending authorizations.

const desktopAuthCodeTTL = 5 * time.Minute

var (
	// S256 code challenge: base64url(SHA-256), always 43 characters unpadded.
	desktopCodeChallengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	// RFC 7636 section 4.1 code verifier alphabet and length.
	desktopCodeVerifierPattern = regexp.MustCompile(`^[A-Za-z0-9\-._~]{43,128}$`)

	desktopAuthCodesMu sync.Mutex
	desktopAuthCodes   = map[string]desktopAuthCode{}
)

type desktopAuthCode struct {
	userID        uint
	codeChallenge string
	expiresAt     time.Time
}

// CreateDesktopAuthCode issues a single-use authorization code bound to the
// given user and S256 PKCE code challenge.
func (s *AuthService) CreateDesktopAuthCode(userID uint, codeChallenge string) (string, error) {
	if userID == 0 {
		return "", errors.New("invalid user")
	}
	if !desktopCodeChallengePattern.MatchString(codeChallenge) {
		return "", errors.New("invalid code challenge")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	code := base64.RawURLEncoding.EncodeToString(raw[:])

	now := time.Now()
	desktopAuthCodesMu.Lock()
	defer desktopAuthCodesMu.Unlock()
	for existing, entry := range desktopAuthCodes {
		if now.After(entry.expiresAt) {
			delete(desktopAuthCodes, existing)
		}
	}
	desktopAuthCodes[code] = desktopAuthCode{
		userID:        userID,
		codeChallenge: codeChallenge,
		expiresAt:     now.Add(desktopAuthCodeTTL),
	}
	return code, nil
}

// ExchangeDesktopAuthCode consumes an authorization code, verifies the PKCE
// code verifier against the stored challenge, and returns the user with a
// freshly issued JWT.
func (s *AuthService) ExchangeDesktopAuthCode(code string, codeVerifier string) (*model.User, string, error) {
	if code == "" || !desktopCodeVerifierPattern.MatchString(codeVerifier) {
		return nil, "", errors.New("invalid authorization code or verifier")
	}

	desktopAuthCodesMu.Lock()
	entry, ok := desktopAuthCodes[code]
	if ok {
		delete(desktopAuthCodes, code)
	}
	desktopAuthCodesMu.Unlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, "", errors.New("authorization code is invalid or expired")
	}

	digest := sha256.Sum256([]byte(codeVerifier))
	computed := base64.RawURLEncoding.EncodeToString(digest[:])
	if subtle.ConstantTimeCompare([]byte(computed), []byte(entry.codeChallenge)) != 1 {
		return nil, "", errors.New("code verifier does not match")
	}

	var user model.User
	if err := model.DB.First(&user, entry.userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errors.New("user no longer exists")
		}
		return nil, "", err
	}

	token, err := s.issueJWT(&user)
	if err != nil {
		return nil, "", err
	}
	return &user, token, nil
}
