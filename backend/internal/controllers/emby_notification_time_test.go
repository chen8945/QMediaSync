package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestEmbyNotificationTimeFallback(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("CST", 8*3600)
	t.Cleanup(func() { time.Local = previousLocal })
	received := time.Date(2026, 10, 18, 12, 34, 56, 0, time.Local)
	for _, test := range []struct {
		name, created, event, want string
	}{
		{"UTC 跨日与小数秒", "2026-10-17T18:01:02.1234567Z", "2026-10-18T04:00:00Z", "2026-10-18 02:01:02"},
		{"带时区偏移", "2026-10-17T23:01:02-04:00", "", "2026-10-18 11:01:02"},
		{"创建时间优先于更新的事件时间", "2020-01-01T00:00:00Z", "2026-10-18T04:00:00Z", "2020-01-01 08:00:00"},
		{"缺失创建时间", "", "2026-10-17T18:01:02Z", "2026-10-18 02:01:02"},
		{"无效创建时间", "invalid", "2026-10-17T18:01:02Z", "2026-10-18 02:01:02"},
		{"零值创建时间", "0001-01-01T00:00:00Z", "2026-10-17T18:01:02Z", "2026-10-18 02:01:02"},
		{"缺失时间回退接收时间", "", "", "2026-10-18 12:34:56"},
		{"无效时间回退接收时间", "invalid", "invalid", "2026-10-18 12:34:56"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fallback := parseEmbyNotificationTime(test.event, received)
			got := parseEmbyNotificationTime(test.created, fallback).Format("2006-01-02 15:04:05")
			if got != test.want {
				t.Fatalf("入库时间 = %s，期望 %s", got, test.want)
			}
		})
	}
}

func TestEmbyNotificationEpisodeTime(t *testing.T) {
	previousConfig, previousLogger := models.GlobalEmbyConfig, helpers.AppLogger
	t.Cleanup(func() { models.GlobalEmbyConfig, helpers.AppLogger = previousConfig, previousLogger })
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	fallback := time.Date(2026, 10, 18, 5, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, body, want string
		status           int
	}{
		{
			name: "老剧更新取本批单集最新时间并忽略其他媒体",
			body: `{"TotalRecordCount":4,"Items":[{"Id":"21","Type":"Episode","DateCreated":"2020-01-01T00:00:00Z"},{"Id":"22","Type":"Episode","DateCreated":"2026-10-17T22:30:00Z"},{"Id":"series","Type":"Series","DateCreated":"2030-01-01T00:00:00Z"},{"Id":"99","Type":"Episode","DateCreated":"2030-01-01T00:00:00Z"}]}`,
			want: "2026-10-17T22:30:00Z", status: http.StatusOK,
		},
		{
			name: "部分单集缺失或无效时独立兜底",
			body: `{"TotalRecordCount":1,"Items":[{"Id":"21","Type":"Episode","DateCreated":"invalid"}]}`,
			want: "2026-10-18T05:00:00Z", status: http.StatusOK,
		},
		{
			name: "请求失败保留各单集兜底时间",
			want: "2026-10-18T05:00:00Z", status: http.StatusServiceUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/emby/Items" || r.URL.Query().Get("Ids") != "21,22" ||
					r.URL.Query().Get("Fields") != "DateCreated" || r.URL.Query().Get("IncludeItemTypes") != "Episode" {
					t.Errorf("单集查询范围错误：%s", r.URL.Path+"?"+r.URL.RawQuery)
				}
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			models.GlobalEmbyConfig = &models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "test"}
			episodes := map[string]time.Time{"21": fallback.Add(-time.Hour), "22": fallback}
			got := newSeriesAddedTime(t.Context(), episodes)
			want, err := time.Parse(time.RFC3339, test.want)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(want) {
				t.Fatalf("合并入库时间 = %s，期望 %s", got, test.want)
			}
			if !episodes["22"].Equal(fallback) {
				t.Fatal("查询不能改写已保存的事件时间")
			}
		})
	}
}

func TestEmbyNotificationEpisodeBatch(t *testing.T) {
	previousConfig := models.GlobalEmbyConfig
	t.Cleanup(func() { models.GlobalEmbyConfig = previousConfig })
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		ids := strings.Split(r.URL.Query().Get("Ids"), ",")
		if len(ids) > 100 || ids[0] == "" {
			t.Errorf("单集批次 ID 数量无效：%d", len(ids))
		}
		items := make([]embyclientrestgo.BaseItemDtoV2, 0, len(ids))
		for _, id := range ids {
			items = append(items, embyclientrestgo.BaseItemDtoV2{Id: id, Type: "Episode", DateCreated: "2026-10-18T01:02:03Z"})
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items)})
	}))
	defer server.Close()
	models.GlobalEmbyConfig = &models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "test"}
	episodes := make(map[string]time.Time)
	for id := 1; id <= 201; id++ {
		episodes[fmt.Sprint(id)] = time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	}
	got := newSeriesAddedTime(t.Context(), episodes)
	if requests.Load() != 3 || got.UTC().Format(time.RFC3339) != "2026-10-18T01:02:03Z" {
		t.Fatalf("请求数 = %d，入库时间 = %s", requests.Load(), got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	previousLogger := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = previousLogger })
	got = newSeriesAddedTime(ctx, episodes)
	if requests.Load() != 3 || !got.Equal(episodes["1"]) {
		t.Fatalf("取消后应直接使用事件时间：请求数 = %d，入库时间 = %s", requests.Load(), got)
	}
}

func TestEmbyNotificationEpisodeBuffer(t *testing.T) {
	previousLogger := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	newSeriesBufferMu.Lock()
	previousBuffer := newSeriesBuffer
	newSeriesBuffer = make(map[string]newSeries)
	newSeriesBufferMu.Unlock()
	newSeriesBufferTickerStartedMu.Lock()
	previousStarted := newSeriesBufferTickerStarted
	newSeriesBufferTickerStarted = true
	newSeriesBufferTickerStartedMu.Unlock()
	t.Cleanup(func() {
		newSeriesBufferMu.Lock()
		newSeriesBuffer = previousBuffer
		newSeriesBufferMu.Unlock()
		newSeriesBufferTickerStartedMu.Lock()
		newSeriesBufferTickerStarted = previousStarted
		newSeriesBufferTickerStartedMu.Unlock()
		helpers.AppLogger = previousLogger
	})
	latest := time.Date(2026, 10, 18, 1, 2, 3, 0, time.UTC)
	var workers sync.WaitGroup
	for offset := range 10 {
		workers.Go(func() {
			addItemToEpisodeBuffer("series", 1, 1, "21", latest.Add(-time.Duration(offset)*time.Hour))
		})
	}
	workers.Wait()
	newSeriesBufferMu.Lock()
	defer newSeriesBufferMu.Unlock()
	series := newSeriesBuffer["series"]
	if !series.Episodes["21"].Equal(latest) || len(series.Seasons[1]) != 10 {
		t.Fatalf("合并单集 ID、时间或季集信息丢失：%+v", series)
	}
}
