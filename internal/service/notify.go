package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"qinglong-go/internal/config"
	"qinglong-go/internal/i18n"
	"qinglong-go/internal/store"
)

// Notification audit limits, copied from back/shared/notificationAudit.ts.
const (
	notifyLogKeep      = 200
	notifyTitleLimit   = 512
	notifyContentLimit = 8192
	notifyTypeLimit    = 64
	notifyScriptLimit  = 512
)

// NotifyStatus values of back/data/system.ts.
const (
	NotifyStatusSuccess int64 = 0
	NotifyStatusFail    int64 = 1
)

// NotifyLogEntry is one row of the notification audit trail (Auths.notifyLog).
type NotifyLogEntry struct {
	Title      string
	Content    string
	Status     int64
	NotifyType string
	// Source is "script" for ql API reports and "system" for panel sends.
	Source     string
	ScriptName string
	TaskID     string
}

// NotificationRequest is one delivery attempt. Type overrides the saved
// channel; Params carries the per-call channel fields (NotificationInfo).
type NotificationRequest struct {
	Title   string
	Content string
	Type    string
	Params  map[string]string
}

// serverChanHost extracts the sctp<id>t prefix of a ServerChan key, which
// selects the regional push endpoint.
var serverChanHost = regexp.MustCompile(`^sctp(\d+)t`)

// NotifyService delivers notifications and owns the audit trail. M6.5 needs it
// for the ql API (SystemNotify/RecordNotifyLog); M8 adds the settings page and
// the remaining channels.
type NotifyService struct {
	cfg    *config.Config
	store  *store.Store
	logger *slog.Logger
	client *http.Client
}

// NewNotifyService wires the notification service.
func NewNotifyService(cfg *config.Config, st *store.Store, logger *slog.Logger) *NotifyService {
	if logger == nil {
		logger = slog.Default()
	}
	return &NotifyService{
		cfg:    cfg,
		store:  st,
		logger: logger,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// notifyLogInfo is the JSON document Node writes into Auths.info. Field order
// matches persistNotificationAudit so the rows are comparable byte for byte.
type notifyLogInfo struct {
	Timestamp  int64  `json:"timestamp"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	Status     int64  `json:"status"`
	NotifyType string `json:"notifyType"`
	Source     string `json:"source"`
	ScriptName string `json:"scriptName"`
	TaskID     string `json:"taskId,omitempty"`
}

// Record appends one audit row and keeps the newest 200, exactly like
// persistNotificationAudit. Failures are logged, never returned: the audit sink
// is best effort in Node too, so a broken database cannot fail a notification.
func (s *NotifyService) Record(ctx context.Context, entry NotifyLogEntry) error {
	status := NotifyStatusFail
	if entry.Status == NotifyStatusSuccess {
		status = NotifyStatusSuccess
	}
	source := "system"
	if entry.Source == "script" {
		source = "script"
	}
	taskID := ""
	if isDigits(entry.TaskID) {
		taskID = entry.TaskID
	}

	info := notifyLogInfo{
		Timestamp:  time.Now().UnixMilli(),
		Title:      truncateRunes(entry.Title, notifyTitleLimit),
		Content:    truncateRunes(entry.Content, notifyContentLimit),
		Status:     status,
		NotifyType: truncateRunes(entry.NotifyType, notifyTypeLimit),
		Source:     source,
		ScriptName: truncateRunes(entry.ScriptName, notifyScriptLimit),
		TaskID:     taskID,
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		s.logger.Error("[notify] encode audit row failed", "error", err)
		return nil
	}
	if _, err := s.store.InsertAuthRow(ctx, store.AuthTypeNotifyLog, encoded); err != nil {
		s.logger.Error("[notify] persist audit row failed", "error", err)
		return nil
	}
	if err := s.store.PruneAuthRows(ctx, store.AuthTypeNotifyLog, notifyLogKeep); err != nil {
		s.logger.Warn("[notify] prune audit rows failed", "error", err)
	}
	return nil
}

// Notify delivers one notification and always records the outcome, mirroring
// SystemService.notify + NotificationService.notify.
func (s *NotifyService) Notify(ctx context.Context, req NotificationRequest) *Reply {
	saved := s.savedConfig(ctx)
	channel := strings.TrimSpace(req.Type)
	if channel == "" {
		channel = strings.TrimSpace(saved["type"])
	}

	params := make(map[string]string, len(saved)+len(req.Params))
	for key, value := range saved {
		if key == "type" {
			continue
		}
		params[key] = value
	}
	for key, value := range req.Params {
		params[key] = value
	}

	success := false
	if channel != "" {
		success = s.deliver(ctx, channel, req.Title, req.Content, params)
	}

	status := NotifyStatusFail
	if success {
		status = NotifyStatusSuccess
	}
	_ = s.Record(ctx, NotifyLogEntry{
		Title:      req.Title,
		Content:    req.Content,
		Status:     status,
		NotifyType: channel,
		Source:     "system",
	})

	if success {
		return &Reply{Code: 200, Message: i18n.T("通知发送成功")}
	}
	return &Reply{Code: 400, Message: i18n.T("通知发送失败，请检查系统设置/通知配置")}
}

// UpdateConfig implements PUT /api/user/notification (and /init): it stores the
// channel configuration only after a test send succeeds, exactly like
// UserService.updateNotificationMode in Node. A payload without a type clears
// the configuration. The stored shape is the whole payload minus nothing - the
// type field selects the channel when a real notification is sent.
func (s *NotifyService) UpdateConfig(ctx context.Context, payload map[string]any) *Reply {
	channel := ""
	if raw, ok := payload["type"].(string); ok {
		channel = strings.TrimSpace(raw)
	}

	if channel == "" {
		if err := s.saveConfig(ctx, payload); err != nil {
			s.logger.Error("[notify] save config failed", "error", err)
			return &Reply{Code: 400, Message: i18n.T("通知发送失败，请检查参数")}
		}
		return &Reply{Code: 200, Data: map[string]any{"code": randomCode()}}
	}

	// The settings page sends a probe through the chosen channel; only a
	// confirmed delivery is persisted, matching Node's testNotify flow.
	params := paramMap(payload)
	result := s.Notify(ctx, NotificationRequest{
		Title:   i18n.T("青龙"),
		Content: i18n.T("【蛟龙】测试通知 https://t.me/jiao_long"),
		Type:    channel,
		Params:  params,
	})
	if result.Code != 200 {
		return result
	}
	if err := s.saveConfig(ctx, payload); err != nil {
		s.logger.Error("[notify] save config failed", "error", err)
		return &Reply{Code: 400, Message: i18n.T("通知发送失败，请检查参数")}
	}
	return &Reply{Code: 200, Data: map[string]any{"code": randomCode()}}
}

// saveConfig writes the raw channel configuration into Auths.notification.
func (s *NotifyService) saveConfig(ctx context.Context, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.store.UpsertAuthRow(ctx, store.AuthTypeNotification, encoded)
}

// savedConfig reads Auths.notification.info. The column is NULL until the
// settings page saves a channel, so an empty map is a normal state.
// paramMap flattens a JSON object into the string map the channel senders
// consume. Only string/number/bool leaves survive, matching the stored config.
func paramMap(raw map[string]any) map[string]string {
	values := make(map[string]string, len(raw))
	for key, value := range raw {
		switch typed := value.(type) {
		case string:
			values[key] = typed
		case float64:
			values[key] = strconv.FormatFloat(typed, 'f', -1, 64)
		case bool:
			values[key] = strconv.FormatBool(typed)
		}
	}
	return values
}

func (s *NotifyService) savedConfig(ctx context.Context) map[string]string {
	row, err := s.store.AuthRowByType(ctx, store.AuthTypeNotification)
	if err != nil {
		s.logger.Warn("[notify] read notification config failed", "error", err)
		return map[string]string{}
	}
	if row == nil || len(row.Info) == 0 {
		return map[string]string{}
	}
	raw := map[string]any{}
	if err := json.Unmarshal(row.Info, &raw); err != nil {
		s.logger.Warn("[notify] decode notification config failed", "error", err)
		return map[string]string{}
	}
	return paramMap(raw)
}

// deliver dispatches one channel. Channels M8 still owns return false with a
// warning instead of pretending to have sent something.
func (s *NotifyService) deliver(ctx context.Context, channel, title, content string, params map[string]string) bool {
	var err error
	var ok bool
	switch channel {
	case "gotify":
		ok, err = s.gotify(ctx, title, content, params)
	case "goCqHttpBot":
		ok, err = s.goCqHTTPBot(ctx, title, content, params)
	case "serverChan":
		ok, err = s.serverChan(ctx, title, content, params)
	case "pushDeer":
		ok, err = s.pushDeer(ctx, title, content, params)
	case "chat":
		ok, err = s.synologyChat(ctx, title, content, params)
	case "bark":
		ok, err = s.bark(ctx, title, content, params)
	case "telegramBot":
		ok, err = s.telegramBot(ctx, title, content, params)
	case "dingtalkBot":
		ok, err = s.dingTalkBot(ctx, title, content, params)
	case "weWorkBot":
		ok, err = s.weWorkBot(ctx, title, content, params)
	case "iGot":
		ok, err = s.iGot(ctx, title, content, params)
	case "pushPlus":
		ok, err = s.pushPlus(ctx, title, content, params)
	case "pushMe":
		ok, err = s.pushMe(ctx, title, content, params)
	case "ntfy":
		ok, err = s.ntfy(ctx, title, content, params)
	case "feishu", "lark":
		ok, err = s.feishu(ctx, title, content, params)
	case "webhook":
		ok, err = s.webhook(ctx, title, content, params)
	default:
		s.logger.Warn("[notify] channel not implemented yet", "channel", channel)
		return false
	}
	if err != nil {
		s.logger.Warn("[notify] send failed", "channel", channel, "error", err)
	}
	return ok
}

// ---------------------------------------------------------------------------
// Channel implementations
// ---------------------------------------------------------------------------

func (s *NotifyService) gotify(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	base := strings.TrimSuffix(params["gotifyUrl"], "/")
	if base == "" || params["gotifyToken"] == "" {
		return false, nil
	}
	priority := params["gotifyPriority"]
	if priority == "" {
		priority = "1"
	}
	body := url.Values{}
	body.Set("title", title)
	body.Set("message", content)
	body.Set("priority", priority)
	data, err := s.postForm(ctx, base+"/message?token="+url.QueryEscape(params["gotifyToken"]), body, nil)
	if err != nil {
		return false, err
	}
	if _, ok := data["id"]; ok {
		return true, nil
	}
	return false, nil
}

func (s *NotifyService) goCqHTTPBot(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	target := params["goCqHttpBotUrl"]
	if target == "" {
		return false, nil
	}
	if qq := params["goCqHttpBotQq"]; qq != "" {
		target += "?" + qq
	}
	headers := map[string]string{"Authorization": "Bearer " + params["goCqHttpBotToken"]}
	data, err := s.postJSON(ctx, target, map[string]any{"message": title + "\n" + content}, headers)
	if err != nil {
		return false, err
	}
	return numberEquals(data["retcode"], 0), nil
}

func (s *NotifyService) serverChan(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	key := params["serverChanKey"]
	if key == "" {
		return false, nil
	}
	target := "https://sctapi.ftqq.com/" + key + ".send"
	if match := serverChanHost.FindStringSubmatch(key); match != nil && match[1] != "" {
		target = "https://" + match[1] + ".push.ft07.com/send/" + key + ".send"
	}
	body := url.Values{}
	body.Set("title", title)
	body.Set("desp", content)
	data, err := s.postForm(ctx, target, body, nil)
	if err != nil {
		return false, err
	}
	if numberEquals(data["errno"], 0) {
		return true, nil
	}
	if nested, ok := data["data"].(map[string]any); ok && numberEquals(nested["errno"], 0) {
		return true, nil
	}
	return false, nil
}

func (s *NotifyService) pushDeer(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	target := params["pushDeerUrl"]
	if target == "" {
		target = "https://api2.pushdeer.com/message/push"
	}
	body := url.Values{}
	body.Set("pushkey", params["pushDeerKey"])
	body.Set("text", title)
	body.Set("desp", content)
	body.Set("type", "markdown")
	data, err := s.postForm(ctx, target, body, nil)
	if err != nil {
		return false, err
	}
	if content, ok := data["content"].(map[string]any); ok {
		if result, ok := content["result"].([]any); ok && len(result) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (s *NotifyService) synologyChat(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	target := params["synologyChatUrl"]
	if target == "" {
		return false, nil
	}
	payload, err := json.Marshal(map[string]string{"text": title + "\n" + content})
	if err != nil {
		return false, err
	}
	body := url.Values{}
	body.Set("payload", string(payload))
	data, err := s.postForm(ctx, target, body, nil)
	if err != nil {
		return false, err
	}
	return data["success"] == true, nil
}

func (s *NotifyService) bark(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	push := params["barkPush"]
	if push == "" {
		return false, nil
	}
	if !strings.HasPrefix(push, "http") {
		push = "https://api.day.app/" + push
	}
	payload := map[string]any{
		"title":     title,
		"body":      content,
		"icon":      params["barkIcon"],
		"sound":     params["barkSound"],
		"group":     params["barkGroup"],
		"isArchive": params["barkArchive"],
		"level":     params["barkLevel"],
		"url":       params["barkUrl"],
	}
	data, err := s.postJSON(ctx, push, payload, nil)
	if err != nil {
		return false, err
	}
	return numberEquals(data["code"], 200), nil
}

func (s *NotifyService) telegramBot(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	token := params["telegramBotToken"]
	if token == "" {
		return false, nil
	}
	host := strings.TrimSuffix(params["telegramBotApiHost"], "/")
	if host == "" {
		host = "https://api.telegram.org"
	}
	body := url.Values{}
	body.Set("chat_id", params["telegramBotUserId"])
	body.Set("text", title+"\n\n"+content)
	body.Set("disable_web_page_preview", "true")
	data, err := s.postForm(ctx, host+"/bot"+token+"/sendMessage", body, nil)
	if err != nil {
		return false, err
	}
	return data["ok"] == true, nil
}

func (s *NotifyService) dingTalkBot(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	token := params["dingtalkBotToken"]
	if token == "" {
		return false, nil
	}
	target := "https://oapi.dingtalk.com/robot/send?access_token=" + url.QueryEscape(token)
	if secret := params["dingtalkBotSecret"]; secret != "" {
		stamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(stamp + "\n" + secret))
		sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		target += "&timestamp=" + stamp + "&sign=" + sign
	}
	payload := map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": " " + title + "\n\n" + content},
	}
	data, err := s.postJSON(ctx, target, payload, nil)
	if err != nil {
		return false, err
	}
	return numberEquals(data["errcode"], 0), nil
}

func (s *NotifyService) weWorkBot(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	key := params["weWorkBotKey"]
	if key == "" {
		return false, nil
	}
	origin := strings.TrimSuffix(params["weWorkOrigin"], "/")
	if origin == "" {
		origin = "https://qyapi.weixin.qq.com"
	}
	payload := map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": " " + title + "\n\n" + content},
	}
	data, err := s.postJSON(ctx, origin+"/cgi-bin/webhook/send?key="+url.QueryEscape(key), payload, nil)
	if err != nil {
		return false, err
	}
	return numberEquals(data["errcode"], 0), nil
}

func (s *NotifyService) iGot(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	key := params["iGotPushKey"]
	if key == "" {
		return false, nil
	}
	body := url.Values{}
	body.Set("title", title)
	body.Set("content", content)
	data, err := s.postForm(ctx, "https://push.hellyw.com/"+strings.ToLower(key), body, nil)
	if err != nil {
		return false, err
	}
	return numberEquals(data["ret"], 0), nil
}

func (s *NotifyService) pushPlus(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	token := params["pushPlusToken"]
	if token == "" {
		return false, nil
	}
	payload := map[string]any{
		"token":       token,
		"title":       title,
		"content":     strings.NewReplacer("\n", "<br>", "\r", "<br>").Replace(content),
		"topic":       params["pushPlusUser"],
		"template":    firstNonEmptyString(params["pushPlusTemplate"], "html"),
		"channel":     firstNonEmptyString(params["pushplusChannel"], "wechat"),
		"webhook":     params["pushplusWebhook"],
		"callbackUrl": params["pushplusCallbackUrl"],
		"to":          params["pushplusTo"],
	}
	data, err := s.postJSON(ctx, "https://www.pushplus.plus/send", payload, nil)
	if err != nil {
		return false, err
	}
	return numberEquals(data["code"], 200), nil
}

func (s *NotifyService) pushMe(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	target := params["pushMeUrl"]
	if target == "" {
		target = "https://push.i-i.me/"
	}
	payload := map[string]any{
		"push_key": params["pushMeKey"],
		"title":    title,
		"content":  content,
	}
	body, err := s.postRaw(ctx, target, "application/json", mustJSON(payload))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(body) == "success", nil
}

func (s *NotifyService) ntfy(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	topic := params["ntfyTopic"]
	if topic == "" {
		return false, nil
	}
	base := strings.TrimSuffix(params["ntfyUrl"], "/")
	if base == "" {
		base = "https://ntfy.sh"
	}
	headers := map[string]string{
		"Title":    rfc2047(title),
		"Priority": firstNonEmptyString(params["ntfyPriority"], "3"),
		"Icon":     "https://qn.whyour.cn/logo.png",
	}
	if token := params["ntfyToken"]; token != "" {
		headers["Authorization"] = "Bearer " + token
	} else if params["ntfyUsername"] != "" && params["ntfyPassword"] != "" {
		headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString(
			[]byte(params["ntfyUsername"]+":"+params["ntfyPassword"]))
	}
	if actions := params["ntfyActions"]; actions != "" {
		headers["Actions"] = rfc2047(actions)
	}
	status, _, err := s.do(ctx, http.MethodPost, base+"/"+url.PathEscape(topic), "text/plain; charset=utf-8", strings.NewReader(content), headers)
	if err != nil {
		return false, err
	}
	return status == http.StatusOK, nil
}

func (s *NotifyService) feishu(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	target := params["larkKey"]
	if target == "" {
		return false, nil
	}
	if !strings.HasPrefix(target, "http") {
		target = "https://open.feishu.cn/open-apis/bot/v2/hook/" + target
	}
	payload := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": title + "\n\n" + content},
	}
	if secret := params["larkSecret"]; secret != "" {
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(stamp+"\n"+secret))
		payload["timestamp"] = stamp
		payload["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	data, err := s.postJSON(ctx, target, payload, nil)
	if err != nil {
		return false, err
	}
	return numberEquals(data["StatusCode"], 0) || numberEquals(data["code"], 0), nil
}

func (s *NotifyService) webhook(ctx context.Context, title, content string, params map[string]string) (bool, error) {
	target := params["webhookUrl"]
	rawBody := params["webhookBody"]
	if target == "" && rawBody == "" {
		return false, nil
	}
	if !strings.Contains(target, "$title") && !strings.Contains(rawBody, "$title") {
		return false, fmt.Errorf("webhook url or body must contain $title")
	}

	method := strings.ToUpper(firstNonEmptyString(params["webhookMethod"], http.MethodPost))
	contentType := firstNonEmptyString(params["webhookContentType"], "application/json")
	replace := func(value string) string {
		value = strings.ReplaceAll(value, "$title", title)
		return strings.ReplaceAll(value, "$content", content)
	}

	headers := parseWebhookHeaders(params["webhookHeaders"])
	headers["Content-Type"] = contentType
	body := ""
	switch contentType {
	case "application/json":
		body = mustJSON(parseWebhookBody(replace(rawBody)))
	case "application/x-www-form-urlencoded":
		body = encodeForm(parseWebhookBody(replace(rawBody)))
	case "text/plain":
		body = replace(rawBody)
	default:
		body = replace(rawBody)
	}

	endpoint := strings.ReplaceAll(target, "$title", url.QueryEscape(title))
	endpoint = strings.ReplaceAll(endpoint, "$content", url.QueryEscape(content))

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	status, text, err := s.do(ctx, method, endpoint, contentType, reader, headers)
	if err != nil {
		return false, err
	}
	if status >= 200 && status < 300 {
		return true, nil
	}
	return false, fmt.Errorf("webhook returned %d: %s", status, truncateRunes(text, 200))
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (s *NotifyService) postForm(ctx context.Context, target string, body url.Values, headers map[string]string) (map[string]any, error) {
	status, text, err := s.do(ctx, http.MethodPost, target, "application/x-www-form-urlencoded", strings.NewReader(body.Encode()), headers)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("http %d: %s", status, truncateRunes(text, 200))
	}
	return decodeJSONObject(text), nil
}

func (s *NotifyService) postJSON(ctx context.Context, target string, payload any, headers map[string]string) (map[string]any, error) {
	status, text, err := s.do(ctx, http.MethodPost, target, "application/json", strings.NewReader(mustJSON(payload)), headers)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("http %d: %s", status, truncateRunes(text, 200))
	}
	return decodeJSONObject(text), nil
}

func (s *NotifyService) postRaw(ctx context.Context, target, contentType, body string) (string, error) {
	status, text, err := s.do(ctx, http.MethodPost, target, contentType, strings.NewReader(body), nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("http %d: %s", status, truncateRunes(text, 200))
	}
	return text, nil
}

func (s *NotifyService) do(ctx context.Context, method, target, contentType string, body io.Reader, headers map[string]string) (int, string, error) {
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return 0, "", err
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	for key, value := range headers {
		if value != "" {
			request.Header.Set(key, value)
		}
	}
	response, err := s.client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, "", err
	}
	return response.StatusCode, string(payload), nil
}

func decodeJSONObject(text string) map[string]any {
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return map[string]any{}
	}
	return decoded
}

func numberEquals(value any, target float64) bool {
	switch typed := value.(type) {
	case float64:
		return typed == target
	case int:
		return float64(typed) == target
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return err == nil && parsed == target
	default:
		return false
	}
}

// parseWebhookHeaders reads the "Key: value" block the settings page stores.
func parseWebhookHeaders(raw string) map[string]string {
	headers := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if existing, exists := headers[key]; exists {
			headers[key] = existing + ", " + strings.TrimSpace(value)
			continue
		}
		headers[key] = strings.TrimSpace(value)
	}
	return headers
}

// parseWebhookBody reads the "key: value" block of a webhook body.
func parseWebhookBody(raw string) map[string]string {
	values := map[string]string{}
	lines := strings.Split(raw, "\n")
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSuffix(lines[index], "\r")
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		// Multi-line values continue until the next "key:" line.
		parts := []string{strings.TrimSpace(value)}
		for index+1 < len(lines) {
			next := strings.TrimSuffix(lines[index+1], "\r")
			if _, _, ok := strings.Cut(next, ":"); ok && !strings.HasPrefix(next, " ") && !strings.HasPrefix(next, "\t") {
				break
			}
			index++
			parts = append(parts, strings.TrimSpace(next))
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return values
}

func encodeForm(values map[string]string) string {
	form := url.Values{}
	for key, value := range values {
		form.Set(key, value)
	}
	return form.Encode()
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// rfc2047 encodes a header value the way ntfy expects non-ASCII text.
func rfc2047(text string) string {
	return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(text)) + "?="
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
