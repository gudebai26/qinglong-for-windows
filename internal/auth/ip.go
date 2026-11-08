package auth

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

// NormalizeIP mirrors normalizeClientIp(): lowercase, strip brackets, drop the
// IPv6 zone and unwrap IPv4-mapped addresses.
func NormalizeIP(value string) string {
	ip := strings.ToLower(strings.TrimSpace(value))
	if ip == "" {
		return ""
	}
	if strings.HasPrefix(ip, "[") && strings.HasSuffix(ip, "]") {
		ip = ip[1 : len(ip)-1]
	}
	if idx := strings.IndexByte(ip, '%'); idx >= 0 {
		ip = ip[:idx]
	}
	ip = strings.TrimPrefix(ip, "::ffff:")
	return ip
}

// ClientIP resolves the caller address the way Express does with the panel's
// trust proxy setting (loopback by default).
func ClientIP(r *http.Request, trustProxy string) string {
	remote := NormalizeIP(hostOnly(r.RemoteAddr))
	trust := parseTrustProxy(trustProxy)

	if !trust.trusted(remote) {
		return remote
	}
	header := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if header == "" {
		return remote
	}
	parts := make([]string, 0, 4)
	for _, part := range strings.Split(header, ",") {
		if normalized := NormalizeIP(part); normalized != "" {
			parts = append(parts, normalized)
		}
	}
	if len(parts) == 0 {
		return remote
	}
	if trust.all {
		return parts[0]
	}
	if trust.hops > 0 {
		index := len(parts) - trust.hops
		if index < 0 {
			index = 0
		}
		return parts[index]
	}
	// Walk from the nearest hop outwards and return the first untrusted address.
	for i := len(parts) - 1; i >= 0; i-- {
		if !trust.trusted(parts[i]) {
			return parts[i]
		}
	}
	return parts[0]
}

// Address resolves an IP to the string stored in lastaddr/login logs.
//
// The Node panel uses ip2region and returns "内网IP" for loopback and private
// ranges. Public geolocation needs the 11 MB ip2region database, which M2 does
// not embed yet, so public addresses resolve to an empty string for now.
func Address(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	if parsed.IsLoopback() || parsed.IsPrivate() || parsed.IsLinkLocalUnicast() || parsed.IsUnspecified() {
		return "内网IP"
	}
	return ""
}

type trustConfig struct {
	all bool
	// hopsSet marks a numeric setting; hops is then the trusted hop count and
	// the address lists are unused.
	hopsSet bool
	hops    int
	ranges  []*net.IPNet
	exact   []net.IP
}

func (t trustConfig) trusted(ip string) bool {
	if ip == "" {
		return false
	}
	if t.all {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, candidate := range t.exact {
		if candidate.Equal(parsed) {
			return true
		}
	}
	for _, block := range t.ranges {
		if block.Contains(parsed) {
			return true
		}
	}
	return false
}

// parseTrustProxy understands the subset of Express' trust proxy syntax the
// panel documents: loopback (default), true/false, a hop count, or a
// comma-separated list of IPs/CIDRs.
func parseTrustProxy(value string) trustConfig {
	raw := strings.TrimSpace(value)
	if raw == "" {
		raw = "loopback"
	}
	switch strings.ToLower(raw) {
	case "true":
		return trustConfig{all: true}
	case "false":
		return trustConfig{}
	}
	if hops, err := strconv.Atoi(raw); err == nil {
		if hops < 0 {
			hops = 0
		}
		return trustConfig{hops: hops, hopsSet: true}
	}

	config := trustConfig{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.EqualFold(entry, "loopback") {
			config.exact = append(config.exact, net.ParseIP("127.0.0.1"), net.ParseIP("::1"))
			if _, block, err := net.ParseCIDR("127.0.0.0/8"); err == nil {
				config.ranges = append(config.ranges, block)
			}
			continue
		}
		if strings.EqualFold(entry, "linklocal") || strings.EqualFold(entry, "uniquelocal") {
			for _, cidr := range []string{"169.254.0.0/16", "fe80::/10", "fc00::/7"} {
				if _, block, err := net.ParseCIDR(cidr); err == nil {
					config.ranges = append(config.ranges, block)
				}
			}
			continue
		}
		if _, block, err := net.ParseCIDR(entry); err == nil {
			config.ranges = append(config.ranges, block)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			config.exact = append(config.exact, ip)
		}
	}
	return config
}

func hostOnly(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	return host
}

// TrustProxySetting is the parsed form of Express' `trust proxy` option. The
// client-IP diagnostic needs the per-hop predicate, which ClientIP does not
// expose.
type TrustProxySetting struct {
	config trustConfig
}

// ParseTrustProxySetting parses the same values the panel accepts.
func ParseTrustProxySetting(value string) TrustProxySetting {
	return TrustProxySetting{config: parseTrustProxy(value)}
}

// Trusted reports whether the address at this hop distance from the socket is
// a trusted proxy. A numeric setting trusts the first N hops only.
func (t TrustProxySetting) Trusted(ip string, hop int) bool {
	if t.config.hopsSet {
		return hop < t.config.hops
	}
	return t.config.trusted(ip)
}
