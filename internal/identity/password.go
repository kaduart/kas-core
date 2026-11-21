package identity

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Password hashing uses PBKDF2-SHA256 from the standard library (no external
// dependency). The iteration count follows the OWASP 2023 recommendation and
// is stored in the hash, so it can be raised later without breaking old hashes.
const (
	pbkdf2Iterations = 600_000
	saltBytes        = 16
	keyBytes         = 32
	hashPrefix       = "pbkdf2-sha256"
)

// MinPasswordLength is the shortest password accepted when creating a user.
const MinPasswordLength = 10

// ErrWeakPassword is returned for a password shorter than MinPasswordLength
// or longer than 128 characters.
var ErrWeakPassword = errors.New("password must have between 10 and 128 characters")

func checkPasswordPolicy(password string) error {
	n := utf8.RuneCountInString(password)
	if n < MinPasswordLength || n > 128 {
		return ErrWeakPassword
	}
	return nil
}

// HashPassword returns "pbkdf2-sha256$<iterations>$<salt>$<key>" (base64 raw).
func HashPassword(password string) (string, error) {
	return hashWith(password, pbkdf2Iterations)
}

func hashWith(password string, iterations int) (string, error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyBytes)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("%s$%d$%s$%s", hashPrefix, iterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// VerifyPassword compares in constant time. A malformed hash never matches.
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != hashPrefix {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1000 || iterations > 10_000_000 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) != keyBytes {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyBytes)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}
