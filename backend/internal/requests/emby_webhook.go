package requests

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"qmediasync/internal/models"
)

// EmbyWebhookMaxBytes 限制公开回调的 JSON 大小；媒体详情无需在通知中传输。
const EmbyWebhookMaxBytes = 4 << 20

// EmbyWebhookRequest 保留既有通知字段；持久工作仅使用 ToEnvelope 的最小脱敏输入。
type EmbyWebhookRequest struct {
	Title       string `json:"Title"`
	Description string `json:"Description"`
	Date        string `json:"Date"`
	Event       string `json:"Event"`
	Severity    string `json:"Severity"`
	Server      struct {
		Name    string `json:"Name"`
		ID      string `json:"Id"`
		Version string `json:"Version"`
	} `json:"Server"`
	Item struct {
		Name              string            `json:"Name"`
		ID                string            `json:"Id"`
		ServerID          string            `json:"ServerId"`
		Type              string            `json:"Type"`
		IsFolder          bool              `json:"IsFolder"`
		FileName          string            `json:"FileName"`
		Path              string            `json:"Path"`
		Overview          string            `json:"Overview"`
		SeriesName        string            `json:"SeriesName"`
		SeasonName        string            `json:"SeasonName"`
		ParentID          string            `json:"ParentId"`
		SeriesId          string            `json:"SeriesId"`
		SeasonId          string            `json:"SeasonId"`
		ExtraType         string            `json:"ExtraType"`
		IndexNumber       int               `json:"IndexNumber"`
		ParentIndexNumber int               `json:"ParentIndexNumber"`
		ProductionYear    int               `json:"ProductionYear"`
		Genres            []string          `json:"Genres"`
		ImageTags         map[string]string `json:"ImageTags"`
	} `json:"Item"`
	hasItem              bool
	hasIndexNumber       bool
	hasParentIndexNumber bool
}

// ParseEmbyWebhook 检查重复键、大小、深度及关键字段类型后只解码一次路径转义。
// 普通 json.Unmarshal 会静默覆盖重复键并接受 null 标量，不适用于删除证据。
func ParseEmbyWebhook(body []byte) (EmbyWebhookRequest, error) {
	var request EmbyWebhookRequest
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
	event, ok := object["Event"].(string)
	if !ok || event == "" {
		return request, errors.New("Webhook Event 必须为非空字符串")
	}
	request.Event = event
	if request.Managed() {
		if err := validateEmbyJSONFields(object, "root"); err != nil {
			return request, err
		}
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return request, errors.New("Webhook 字段类型无效")
	}
	item, requestHasItem := object["Item"].(map[string]any)
	request.hasItem = requestHasItem
	_, request.hasIndexNumber = item["IndexNumber"]
	_, request.hasParentIndexNumber = item["ParentIndexNumber"]
	return request, request.Validate()
}

// encoding/json 会把孤立 UTF-16 代理项替换为 U+FFFD；物理路径不能被静默改写。
func validEmbyJSONSurrogates(body []byte) bool {
	inString := false
	for index := 0; index < len(body); index++ {
		if body[index] == '"' {
			inString = !inString
			continue
		}
		if !inString || body[index] != '\\' {
			continue
		}
		index++
		if index >= len(body) || body[index] != 'u' {
			continue
		}
		if index+4 >= len(body) {
			return false
		}
		value, err := strconv.ParseUint(string(body[index+1:index+5]), 16, 16)
		if err != nil {
			return false
		}
		index += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value < 0xd800 || value > 0xdbff {
			continue
		}
		if index+6 >= len(body) || body[index+1] != '\\' || body[index+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(body[index+3:index+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		index += 6
	}
	return true
}

func readEmbyJSON(decoder *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("JSON 嵌套过深")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		object := make(map[string]any)
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok || seen[strings.ToLower(key)] {
				return nil, errors.New("JSON 重复键")
			}
			seen[strings.ToLower(key)] = true
			value, err := readEmbyJSON(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		_, err := decoder.Token()
		return object, err
	case json.Delim('['):
		var values []any
		for decoder.More() {
			value, err := readEmbyJSON(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		_, err := decoder.Token()
		return values, err
	default:
		return token, nil
	}
}

var embyWebhookFields = map[string]map[string]string{
	"root":   {"Title": "string", "Description": "string", "Date": "string", "Event": "string", "Severity": "string", "Item": "item", "Server": "server", "User": "user"},
	"server": {"Name": "string", "Id": "string", "Version": "string"},
	"user":   {"Name": "string", "Id": "string"},
	"item": {
		"Name": "string", "Id": "string", "ServerId": "string", "Type": "string", "Path": "string", "FileName": "string",
		"Overview": "string", "SeriesName": "string", "SeasonName": "string", "SeriesId": "string", "SeasonId": "string", "ParentId": "string", "ExtraType": "string",
		"IndexNumber": "number", "ParentIndexNumber": "number", "ProductionYear": "number", "IsFolder": "bool", "Genres": "array", "ImageTags": "object",
	},
}

func validateEmbyJSONFields(object map[string]any, scope string) error {
	for key, value := range object {
		kind, known := embyWebhookFields[scope][key]
		if !known {
			for canonical := range embyWebhookFields[scope] {
				if strings.EqualFold(key, canonical) {
					return errors.New("Webhook 关键字段大小写无效")
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
		case "array":
			_, valid = value.([]any)
		case "object", "item", "server", "user":
			child, ok := value.(map[string]any)
			valid = ok
			if ok && kind != "object" {
				if err := validateEmbyJSONFields(child, kind); err != nil {
					return err
				}
			}
		}
		if !valid {
			return fmt.Errorf("Webhook %s.%s 字段类型无效", scope, key)
		}
	}
	return nil
}

// Managed 判断是否交给持久条目工作；其他事件仍由既有通知逻辑处理。
func (request EmbyWebhookRequest) Managed() bool {
	switch request.Event {
	case "library.new", "library.modified", "library.deleted", "deep.delete":
		return true
	default:
		return false
	}
}

// Validate 校验格式，不把事件日期或当前路径当作物理文件代际。
func (request EmbyWebhookRequest) Validate() error {
	if request.Event == "" {
		return errors.New("Webhook Event 不能为空")
	}
	if !request.Managed() {
		return nil
	}
	if !request.hasItem || request.Item.Type == "" || !validEmbyWebhookID(request.Item.ID) {
		return errors.New("Webhook Item 缺失或条目 ID 无效")
	}
	for _, id := range []string{request.Item.ParentID, request.Item.SeriesId, request.Item.SeasonId} {
		if id != "" && !validEmbyWebhookID(id) {
			return errors.New("Webhook 父级条目 ID 无效")
		}
	}
	if request.Date != "" {
		if _, err := time.Parse(time.RFC3339Nano, request.Date); err != nil {
			return errors.New("Webhook Date 无效")
		}
	}
	if strings.ContainsRune(request.Item.Path, 0) {
		return errors.New("Webhook Item.Path 包含无效字符")
	}
	return nil
}

func validEmbyWebhookID(id string) bool {
	value, err := strconv.ParseInt(id, 10, 64)
	return err == nil && value > 0 && strconv.FormatInt(value, 10) == id
}

// ToEnvelope 仅输出不含原始正文和 URL 凭据的最小证据；名称字段经脱敏截断后仅用于诊断展示，候选不授予删除权限。
func (request EmbyWebhookRequest) ToEnvelope() models.EmbyWebhookEnvelope {
	envelope := models.EmbyWebhookEnvelope{
		Event: request.Event, ServerID: request.Server.ID, ItemServerID: request.Item.ServerID,
		ItemID: request.Item.ID, ItemType: request.Item.Type,
		ItemName: request.Item.Name, SeriesName: request.Item.SeriesName, SeasonName: request.Item.SeasonName,
		ItemPath: safeEmbyWebhookPath(request.Item.Path),
		Date:     request.Date, Source: "official", ParentID: request.Item.ParentID,
		SeriesID: request.Item.SeriesId, SeasonID: request.Item.SeasonId, ExtraType: request.Item.ExtraType, IsFolder: request.Item.IsFolder,
	}
	if request.hasIndexNumber {
		envelope.IndexNumber = &request.Item.IndexNumber
	}
	if request.hasParentIndexNumber {
		envelope.ParentIndexNumber = &request.Item.ParentIndexNumber
	}
	if request.Server.ID == "" {
		envelope.Blocked = true
		envelope.Issues = append(envelope.Issues, "missing_server_identity")
	}
	if request.Item.ServerID != "" && request.Server.ID != request.Item.ServerID {
		envelope.Blocked = true
		envelope.Issues = append(envelope.Issues, "conflicting_server_identity")
	}
	if (request.Event == "library.deleted" || request.Event == "deep.delete") && request.Item.Path == "" {
		envelope.Blocked = true
		envelope.Issues = append(envelope.Issues, "missing_original_item_path")
	}
	if request.Event == "deep.delete" {
		parseEmbyDeepDescription(request.Description, &envelope)
	}
	return envelope
}

func parseEmbyDeepDescription(description string, envelope *models.EmbyWebhookEnvelope) {
	envelope.Source = "deep-unsupported"
	const nameHeader = "Item Name:\n"
	const pathHeader = "\n\nItem Path:\n"
	const mountHeader = "\n\nMount Paths:\n"
	if !strings.HasPrefix(description, nameHeader) || strings.Count(description, "Item Name:") != 1 ||
		strings.Count(description, "Item Path:") != 1 || strings.Count(description, "Mount Paths:") != 1 || strings.ContainsRune(description, '\r') {
		envelope.Issues = append(envelope.Issues, "unsupported_deep_description")
		return
	}
	_, rest, hasPath := strings.Cut(description, pathHeader)
	itemPath, mountPaths, hasMount := strings.Cut(rest, mountHeader)
	if !hasPath || !hasMount || itemPath == "" || mountPaths == "" {
		envelope.Issues = append(envelope.Issues, "unsupported_deep_description")
		return
	}
	envelope.Source = "deep-description-v1"
	// 多行正文只保留格式原因；没有可靠数组边界，不能持久化可能夹带凭据的自由文本。
	if !strings.ContainsAny(itemPath, "\r\n") && isEmbyWebhookSource(itemPath) {
		envelope.DeepItemPath = safeEmbyWebhookPath(itemPath)
	}
	if strings.ContainsAny(mountPaths, "\r\n") {
		envelope.Issues = append(envelope.Issues, "ambiguous_multiline_mount_paths")
		return
	}
	if !isEmbyWebhookSource(mountPaths) {
		envelope.Issues = append(envelope.Issues, "unsupported_mount_path")
		return
	}
	envelope.DeepMountPaths = safeEmbyWebhookPath(mountPaths)
	candidate := models.EmbyWebhookCandidate{Path: envelope.DeepMountPaths}
	if parsed, err := url.Parse(mountPaths); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil {
			envelope.Issues = append(envelope.Issues, "invalid_source_query")
			return
		}
		for _, key := range []string{"pickcode", "pick_code"} {
			if len(query[key]) > 1 {
				envelope.Issues = append(envelope.Issues, "conflicting_pickcode_candidates")
				return
			}
			if code := query.Get(key); code != "" {
				if candidate.PickCode != "" && candidate.PickCode != code {
					envelope.Issues = append(envelope.Issues, "conflicting_pickcode_candidates")
					return
				}
				candidate.PickCode = code
			}
		}
	}
	envelope.Candidates = []models.EmbyWebhookCandidate{candidate}
}

func isEmbyWebhookSource(value string) bool {
	if strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	if strings.HasPrefix(value, "/") || (len(value) >= 3 && value[1] == ':' && (value[2] == '\\' || value[2] == '/')) || strings.HasPrefix(value, `\\`) {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != ""
}

func safeEmbyWebhookPath(value string) string {
	if !strings.HasPrefix(strings.ToLower(value), "http:") && !strings.HasPrefix(strings.ToLower(value), "https:") {
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return "[invalid-url]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
