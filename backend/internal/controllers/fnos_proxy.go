package controllers

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/fnosproxy"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/validation"
)

// FnosProxyController 管理已保存配置，状态由独立运行实例提供。
type FnosProxyController struct {
	saveMu        sync.Mutex
	status        func(models.FnosProxyConfig) models.FnosProxyRuntimeStatus
	apply         func(context.Context, models.FnosProxyConfig) error
	reservedPorts []int
}

// NewFnosProxyController 接收状态、应用回调及主站、Emby 已知监听端口。
func NewFnosProxyController(status func(models.FnosProxyConfig) models.FnosProxyRuntimeStatus, apply func(context.Context, models.FnosProxyConfig) error, reservedPorts ...int) *FnosProxyController {
	return &FnosProxyController{status: status, apply: apply, reservedPorts: append([]int(nil), reservedPorts...)}
}

// GetConfig 获取飞牛影视反代设置及实际运行状态。
func (controller *FnosProxyController) GetConfig(c *gin.Context) {
	controller.saveMu.Lock()
	defer controller.saveMu.Unlock()
	controller.respond(c, "获取飞牛影视反代配置成功")
}

// SaveConfig 串行保存并应用配置；数据库失败时不触碰运行实例。
func (controller *FnosProxyController) SaveConfig(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, requests.MaxFnosProxyConfigBodyBytes)
	var request requests.SaveFnosProxyConfigRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			httpapi.Failure(c, http.StatusRequestEntityTooLarge, httpapi.Error{Code: "REQUEST_TOO_LARGE", Message: "请求内容过大", Cause: err})
		} else {
			httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		}
		return
	}
	if err := request.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	config, err := request.Config()
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(validation.New("path_mappings", err.Error())))
		return
	}
	if err := fnosproxy.ValidatePorts(config, fnosproxy.DefaultHTTPPort, fnosproxy.DefaultHTTPSPort, controller.reservedPorts...); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	controller.saveMu.Lock()
	defer controller.saveMu.Unlock()
	if err := models.SaveFnosProxyConfig(c.Request.Context(), config); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "保存飞牛影视反代配置失败，请查看服务日志", Cause: err})
		return
	}
	message := "飞牛影视反代配置已保存并生效"
	var warnings []string
	if controller.apply != nil {
		// 保存提交后即使浏览器断开，也完成这次有界切换，避免留下半应用状态。
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
		err := controller.apply(ctx, config)
		cancel()
		if err != nil {
			message = "飞牛影视反代配置已保存"
			warnings = append(warnings, "配置已保存，但未能完全应用，请查看运行状态并再次保存重试")
			if helpers.AppLogger != nil {
				helpers.AppLogger.Warnf("应用飞牛影视反代配置失败：%s", helpers.RedactSensitiveLog(err.Error()))
			}
		}
	}
	controller.respondConfig(c, message, config, warnings...)
}

func (controller *FnosProxyController) respond(c *gin.Context, message string) {
	config, err := models.ReadFnosProxyConfig(c.Request.Context())
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取飞牛影视反代配置失败，请查看服务日志", Cause: err})
		return
	}
	controller.respondConfig(c, message, config)
}

func (controller *FnosProxyController) respondConfig(c *gin.Context, message string, config models.FnosProxyConfig, warnings ...string) {
	status := models.FnosProxyRuntimeStatus{RestartRequired: config.Enabled}
	if controller.status != nil {
		status = controller.status(config)
	}
	c.Header("Cache-Control", "no-store, private")
	httpapi.Success(c, http.StatusOK, gin.H{"config": config, "runtime": status}, message, warnings...)
}
