package emby

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestStartParseEmbyMediaInfoRunsOneAtATime(t *testing.T) {
	oldLogger, oldConfig := helpers.AppLogger, models.GlobalEmbyConfig
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	// 首轮扫描阻塞在读取媒体库，模拟提取仍在后台运行。
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseServer := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusInternalServerError)
	}))
	models.GlobalEmbyConfig = &models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "key"}
	waitIdle := func() bool {
		deadline := time.Now().Add(5 * time.Second)
		for embyMediaInfoRunning.Load() == 1 {
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(time.Millisecond)
		}
		return true
	}
	t.Cleanup(func() {
		releaseServer()
		waitIdle()
		server.Close()
		helpers.AppLogger, models.GlobalEmbyConfig = oldLogger, oldConfig
	})

	if !StartParseEmbyMediaInfo() {
		t.Fatal("首次提取应启动")
	}
	if StartParseEmbyMediaInfo() {
		t.Fatal("提取运行期间不能再启动一轮")
	}
	releaseServer()
	if !waitIdle() {
		t.Fatal("后台提取结束后没有释放运行标记")
	}
}
