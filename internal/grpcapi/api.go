package grpcapi

import (
	"context"
	"log/slog"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"qinglong-go/internal/service"
)

// api implements com.ql.api.Api.
//
// The two error styles of back/schedule/api.ts are preserved on purpose:
// GetEnvs, GetCrons and CreateEnv report failures inside the response body
// (code 500) because the script client only rejects on a gRPC error, while
// every other RPC rejects the promise. Mixing the two would silently change
// what a task sees in its catch block.
type api struct {
	UnimplementedApiServer

	envs   *service.EnvService
	crons  *service.CronService
	notify *service.NotifyService
	logger *slog.Logger
}

func newAPI(services Services, logger *slog.Logger) *api {
	if logger == nil {
		logger = slog.Default()
	}
	return &api{
		envs:   services.Envs,
		crons:  services.Crons,
		notify: services.Notify,
		logger: logger,
	}
}

// ---------------------------------------------------------------------------
// Environment variables
// ---------------------------------------------------------------------------

// GetEnvs returns every environment variable; remarks is never null because
// the Node handler maps it with `x.remarks || ”`.
func (a *api) GetEnvs(ctx context.Context, req *GetEnvsRequest) (*EnvsResponse, error) {
	items, err := a.envs.List(ctx, req.GetSearchValue())
	if err != nil {
		return &EnvsResponse{Code: 500, Data: []*EnvItem{}, Message: stringPointer(err.Error())}, nil
	}
	data := make([]*EnvItem, 0, len(items))
	for _, item := range items {
		data = append(data, envItem(item, true))
	}
	return &EnvsResponse{Code: 200, Data: data}, nil
}

// CreateEnv inserts the payloads in order. Failures are gRPC errors, not body
// codes, matching createEnv in the Node service.
func (a *api) CreateEnv(ctx context.Context, req *CreateEnvRequest) (*EnvsResponse, error) {
	payloads := make([]service.EnvInput, 0, len(req.GetEnvs()))
	for _, item := range req.GetEnvs() {
		payload := service.EnvInput{
			Name:  item.GetName(),
			Value: item.GetValue(),
		}
		if item.Remarks != nil {
			payload.Remarks = item.Remarks
		}
		payloads = append(payloads, payload)
	}
	created, err := a.envs.Create(ctx, payloads)
	if err != nil {
		return nil, grpcError(err)
	}
	data := make([]*EnvItem, 0, len(created))
	for _, item := range created {
		data = append(data, envItem(item, false))
	}
	return &EnvsResponse{Code: 200, Data: data}, nil
}

// UpdateEnv rewrites one row. Node picks id/name/value/remarks, so fields the
// caller did not send keep their stored value.
func (a *api) UpdateEnv(ctx context.Context, req *UpdateEnvRequest) (*EnvResponse, error) {
	env := req.GetEnv()
	id := int64(env.GetId())
	if id == 0 {
		return &EnvResponse{Code: 400, Message: stringPointer("id parameter is required")}, nil
	}

	current, err := a.envs.Get(ctx, id)
	if err != nil {
		return nil, grpcError(err)
	}
	input := service.EnvUpdateInput{ID: id}
	if current.Name != nil {
		input.Name = *current.Name
	}
	if current.Value != nil {
		input.Value = *current.Value
	}
	if env.Name != nil {
		input.Name = *env.Name
	}
	if env.Value != nil {
		input.Value = *env.Value
	}
	if env.Remarks != nil {
		input.Remarks = env.Remarks
		input.RemarksSet = true
	}

	updated, err := a.envs.Update(ctx, input)
	if err != nil {
		return nil, grpcError(err)
	}
	return &EnvResponse{Code: 200, Data: envItem(*updated, false)}, nil
}

// DeleteEnvs removes the rows; an empty id list is a body-level 400.
func (a *api) DeleteEnvs(ctx context.Context, req *DeleteEnvsRequest) (*Response, error) {
	if len(req.GetIds()) == 0 {
		return &Response{Code: 400, Message: stringPointer("ids parameter is required")}, nil
	}
	if err := a.envs.Remove(ctx, int64IDs(req.GetIds())); err != nil {
		return nil, grpcError(err)
	}
	return &Response{Code: 200}, nil
}

// MoveEnv reorders one row between two neighbours.
func (a *api) MoveEnv(ctx context.Context, req *MoveEnvRequest) (*EnvResponse, error) {
	if req.GetId() == 0 {
		return &EnvResponse{Code: 400, Message: stringPointer("id parameter is required")}, nil
	}
	moved, err := a.envs.Move(ctx, int64(req.GetId()), int(req.GetFromIndex()), int(req.GetToIndex()))
	if err != nil {
		return nil, grpcError(err)
	}
	return &EnvResponse{Code: 200, Data: envItem(*moved, false)}, nil
}

// DisableEnvs disables the rows.
func (a *api) DisableEnvs(ctx context.Context, req *DisableEnvsRequest) (*Response, error) {
	if len(req.GetIds()) == 0 {
		return &Response{Code: 400, Message: stringPointer("ids parameter is required")}, nil
	}
	if err := a.envs.SetDisabled(ctx, int64IDs(req.GetIds()), true); err != nil {
		return nil, grpcError(err)
	}
	return &Response{Code: 200}, nil
}

// EnableEnvs enables the rows.
func (a *api) EnableEnvs(ctx context.Context, req *EnableEnvsRequest) (*Response, error) {
	if len(req.GetIds()) == 0 {
		return &Response{Code: 400, Message: stringPointer("ids parameter is required")}, nil
	}
	if err := a.envs.SetDisabled(ctx, int64IDs(req.GetIds()), false); err != nil {
		return nil, grpcError(err)
	}
	return &Response{Code: 200}, nil
}

// UpdateEnvNames renames a batch of rows.
func (a *api) UpdateEnvNames(ctx context.Context, req *UpdateEnvNamesRequest) (*Response, error) {
	if len(req.GetIds()) == 0 {
		return &Response{Code: 400, Message: stringPointer("ids parameter is required")}, nil
	}
	if err := a.envs.UpdateNames(ctx, int64IDs(req.GetIds()), req.GetName()); err != nil {
		return nil, grpcError(err)
	}
	return &Response{Code: 200}, nil
}

// GetEnvById returns one row, with remarks mapped to "" when it is null.
func (a *api) GetEnvById(ctx context.Context, req *GetEnvByIdRequest) (*EnvResponse, error) {
	if req.GetId() == 0 {
		return &EnvResponse{Code: 400, Message: stringPointer("id parameter is required")}, nil
	}
	item, err := a.envs.Get(ctx, int64(req.GetId()))
	if err != nil {
		return nil, grpcError(err)
	}
	return &EnvResponse{Code: 200, Data: envItem(*item, true)}, nil
}

// ---------------------------------------------------------------------------
// Notifications
// ---------------------------------------------------------------------------

// SystemNotify sends through the panel notification channel and returns the
// same Response the HTTP settings page gets.
func (a *api) SystemNotify(ctx context.Context, req *SystemNotifyRequest) (*Response, error) {
	if a.notify == nil {
		return &Response{Code: 400, Message: stringPointer("notification service is not available")}, nil
	}
	request := service.NotificationRequest{
		Title:   req.GetTitle(),
		Content: req.GetContent(),
		Params:  notificationParams(req.GetNotificationInfo()),
	}
	// A present NotificationInfo always carries an enum value (proto3 has no
	// presence for it), which is exactly how the Node handler reads it.
	if info := req.GetNotificationInfo(); info != nil {
		request.Type = notificationChannel(info.GetType())
	}
	reply := a.notify.Notify(ctx, request)
	return &Response{Code: int32(reply.Code), Message: stringPointer(reply.Message)}, nil
}

// RecordNotifyLog stores one audit row reported by a task. The status is
// inverted on purpose: the RPC sends `success`, the table stores 0 for success.
func (a *api) RecordNotifyLog(ctx context.Context, req *RecordNotifyLogRequest) (*Response, error) {
	if a.notify == nil {
		return &Response{Code: 200}, nil
	}
	status := service.NotifyStatusFail
	if req.GetSuccess() {
		status = service.NotifyStatusSuccess
	}
	err := a.notify.Record(ctx, service.NotifyLogEntry{
		Title:      req.GetTitle(),
		Content:    req.GetContent(),
		Status:     status,
		NotifyType: req.GetNotifyType(),
		Source:     "script",
		ScriptName: req.GetScriptName(),
		TaskID:     req.GetTaskId(),
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return &Response{Code: 200}, nil
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

// GetCronDetail finds the task a log file belongs to.
func (a *api) GetCronDetail(ctx context.Context, req *CronDetailRequest) (*CronDetailResponse, error) {
	if req.GetLogPath() == "" {
		return &CronDetailResponse{Code: 400, Message: stringPointer("log_path parameter is required")}, nil
	}
	row, err := a.crons.FindByLogPath(ctx, req.GetLogPath())
	if err != nil {
		return nil, grpcError(err)
	}
	return &CronDetailResponse{Code: 200, Data: cronItem(row)}, nil
}

// CreateCron inserts a task. The gRPC path skips the celebrate schema (Node
// does the same), so a malformed cron expression surfaces as a gRPC error from
// the service rather than a 400 body.
func (a *api) CreateCron(ctx context.Context, req *CreateCronRequest) (*CronResponse, error) {
	input := &service.CrontabInput{
		Command:           req.GetCommand(),
		Schedule:          req.GetSchedule(),
		Labels:            labelValues(req.GetLabels()),
		LabelsSet:         true,
		ExtraSchedules:    extraScheduleValues(req.GetExtraSchedules()),
		ExtraSchedulesSet: true,
	}
	if req.Name != nil {
		input.NameSet = true
		input.Name = req.Name
	}
	if req.SubId != nil {
		value := int64(req.GetSubId())
		input.SubIDSet = true
		input.SubID = &value
	}
	if req.TaskBefore != nil {
		input.TaskBeforeSet = true
		input.TaskBefore = req.TaskBefore
	}
	if req.TaskAfter != nil {
		input.TaskAfterSet = true
		input.TaskAfter = req.TaskAfter
	}

	created, err := a.crons.CreateFromAPI(ctx, input)
	if err != nil {
		return nil, grpcError(err)
	}
	return &CronResponse{Code: 200, Data: cronItem(created)}, nil
}

// UpdateCron applies the fields the caller sent. Pointer fields left nil are
// skipped: proto3 optional means "not sent", not zero, and writing zero would
// silently clear a task's sub_id or hooks. Repeated fields are always present
// in the request, exactly like the Node implementation sees them.
func (a *api) UpdateCron(ctx context.Context, req *UpdateCronRequest) (*CronResponse, error) {
	id := int64(req.GetId())
	current, err := a.crons.Get(ctx, id)
	if err != nil {
		return nil, grpcError(err)
	}

	input := &service.CrontabInput{
		ID:                id,
		Command:           current.Command,
		Labels:            labelValues(req.GetLabels()),
		LabelsSet:         true,
		ExtraSchedules:    extraScheduleValues(req.GetExtraSchedules()),
		ExtraSchedulesSet: true,
	}
	if current.Schedule != nil {
		input.Schedule = *current.Schedule
	}
	if req.Schedule != nil {
		input.Schedule = req.GetSchedule()
	}
	if req.Command != nil {
		input.Command = req.GetCommand()
	}
	if req.Name != nil {
		input.NameSet = true
		input.Name = req.Name
	}
	if req.SubId != nil {
		value := int64(req.GetSubId())
		input.SubIDSet = true
		input.SubID = &value
	}
	if req.TaskBefore != nil {
		input.TaskBeforeSet = true
		input.TaskBefore = req.TaskBefore
	}
	if req.TaskAfter != nil {
		input.TaskAfterSet = true
		input.TaskAfter = req.TaskAfter
	}

	updated, err := a.crons.Update(ctx, input)
	if err != nil {
		return nil, grpcError(err)
	}
	return &CronResponse{Code: 200, Data: cronItem(updated)}, nil
}

// DeleteCrons removes the tasks.
func (a *api) DeleteCrons(ctx context.Context, req *DeleteCronsRequest) (*Response, error) {
	if len(req.GetIds()) == 0 {
		return &Response{Code: 400, Message: stringPointer("ids parameter is required")}, nil
	}
	if err := a.crons.Remove(ctx, int64IDs(req.GetIds())); err != nil {
		return nil, grpcError(err)
	}
	return &Response{Code: 200}, nil
}

// GetCrons returns the full list (page=0/size=0) in the normalised shape the
// script client relies on.
func (a *api) GetCrons(ctx context.Context, req *GetCronsRequest) (*CronsResponse, error) {
	list, err := a.crons.List(ctx, service.CronListParams{
		SearchValue: req.GetSearchValue(),
		Page:        0,
		Size:        0,
	})
	if err != nil {
		return &CronsResponse{Code: 500, Data: []*CronItem{}, Message: stringPointer(err.Error())}, nil
	}
	data := make([]*CronItem, 0, len(list.Data))
	for index := range list.Data {
		data = append(data, cronItem(&list.Data[index]))
	}
	return &CronsResponse{Code: 200, Data: data}, nil
}

// GetCronById returns one task, or a 404 body when it does not exist.
func (a *api) GetCronById(ctx context.Context, req *GetCronByIdRequest) (*CronResponse, error) {
	if req.GetId() == 0 {
		return &CronResponse{Code: 400, Message: stringPointer("id parameter is required")}, nil
	}
	row, err := a.crons.Get(ctx, int64(req.GetId()))
	if err != nil {
		return &CronResponse{Code: 404, Message: stringPointer(err.Error())}, nil
	}
	return &CronResponse{Code: 200, Data: cronItem(row)}, nil
}

// EnableCrons enables the tasks.
func (a *api) EnableCrons(ctx context.Context, req *EnableCronsRequest) (*Response, error) {
	if len(req.GetIds()) == 0 {
		return &Response{Code: 400, Message: stringPointer("ids parameter is required")}, nil
	}
	if err := a.crons.SetDisabled(ctx, int64IDs(req.GetIds()), false); err != nil {
		return nil, grpcError(err)
	}
	return &Response{Code: 200}, nil
}

// DisableCrons disables the tasks.
func (a *api) DisableCrons(ctx context.Context, req *DisableCronsRequest) (*Response, error) {
	if len(req.GetIds()) == 0 {
		return &Response{Code: 400, Message: stringPointer("ids parameter is required")}, nil
	}
	if err := a.crons.SetDisabled(ctx, int64IDs(req.GetIds()), true); err != nil {
		return nil, grpcError(err)
	}
	return &Response{Code: 200}, nil
}

// RunCrons queues the tasks and returns immediately; the caller must not wait
// for the processes to finish.
func (a *api) RunCrons(ctx context.Context, req *RunCronsRequest) (*Response, error) {
	if len(req.GetIds()) == 0 {
		return &Response{Code: 400, Message: stringPointer("ids parameter is required")}, nil
	}
	if err := a.crons.Run(ctx, int64IDs(req.GetIds())); err != nil {
		return nil, grpcError(err)
	}
	return &Response{Code: 200}, nil
}

// ---------------------------------------------------------------------------
// Mapping helpers
// ---------------------------------------------------------------------------

// grpcError mirrors `callback(e)` in the Node service: a plain Error surfaces
// as UNKNOWN with the message as details.
func grpcError(err error) error {
	if err == nil {
		return nil
	}
	return status.Error(codes.Unknown, err.Error())
}

func stringPointer(value string) *string { return &value }

func int64IDs(values []int32) []int64 {
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		ids = append(ids, int64(value))
	}
	return ids
}

// envItem converts a service row. withRemarksFallback mirrors the `|| ”`
// mapping GetEnvs/GetEnvById apply; CreateEnv/UpdateEnv return the raw column.
func envItem(item service.EnvResponse, withRemarksFallback bool) *EnvItem {
	out := &EnvItem{}
	id := int32(item.ID)
	out.Id = &id
	if item.Name != nil {
		out.Name = item.Name
	}
	if item.Value != nil {
		out.Value = item.Value
	}
	if item.Remarks != nil {
		out.Remarks = item.Remarks
	} else if withRemarksFallback {
		out.Remarks = stringPointer("")
	}
	if item.Status != nil {
		status := int32(*item.Status)
		out.Status = &status
	}
	if item.Position != nil {
		position := int64(*item.Position)
		out.Position = &position
	}
	return out
}

// cronItem applies normalizeCronData from back/schedule/api.ts: labels and
// extra_schedules never come back null, and the "not set" columns (sub_id, pid,
// task_before, task_after) are dropped instead of being sent as 0 or "".
func cronItem(item *service.CrontabResponse) *CronItem {
	if item == nil {
		return nil
	}
	out := &CronItem{
		Id:                int32Pointer(item.ID),
		Command:           stringPointer(item.Command),
		Schedule:          item.Schedule,
		Name:              item.Name,
		Labels:            labelList(item.Labels),
		ExtraSchedules:    extraScheduleList(item.ExtraSchedules),
		Status:            int32PointerFrom(item.Status),
		LogPath:           item.LogPath,
		LastRunningTime:   item.LastRunningTime,
		LastExecutionTime: item.LastExecutionTime,
	}
	if item.SubID != nil && *item.SubID != 0 {
		out.SubId = int32Pointer(*item.SubID)
	}
	if item.Pid != nil && *item.Pid != 0 {
		out.Pid = int32Pointer(*item.Pid)
	}
	if item.TaskBefore != nil && *item.TaskBefore != "" {
		out.TaskBefore = item.TaskBefore
	}
	if item.TaskAfter != nil && *item.TaskAfter != "" {
		out.TaskAfter = item.TaskAfter
	}
	return out
}

func int32Pointer(value int64) *int32 {
	converted := int32(value)
	return &converted
}

func int32PointerFrom(value *int64) *int32 {
	if value == nil {
		return nil
	}
	return int32Pointer(*value)
}

// labelList decodes the JSON label column into the repeated field. NULL, an
// empty string and malformed JSON all collapse to an empty list.
func labelList(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		labels := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				labels = append(labels, text)
			}
		}
		return labels
	default:
		return []string{}
	}
}

// extraScheduleList decodes the JSON column of {"schedule": "..."} objects.
func extraScheduleList(value any) []*ExtraScheduleItem {
	items := make([]*ExtraScheduleItem, 0)
	list, ok := value.([]any)
	if !ok {
		return items
	}
	for _, item := range list {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		schedule, ok := object["schedule"].(string)
		if !ok || schedule == "" {
			continue
		}
		items = append(items, &ExtraScheduleItem{Schedule: schedule})
	}
	return items
}

// labelValues converts the repeated request field into the JSON payload the
// service stores.
func labelValues(labels []string) []any {
	values := make([]any, 0, len(labels))
	for _, label := range labels {
		values = append(values, label)
	}
	return values
}

// extraScheduleValues converts the repeated request field into the JSON column
// shape used by the panel.
func extraScheduleValues(items []*ExtraScheduleItem) []any {
	values := make([]any, 0, len(items))
	for _, item := range items {
		values = append(values, map[string]any{"schedule": item.GetSchedule()})
	}
	return values
}

// notificationChannel maps the proto enum to the channel name the notifier and
// the settings page use (NotificationModeStringMap in back/config/const.ts).
func notificationChannel(mode NotificationMode) string {
	switch mode {
	case NotificationMode_gotify:
		return "gotify"
	case NotificationMode_goCqHttpBot:
		return "goCqHttpBot"
	case NotificationMode_serverChan:
		return "serverChan"
	case NotificationMode_pushDeer:
		return "pushDeer"
	case NotificationMode_bark:
		return "bark"
	case NotificationMode_chat:
		return "chat"
	case NotificationMode_telegramBot:
		return "telegramBot"
	case NotificationMode_dingtalkBot:
		return "dingtalkBot"
	case NotificationMode_weWorkBot:
		return "weWorkBot"
	case NotificationMode_weWorkApp:
		return "weWorkApp"
	case NotificationMode_aibotk:
		return "aibotk"
	case NotificationMode_iGot:
		return "iGot"
	case NotificationMode_pushPlus:
		return "pushPlus"
	case NotificationMode_wePlusBot:
		return "wePlusBot"
	case NotificationMode_email:
		return "email"
	case NotificationMode_pushMe:
		return "pushMe"
	case NotificationMode_feishu:
		return "feishu"
	case NotificationMode_webhook:
		return "webhook"
	case NotificationMode_chronocat:
		return "chronocat"
	case NotificationMode_ntfy:
		return "ntfy"
	case NotificationMode_wxPusherBot:
		return "wxPusherBot"
	case NotificationMode_wxPusherSpt:
		return "wxPusherSpt"
	case NotificationMode_wpush:
		return "wpush"
	default:
		return ""
	}
}

// notificationParams flattens the per-call channel overrides into the string
// map the notifier merges over the saved configuration.
func notificationParams(info *NotificationInfo) map[string]string {
	if info == nil {
		return nil
	}
	params := map[string]string{}
	set := func(key string, value *string) {
		if value != nil {
			params[key] = *value
		}
	}
	set("gotifyUrl", info.GotifyUrl)
	set("gotifyToken", info.GotifyToken)
	set("goCqHttpBotUrl", info.GoCqHttpBotUrl)
	set("goCqHttpBotToken", info.GoCqHttpBotToken)
	set("goCqHttpBotQq", info.GoCqHttpBotQq)
	set("serverChanKey", info.ServerChanKey)
	set("pushDeerKey", info.PushDeerKey)
	set("pushDeerUrl", info.PushDeerUrl)
	set("synologyChatUrl", info.SynologyChatUrl)
	set("barkPush", info.BarkPush)
	set("barkIcon", info.BarkIcon)
	set("barkSound", info.BarkSound)
	set("barkGroup", info.BarkGroup)
	set("barkLevel", info.BarkLevel)
	set("barkUrl", info.BarkUrl)
	set("barkArchive", info.BarkArchive)
	set("telegramBotToken", info.TelegramBotToken)
	set("telegramBotUserId", info.TelegramBotUserId)
	set("telegramBotProxyHost", info.TelegramBotProxyHost)
	set("telegramBotProxyPort", info.TelegramBotProxyPort)
	set("telegramBotProxyAuth", info.TelegramBotProxyAuth)
	set("telegramBotApiHost", info.TelegramBotApiHost)
	set("dingtalkBotToken", info.DingtalkBotToken)
	set("dingtalkBotSecret", info.DingtalkBotSecret)
	set("weWorkBotKey", info.WeWorkBotKey)
	set("weWorkOrigin", info.WeWorkOrigin)
	set("weWorkAppKey", info.WeWorkAppKey)
	set("aibotkKey", info.AibotkKey)
	set("aibotkType", info.AibotkType)
	set("aibotkName", info.AibotkName)
	set("iGotPushKey", info.IGotPushKey)
	set("pushPlusToken", info.PushPlusToken)
	set("pushPlusUser", info.PushPlusUser)
	set("pushPlusTemplate", info.PushPlusTemplate)
	set("pushplusChannel", info.PushplusChannel)
	set("pushplusWebhook", info.PushplusWebhook)
	set("pushplusCallbackUrl", info.PushplusCallbackUrl)
	set("pushplusTo", info.PushplusTo)
	set("wePlusBotToken", info.WePlusBotToken)
	set("wePlusBotReceiver", info.WePlusBotReceiver)
	set("wePlusBotVersion", info.WePlusBotVersion)
	set("emailService", info.EmailService)
	set("emailUser", info.EmailUser)
	set("emailPass", info.EmailPass)
	set("emailTo", info.EmailTo)
	set("pushMeKey", info.PushMeKey)
	set("pushMeUrl", info.PushMeUrl)
	set("chronocatURL", info.ChronocatURL)
	set("chronocatQQ", info.ChronocatQQ)
	set("chronocatToken", info.ChronocatToken)
	set("webhookHeaders", info.WebhookHeaders)
	set("webhookBody", info.WebhookBody)
	set("webhookUrl", info.WebhookUrl)
	set("webhookMethod", info.WebhookMethod)
	set("webhookContentType", info.WebhookContentType)
	set("larkKey", info.LarkKey)
	set("larkSecret", info.LarkSecret)
	set("ntfyUrl", info.NtfyUrl)
	set("ntfyTopic", info.NtfyTopic)
	set("ntfyPriority", info.NtfyPriority)
	set("ntfyToken", info.NtfyToken)
	set("ntfyUsername", info.NtfyUsername)
	set("ntfyPassword", info.NtfyPassword)
	set("ntfyActions", info.NtfyActions)
	set("wxPusherBotAppToken", info.WxPusherBotAppToken)
	set("wxPusherBotTopicIds", info.WxPusherBotTopicIds)
	set("wxPusherBotUids", info.WxPusherBotUids)
	set("wxPusherSptList", info.WxPusherSptList)
	set("wpushApiKey", info.WpushApiKey)
	set("wpushChannel", info.WpushChannel)
	set("wpushTopicCode", info.WpushTopicCode)
	if info.GotifyPriority != nil {
		params["gotifyPriority"] = strconv.Itoa(int(info.GetGotifyPriority()))
	}
	return params
}
