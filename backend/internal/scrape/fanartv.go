package scrape

import (
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// 从 fanart.tv 刮削元数据
func (m *movieScrapeImpl) DownloadMovieImagesFromFanart(sm *models.ScrapeMediaFile) map[string]string {
	client := m.fanartClient
	if sm.TmdbId == 0 {
		return nil
	}
	resp, err := client.GetMovieImages(m.ctx, sm.TmdbId)
	if err != nil {
		helpers.AppLogger.Errorf("从 fanart.tv 查询电影图片失败：TMDB ID=%d，%v", sm.TmdbId, err)
		return nil
	}
	helpers.AppLogger.Infof("从 fanart.tv 查询电影图片成功：TMDB ID=%d", sm.TmdbId)
	fileList := map[string]string{}
	// clearart
	if len(resp.HDMovieClearArt) > 0 {
		fileList[m.GetMovieRealName(sm, "clearart.jpg", "image")] = resp.HDMovieClearArt[0].URL
		helpers.AppLogger.Infof("clearart.jpg => %s", resp.HDMovieClearArt[0].URL)
	}
	// disc
	if len(resp.MovieDisc) > 0 {
		fileList[m.GetMovieRealName(sm, "disc.jpg", "image")] = resp.MovieDisc[0].URL
		helpers.AppLogger.Infof("disc.jpg => %s", resp.MovieDisc[0].URL)
	}
	// background
	if len(resp.MovieBackground) > 0 {
		fileList[m.GetMovieRealName(sm, "background.jpg", "image")] = resp.MovieBackground[0].URL
		helpers.AppLogger.Infof("background.jpg => %s", resp.MovieBackground[0].URL)
	}
	// thumb
	if len(resp.MovieThumb) > 0 {
		fileList[m.GetMovieRealName(sm, "thumb.jpg", "image")] = resp.MovieThumb[0].URL
		helpers.AppLogger.Infof("thumb.jpg => %s", resp.MovieThumb[0].URL)
	}
	// square
	if len(resp.MovieSquare) > 0 {
		fileList[m.GetMovieRealName(sm, "square.jpg", "image")] = resp.MovieSquare[0].URL
		helpers.AppLogger.Infof("square.jpg => %s", resp.MovieSquare[0].URL)
	}
	// logo
	if len(resp.HDMovieLogo) > 0 {
		fileList[m.GetMovieRealName(sm, "logo.jpg", "image")] = resp.HDMovieLogo[0].URL
		helpers.AppLogger.Infof("logo.jpg => %s", resp.HDMovieLogo[0].URL)
	}
	// 4kbackground
	if len(resp.Movie4kBackground) > 0 {
		fileList[m.GetMovieRealName(sm, "4kbackground.jpg", "image")] = resp.Movie4kBackground[0].URL
		helpers.AppLogger.Infof("4kbackground.jpg => %s", resp.Movie4kBackground[0].URL)
	}
	// banner
	if len(resp.MovieBanner) > 0 {
		fileList[m.GetMovieRealName(sm, "banner.jpg", "image")] = resp.MovieBanner[0].URL
		helpers.AppLogger.Infof("banner.jpg => %s", resp.MovieBanner[0].URL)
	}
	return fileList
}
