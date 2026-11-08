package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// JWTError carries the exact message express-jwt puts in the 401 body. The
// frontend surfaces it verbatim, so the strings are part of the contract.
type JWTError struct {
	Message string
}

func (e *JWTError) Error() string { return e.Message }

var (
	// ErrMalformed is jsonwebtoken's "jwt malformed" (wrong segment count).
	ErrMalformed = &JWTError{Message: "jwt malformed"}
	// ErrInvalidToken covers undecodable header/payload segments.
	ErrInvalidToken = &JWTError{Message: "invalid token"}
	// ErrInvalidAlgorithm rejects anything that is not HS384.
	ErrInvalidAlgorithm = &JWTError{Message: "invalid algorithm"}
	// ErrInvalidSignature is a well-formed token signed with another secret.
	ErrInvalidSignature = &JWTError{Message: "invalid signature"}
	// ErrExpired is an expired token.
	ErrExpired = &JWTError{Message: "jwt expired"}
	// ErrNotActive is a token used before its nbf claim.
	ErrNotActive = &JWTError{Message: "jwt not active"}
)

// Claims is the panel token payload. Node signs `{data}` and jsonwebtoken adds
// iat/exp.
type Claims struct {
	Data   string
	Iat    int64
	Exp    int64
	HasExp bool
}

var rawURL = base64.RawURLEncoding

// Sign creates an HS384 token with the same claim layout as jsonwebtoken.
func Sign(secret, data string, ttl time.Duration) (string, error) {
	now := time.Now()
	header := map[string]any{"alg": "HS384", "typ": "JWT"}
	payload := map[string]any{
		"data": data,
		"iat":  now.Unix(),
		"exp":  now.Add(ttl).Unix(),
	}
	head, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	signingInput := rawURL.EncodeToString(head) + "." + rawURL.EncodeToString(body)
	mac := hmac.New(sha512.New384, []byte(secret))
	mac.Write([]byte(signingInput))
	return signingInput + "." + rawURL.EncodeToString(mac.Sum(nil)), nil
}

// Verify checks the HS384 signature and the time claims. Errors are *JWTError
// with the message express-jwt would produce.
func Verify(secret, token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrMalformed
	}

	headerBytes, err := rawURL.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidToken
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, ErrInvalidToken
	}
	// jsonwebtoken validates the algorithm before the signature.
	if header.Alg != "HS384" {
		return nil, ErrInvalidAlgorithm
	}

	signature, err := rawURL.DecodeString(parts[2])
	if err != nil {
		return nil, ErrInvalidToken
	}
	mac := hmac.New(sha512.New384, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, ErrInvalidSignature
	}

	payloadBytes, err := rawURL.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}
	dec := json.NewDecoder(strings.NewReader(string(payloadBytes)))
	dec.UseNumber()
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil {
		return nil, ErrInvalidToken
	}

	claims := &Claims{}
	if value, ok := payload["data"]; ok {
		switch typed := value.(type) {
		case string:
			claims.Data = typed
		default:
			encoded, _ := json.Marshal(typed)
			claims.Data = string(encoded)
		}
	}
	if value, ok := payload["iat"]; ok {
		claims.Iat = toInt64(value)
	}
	now := time.Now().Unix()
	if value, ok := payload["exp"]; ok {
		claims.Exp = toInt64(value)
		claims.HasExp = true
		if claims.Exp <= now {
			return nil, ErrExpired
		}
	}
	if value, ok := payload["nbf"]; ok {
		if toInt64(value) > now {
			return nil, ErrNotActive
		}
	}
	return claims, nil
}

// RandomString mirrors createRandomString(min, max): a random length in
// [min,max] that always contains a digit, a lowercase letter, an uppercase
// letter and one of "-_" before shuffling.
func RandomString(min, max int) string {
	if max < min {
		max = min
	}
	digits := []byte("0123456789")
	lower := []byte("abcdefghijklmnopqrstuvwxyz")
	upper := []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	special := []byte("-_")
	pool := make([]byte, 0, len(digits)+len(lower)+len(upper)+len(special))
	pool = append(pool, digits...)
	pool = append(pool, lower...)
	pool = append(pool, upper...)
	pool = append(pool, special...)

	length := min + randInt(max-min+1)
	out := make([]byte, 0, length)
	out = append(out, digits[randInt(len(digits))])
	out = append(out, lower[randInt(len(lower))])
	out = append(out, upper[randInt(len(upper))])
	out = append(out, special[randInt(len(special))])
	for i := 4; i < length; i++ {
		out = append(out, pool[randInt(len(pool))])
	}
	// Fisher-Yates with crypto randomness, like the original splice shuffle.
	for i := len(out) - 1; i > 0; i-- {
		j := randInt(i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// randInt returns a uniform value in [0,n) using crypto/rand.
func randInt(n int) int {
	if n <= 1 {
		return 0
	}
	max := int64(^uint32(0)) - (int64(^uint32(0)) % int64(n))
	for {
		var buf [4]byte
		if _, err := rand.Read(buf[:]); err != nil {
			panic(fmt.Sprintf("crypto/rand failed: %v", err))
		}
		value := int64(uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3]))
		if value < max {
			return int(value % int64(n))
		}
	}
}

// ErrNoToken is returned by ParseAuthorization when the header is absent.
var ErrNoToken = errors.New("no authorization token")

// ErrBadScheme is returned for a header that is not exactly "Bearer <token>".
var ErrBadScheme = errors.New("bad authorization scheme")

// ParseAuthorization splits the Authorization header the way express-jwt does.
func ParseAuthorization(header string) (string, error) {
	if strings.TrimSpace(header) == "" {
		return "", ErrNoToken
	}
	parts := strings.Split(header, " ")
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", ErrBadScheme
	}
	return parts[1], nil
}

// TokenFromHeader mirrors getToken(): it accepts the panel's legacy
// "mobile-"/"desktop-" prefixes on top of a Bearer header.
func TokenFromHeader(header string) string {
	token, err := ParseAuthorization(header)
	if err != nil {
		return ""
	}
	token = strings.TrimPrefix(token, "mobile-")
	token = strings.TrimPrefix(token, "desktop-")
	return token
}
