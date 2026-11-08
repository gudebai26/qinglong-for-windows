package auth

import (
	"bytes"
	"encoding/json"
)

// TokenInfo is one entry of authInfo.tokens[platform].
//
// The Node model (back/data/system.ts) is:
//
//	{ value, timestamp, ip, address, platform, expiration? }
//
// expiration is an optional Unix timestamp in seconds; when absent the JWT's
// own exp claim is authoritative.
type TokenInfo struct {
	Value      string `json:"value"`
	Timestamp  int64  `json:"timestamp"`
	IP         string `json:"ip"`
	Address    string `json:"address"`
	Platform   string `json:"platform"`
	Expiration int64  `json:"expiration,omitempty"`
}

// AuthInfo wraps the authInfo document.
//
// It is kept as an ordered-free map instead of a struct on purpose: the Node
// implementation spreads `{...authInfo, ...patch}` and writes the result back,
// so fields that were never present must stay absent (e.g. avatar, lastip on a
// first login). A struct would silently add zero values and change the JSON the
// Node panel reads afterwards.
type AuthInfo struct {
	raw map[string]any
}

// NewAuthInfo builds an AuthInfo from a decoded document.
func NewAuthInfo(doc map[string]any) *AuthInfo {
	if doc == nil {
		doc = map[string]any{}
	}
	return &AuthInfo{raw: doc}
}

// ParseAuthInfo decodes a stored document, preserving numeric literals.
func ParseAuthInfo(data []byte) (*AuthInfo, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	doc := map[string]any{}
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	return NewAuthInfo(doc), nil
}

// DefaultAuthInfo is the bootstrap account created by initData.ts.
func DefaultAuthInfo() *AuthInfo {
	return NewAuthInfo(map[string]any{
		"username": "admin",
		"password": "admin",
	})
}

// Clone returns a deep-enough copy for mutation before persisting.
func (a *AuthInfo) Clone() *AuthInfo {
	if a == nil {
		return NewAuthInfo(nil)
	}
	doc := make(map[string]any, len(a.raw))
	for key, value := range a.raw {
		doc[key] = value
	}
	return NewAuthInfo(doc)
}

// Bytes serializes the document for storage.
func (a *AuthInfo) Bytes() ([]byte, error) {
	if a == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(a.raw)
}

// Get returns a raw field.
func (a *AuthInfo) Get(key string) (any, bool) {
	if a == nil {
		return nil, false
	}
	value, ok := a.raw[key]
	return value, ok
}

// Set writes a field.
func (a *AuthInfo) Set(key string, value any) {
	if a == nil {
		return
	}
	if a.raw == nil {
		a.raw = map[string]any{}
	}
	a.raw[key] = value
}

// String returns a string field or "".
func (a *AuthInfo) String(key string) string {
	value, _ := a.Get(key)
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

// Int returns an integer field.
func (a *AuthInfo) Int(key string) (int64, bool) {
	value, ok := a.Get(key)
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			if float, ferr := typed.Float64(); ferr == nil {
				return int64(float), true
			}
			return 0, false
		}
		return parsed, true
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}

// Bool returns a boolean field.
func (a *AuthInfo) Bool(key string) bool {
	value, _ := a.Get(key)
	typed, _ := value.(bool)
	return typed
}

// IsDefault mirrors isDefaultAuthInfo(): the untouched bootstrap account.
func (a *AuthInfo) IsDefault() bool {
	return a.String("username") == "admin" && a.String("password") == "admin"
}

// StringSlice reads a []string field such as blockedIps.
func (a *AuthInfo) StringSlice(key string) []string {
	value, _ := a.Get(key)
	items, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]string); ok {
			return typed
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// SetStringSlice writes a []string field, preserving [] (not null) semantics.
func (a *AuthInfo) SetStringSlice(key string, values []string) {
	if values == nil {
		values = []string{}
	}
	a.Set(key, values)
}

// normalizedTokens converts every platform entry to []TokenInfo, mirroring
// normalizeTokens() in back/services/user.ts (legacy string tokens become a
// one-element list stamped with now).
func (a *AuthInfo) normalizedTokens(nowMillis int64) map[string][]TokenInfo {
	out := map[string][]TokenInfo{}
	value, ok := a.Get("tokens")
	if !ok || value == nil {
		return out
	}
	platforms, ok := value.(map[string]any)
	if !ok {
		return out
	}
	for platform, raw := range platforms {
		switch typed := raw.(type) {
		case string:
			if typed == "" {
				out[platform] = []TokenInfo{}
			} else {
				out[platform] = []TokenInfo{{
					Value:     typed,
					Timestamp: nowMillis,
					Platform:  platform,
				}}
			}
		case []any:
			list := make([]TokenInfo, 0, len(typed))
			for _, item := range typed {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				list = append(list, tokenFromMap(entry))
			}
			out[platform] = list
		case []TokenInfo:
			out[platform] = typed
		default:
			out[platform] = []TokenInfo{}
		}
	}
	return out
}

func tokenFromMap(entry map[string]any) TokenInfo {
	token := TokenInfo{
		Value:    stringField(entry, "value"),
		IP:       stringField(entry, "ip"),
		Address:  stringField(entry, "address"),
		Platform: stringField(entry, "platform"),
	}
	if value, ok := entry["timestamp"]; ok {
		token.Timestamp = toInt64(value)
	}
	if value, ok := entry["expiration"]; ok {
		token.Expiration = toInt64(value)
	}
	return token
}

func stringField(entry map[string]any, key string) string {
	if value, ok := entry[key].(string); ok {
		return value
	}
	return ""
}

func toInt64(value any) int64 {
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed
		}
		if float, err := typed.Float64(); err == nil {
			return int64(float)
		}
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	}
	return 0
}

// TokenList returns the tokens of one platform in stored order.
func (a *AuthInfo) TokenList(platform string, nowMillis int64) []TokenInfo {
	return a.normalizedTokens(nowMillis)[platform]
}

// SetTokenList writes the normalized platform map back.
func (a *AuthInfo) SetTokenList(tokens map[string][]TokenInfo) {
	platforms := make(map[string]any, len(tokens))
	for platform, list := range tokens {
		platforms[platform] = list
	}
	a.Set("tokens", platforms)
}

// AddToken mirrors addTokenToList(): normalize every platform, unshift the new
// token and keep at most max entries for that platform.
func (a *AuthInfo) AddToken(platform string, token TokenInfo, max int, nowMillis int64) {
	if max < 1 {
		max = 10
	}
	tokens := a.normalizedTokens(nowMillis)
	list := append([]TokenInfo{token}, tokens[platform]...)
	if len(list) > max {
		list = list[:max]
	}
	tokens[platform] = list
	a.SetTokenList(tokens)
}

// RemoveToken mirrors removeTokenFromList(): drop every entry with that value.
func (a *AuthInfo) RemoveToken(platform, value string, nowMillis int64) {
	tokens := a.normalizedTokens(nowMillis)
	list := tokens[platform]
	filtered := make([]TokenInfo, 0, len(list))
	for _, token := range list {
		if token.Value != value {
			filtered = append(filtered, token)
		}
	}
	tokens[platform] = filtered
	a.SetTokenList(tokens)
}

// HasToken mirrors isValidToken()'s storage check (the JWT signature is
// verified separately, before this is called).
func (a *AuthInfo) HasToken(platform, value string, nowMillis int64) bool {
	if a == nil || value == "" {
		return false
	}
	if a.String("token") == value {
		return true
	}
	nowSeconds := nowMillis / 1000
	for _, token := range a.TokenList(platform, nowMillis) {
		if token.Value != value {
			continue
		}
		if token.Expiration == 0 || token.Expiration > nowSeconds {
			return true
		}
	}
	return false
}

// ContainsToken reports whether the platform list holds the value, ignoring
// expiration. Logout uses this to decide whether anything needs rewriting.
func (a *AuthInfo) ContainsToken(platform, value string) bool {
	if a == nil || value == "" {
		return false
	}
	for _, token := range a.TokenList(platform, 0) {
		if token.Value == value {
			return true
		}
	}
	return false
}

// ClearTokens mirrors `token: ”, tokens: {}` used by every credential change.
func (a *AuthInfo) ClearTokens() {
	a.Set("token", "")
	a.Set("tokens", map[string]any{})
}

// BlockedIPs returns normalized blocked addresses.
func (a *AuthInfo) BlockedIPs() []string {
	raw := a.StringSlice("blockedIps")
	out := make([]string, 0, len(raw))
	for _, ip := range raw {
		if normalized := NormalizeIP(ip); normalized != "" {
			out = append(out, normalized)
		}
	}
	return out
}

// IsIPBlocked reports whether ip matches the stored blacklist.
func (a *AuthInfo) IsIPBlocked(ip string) bool {
	if ip == "" {
		return false
	}
	for _, blocked := range a.BlockedIPs() {
		if blocked == ip {
			return true
		}
	}
	return false
}

// UniqStrings removes duplicates while keeping order.
func UniqStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
