package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"gorm.io/gorm"

	"qmediasync/internal/httpapi"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"

	"github.com/gin-gonic/gin"
)

// CreateAPIKeyResponse 创建 API Key 响应。
type CreateAPIKeyResponse struct {
	ID        uint   `json:"id"`
	Name      string `json:"name"`
	Key       string `json:"key"`        // 完整的 API Key，仅此一次返回
	KeyPrefix string `json:"key_prefix"` // 前缀用于显示
	CreatedAt int64  `json:"created_at"`
	IsActive  bool   `json:"is_active"`
}

// APIKeyListItem API Key 列表项（不包含完整密钥）。
type APIKeyListItem struct {
	ID         uint   `json:"id"`
	Name       string `json:"name"`
	KeyPrefix  string `json:"key_prefix"`
	LastUsedAt int64  `json:"last_used_at"`
	CreatedAt  int64  `json:"created_at"`
	IsActive   bool   `json:"is_active"`
}

// CreateAPIKey 创建新的 API Key。
func CreateAPIKey(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	var req requests.CreateAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	currentUser, ok := CurrentUser(c)
	if !ok {
		setAuthenticationChallenge(c)
		httpapi.Failure(c, http.StatusUnauthorized, httpapi.Error{Code: ErrorCodeAuthenticationRequired, Message: "用户未登录", Data: nil})
		return
	}

	// 创建 API Key
	apiKey, rawKey, err := models.CreateAPIKey(currentUser.ID, strings.TrimSpace(req.Name))
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "创建 API Key 失败", Cause: err})
		return
	}
	helpers.AppLogger.Infof("用户 %s（ID：%d）创建了 API Key：%s（ID：%d）", currentUser.Username, currentUser.ID, apiKey.Name, apiKey.ID)

	// 返回包含完整密钥的响应（仅此一次）
	resp := CreateAPIKeyResponse{
		ID:        apiKey.ID,
		Name:      apiKey.Name,
		Key:       rawKey, // 完整密钥仅返回一次
		KeyPrefix: apiKey.KeyPrefix,
		CreatedAt: apiKey.CreatedAt,
		IsActive:  apiKey.IsActive,
	}

	httpapi.Success(c, http.StatusCreated, resp, "API Key 已创建，请妥善保管密钥，此密钥仅显示一次")
}

// ListAPIKeys 获取 API Key 列表。
func ListAPIKeys(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	currentUser, ok := CurrentUser(c)
	if !ok {
		setAuthenticationChallenge(c)
		httpapi.Failure(c, http.StatusUnauthorized, httpapi.Error{Code: ErrorCodeAuthenticationRequired, Message: "用户未登录", Data: nil})
		return
	}

	// 查询用户的 API Key
	apiKeys, err := models.GetAPIKeysByUserID(currentUser.ID)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "查询 API Key 失败", Cause: err})
		return
	}

	// 转换为响应格式（不包含完整密钥）
	resp := make([]APIKeyListItem, 0, len(apiKeys))
	for _, apiKey := range apiKeys {
		resp = append(resp, APIKeyListItem{
			ID:         apiKey.ID,
			Name:       apiKey.Name,
			KeyPrefix:  apiKey.KeyPrefix,
			LastUsedAt: apiKey.LastUsedAt,
			CreatedAt:  apiKey.CreatedAt,
			IsActive:   apiKey.IsActive,
		})
	}

	httpapi.Success(c, http.StatusOK, resp, "查询成功")
}

// DeleteAPIKey 删除 API Key。
func DeleteAPIKey(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	currentUser, ok := CurrentUser(c)
	if !ok {
		setAuthenticationChallenge(c)
		httpapi.Failure(c, http.StatusUnauthorized, httpapi.Error{Code: ErrorCodeAuthenticationRequired, Message: "用户未登录", Data: nil})
		return
	}

	// 获取 API Key ID
	idReq, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	apiKey, err := models.GetAPIKeyByIDAndUserID(idReq.ID, currentUser.ID)
	if err != nil {
		writeAPIKeyLookupFailure(c, err)
		return
	}

	// 删除 API Key（确保只能删除自己的）
	err = models.DeleteAPIKey(idReq.ID, currentUser.ID)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "删除 API Key 失败", Cause: err})
		return
	}
	helpers.AppLogger.Infof("用户 %s（ID：%d）删除了 API Key：%s（ID：%d）", currentUser.Username, currentUser.ID, apiKey.Name, apiKey.ID)

	httpapi.Success(c, http.StatusOK, nil, "删除成功")
}

// UpdateAPIKeyStatus 更新 API Key 状态。
func UpdateAPIKeyStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	currentUser, ok := CurrentUser(c)
	if !ok {
		setAuthenticationChallenge(c)
		httpapi.Failure(c, http.StatusUnauthorized, httpapi.Error{Code: ErrorCodeAuthenticationRequired, Message: "用户未登录", Data: nil})
		return
	}

	// 获取 API Key ID
	idReq, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	// 解析请求体
	var req requests.UpdateAPIKeyStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	apiKey, err := models.GetAPIKeyByIDAndUserID(idReq.ID, currentUser.ID)
	if err != nil {
		writeAPIKeyLookupFailure(c, err)
		return
	}

	// 更新 API Key 状态（确保只能更新自己的）
	err = models.UpdateAPIKeyStatus(idReq.ID, currentUser.ID, *req.IsActive)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "更新 API Key 状态失败", Cause: err})
		return
	}

	statusText := "禁用"
	if *req.IsActive {
		statusText = "启用"
	}
	helpers.AppLogger.Infof("用户 %s（ID：%d）已%s API Key：%s（ID：%d）", currentUser.Username, currentUser.ID, statusText, apiKey.Name, apiKey.ID)

	httpapi.Success(c, http.StatusOK, nil, fmt.Sprintf("API Key 已%s", statusText))
}

func writeAPIKeyLookupFailure(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "API_KEY_NOT_FOUND", Message: "API Key 不存在或无权限访问"})
		return
	}
	httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取 API Key 失败", Cause: err})
}
