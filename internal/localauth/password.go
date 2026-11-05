// Package localauth owns the standalone password format. It has no cloud identity dependencies.
package localauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const prefix = "$argon2id$v=19$m=65536,t=3,p=2$"

func Username(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 3 || len(value) > 128 {
		return "", errors.New("username must contain 3 to 128 characters")
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("@._-+", r)) {
			return "", errors.New("username contains unsupported characters")
		}
	}
	return value, nil
}

func Hash(password string) (string, error) {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 15 || len(password) > 1024 {
		return "", errors.New("password must contain at least 15 characters and at most 1024 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return prefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func ValidHash(encoded string) bool {
	_, _, ok := decode(encoded)
	return ok
}

func decode(encoded string) ([]byte, []byte, bool) {
	if !strings.HasPrefix(encoded, prefix) {
		return nil, nil, false
	}
	parts := strings.Split(strings.TrimPrefix(encoded, prefix), "$")
	if len(parts) != 2 {
		return nil, nil, false
	}
	salt, a := base64.RawStdEncoding.Strict().DecodeString(parts[0])
	key, b := base64.RawStdEncoding.Strict().DecodeString(parts[1])
	return salt, key, a == nil && b == nil && len(salt) == 16 && len(key) == 32
}

// Verify also performs the same bounded work for unknown users or invalid hashes.
func Verify(encoded, password string) bool {
	salt, want, ok := decode(encoded)
	if !ok {
		salt, want = make([]byte, 16), make([]byte, 32)
	}
	if len(password) > 1024 {
		password = ""
		ok = false
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(got, want) == 1 && ok
}
