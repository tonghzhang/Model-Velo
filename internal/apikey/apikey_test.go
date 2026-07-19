package apikey

import (
	"bytes"
	"encoding/base64"
	"errors"
	"regexp"
	"testing"
	"time"
)

func TestGenerateTokenFormatAndUniqueness(t *testing.T) {
	pepper := bytes.Repeat([]byte{0x5a}, 32)
	const sampleSize = 4096
	seen := make(map[string]struct{}, sampleSize)
	uuidSeen := make(map[string]struct{}, sampleSize)
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	locatorWithUnderscores := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, locatorByteCount))
	secretWithUnderscores := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, secretByteCount))
	underscoreToken := tokenNamespace + "_" + locatorWithUnderscores + "_" + secretWithUnderscores
	if _, err := parseToken(underscoreToken); err != nil {
		t.Fatalf("parseToken() rejected valid Base64URL underscores: %v", err)
	}

	for index := 0; index < sampleSize; index++ {
		token, err := generateToken(pepper)
		if err != nil {
			t.Fatalf("generateToken() sample %d error = %v", index, err)
		}
		if _, exists := seen[token.plaintext]; exists {
			t.Fatalf("generateToken() produced a duplicate at sample %d", index)
		}
		seen[token.plaintext] = struct{}{}

		parsed, err := parseToken(token.plaintext)
		if err != nil {
			t.Fatalf("parseToken(generated token) sample %d error = %v", index, err)
		}
		if parsed.prefix != token.prefix {
			t.Fatalf("parsed prefix = %q, want %q", parsed.prefix, token.prefix)
		}
		if !verifyToken(parsed, token.keyHash, token.hashVersion, pepper) {
			t.Fatalf("verifyToken(generated token) sample %d = false", index)
		}
		if len(token.lookupDigest) != 32 || len(token.keyHash) != 32 {
			t.Fatalf("digest lengths = lookup %d, hash %d; want 32 each", len(token.lookupDigest), len(token.keyHash))
		}

		uuid, err := randomUUID()
		if err != nil {
			t.Fatalf("randomUUID() sample %d error = %v", index, err)
		}
		if !uuidPattern.MatchString(uuid) {
			t.Fatalf("randomUUID() sample %d = %q, want RFC 4122 version 4 format", index, uuid)
		}
		if _, exists := uuidSeen[uuid]; exists {
			t.Fatalf("randomUUID() produced a duplicate at sample %d", index)
		}
		uuidSeen[uuid] = struct{}{}
	}
}

func TestAPIKeyExpirationBoundary(t *testing.T) {
	now := time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC)
	past := now.Add(-time.Nanosecond)
	equal := now
	futureInShanghai := now.Add(time.Nanosecond).In(time.FixedZone("Asia/Shanghai", 8*60*60))

	tests := []struct {
		name        string
		expiresAt   *time.Time
		wantInvalid bool
		wantExpired bool
	}{
		{name: "no expiration"},
		{name: "past", expiresAt: &past, wantInvalid: true, wantExpired: true},
		{name: "exact boundary", expiresAt: &equal, wantInvalid: true, wantExpired: true},
		{name: "future in another timezone", expiresAt: &futureInShanghai},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, err := normalizeCreateKeyInput(CreateKeyInput{
				TenantID:  "tenant-test",
				Label:     "boundary-test",
				ExpiresAt: test.expiresAt,
			}, now)
			if got := errors.Is(err, ErrInvalidInput); got != test.wantInvalid {
				t.Fatalf("normalizeCreateKeyInput() invalid = %t, want %t; error = %v", got, test.wantInvalid, err)
			}
			if got := expirationReached(test.expiresAt, now); got != test.wantExpired {
				t.Errorf("expirationReached() = %t, want %t", got, test.wantExpired)
			}
			if err == nil && input.ExpiresAt != nil && input.ExpiresAt.Location() != time.UTC {
				t.Errorf("normalized expiration location = %s, want UTC", input.ExpiresAt.Location())
			}
		})
	}
}
