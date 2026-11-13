package requests

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"qmediasync/internal/models"
)

// JellyfinWebhookRequest 对应官方 Webhook v21/v22 的 Send All Properties 输出。
// 插件不提供原始 Path/ParentId，删除必须使用本端独立的历史证据。
type JellyfinWebhookRequest struct {
	NotificationType      string
	ServerId              string
	ServerName            string
	ItemId                string
	ItemType              string
	Name                  string
	Overview              string
	SeriesId              string
	SeasonId              string
	SeriesName            string
	SeasonNumber          *int
	EpisodeNumber         *int
	Year                  int
	UtcTimestamp          string
	Timestamp             string
	UserId                string
	NotificationUsername  string
	DeviceName            string
	Client                string
	ClientName            string
	Id                    string
	PlaybackPositionTicks int64
	RunTimeTicks          int64
	IsPaused              bool
}

// ParseJellyfinWebhook 复用有上限、重复键与编码检查，不按 MIME 放宽身份检查。
func ParseJellyfinWebhook(body []byte) (JellyfinWebhookRequest, error) {
	var request JellyfinWebhookRequest
	if len(body) > EmbyWebhookMaxBytes || !utf8.Valid(body) || !validEmbyJSONSurrogates(body) {
		return request, errors.New("Webhook JSON 大小或编码无效")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := readEmbyJSON(decoder, 0)
	if err != nil {
		return request, errors.New("Webhook JSON 无效或存在重复键")
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return request, errors.New("Webhook 只能包含一个 JSON 对象")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return request, errors.New("Webhook 必须为 JSON 对象")
	}
	fields := map[string]string{}
	for _, key := range []string{"NotificationType", "ServerId", "ServerName", "ItemId", "ItemType", "Name", "Overview", "SeriesId", "SeasonId", "SeriesName", "UtcTimestamp", "Timestamp", "UserId", "NotificationUsername", "DeviceName", "Client", "ClientName", "Id"} {
		fields[key] = "string"
	}
	for _, key := range []string{"SeasonNumber", "EpisodeNumber", "Year", "PlaybackPositionTicks", "RunTimeTicks"} {
		fields[key] = "number"
	}
	fields["IsPaused"] = "bool"
	for key, value := range object {
		kind, known := fields[key]
		if !known {
			for canonical := range fields {
				if strings.EqualFold(key, canonical) {
					return request, errors.New("Webhook 关键字段大小写无效")
				}
			}
			continue
		}
		valid := false
		switch kind {
		case "string":
			_, valid = value.(string)
		case "number":
			_, valid = value.(json.Number)
		case "bool":
			_, valid = value.(bool)
		}
		if !valid {
			return request, errors.New("Webhook 字段类型无效")
		}
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return request, errors.New("Webhook 字段类型无效")
	}
	if request.NotificationType == "" {
		return request, errors.New("Webhook NotificationType 不能为空")
	}
	if request.Managed() || strings.HasPrefix(request.Event(), "playback.") {
		id, err := uuid.Parse(request.ItemId)
		if err != nil || id == uuid.Nil || request.ItemType == "" {
			return request, errors.New("Webhook ItemId 或 ItemType 无效")
		}
		request.ItemId = strings.ReplaceAll(id.String(), "-", "")
		for _, field := range []*string{&request.SeriesId, &request.SeasonId} {
			if *field != "" {
				id, err := uuid.Parse(*field)
				if err != nil || id == uuid.Nil {
					return request, errors.New("Webhook 父级条目 ID 无效")
				}
				*field = strings.ReplaceAll(id.String(), "-", "")
			}
		}
	}
	// 仅撤销已知展示字段的一层引号预处理；保留反斜杠、LF 和身份原文。
	for _, field := range []*string{&request.Name, &request.Overview, &request.SeriesName, &request.ServerName, &request.NotificationUsername, &request.DeviceName, &request.Client} {
		*field = strings.ReplaceAll(*field, `\"`, `"`)
	}
	return request, nil
}

func (r JellyfinWebhookRequest) Event() string {
	switch r.NotificationType {
	case "ItemAdded":
		return "library.new"
	case "ItemDeleted":
		return "library.deleted"
	case "PlaybackStart":
		return "playback.start"
	case "PlaybackStop":
		return "playback.stop"
	}
	return ""
}
func (r JellyfinWebhookRequest) Managed() bool {
	return r.NotificationType == "ItemAdded" || r.NotificationType == "ItemDeleted"
}
func (r JellyfinWebhookRequest) ToEnvelope() models.EmbyWebhookEnvelope {
	e := models.EmbyWebhookEnvelope{MediaServerType: models.MediaServerJellyfin, Event: r.Event(), ServerID: r.ServerId, ItemID: r.ItemId, ItemType: r.ItemType, ItemName: r.Name, SeriesName: r.SeriesName, SeriesID: r.SeriesId, SeasonID: r.SeasonId, IndexNumber: r.EpisodeNumber, ParentIndexNumber: r.SeasonNumber, Date: r.UtcTimestamp, Source: "jellyfin-official"}
	if e.Date == "" {
		e.Date = r.Timestamp
	}
	if r.ServerId == "" {
		e.Blocked = true
		e.Issues = []string{"missing_server_identity"}
	}
	return e
}
func (r JellyfinWebhookRequest) NotificationEvent() EmbyWebhookRequest {
	var e EmbyWebhookRequest
	e.Event = r.Event()
	e.Date = r.UtcTimestamp
	if e.Date == "" {
		e.Date = r.Timestamp
	}
	e.Server.ID = r.ServerId
	e.Item.ID = r.ItemId
	e.Item.Type = r.ItemType
	e.Item.Name = r.Name
	e.Item.Overview = r.Overview
	e.Item.SeriesId = r.SeriesId
	e.Item.SeasonId = r.SeasonId
	e.Item.SeriesName = r.SeriesName
	e.Item.ProductionYear = r.Year
	if r.SeasonNumber != nil {
		e.Item.ParentIndexNumber = *r.SeasonNumber
	}
	if r.EpisodeNumber != nil {
		e.Item.IndexNumber = *r.EpisodeNumber
	}
	return e
}
func (r JellyfinWebhookRequest) Playback() models.EmbyPlaybackWebhook {
	client := r.Client
	if client == "" {
		client = r.ClientName
	}
	e := models.EmbyPlaybackWebhook{Event: r.Event(), User: models.EmbyPlaybackUser{ID: r.UserId, Name: r.NotificationUsername}, Session: models.EmbyPlaybackSession{DeviceName: r.DeviceName, Client: client}, PlaybackInfo: models.EmbyPlaybackInfo{PositionTicks: r.PlaybackPositionTicks, PlaySessionId: r.Id, MediaSource: models.EmbyMediaSource{RunTimeTicks: r.RunTimeTicks}}, Item: models.EmbyPlaybackItem{ID: r.ItemId, Type: r.ItemType, Name: r.Name, SeriesName: r.SeriesName, ProductionYear: r.Year}}
	if r.SeasonNumber != nil {
		e.Item.SeasonNumber = *r.SeasonNumber
	}
	if r.EpisodeNumber != nil {
		e.Item.EpisodeNumber = *r.EpisodeNumber
	}
	return e
}
