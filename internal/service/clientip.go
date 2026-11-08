package service

import (
	"context"
	"strconv"
	"strings"

	"qinglong-go/internal/auth"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/store"
)

// DefaultTrustProxy matches DEFAULT_TRUST_PROXY in back/shared/trustProxy.ts.
const DefaultTrustProxy = "loopback"

// ClientIPConfig is GET /api/system/client-ip/config.
type ClientIPConfig struct {
	TrustProxy string `json:"trustProxy"`
	Source     string `json:"source"`
	Editable   bool   `json:"editable"`
}

// ClientIPHop is one row of the diagnose table.
type ClientIPHop struct {
	IP     string `json:"ip"`
	Hop    int    `json:"hop"`
	Status string `json:"status"`
}

// ClientIPDiagnosis is GET /api/system/client-ip/diagnose.
type ClientIPDiagnosis struct {
	ClientIPConfig
	RemoteAddress string        `json:"remoteAddress"`
	ForwardedFor  []string      `json:"forwardedFor"`
	ExpressIPs    []string      `json:"expressIps"`
	ClientIP      string        `json:"clientIp"`
	Hops          []ClientIPHop `json:"hops"`
}

// ClientIPConfigFor implements GET /api/system/client-ip/config. The
// environment wins over the stored value, and only the stored value is editable.
func (s *SystemService) ClientIPConfigFor(ctx context.Context) (*ClientIPConfig, error) {
	environment := strings.TrimSpace(s.cfg.Env("QL_TRUST_PROXY"))
	stored := s.storedTrustProxy(ctx)
	effective := environment
	source := "environment"
	if effective == "" {
		effective = stored
		source = "system"
	}
	if effective == "" {
		effective = DefaultTrustProxy
		source = "default"
	}
	return &ClientIPConfig{
		TrustProxy: effective,
		Source:     source,
		Editable:   environment == "",
	}, nil
}

// storedTrustProxy reads systemConfig.info.trustProxy.
func (s *SystemService) storedTrustProxy(ctx context.Context) string {
	row, err := s.store.AuthRowByType(ctx, store.AuthTypeSystemConfig)
	if err != nil || row == nil {
		return ""
	}
	info := decodeInfo(row.Info)
	if value, ok := info["trustProxy"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// UpdateTrustProxy implements PUT /api/system/client-ip/config. Errors are
// answered as {code:400,message} with HTTP 200, like the Node handler.
func (s *SystemService) UpdateTrustProxy(ctx context.Context, value string) (*ClientIPConfig, *Reply) {
	if strings.TrimSpace(s.cfg.Env("QL_TRUST_PROXY")) != "" {
		message := i18n.T("环境变量 QL_TRUST_PROXY 已生效，系统设置不可覆盖")
		return nil, &Reply{Code: 400, Message: message}
	}
	setting := strings.TrimSpace(value)
	if setting == "" {
		setting = DefaultTrustProxy
	}
	if len(setting) > 500 || strings.ContainsAny(setting, "\r\n") {
		return nil, &Reply{Code: 400, Message: i18n.T("trust proxy 配置格式无效")}
	}
	if hops, err := strconv.Atoi(setting); err == nil && hops > 20 {
		return nil, &Reply{Code: 400, Message: i18n.T("代理层数不能超过 20")}
	}
	if _, err := s.updateInfo(ctx, map[string]any{"trustProxy": setting}); err != nil {
		return nil, &Reply{Code: 400, Message: i18n.T("系统配置不存在")}
	}
	config, err := s.ClientIPConfigFor(ctx)
	if err != nil {
		return nil, &Reply{Code: 400, Message: i18n.T("配置更新失败")}
	}
	return config, nil
}

// DiagnoseClientIP implements GET /api/system/client-ip/diagnose. remote is the
// socket address, forwarded the X-Forwarded-For entries in header order.
func (s *SystemService) DiagnoseClientIP(
	ctx context.Context,
	remote string,
	forwarded []string,
) (*ClientIPDiagnosis, *Reply) {
	config, err := s.ClientIPConfigFor(ctx)
	if err != nil {
		return nil, &Reply{Code: 400, Message: i18n.T("配置更新失败")}
	}
	remoteAddress := auth.NormalizeIP(remote)

	hopsFromApp := make([]string, 0, len(forwarded)+1)
	if remoteAddress != "" {
		hopsFromApp = append(hopsFromApp, remoteAddress)
	}
	for index := len(forwarded) - 1; index >= 0; index-- {
		hopsFromApp = append(hopsFromApp, forwarded[index])
	}

	setting := auth.ParseTrustProxySetting(config.TrustProxy)
	selectedIndex := len(hopsFromApp) - 1
	if selectedIndex < 0 {
		selectedIndex = 0
	}
	for index := 0; index < len(hopsFromApp)-1; index++ {
		if !setting.Trusted(hopsFromApp[index], index) {
			selectedIndex = index
			break
		}
	}

	hops := make([]ClientIPHop, 0, len(hopsFromApp))
	for index, ip := range hopsFromApp {
		status := "not_checked"
		switch {
		case index < selectedIndex:
			status = "trusted"
		case index == selectedIndex:
			status = "client"
		}
		hops = append(hops, ClientIPHop{IP: ip, Hop: index, Status: status})
	}

	clientIP := remoteAddress
	if selectedIndex < len(hopsFromApp) && len(hopsFromApp) > 0 {
		clientIP = hopsFromApp[selectedIndex]
	}

	// expressIps is the trusted chain without the socket address, furthest
	// first (proxy-addr all() reversed with the socket popped).
	expressIPs := []string{}
	for index := selectedIndex - 1; index >= 1; index-- {
		expressIPs = append(expressIPs, hopsFromApp[index])
	}

	return &ClientIPDiagnosis{
		ClientIPConfig: *config,
		RemoteAddress:  remoteAddress,
		ForwardedFor:   forwarded,
		ExpressIPs:     expressIPs,
		ClientIP:       clientIP,
		Hops:           hops,
	}, nil
}
