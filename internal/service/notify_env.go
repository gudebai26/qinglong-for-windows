package service

import "context"

// notificationEnvKeys maps a saved notification channel field onto the variable
// name the task-side notifiers read.
//
// QingLong ships two notifiers that run inside a task: shell/preload/__ql_notify__
// (used by notify.send/QLAPI.notify) and data/scripts/{sendNotify.js,notify.py}
// (the copy most scripts require). Both build their channel table from
// `process.env`, so the settings page used to have no effect on scripts at all:
// the panel stored the channel in Auths.notification, while a script only saw
// what config.sh and the environment page exported. Node has the same gap.
//
// Exporting the saved channel into the task environment closes it: a script
// that calls sendNotify() now reaches the channel the operator configured in
// the panel, without editing config.sh. Values already present in config.sh or
// the environment page win, so an explicit setting is never overridden.
var notificationEnvKeys = map[string]string{
	// gotify
	"gotifyUrl":      "GOTIFY_URL",
	"gotifyToken":    "GOTIFY_TOKEN",
	"gotifyPriority": "GOTIFY_PRIORITY",
	// go-cqhttp
	"goCqHttpBotUrl":   "GOBOT_URL",
	"goCqHttpBotToken": "GOBOT_TOKEN",
	"goCqHttpBotQq":    "GOBOT_QQ",
	// ServerChan / PushDeer
	"serverChanKey": "PUSH_KEY",
	"pushDeerKey":   "DEER_KEY",
	"pushDeerUrl":   "DEER_URL",
	// Synology Chat
	"synologyChatUrl": "CHAT_URL",
	// Bark
	"barkPush":    "BARK_PUSH",
	"barkArchive": "BARK_ARCHIVE",
	"barkGroup":   "BARK_GROUP",
	"barkSound":   "BARK_SOUND",
	"barkIcon":    "BARK_ICON",
	"barkLevel":   "BARK_LEVEL",
	"barkUrl":     "BARK_URL",
	// Telegram
	"telegramBotToken":     "TG_BOT_TOKEN",
	"telegramBotUserId":    "TG_USER_ID",
	"telegramBotApiHost":   "TG_API_HOST",
	"telegramBotProxyHost": "TG_PROXY_HOST",
	"telegramBotProxyPort": "TG_PROXY_PORT",
	"telegramBotProxyAuth": "TG_PROXY_AUTH",
	// DingTalk
	"dingtalkBotToken":  "DD_BOT_TOKEN",
	"dingtalkBotSecret": "DD_BOT_SECRET",
	// WeCom (bot and app)
	"weWorkBotKey":  "QYWX_KEY",
	"weWorkAppKey":  "QYWX_AM",
	"weWorkOrigin":  "QYWX_ORIGIN",
	"iGotPushKey":   "IGOT_PUSH_KEY",
	"pushPlusToken": "PUSH_PLUS_TOKEN",
	"pushPlusUser":  "PUSH_PLUS_USER",
	// PushPlus
	"pushPlusTemplate":    "PUSH_PLUS_TEMPLATE",
	"pushplusChannel":     "PUSH_PLUS_CHANNEL",
	"pushplusWebhook":     "PUSH_PLUS_WEBHOOK",
	"pushplusCallbackUrl": "PUSH_PLUS_CALLBACKURL",
	"pushplusTo":          "PUSH_PLUS_TO",
	// WePlus
	"wePlusBotToken":    "WE_PLUS_BOT_TOKEN",
	"wePlusBotReceiver": "WE_PLUS_BOT_RECEIVER",
	"wePlusBotVersion":  "WE_PLUS_BOT_VERSION",
	// 智能微秘书
	"aibotkKey":  "AIBOTK_KEY",
	"aibotkType": "AIBOTK_TYPE",
	"aibotkName": "AIBOTK_NAME",
	// 邮件（JS 侧按服务名解析，Python 侧还需要 SMTP_SERVER，见 ScriptEnv）
	"emailService": "SMTP_SERVICE",
	"emailUser":    "SMTP_EMAIL",
	"emailPass":    "SMTP_PASSWORD",
	"emailTo":      "SMTP_EMAIL_TO",
	// PushMe
	"pushMeKey": "PUSHME_KEY",
	// Chronocat
	"chronocatURL":   "CHRONOCAT_URL",
	"chronocatQQ":    "CHRONOCAT_QQ",
	"chronocatToken": "CHRONOCAT_TOKEN",
	// Webhook
	"webhookUrl":         "WEBHOOK_URL",
	"webhookBody":        "WEBHOOK_BODY",
	"webhookHeaders":     "WEBHOOK_HEADERS",
	"webhookMethod":      "WEBHOOK_METHOD",
	"webhookContentType": "WEBHOOK_CONTENT_TYPE",
	// 飞书 / Lark
	"larkKey":    "FSKEY",
	"larkSecret": "FSSECRET",
	// ntfy
	"ntfyUrl":      "NTFY_URL",
	"ntfyTopic":    "NTFY_TOPIC",
	"ntfyPriority": "NTFY_PRIORITY",
	"ntfyToken":    "NTFY_TOKEN",
	"ntfyUsername": "NTFY_USERNAME",
	"ntfyPassword": "NTFY_PASSWORD",
	"ntfyActions":  "NTFY_ACTIONS",
	// WxPusher
	"wxPusherBotAppToken": "WXPUSHER_APP_TOKEN",
	"wxPusherBotTopicIds": "WXPUSHER_TOPIC_IDS",
	"wxPusherBotUids":     "WXPUSHER_UIDS",
	"wxPusherSptList":     "WXPUSHER_SPT_LIST",
	// OpeniLink
	"openiLinkAppToken":     "OPENILINK_APP_TOKEN",
	"openiLinkHubUrl":       "OPENILINK_HUB_URL",
	"openiLinkContextToken": "OPENILINK_CONTEXT_TOKEN",
	// WPUSH
	"wpushApiKey":    "WPUSH_APIKEY",
	"wpushChannel":   "WPUSH_CHANNEL",
	"wpushTopicCode": "WPUSH_TOPIC_CODE",
}

// ScriptEnv renders the saved notification channel as task environment
// variables, so notify.send() / sendNotify() in a script reaches the channel
// configured in 设置 → 通知设置 without duplicating it in config.sh.
//
// Empty fields are dropped: the settings page submits every field of the form,
// including the ones the chosen channel does not use.
func (s *NotifyService) ScriptEnv(ctx context.Context) map[string]string {
	saved := s.savedConfig(ctx)
	values := make(map[string]string, len(saved))
	for field, key := range notificationEnvKeys {
		if value := saved[field]; value != "" {
			values[key] = value
		}
	}
	// The mail channel carries one recipient list. The JS notifier reads
	// SMTP_EMAIL_TO and falls back to the legacy SMTP_TO; the Python one only
	// knows SMTP_EMAIL_TO, so publishing both keeps old scripts working.
	if recipient := values["SMTP_EMAIL_TO"]; recipient != "" {
		values["SMTP_TO"] = recipient
	}
	return values
}
