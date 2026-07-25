package service

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

const (
	passkeyPurposeRegistration = "passkey_registration"
	passkeyPurposeLogin        = "passkey_login"
	passkeyChallengeTTL        = 5 * time.Minute
)

type PasskeyCredentialDescriptor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type PasskeyOptions struct {
	PublicKey map[string]interface{} `json:"publicKey"`
}

type PasskeyRegistrationInput struct {
	Name       string                        `json:"name"`
	Credential PasskeyRegistrationCredential `json:"credential"`
}

type PasskeyRegistrationCredential struct {
	ID       string                      `json:"id"`
	RawID    string                      `json:"rawId"`
	Type     string                      `json:"type"`
	Response PasskeyRegistrationResponse `json:"response"`
}

type PasskeyRegistrationResponse struct {
	ClientDataJSON    string `json:"clientDataJSON"`
	AttestationObject string `json:"attestationObject"`
}

type PasskeyAuthenticationCredential struct {
	ID       string                        `json:"id"`
	RawID    string                        `json:"rawId"`
	Type     string                        `json:"type"`
	Response PasskeyAuthenticationResponse `json:"response"`
}

type PasskeyAuthenticationResponse struct {
	ClientDataJSON    string `json:"clientDataJSON"`
	AuthenticatorData string `json:"authenticatorData"`
	Signature         string `json:"signature"`
	UserHandle        string `json:"userHandle"`
}

type clientData struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Origin    string `json:"origin"`
}

type parsedAuthenticatorData struct {
	RPIDHash            []byte
	Flags               byte
	SignCount           uint32
	AAGUID              []byte
	CredentialID        []byte
	CredentialPublicKey []byte
}

func (s *AuthService) BeginPasskeyRegistration(user *model.User, rpName, rpID, origin string) (PasskeyOptions, error) {
	if err := ensurePasskeyEnabled(); err != nil {
		return PasskeyOptions{}, err
	}
	if user == nil || user.ID == 0 {
		return PasskeyOptions{}, errors.New("user is required")
	}
	challenge, err := createWebAuthnChallenge(passkeyPurposeRegistration, &user.ID, rpID, origin)
	if err != nil {
		return PasskeyOptions{}, err
	}

	var credentials []model.PasskeyCredential
	if err := model.DB.Where("user_id = ?", user.ID).Order("created_at ASC").Find(&credentials).Error; err != nil {
		return PasskeyOptions{}, err
	}
	excludeCredentials := make([]PasskeyCredentialDescriptor, 0, len(credentials))
	for _, credential := range credentials {
		excludeCredentials = append(excludeCredentials, PasskeyCredentialDescriptor{
			Type: "public-key",
			ID:   encodeBase64URL(credential.CredentialID),
		})
	}

	return PasskeyOptions{PublicKey: map[string]interface{}{
		"challenge": challenge,
		"rp": map[string]interface{}{
			"name": firstNonEmpty(strings.TrimSpace(rpName), "flai"),
			"id":   rpID,
		},
		"user": map[string]interface{}{
			"id":          encodeBase64URL([]byte(fmt.Sprintf("%d", user.ID))),
			"name":        firstNonEmpty(strings.TrimSpace(user.Email), strings.TrimSpace(user.Username)),
			"displayName": firstNonEmpty(strings.TrimSpace(user.Username), strings.TrimSpace(user.Email)),
		},
		"pubKeyCredParams": []map[string]interface{}{
			{"type": "public-key", "alg": -7},
			{"type": "public-key", "alg": -257},
		},
		"timeout":            60000,
		"attestation":        "none",
		"excludeCredentials": excludeCredentials,
		"authenticatorSelection": map[string]interface{}{
			"userVerification": "preferred",
			"residentKey":      "preferred",
		},
	}}, nil
}

func (s *AuthService) FinishPasskeyRegistration(user *model.User, input PasskeyRegistrationInput) (model.PasskeyCredential, error) {
	if err := ensurePasskeyEnabled(); err != nil {
		return model.PasskeyCredential{}, err
	}
	if user == nil || user.ID == 0 {
		return model.PasskeyCredential{}, errors.New("user is required")
	}
	credential := input.Credential
	if credential.Type != "" && credential.Type != "public-key" {
		return model.PasskeyCredential{}, errors.New("invalid credential type")
	}

	clientDataJSON, err := decodeBase64URL(credential.Response.ClientDataJSON)
	if err != nil {
		return model.PasskeyCredential{}, errors.New("invalid client data")
	}
	var client clientData
	if err := json.Unmarshal(clientDataJSON, &client); err != nil {
		return model.PasskeyCredential{}, errors.New("invalid client data")
	}
	if client.Type != "webauthn.create" {
		return model.PasskeyCredential{}, errors.New("invalid passkey registration response")
	}

	challenge, err := consumeWebAuthnChallenge(client.Challenge, passkeyPurposeRegistration, &user.ID)
	if err != nil {
		return model.PasskeyCredential{}, err
	}
	if err := verifyPasskeyOrigin(client.Origin, challenge.Origin); err != nil {
		return model.PasskeyCredential{}, err
	}

	attestationObject, err := decodeBase64URL(credential.Response.AttestationObject)
	if err != nil {
		return model.PasskeyCredential{}, errors.New("invalid attestation object")
	}
	authData, err := authDataFromAttestationObject(attestationObject)
	if err != nil {
		return model.PasskeyCredential{}, err
	}
	parsed, err := parseAuthenticatorData(authData, true)
	if err != nil {
		return model.PasskeyCredential{}, err
	}
	if err := verifyAuthenticatorData(parsed, challenge.RPID, true); err != nil {
		return model.PasskeyCredential{}, err
	}
	if _, err := publicKeyFromCOSE(parsed.CredentialPublicKey); err != nil {
		return model.PasskeyCredential{}, err
	}
	if rawID := firstNonEmpty(credential.RawID, credential.ID); rawID != "" {
		decodedRawID, err := decodeBase64URL(rawID)
		if err == nil && len(decodedRawID) > 0 && string(decodedRawID) != string(parsed.CredentialID) {
			return model.PasskeyCredential{}, errors.New("credential id mismatch")
		}
	}

	name := truncatePasskeyName(strings.TrimSpace(input.Name))
	if name == "" {
		name = "Passkey"
	}
	record := model.PasskeyCredential{
		UserID:        user.ID,
		Name:          name,
		CredentialID:  parsed.CredentialID,
		PublicKeyCOSE: parsed.CredentialPublicKey,
		AAGUID:        parsed.AAGUID,
		SignCount:     parsed.SignCount,
	}
	if err := model.DB.Create(&record).Error; err != nil {
		return model.PasskeyCredential{}, err
	}
	return record, nil
}

func (s *AuthService) BeginPasskeyLogin(identifier, rpID, origin string) (PasskeyOptions, error) {
	if required, err := s.InitialSetupRequired(); err != nil {
		return PasskeyOptions{}, err
	} else if required {
		return PasskeyOptions{}, ErrInitialSetupRequired
	}
	if err := ensurePasskeyEnabled(); err != nil {
		return PasskeyOptions{}, err
	}

	var userID *uint
	allowCredentials := []PasskeyCredentialDescriptor{}
	identifier = strings.TrimSpace(identifier)
	if identifier != "" {
		var user model.User
		err := model.DB.Where("username = ? OR email = ?", identifier, strings.ToLower(identifier)).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PasskeyOptions{}, errors.New("no passkey is available for this account")
		}
		if err != nil {
			return PasskeyOptions{}, err
		}
		userID = &user.ID

		var credentials []model.PasskeyCredential
		if err := model.DB.Where("user_id = ?", user.ID).Order("created_at ASC").Find(&credentials).Error; err != nil {
			return PasskeyOptions{}, err
		}
		if len(credentials) == 0 {
			return PasskeyOptions{}, errors.New("no passkey is available for this account")
		}
		for _, credential := range credentials {
			allowCredentials = append(allowCredentials, PasskeyCredentialDescriptor{
				Type: "public-key",
				ID:   encodeBase64URL(credential.CredentialID),
			})
		}
	}

	challenge, err := createWebAuthnChallenge(passkeyPurposeLogin, userID, rpID, origin)
	if err != nil {
		return PasskeyOptions{}, err
	}
	return PasskeyOptions{PublicKey: map[string]interface{}{
		"challenge":        challenge,
		"timeout":          60000,
		"rpId":             rpID,
		"allowCredentials": allowCredentials,
		"userVerification": "preferred",
	}}, nil
}

func (s *AuthService) FinishPasskeyLogin(credential PasskeyAuthenticationCredential) (*model.User, string, error) {
	if required, err := s.InitialSetupRequired(); err != nil {
		return nil, "", err
	} else if required {
		return nil, "", ErrInitialSetupRequired
	}
	if err := ensurePasskeyEnabled(); err != nil {
		return nil, "", err
	}
	if credential.Type != "" && credential.Type != "public-key" {
		return nil, "", errors.New("invalid credential type")
	}

	clientDataJSON, err := decodeBase64URL(credential.Response.ClientDataJSON)
	if err != nil {
		return nil, "", errors.New("invalid client data")
	}
	var client clientData
	if err := json.Unmarshal(clientDataJSON, &client); err != nil {
		return nil, "", errors.New("invalid client data")
	}
	if client.Type != "webauthn.get" {
		return nil, "", errors.New("invalid passkey login response")
	}

	challenge, err := consumeWebAuthnChallenge(client.Challenge, passkeyPurposeLogin, nil)
	if err != nil {
		return nil, "", err
	}
	if err := verifyPasskeyOrigin(client.Origin, challenge.Origin); err != nil {
		return nil, "", err
	}

	credentialID, err := decodeBase64URL(firstNonEmpty(credential.RawID, credential.ID))
	if err != nil || len(credentialID) == 0 {
		return nil, "", errors.New("invalid credential id")
	}
	var record model.PasskeyCredential
	if err := model.DB.Preload("User").Where("credential_id = ?", credentialID).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errors.New("passkey not found")
		}
		return nil, "", err
	}
	if challenge.UserID != nil && *challenge.UserID != record.UserID {
		return nil, "", errors.New("passkey does not belong to this account")
	}

	authenticatorData, err := decodeBase64URL(credential.Response.AuthenticatorData)
	if err != nil {
		return nil, "", errors.New("invalid authenticator data")
	}
	parsed, err := parseAuthenticatorData(authenticatorData, false)
	if err != nil {
		return nil, "", err
	}
	if err := verifyAuthenticatorData(parsed, challenge.RPID, false); err != nil {
		return nil, "", err
	}

	signature, err := decodeBase64URL(credential.Response.Signature)
	if err != nil {
		return nil, "", errors.New("invalid signature")
	}
	if err := verifyPasskeySignature(record.PublicKeyCOSE, authenticatorData, clientDataJSON, signature); err != nil {
		return nil, "", err
	}

	now := time.Now()
	updates := map[string]interface{}{"last_used_at": now}
	if parsed.SignCount > record.SignCount {
		updates["sign_count"] = parsed.SignCount
	}
	if err := model.DB.Model(&record).Updates(updates).Error; err != nil {
		return nil, "", err
	}

	user := record.User
	if err := EnsureFirstAdmin(&user); err != nil {
		return nil, "", err
	}
	token, err := s.issueJWT(&user)
	if err != nil {
		return nil, "", err
	}
	return &user, token, nil
}

func ensurePasskeyEnabled() error {
	if !settingBool("passkey_enabled", false) {
		return errors.New("passkey authentication is disabled")
	}
	return nil
}

func createWebAuthnChallenge(purpose string, userID *uint, rpID, origin string) (string, error) {
	if strings.TrimSpace(rpID) == "" || strings.TrimSpace(origin) == "" {
		return "", errors.New("passkey relying party is not configured")
	}
	if err := model.DB.Where("expires_at < ?", time.Now()).Delete(&model.WebAuthnChallenge{}).Error; err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	challenge := encodeBase64URL(raw)
	record := model.WebAuthnChallenge{
		Challenge: challenge,
		Purpose:   purpose,
		UserID:    userID,
		RPID:      strings.TrimSpace(rpID),
		Origin:    strings.TrimRight(strings.TrimSpace(origin), "/"),
		ExpiresAt: time.Now().Add(passkeyChallengeTTL),
	}
	if err := model.DB.Create(&record).Error; err != nil {
		return "", err
	}
	return challenge, nil
}

func consumeWebAuthnChallenge(challengeValue string, purpose string, userID *uint) (model.WebAuthnChallenge, error) {
	challengeValue = strings.TrimSpace(challengeValue)
	if challengeValue == "" {
		return model.WebAuthnChallenge{}, errors.New("challenge is required")
	}
	var record model.WebAuthnChallenge
	err := model.DB.Where("challenge = ? AND purpose = ? AND expires_at > ?", challengeValue, purpose, time.Now()).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.WebAuthnChallenge{}, errors.New("invalid or expired passkey challenge")
	}
	if err != nil {
		return model.WebAuthnChallenge{}, err
	}
	if userID != nil {
		if record.UserID == nil || *record.UserID != *userID {
			return model.WebAuthnChallenge{}, errors.New("invalid passkey challenge")
		}
	}
	if err := model.DB.Delete(&record).Error; err != nil {
		return model.WebAuthnChallenge{}, err
	}
	return record, nil
}

func verifyPasskeyOrigin(actual, expected string) error {
	actual = strings.TrimRight(strings.TrimSpace(actual), "/")
	expected = strings.TrimRight(strings.TrimSpace(expected), "/")
	if actual == "" || expected == "" || !strings.EqualFold(actual, expected) {
		return errors.New("invalid passkey origin")
	}
	return nil
}

func authDataFromAttestationObject(attestationObject []byte) ([]byte, error) {
	value, next, err := parseCBOR(attestationObject, 0)
	if err != nil {
		return nil, errors.New("invalid attestation object")
	}
	if next != len(attestationObject) {
		return nil, errors.New("invalid attestation object")
	}
	item, ok := value.(map[interface{}]interface{})
	if !ok {
		return nil, errors.New("invalid attestation object")
	}
	authData, ok := cborMapBytes(item, "authData")
	if !ok || len(authData) == 0 {
		return nil, errors.New("attestation object is missing auth data")
	}
	return authData, nil
}

func parseAuthenticatorData(authData []byte, requireAttestedCredential bool) (parsedAuthenticatorData, error) {
	if len(authData) < 37 {
		return parsedAuthenticatorData{}, errors.New("invalid authenticator data")
	}
	parsed := parsedAuthenticatorData{
		RPIDHash:  append([]byte(nil), authData[:32]...),
		Flags:     authData[32],
		SignCount: binary.BigEndian.Uint32(authData[33:37]),
	}
	if !requireAttestedCredential {
		return parsed, nil
	}
	if parsed.Flags&0x40 == 0 {
		return parsedAuthenticatorData{}, errors.New("authenticator data is missing attested credential data")
	}
	offset := 37
	if len(authData) < offset+18 {
		return parsedAuthenticatorData{}, errors.New("invalid attested credential data")
	}
	parsed.AAGUID = append([]byte(nil), authData[offset:offset+16]...)
	offset += 16
	credentialIDLength := int(binary.BigEndian.Uint16(authData[offset : offset+2]))
	offset += 2
	if credentialIDLength <= 0 || len(authData) < offset+credentialIDLength {
		return parsedAuthenticatorData{}, errors.New("invalid credential id")
	}
	parsed.CredentialID = append([]byte(nil), authData[offset:offset+credentialIDLength]...)
	offset += credentialIDLength

	_, next, err := parseCBOR(authData, offset)
	if err != nil {
		return parsedAuthenticatorData{}, errors.New("invalid credential public key")
	}
	parsed.CredentialPublicKey = append([]byte(nil), authData[offset:next]...)
	return parsed, nil
}

func verifyAuthenticatorData(authData parsedAuthenticatorData, rpID string, requireAttestedCredential bool) error {
	expected := sha256.Sum256([]byte(rpID))
	if string(authData.RPIDHash) != string(expected[:]) {
		return errors.New("invalid passkey relying party")
	}
	if authData.Flags&0x01 == 0 {
		return errors.New("passkey user presence is required")
	}
	if requireAttestedCredential && authData.Flags&0x40 == 0 {
		return errors.New("authenticator data is missing attested credential data")
	}
	return nil
}

func verifyPasskeySignature(publicKeyCOSE []byte, authenticatorData []byte, clientDataJSON []byte, signature []byte) error {
	key, err := publicKeyFromCOSE(publicKeyCOSE)
	if err != nil {
		return err
	}
	clientHash := sha256.Sum256(clientDataJSON)
	signedData := make([]byte, 0, len(authenticatorData)+len(clientHash))
	signedData = append(signedData, authenticatorData...)
	signedData = append(signedData, clientHash[:]...)
	digest := sha256.Sum256(signedData)

	switch publicKey := key.(type) {
	case *ecdsa.PublicKey:
		var ecdsaSignature struct {
			R *big.Int
			S *big.Int
		}
		if _, err := asn1.Unmarshal(signature, &ecdsaSignature); err != nil {
			return errors.New("invalid passkey signature")
		}
		if ecdsaSignature.R == nil || ecdsaSignature.S == nil || !ecdsa.Verify(publicKey, digest[:], ecdsaSignature.R, ecdsaSignature.S) {
			return errors.New("invalid passkey signature")
		}
		return nil
	case *rsa.PublicKey:
		if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
			return errors.New("invalid passkey signature")
		}
		return nil
	default:
		return errors.New("unsupported passkey public key")
	}
}

func publicKeyFromCOSE(publicKeyCOSE []byte) (interface{}, error) {
	value, next, err := parseCBOR(publicKeyCOSE, 0)
	if err != nil || next != len(publicKeyCOSE) {
		return nil, errors.New("invalid credential public key")
	}
	item, ok := value.(map[interface{}]interface{})
	if !ok {
		return nil, errors.New("invalid credential public key")
	}
	kty, _ := cborMapInt(item, int64(1))
	alg, _ := cborMapInt(item, int64(3))
	switch {
	case kty == 2 && alg == -7:
		crv, _ := cborMapInt(item, int64(-1))
		x, xOK := cborMapBytes(item, int64(-2))
		y, yOK := cborMapBytes(item, int64(-3))
		if crv != 1 || !xOK || !yOK {
			return nil, errors.New("unsupported passkey elliptic curve")
		}
		publicKey := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !publicKey.Curve.IsOnCurve(publicKey.X, publicKey.Y) {
			return nil, errors.New("invalid passkey public key")
		}
		return publicKey, nil
	case kty == 3 && alg == -257:
		n, nOK := cborMapBytes(item, int64(-1))
		eBytes, eOK := cborMapBytes(item, int64(-2))
		if !nOK || !eOK {
			return nil, errors.New("invalid RSA passkey public key")
		}
		e := int(new(big.Int).SetBytes(eBytes).Int64())
		if e == 0 {
			e = 65537
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: e}, nil
	default:
		// Some authenticators encode public keys as DER despite WebAuthn requiring COSE.
		if parsed, err := x509.ParsePKIXPublicKey(publicKeyCOSE); err == nil {
			return parsed, nil
		}
		return nil, errors.New("unsupported passkey public key algorithm")
	}
}

func encodeBase64URL(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodeBase64URL(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("empty value")
	}
	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	var lastErr error
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(value)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func truncatePasskeyName(value string) string {
	runes := []rune(value)
	if len(runes) <= 100 {
		return string(runes)
	}
	return string(runes[:100])
}

func cborMapBytes(item map[interface{}]interface{}, key interface{}) ([]byte, bool) {
	value, ok := item[key]
	if !ok {
		return nil, false
	}
	bytes, ok := value.([]byte)
	return bytes, ok
}

func cborMapInt(item map[interface{}]interface{}, key interface{}) (int64, bool) {
	value, ok := item[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int64:
		return typed, true
	case uint64:
		if typed > uint64(^uint(0)>>1) {
			return 0, false
		}
		return int64(typed), true
	default:
		return 0, false
	}
}

func parseCBOR(data []byte, offset int) (interface{}, int, error) {
	if offset >= len(data) {
		return nil, offset, errors.New("unexpected end of cbor")
	}
	initial := data[offset]
	offset++
	major := initial >> 5
	additional := initial & 0x1f
	value, next, err := cborReadArgument(data, offset, additional)
	if err != nil {
		return nil, next, err
	}
	offset = next

	switch major {
	case 0:
		return int64(value), offset, nil
	case 1:
		return int64(-1) - int64(value), offset, nil
	case 2:
		if len(data) < offset+int(value) {
			return nil, offset, errors.New("invalid cbor byte string")
		}
		return append([]byte(nil), data[offset:offset+int(value)]...), offset + int(value), nil
	case 3:
		if len(data) < offset+int(value) {
			return nil, offset, errors.New("invalid cbor text string")
		}
		return string(data[offset : offset+int(value)]), offset + int(value), nil
	case 4:
		items := make([]interface{}, 0, int(value))
		for i := 0; i < int(value); i++ {
			item, next, err := parseCBOR(data, offset)
			if err != nil {
				return nil, next, err
			}
			items = append(items, item)
			offset = next
		}
		return items, offset, nil
	case 5:
		items := make(map[interface{}]interface{}, int(value))
		for i := 0; i < int(value); i++ {
			key, next, err := parseCBOR(data, offset)
			if err != nil {
				return nil, next, err
			}
			offset = next
			mapValue, next, err := parseCBOR(data, offset)
			if err != nil {
				return nil, next, err
			}
			offset = next
			items[key] = mapValue
		}
		return items, offset, nil
	case 6:
		return parseCBOR(data, offset)
	case 7:
		switch additional {
		case 20:
			return false, offset, nil
		case 21:
			return true, offset, nil
		case 22, 23:
			return nil, offset, nil
		default:
			return nil, offset, errors.New("unsupported cbor simple value")
		}
	default:
		return nil, offset, errors.New("unsupported cbor major type")
	}
}

func cborReadArgument(data []byte, offset int, additional byte) (uint64, int, error) {
	switch {
	case additional < 24:
		return uint64(additional), offset, nil
	case additional == 24:
		if len(data) < offset+1 {
			return 0, offset, errors.New("invalid cbor argument")
		}
		return uint64(data[offset]), offset + 1, nil
	case additional == 25:
		if len(data) < offset+2 {
			return 0, offset, errors.New("invalid cbor argument")
		}
		return uint64(binary.BigEndian.Uint16(data[offset : offset+2])), offset + 2, nil
	case additional == 26:
		if len(data) < offset+4 {
			return 0, offset, errors.New("invalid cbor argument")
		}
		return uint64(binary.BigEndian.Uint32(data[offset : offset+4])), offset + 4, nil
	case additional == 27:
		if len(data) < offset+8 {
			return 0, offset, errors.New("invalid cbor argument")
		}
		return binary.BigEndian.Uint64(data[offset : offset+8]), offset + 8, nil
	default:
		return 0, offset, errors.New("unsupported cbor argument")
	}
}
