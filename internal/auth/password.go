package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"regexp"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// scrypt parameters used by back/shared/password.ts: Node's crypto.scrypt with
// keylen 64 and default cost (N=16384, r=8, p=1).
const (
	scryptN      = 16384
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 64
)

var passwordHashRe = regexp.MustCompile(`^scrypt\$[a-f0-9]{32}\$[a-f0-9]{128}$`)

// IsPasswordHash mirrors isPasswordHash().
func IsPasswordHash(password string) bool {
	return passwordHashRe.MatchString(password)
}

// HashPassword mirrors hashPassword(): a 16-byte hex salt and a 64-byte key,
// both hex encoded.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	saltHex := hex.EncodeToString(salt)
	key, err := scrypt.Key([]byte(password), []byte(saltHex), scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return "", err
	}
	return "scrypt$" + saltHex + "$" + hex.EncodeToString(key), nil
}

// VerifyPassword mirrors verifyPassword(): legacy plaintext passwords are
// compared in constant time and migrated by the caller after a successful
// login.
func VerifyPassword(password, stored string) bool {
	if !IsPasswordHash(stored) {
		return subtle.ConstantTimeCompare([]byte(password), []byte(stored)) == 1
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 3 {
		return false
	}
	salt := parts[1]
	expected, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	key, err := scrypt.Key([]byte(password), []byte(salt), scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(key, expected) == 1
}
