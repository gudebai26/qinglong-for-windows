package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// TOTP parameters used by @otplib/preset-default's authenticator: SHA1, six
// digits, 30 second steps, no verification window.
const (
	totpPeriod = 30
	totpDigits = 6
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret mirrors authenticator.generateSecret(): ten random bytes
// encoded as unpadded base32 (16 characters).
func GenerateTOTPSecret() (string, error) {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return totpEncoding.EncodeToString(buf), nil
}

// TOTPKeyURI mirrors authenticator.keyuri(user, 'qinglong', secret).
func TOTPKeyURI(account, issuer, secret string) string {
	return fmt.Sprintf(
		"otpauth://totp/%s:%s?secret=%s&period=%d&digits=%d&algorithm=SHA1&issuer=%s",
		encodeURIComponent(issuer), encodeURIComponent(account), secret, totpPeriod, totpDigits,
		encodeURIComponent(issuer),
	)
}

// VerifyTOTP checks a six digit code against the current 30 second step.
func VerifyTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits || secret == "" {
		return false
	}
	key, err := decodeBase32(secret)
	if err != nil {
		return false
	}
	expected := totpCode(key, time.Now().Unix()/totpPeriod)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1
}

// TOTPCodeAt returns the code a token generator would show at time t. It exists
// for tests and CLI diagnostics.
func TOTPCodeAt(secret string, at time.Time) (string, error) {
	key, err := decodeBase32(secret)
	if err != nil {
		return "", err
	}
	return totpCode(key, at.Unix()/totpPeriod), nil
}

// TOTPStep returns the current step, used to reject code replay.
func TOTPStep() int64 { return time.Now().Unix() / totpPeriod }

func totpCode(key []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, value%mod)
}

func decodeBase32(secret string) ([]byte, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	if pad := len(normalized) % 8; pad != 0 {
		normalized += strings.Repeat("=", 8-pad)
	}
	return base32.StdEncoding.DecodeString(normalized)
}

// encodeURIComponent matches JavaScript's encodeURIComponent, which otplib
// uses for the otpauth label and issuer.
func encodeURIComponent(value string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	var builder strings.Builder
	for i := 0; i < len(value); i++ {
		char := value[i]
		if strings.IndexByte(unreserved, char) >= 0 {
			builder.WriteByte(char)
			continue
		}
		fmt.Fprintf(&builder, "%%%02X", char)
	}
	return builder.String()
}
