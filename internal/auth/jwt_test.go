package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// nodeToken was signed by jsonwebtoken with HS384 and the panel's default
// secret, i.e. exactly what the Node panel hands to the browser.
const (
	nodeToken = "eyJhbGciOiJIUzM4NCIsInR5cCI6IkpXVCJ9.eyJkYXRhIjoiTTItdGVzdC12ZWN0b3IiLCJpYXQiOjE3MDAwMDAwMDAsImV4cCI6NDEwMjQ0NDgwMH0.UyP4FZ4eEFSU6ymUTMHeU4u2EsnQdz7YPlNRz7jfy4CYhwCy5bKTWpH8OPztjBwm"
	wrongKey  = "eyJhbGciOiJIUzM4NCIsInR5cCI6IkpXVCJ9.eyJkYXRhIjoiTTItdGVzdC12ZWN0b3IiLCJpYXQiOjE3MDAwMDAwMDAsImV4cCI6NDEwMjQ0NDgwMH0.z68cNKwX4dF8efamhyGJfeQaG2Y8JH8s60qImPqcR89k9olQxxUhX7Sngf4v-AIM"
	expired   = "eyJhbGciOiJIUzM4NCIsInR5cCI6IkpXVCJ9.eyJkYXRhIjoieCIsImlhdCI6MTcwMDAwMDAwMCwiZXhwIjoxNzAwMDAwMTAwfQ.UHLZlGy0igWHKoGY7N0_3e82KB7fFM5k6uPBtaJM17__Pm_KK2rRUYQfhrvjNvEq"
	hs256     = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJkYXRhIjoieCIsImlhdCI6MTcwMDAwMDAwMCwiZXhwIjo0MTAyNDQ0ODAwfQ.Aq01RDn7E_xcwPTQDExQ6df_fAK28_xin7R1Qy4hT7o"
)

func TestVerifyNodeToken(t *testing.T) {
	claims, err := Verify("whyour-secret", nodeToken)
	if err != nil {
		t.Fatalf("Node token must verify: %v", err)
	}
	if claims.Data != "M2-test-vector" {
		t.Errorf("data = %q", claims.Data)
	}
	if !claims.HasExp || claims.Exp != 4102444800 {
		t.Errorf("exp = %d hasExp=%v", claims.Exp, claims.HasExp)
	}
}

func TestVerifyErrorMessages(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{"wrong key", wrongKey, "invalid signature"},
		{"expired", expired, "jwt expired"},
		{"algorithm", hs256, "invalid algorithm"},
		{"one segment", "abc", "jwt malformed"},
		{"two segments", "a.b", "jwt malformed"},
		{"bad base64", "a.b.c", "invalid token"},
	}
	for _, tc := range cases {
		_, err := Verify("whyour-secret", tc.token)
		var jwtErr *JWTError
		if !errors.As(err, &jwtErr) {
			t.Errorf("%s: err = %v, want JWTError", tc.name, err)
			continue
		}
		if jwtErr.Message != tc.want {
			t.Errorf("%s: message = %q, want %q", tc.name, jwtErr.Message, tc.want)
		}
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	token, err := Sign("whyour-secret", "payload", 20*24*time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	claims, err := Verify("whyour-secret", token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Data != "payload" {
		t.Errorf("data = %q", claims.Data)
	}
	if delta := claims.Exp - claims.Iat; delta != int64((20*24*time.Hour)/time.Second) {
		t.Errorf("ttl = %d seconds, want 20d", delta)
	}
	if _, err := Verify("another-secret", token); err == nil {
		t.Error("token must not verify with another secret")
	}
}

func TestParseAuthorization(t *testing.T) {
	if _, err := ParseAuthorization(""); !errors.Is(err, ErrNoToken) {
		t.Errorf("empty header err = %v", err)
	}
	for _, header := range []string{"Basic abc", "Bearer", "Bearer a b", "bearer"} {
		if _, err := ParseAuthorization(header); !errors.Is(err, ErrBadScheme) {
			t.Errorf("%q err = %v, want ErrBadScheme", header, err)
		}
	}
	token, err := ParseAuthorization("Bearer abc.def.ghi")
	if err != nil || token != "abc.def.ghi" {
		t.Errorf("token = %q err = %v", token, err)
	}
	if got := TokenFromHeader("Bearer mobile-abc"); got != "abc" {
		t.Errorf("mobile prefix stripped = %q", got)
	}
	if got := TokenFromHeader("Bearer desktop-abc"); got != "abc" {
		t.Errorf("desktop prefix stripped = %q", got)
	}
}

func TestRandomStringShape(t *testing.T) {
	value := RandomString(50, 100)
	if len(value) < 50 || len(value) > 100 {
		t.Fatalf("length = %d", len(value))
	}
	if !strings.ContainsAny(value, "0123456789") ||
		!strings.ContainsAny(value, "abcdefghijklmnopqrstuvwxyz") ||
		!strings.ContainsAny(value, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") ||
		!strings.ContainsAny(value, "-_") {
		t.Errorf("value %q misses a required character class", value)
	}
}
