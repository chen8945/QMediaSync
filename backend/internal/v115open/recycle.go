package v115open

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"resty.dev/v3"
)

const (
	// RecyclePageLimit 是回收站列表接口允许的最大单页条数。
	RecyclePageLimit = 200
	// RecycleBatchLimit 是回收站指定删除和还原允许的最大批量条数。
	RecycleBatchLimit = 1150
)

// RecycleEntry 保留回收站条目的身份和清理条件，数字或字符串 ID 均不经过浮点数。
type RecycleEntry struct {
	ID         json.Number `json:"id"`
	FileName   string      `json:"file_name"`
	Type       json.Number `json:"type"`   // 1 为文件，2 为目录，与普通文件列表的类型不同。
	DeletedAt  json.Number `json:"dtime"`  // 删除时间，Unix 秒。
	Status     json.Number `json:"status"` // -1 为还原中，0 为正常。
	ParentID   json.Number `json:"cid"`
	ParentName string      `json:"parent_name"`
}

// RecycleList 是已核验分页字段和条目身份的单页回收站列表。
type RecycleList struct {
	Offset  int
	Limit   int
	Count   int
	Entries []RecycleEntry
}

// ListRecycle 通过既有队列单次查询回收站，不自动翻页或重试，错误不包含远端消息。
func (c *OpenClient) ListRecycle(ctx context.Context, offset, limit int) (*RecycleList, error) {
	if offset < 0 || limit < 1 || limit > RecyclePageLimit {
		return nil, fmt.Errorf("回收站 offset 不能为负数，limit 必须在 1..%d 范围内", RecyclePageLimit)
	}
	req := c.client.R().SetMethod(http.MethodGet).SetQueryParams(map[string]string{
		"offset": strconv.Itoa(offset),
		"limit":  strconv.Itoa(limit),
	})
	body, status, err := c.doRecycleRequest(ctx, "/open/rb/list", req)
	if err != nil {
		return nil, err
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, &OpenAPIError{HTTPStatus: status, Message: "115 回收站列表格式无效"}
	}
	result := &RecycleList{Entries: []RecycleEntry{}}
	for _, field := range []struct {
		name  string
		value *int
	}{{"offset", &result.Offset}, {"limit", &result.Limit}, {"count", &result.Count}} {
		var number json.Number
		if err := json.Unmarshal(data[field.name], &number); err != nil {
			return nil, &OpenAPIError{HTTPStatus: status, Message: "115 回收站分页字段缺失或无效"}
		}
		value, err := strconv.Atoi(number.String())
		if err != nil || value < 0 {
			return nil, &OpenAPIError{HTTPStatus: status, Message: "115 回收站分页字段必须为非负整数"}
		}
		*field.value = value
		delete(data, field.name)
	}
	if result.Offset != offset || result.Limit != limit {
		return nil, &OpenAPIError{HTTPStatus: status, Message: "115 回收站分页字段与请求不一致"}
	}
	delete(data, "rb_pass")
	for id, raw := range data {
		var entry RecycleEntry
		wire := struct {
			*RecycleEntry
			DeletedAt json.RawMessage `json:"dtime"`
		}{RecycleEntry: &entry}
		if err := json.Unmarshal(raw, &wire); err != nil || copyFileID(json.RawMessage(id)) != id || id == "" || entry.ID.String() != id {
			return nil, &OpenAPIError{HTTPStatus: status, Message: "115 回收站条目身份或格式无效"}
		}
		// 不可信时间交给清理层跳过该条目，不能阻断同页其他条目的维护。
		if err := json.Unmarshal(wire.DeletedAt, &entry.DeletedAt); err != nil {
			entry.DeletedAt = ""
		}
		result.Entries = append(result.Entries, entry)
	}
	if len(result.Entries) > result.Limit || len(result.Entries) > result.Count {
		return nil, &OpenAPIError{HTTPStatus: status, Message: "115 回收站条目数量与分页字段不一致"}
	}
	return result, nil
}

// DeleteRecycle 单次永久删除指定回收站 ID，使用安全错误语义，绝不通过空 ID 列表清空回收站。
func (c *OpenClient) DeleteRecycle(ctx context.Context, ids []string) error {
	_, _, err := c.writeRecycle(ctx, "/open/rb/del", ids)
	return err
}

// RestoreRecycle 单次还原指定回收站 ID，使用安全错误语义；接口成功不代表还原已完成。
func (c *OpenClient) RestoreRecycle(ctx context.Context, ids []string) error {
	body, status, err := c.writeRecycle(ctx, "/open/rb/revert", ids)
	if err != nil {
		return err
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(body, &data); err != nil {
		// 实测成功响应为 data=[]，没有逐项结果；非空数组不属于已确认的协议。
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err == nil && items != nil && len(items) == 0 {
			return nil
		}
		return &OpenAPIError{HTTPStatus: status, Message: "115 回收站还原结果格式无效"}
	}
	for _, id := range ids {
		var item struct {
			State bool `json:"state"`
			Errno *int `json:"errno"`
		}
		if err := json.Unmarshal(data[id], &item); err != nil || item.Errno == nil {
			return &OpenAPIError{HTTPStatus: status, Message: "115 回收站条目缺少还原成功确认"}
		}
		if !item.State || *item.Errno != 0 {
			return &OpenAPIError{Code: *item.Errno, HTTPStatus: status, Message: "115 回收站条目还原未成功"}
		}
	}
	return nil
}

func (c *OpenClient) writeRecycle(ctx context.Context, path string, ids []string) (json.RawMessage, int, error) {
	if len(ids) == 0 || len(ids) > RecycleBatchLimit {
		return nil, 0, fmt.Errorf("回收站操作需要 1..%d 个 ID", RecycleBatchLimit)
	}
	for _, id := range ids {
		if id == "" || copyFileID(json.RawMessage(id)) != id {
			return nil, 0, fmt.Errorf("回收站 ID 必须为十进制正整数")
		}
	}
	req := c.client.R().SetMethod(http.MethodPost).SetMultipartFormData(map[string]string{
		"tid": strings.Join(ids, ","),
	})
	return c.doRecycleRequest(ctx, path, req)
}

func (c *OpenClient) doRecycleRequest(ctx context.Context, path string, req *resty.Request) (json.RawMessage, int, error) {
	// 普通客户端也使用既有单次请求策略：复用连接池、绑定凭据快照，不回显远端消息。
	credentials := c.credentialSnapshot()
	client := &OpenClient{AccountId: c.AccountId, client: c.client, playback: true}
	client.credentials.Store(&credentials)
	response, body, err := client.doAuthRequest(ctx, OPEN_BASE_URL+path, req, MakeRequestConfig(0, 0, 30), nil)
	status := 0
	if response != nil {
		status = response.StatusCode()
	}
	if err != nil {
		return nil, status, err
	}
	var envelope RespBaseBool[json.RawMessage]
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, status, &OpenAPIError{HTTPStatus: status, Message: "115 回收站响应格式无效"}
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices || !envelope.State || envelope.Code != 0 || envelope.Errno != 0 {
		return nil, status, playbackResponseError(status, &envelope)
	}
	return envelope.Data, status, nil
}
