package controllers

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	v115open "qmediasync/internal/v115/openapi"
)

func IsFnOS(c *gin.Context) {
	httpapi.Success(c, http.StatusOK, helpers.IsFnOS, "")
}

func RepairDB(c *gin.Context) {
	// 修复数据库，补齐缺失的表、字段和索引
	err := models.RepairDatabase(db.Db.WithContext(c.Request.Context()))
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "修复数据库失败，请查看服务日志", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "已补齐数据库表结构，并完成主键序列检查")
}

func GetAnnounce(c *gin.Context) {
	// 从 https://api.mqfamily.top/desc.json 获取公告
	bytes, err := helpers.ReadFromUrl("https://api.mqfamily.top/desc.json", v115open.DEFAULTUA)
	if err != nil {
		httpapi.Failure(c, http.StatusBadGateway, httpapi.Error{Code: "ANNOUNCEMENT_FETCH_FAILED", Message: "获取公告失败", Cause: err})
		return
	}
	// helpers.AppLogger.Infof("获取到的公告：%s", string(bytes))
	// 解析 JSON
	type announce struct {
		ID      int    `json:"id"`
		Title   string `json:"title"`
		Content string `json:"content"`
		Time    string `json:"time"`
	}
	var announces []announce
	err = json.Unmarshal(bytes, &announces)
	if err != nil {
		httpapi.Failure(c, http.StatusBadGateway, httpapi.Error{Code: "ANNOUNCEMENT_RESPONSE_INVALID", Message: "公告服务响应无效", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, announces, "获取公告成功")
}

func DeleteAllTabble(c *gin.Context) {
	// 重置数据库，删除所有表，重新初始化数据库
	err := models.BatchDropTable()
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "删除数据库所有表失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "已删除数据库所有表")
}
