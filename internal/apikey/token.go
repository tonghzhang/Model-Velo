package apikey

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

const (
	tokenNamespace   = "mvl"
	locatorByteCount = 12
	secretByteCount  = 32
	hashVersion      = int16(1)
)

var ErrInvalidToken = errors.New("invalid Model-Velo API key")

type generatedToken struct {
	plaintext    string
	prefix       string
	lookupDigest []byte
	keyHash      []byte
	hashVersion  int16
}

type parsedToken struct {
	prefix string
	secret string
}

func generateToken(pepper []byte) (generatedToken, error) {
	locator, err := randomText(locatorByteCount)
	if err != nil {
		return generatedToken{}, errors.New("generate API key locator")
	}

	secret, err := randomText(secretByteCount)
	if err != nil {
		return generatedToken{}, errors.New("generate API key secret")
	}

	prefix := tokenNamespace + "_" + locator

	return generatedToken{
		plaintext:    prefix + "_" + secret,
		prefix:       prefix,
		lookupDigest: digestPrefix(prefix),
		keyHash:      hashSecret(secret, pepper),
		hashVersion:  hashVersion,
	}, nil
}

func parseToken(value string) (parsedToken, error) {
	namespace := tokenNamespace + "_"
	locatorLength := base64.RawURLEncoding.EncodedLen(locatorByteCount)
	secretLength := base64.RawURLEncoding.EncodedLen(secretByteCount)
	wantLength := len(namespace) + locatorLength + 1 + secretLength
	if len(value) != wantLength || !strings.HasPrefix(value, namespace) {
		return parsedToken{}, ErrInvalidToken
	}

	separator := len(namespace) + locatorLength
	if value[separator] != '_' {
		return parsedToken{}, ErrInvalidToken
	}
	locator := value[len(namespace):separator]
	secret := value[separator+1:]
	if !validEncodedSize(locator, locatorByteCount) || !validEncodedSize(secret, secretByteCount) {
		return parsedToken{}, ErrInvalidToken
	}

	return parsedToken{
		prefix: namespace + locator,
		secret: secret,
	}, nil
}

func verifyToken(token parsedToken, expectedHash []byte, version int16, pepper []byte) bool {
	if version != hashVersion || len(expectedHash) != sha256.Size {
		return false
	}
	return hmac.Equal(hashSecret(token.secret, pepper), expectedHash)
}

func digestPrefix(prefix string) []byte {
	digest := sha256.Sum256([]byte(prefix))
	return digest[:]
}

func hashSecret(secret string, pepper []byte) []byte {
	hasher := hmac.New(sha256.New, pepper)
	_, _ = hasher.Write([]byte(secret))
	return hasher.Sum(nil)
}

func randomText(byteCount int) (string, error) {
	buffer := make([]byte, byteCount)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func validEncodedSize(value string, byteCount int) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == byteCount
}
