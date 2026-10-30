package ffmpeg_test

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"testing"

	"qmediasync/internal/emby302/service/lib/ffmpeg"
	"qmediasync/internal/helpers"
)

const host = "http://0.0.0.0:12345"

func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("QMS_INTEGRATION_TESTS") != "1" {
		t.Skip("设置 QMS_INTEGRATION_TESTS=1 后运行 FFmpeg 集成测试")
	}
	helpers.ConfigDir = t.TempDir()
	helpers.AppLogger = helpers.NewLogger("test.log", true, false)
	t.Cleanup(func() {
		helpers.AppLogger.Close()
		helpers.AppLogger = nil
	})
}

func TestInspectInfo(t *testing.T) {
	requireIntegration(t)
	if err := ffmpeg.AutoDownloadExec("../../../../.."); err != nil {
		t.Fatal(err)
		return
	}

	i, err := ffmpeg.InspectInfo("/Users/ambitious/Downloads/test.mp4")
	if err != nil {
		t.Fatal(err)
		return
	}
	log.Println(i)
}

func TestInspectMusicFlac(t *testing.T) {
	requireIntegration(t)
	if err := ffmpeg.AutoDownloadExec("../../../../.."); err != nil {
		t.Fatal(err)
		return
	}
	rmt := host + "/d/%E9%9F%B3%E4%B9%90/%E7%8E%8B%E6%A0%8E%E9%91%AB%20-%20%E5%93%A5%E5%93%A5.flac?sign=3zd3EpjDnt-2I_OmcxCxru0sNujyw7lfymgDUnKt9bU=:0"
	meta, err := ffmpeg.InspectMusic(rmt)
	if err != nil {
		t.Fatal(err)
		return
	}
	log.Println(meta)
}

func TestInspectMusicMP3(t *testing.T) {
	requireIntegration(t)
	if err := ffmpeg.AutoDownloadExec("../../../../.."); err != nil {
		t.Fatal(err)
		return
	}

	rmt := host + "/d/%E9%9F%B3%E4%B9%901/%E9%99%8D%E5%A4%AE%E5%8D%93%E7%8E%9B/%E6%9E%97%E9%9C%9E%E3%80%81%E9%99%8D%E5%A4%AE%E5%8D%93%E7%8E%9B%20-%20%E8%8B%B4%E5%8D%B4%E7%A0%9A.mp3?sign=IELDpVhmd32mKhSIkXWgZP0-xKjw_20EyY2tbjuIXZo=:0"
	meta, err := ffmpeg.InspectMusic(rmt)
	if err != nil {
		t.Fatal(err)
		return
	}
	log.Println(meta)
}

func TestExtractMusicCover(t *testing.T) {
	requireIntegration(t)
	if err := ffmpeg.AutoDownloadExec("../../../../.."); err != nil {
		t.Fatal(err)
		return
	}
	rmt := host + "/d/%E9%9F%B3%E4%B9%901/%E9%99%8D%E5%A4%AE%E5%8D%93%E7%8E%9B/%E9%99%8D%E5%A4%AE%E5%8D%93%E7%8E%9B%20-%20%E8%93%9D%E8%89%B2%E7%9A%84%E8%92%99%E5%8F%A4%E9%AB%98%E5%8E%9F.flac?sign=xkDoQzXTqq8xXrQzi_BQpqMu0Crh-_gbBuzAvP83nVw=:0"
	bytes, err := ffmpeg.ExtractMusicCover(rmt)
	fmt.Printf("http.DetectContentType(bytes): %v\n", http.DetectContentType(bytes))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile("cover.jpg", bytes, 0777)
}
